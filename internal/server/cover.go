// Cover sources: how a recording reaches the RunPod worker.
//
// The worker fetches `source_audio` itself, so it must be a URL it can reach —
// and it has no session cookie, so the owner-scoped /audio route is closed to
// it. Two ways to solve that were considered and rejected: giving the app write
// access to the object store the worker already uses (a new credential holding
// write permission over every generated song), and base64 in the job payload
// (capped by RunPod's request limit — a four-minute song is ~5 MB encoded).
//
// Instead the app serves bytes it already holds, under an unguessable expiring
// token. That needs no new credential at all, and it collapses the three ways a
// user can name a recording — a song in the library, a fresh upload, a pasted
// URL — into one mechanism: produce a URL the worker can fetch.
package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"github.com/sruckh/minmaxmusic3-web/internal/worker"
)

// coverLinkTTL is how long a minted link stays valid.
//
// It must comfortably outlive the worker's queue budget. A job can sit in
// IN_QUEUE for 45 minutes before a GPU picks it up, and only then does the
// worker fetch the recording — so a "tight" five-minute link would expire on
// exactly the jobs that waited longest, and fail them for a reason nobody
// could see. Two hours is that budget with room to spare.
const coverLinkTTL = 2 * time.Hour

// maxUploadBytes bounds an uploaded recording.
//
// Generous — a four-minute song is roughly 4-10 MB depending on codec — but
// bounded, because this is an unauthenticated-length body that becomes a file
// on the same volume as every generated song.
const maxUploadBytes = 64 << 20

// sourceDir is where uploaded recordings are staged. Subordinate to the data
// volume, so it shares the container's single writable mount.
func (s *Server) sourceDir() string {
	return filepath.Join(filepath.Dir(s.cfg.AudioDir), "sources")
}

// mintCoverURL creates a link for one artifact and returns an absolute URL.
//
// The host comes from MM3_PUBLIC_URL and is never hardcoded: that value exists
// for exactly this purpose, and a personal hostname must not enter a tracked
// file. Without it the link would be built from the request Host, which is the
// wrong origin when the request arrives through the proxy.
func (s *Server) mintCoverURL(kind, ref string) (string, error) {
	token, err := s.st.CreateCoverLink(kind, ref, coverLinkTTL)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		return "", fmt.Errorf("cover: MM3_PUBLIC_URL is not set, so no absolute link can be built")
	}
	return base + "/signed/" + token, nil
}

// handleSignedSource serves one artifact to anyone holding a valid token.
//
// This route is public — the worker has no session — so the token is the entire
// authorisation. It is unguessable (32 random bytes), single-purpose, and
// expires; and because it is looked up by hash, a leaked database row is not a
// usable link.
func (s *Server) handleSignedSource(w http.ResponseWriter, r *http.Request) {
	kind, ref, err := s.st.CoverLink(r.PathValue("token"))
	if err != nil {
		s.log.Error("cover: link lookup", "err", err)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if kind == "" {
		// Unknown, expired, or too short to be real — all the same answer, so
		// the route cannot be used to probe which tokens exist.
		http.NotFound(w, r)
		return
	}

	var path string
	switch kind {
	case store.CoverLinkAudio:
		// Admin-scoped deliberately, and this is the one place in the app where
		// that is the right call rather than a shortcut.
		//
		// The ownership predicate exists to stop one *user* reading another's
		// song. Here there is no user: the caller is the RunPod worker, holding
		// an opaque token and no identity at all. The token stands in for the
		// entitlement — it was minted by someone who owned the song, or by an
		// admin, at the moment they asked for a cover — so re-deriving ownership
		// at fetch time is not possible and not the question being asked.
		//
		// What keeps this honest is that the token names no song: it is looked
		// up by hash to (kind, ref), so a holder learns only what they were
		// given, and only until it expires.
		//
		// The stored path is absolute and was written by the worker, never by a
		// caller, so it is not attacker-controlled input.
		song, err := s.st.Song(ref, store.Access{Admin: true})
		if err != nil || song == nil || song.AudioPath == "" {
			http.NotFound(w, r)
			return
		}
		path = song.AudioPath
	case store.CoverLinkSource:
		// An upload. filepath.Base defends the join: `ref` came from this
		// server originally, but a path separator surviving into storage would
		// turn a signed link into an arbitrary-file read.
		path = filepath.Join(s.sourceDir(), filepath.Base(ref))
	default:
		http.NotFound(w, r)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		// A link outliving the file it points at is a normal outcome — a song
		// deleted between mint and fetch, or an upload purged. 404, not 500.
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	// Deliberately NOT a content type derived from the file: an uploaded
	// recording is attacker-supplied bytes served from this app's own origin,
	// and a browser that sniffed it as HTML would run script there. ffmpeg
	// probes the container for itself, so the worker does not need a real type.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// resolveCoverSource turns one of the three ways a user can name a recording
// into a URL the worker can fetch.
func (s *Server) resolveCoverSource(r *http.Request, songID string) (string, string, error) {
	// 1. A pasted URL. Passed through unchanged: the worker fetches it itself,
	//    so making the app download and re-serve it would move bytes twice for
	//    no benefit.
	if raw := strings.TrimSpace(r.FormValue("source_url")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", "", fmt.Errorf("That is not a web address the model can fetch — it needs to start with http:// or https://")
		}
		return raw, "", nil
	}

	// 2. An uploaded file. Staged on the data volume and served by signed link,
	//    so a full-length recording is not capped by the job payload.
	if f, hdr, err := r.FormFile("source_upload"); err == nil {
		defer f.Close()
		if hdr.Size > maxUploadBytes {
			return "", "", fmt.Errorf("That recording is too large — keep it under %d MB.", maxUploadBytes>>20)
		}
		name, err := s.storeUpload(f, hdr.Filename)
		if err != nil {
			return "", "", err
		}
		link, err := s.mintCoverURL(store.CoverLinkSource, name)
		if err != nil {
			return "", "", err
		}
		return link, "", nil
	}

	// 3. The song being viewed. This is the default, and the common case: most
	//    covers are of something already in the library.
	link, err := s.mintCoverURL(store.CoverLinkAudio, songID)
	if err != nil {
		return "", "", err
	}
	return link, songID, nil
}

// handleCoverSong queues a cover of an existing song.
//
// A cover has no score to edit, so the form is smaller than an edit's: a target
// style, and optionally the words. Omit the lyrics and the worker's ASR
// transcribes them from the recording.
func (s *Server) handleCoverSong(w http.ResponseWriter, r *http.Request) {
	// Source recordings arrive as multipart, not a urlencoded form, because one
	// of the three ways to supply them is a file.
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		// A urlencoded body is also acceptable — a pasted URL or a library
		// choice needs no multipart at all.
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
	}
	if !s.genAllowed(w, r, s.genLimiter, "generation") {
		return
	}

	src, err := s.st.Song(r.PathValue("id"), s.caller(r))
	if err != nil {
		http.Error(w, "Could not load that song.", http.StatusInternalServerError)
		return
	}
	// A song the caller cannot see is indistinguishable from one that does not
	// exist, so this route cannot probe for other tenants' ids.
	if src == nil || !s.owns(r, src) {
		http.NotFound(w, r)
		return
	}
	// Cover is YuE2's alone. The worker's other engine has no such mode, and
	// queuing one under it would fail at the endpoint with a schema error.
	if src.Engine != store.EngineYue2 {
		s.renderJobError(w, http.StatusBadRequest,
			"Covers need a song made with YuE2. Pick one of those, or generate a new YuE2 song first.")
		return
	}

	style := strings.TrimSpace(r.FormValue("instructions"))
	if style == "" {
		s.renderJobError(w, http.StatusBadRequest,
			"Describe the style you want the cover in.")
		return
	}
	// Optional on purpose: with none supplied, the worker's ASR transcribes the
	// words from the recording. That is the whole point of a cover.
	lyrics := strings.TrimSpace(r.FormValue("input"))

	sourceURL, sourceSongID, err := s.resolveCoverSource(r, src.ID)
	if err != nil {
		s.renderJobError(w, http.StatusBadRequest, err.Error())
		return
	}

	j := &store.Job{
		ID: worker.NewJobID(), State: store.StateQueued,
		UserID: s.caller(r).UserID,
		Lyrics: lyrics, Caption: style,
		Title:  src.Title,
		Engine: src.Engine,
		Mode:   store.ModeCover,
		// The URL is minted now and stored, because the worker does not fetch it
		// until the job reaches a GPU — which can be 45 minutes later. That is
		// also why the link's TTL has to outlive the queue budget; see
		// coverLinkTTL.
		SourceAudio: sourceURL,
		// Set only when the recording is one of ours, so a cover of an uploaded
		// file is not falsely recorded as derived from this song.
		SourceSongID: sourceSongID,
		Seed:         src.Seed,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the cover — try again.")
		return
	}
	s.renderJob(w, j)
}

// storeUpload writes an uploaded recording to the source directory, returning
// the name it was stored under.
//
// The name is a fresh random token rather than anything the caller sent: the
// uploaded filename is attacker-controlled, and it decides the on-disk path
// only through this function.
func (s *Server) storeUpload(src io.Reader, _ string) (string, error) {
	if err := os.MkdirAll(s.sourceDir(), 0o750); err != nil {
		return "", fmt.Errorf("could not store the recording: %w", err)
	}
	name, err := store.NewSessionToken()
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.sourceDir(), name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", fmt.Errorf("could not store the recording: %w", err)
	}
	defer f.Close()
	// LimitReader enforces the cap even if the declared size lied.
	if _, err := io.Copy(f, io.LimitReader(src, maxUploadBytes)); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("could not store the recording: %w", err)
	}
	return name, nil
}
