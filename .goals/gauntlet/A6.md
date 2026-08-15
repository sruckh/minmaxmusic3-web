package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/sruckh/timbre/internal/voices"
	"github.com/sruckh/timbre/internal/web"
)

// uploadFormKey is the multipart field holding the reference audio file.
const uploadFormKey = "reference"

// maxUploadBytes caps a reference clip. One-shot clone references are short;
// anything larger is almost certainly the wrong file. The body reader is capped
// slightly above this to leave room for multipart framing.
const maxUploadBytes int64 = 10 * 1024 * 1024

// allowedExt is the allowlist that gates "type" validation by extension. The
// common cloned-voice format is WAV; the rest cover what a browser's audio
// picker may hand over.
var allowedExt = map[string]bool{
	".wav":  true,
	".mp3":  true,
	".flac": true,
	".ogg":  true,
	".m4a":  true,
	".aac":  true,
	".webm": true,
	".opus": true,
}

// handleVoiceLibrary answers GET /voices. An Accept: application/json caller
// (used by the verification checks and any API client) gets the row list; a
// browser gets the rendered voice-library page.
func (s *Server) handleVoiceLibrary(w http.ResponseWriter, r *http.Request) {
	userID, _ := s.auth.UserID(r)
	items, err := s.voices.List(r.Context(), userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if wantsJSON(r) {
		if items == nil {
			items = []voices.Voice{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
		return
	}
	_ = web.VoiceLibrary(items).Render(s.navContext(r), w)
}

// handleVoiceUpload answers POST /voices/upload (multipart). It validates type
// and size, stores the reference bytes, inserts a voices row (kind=cloned), and
// returns the refreshed grid fragment with the new clone selected.
func (s *Server) handleVoiceUpload(w http.ResponseWriter, r *http.Request) {
	// Cap the wire before parsing: multipart streams the body through this.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)

	// Buffer 1 MiB in memory; a larger valid upload spills to a temp file.
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			http.Error(w, "reference audio exceeds the 10 MB limit", http.StatusRequestEntityTooLarge)
		default:
			http.Error(w, "could not read upload", http.StatusBadRequest)
		}
		return
	}

	file, header, err := r.FormFile(uploadFormKey)
	if err != nil {
		http.Error(w, "no reference audio provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if header.Size > maxUploadBytes {
		http.Error(w, "reference audio exceeds the 10 MB limit", http.StatusRequestEntityTooLarge)
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedExt[ext] {
		http.Error(w, "unsupported file type; use WAV, MP3, FLAC, or OGG", http.StatusUnsupportedMediaType)
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if int64(len(data)) > maxUploadBytes {
		http.Error(w, "reference audio exceeds the 10 MB limit", http.StatusRequestEntityTooLarge)
		return
	}
	if len(data) == 0 {
		http.Error(w, "reference audio is empty", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = deriveVoiceName(header.Filename)
	}

	userID, _ := s.auth.UserID(r)
	id, err := s.voices.CreateCloned(r.Context(), userID, name, ext, data)
	if err != nil {
		serverError(w, r, err)
		return
	}

	items, err := s.voices.List(r.Context(), userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = web.VoiceGrid(items, id).Render(r.Context(), w)
}

// handleVoiceRename answers POST /voices/{id}/name. A cloned voice arrives
// named after the file it came from; this is how it gets a name that means
// something. It answers with the refreshed grid fragment (the renamed voice
// selected) so HTMX can swap the library in place.
func (s *Server) handleVoiceRename(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid voice id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the form", http.StatusBadRequest)
		return
	}

	switch err := s.voices.Rename(r.Context(), id, r.PostFormValue("name")); {
	case err == nil:
	case errors.Is(err, voices.ErrEmptyName), errors.Is(err, voices.ErrNameTooLong),
		errors.Is(err, voices.ErrNotRenamable):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, voices.ErrNotFound):
		http.Error(w, "voice not found", http.StatusNotFound)
		return
	default:
		serverError(w, r, err)
		return
	}

	userID, _ := s.auth.UserID(r)
	items, err := s.voices.List(r.Context(), userID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if wantsJSON(r) {
		renamed, err := s.voices.Get(r.Context(), id)
		if err != nil {
			serverError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(renamed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = web.VoiceGrid(items, id).Render(r.Context(), w)
}

// handleVoiceReference answers GET /voices/{id}/reference by streaming the
// stored reference clip back to the signed-in user, so a card can play what it
// was cloned from.
//
// This is a session-gated preview, not a public URL: the response is marked
// private and RunPod still receives the bytes base64-inline in the submission
// payload — it is never given a link. Stock voices have no reference and 404.
func (s *Server) handleVoiceReference(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid voice id", http.StatusBadRequest)
		return
	}

	data, format, err := s.voices.Reference(r.Context(), id)
	if err != nil {
		if errors.Is(err, voices.ErrNotFound) || errors.Is(err, voices.ErrNoReference) {
			http.Error(w, "no reference audio for that voice", http.StatusNotFound)
			return
		}
		serverError(w, r, err)
		return
	}

	name := fmt.Sprintf("voice-%d.%s", id, format)
	w.Header().Set("Content-Type", referenceContentType(format))
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name))
	// Private: a reference clip is the user's own upload, never a shared asset.
	w.Header().Set("Cache-Control", "private, max-age=0, no-store")
	// ServeContent rather than Write: the audio element seeks, which needs
	// range support.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// referenceContentType maps a stored container extension onto the media type a
// browser's audio element understands. The extension was allowlist-validated on
// upload, so anything unexpected falls back to a generic audio type rather than
// guessing.
func referenceContentType(format string) string {
	switch strings.ToLower(format) {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "ogg", "opus":
		return "audio/ogg"
	case "m4a", "aac":
		return "audio/mp4"
	case "webm":
		return "audio/webm"
	case "":
		return "audio/wav"
	default:
		return "audio/" + strings.ToLower(format)
	}
}

// deriveVoiceName turns an uploaded filename into a voice name by stripping its
// directory and extension. Empty or odd filenames fall back to "Cloned voice".
func deriveVoiceName(filename string) string {
	base := filepath.Base(filename)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if base = strings.TrimSpace(base); base != "" {
		return base
	}
	return "Cloned voice"
}

// wantsJSON reports whether the caller asked for a JSON response.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// serverError logs an internal error (with the request id) and answers 500 with
// a generic message — never the error text, which could leak internals.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "err", err, "path", r.URL.Path, "req_id", middleware.GetReqID(r.Context()))
	http.Error(w, "internal server error", http.StatusInternalServerError)
}
