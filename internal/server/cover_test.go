package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// mkYue2Song writes a YuE2 song, which is the only kind that can be covered.
func mkYue2Song(t *testing.T, s *Server, id, userID string) *store.Song {
	t.Helper()
	if err := os.MkdirAll(s.cfg.AudioDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.AudioDir, id+".m4a")
	if err := os.WriteFile(path, []byte("yue2-audio-"+id), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &store.Song{
		ID: id, JobID: "job-" + id, UserID: userID,
		Lyrics: "[Verse]\nla", Caption: "sparse piano ballad", Duration: 60,
		Engine: store.EngineYue2, Mode: store.ModeCreate, Cot: "full",
		Delivery: "s3", AudioPath: path, Title: "Song " + id,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	return g
}

// newCoverEnv is a server whose public origin is pinned, which is what a cover
// link is built from.
// newCoverEnv returns the RAW handler, the server, the owner's id and the
// owner's session cookie.
//
// Deliberately not wrapped with signedIn: the signed route's whole purpose is
// to work without a session, and a wrapper that attaches a cookie to every
// request would make the no-session assertion pass without ever testing it —
// which is exactly how an earlier version of these tests hid the fact that the
// route was still being redirected to /login.
func newCoverEnv(t *testing.T) (http.Handler, *Server, string, string) {
	t.Helper()
	return newCoverEnvAs(t, "cover-owner")
}

// newCoverEnvAs is newCoverEnv with a named account, returning that account's
// real generated id. The harness mints ids rather than using the username, so a
// song has to be owned by the id or owns() correctly refuses it.
func newCoverEnvAs(t *testing.T, username string) (http.Handler, *Server, string, string) {
	t.Helper()
	h, _, srv := newTestEnvWith(t, func(c *config.Config) {
		c.Yue2Endpoint = c.RunPodEndpoint
		c.PublicURL = "https://songs.example.test"
		c.MaxInFlight = 0 // freeze the queue so assertions see what the handler wrote
	})
	u, tok := mkSession(t, srv, username, store.StatusApproved, store.RoleUser)
	return h, srv, u.ID, tok
}

// postCover issues an authenticated cover request, which is what a browser does.
func postCover(t *testing.T, h http.Handler, songID string, form url.Values, tok string) *httptest.ResponseRecorder {
	t.Helper()
	return postFormAs(h, "/songs/"+songID+"/cover", form, tok)
}

// A cover link is the entire authorisation for the signed route, so what it
// does and does not expose is the security surface worth pinning.
func TestSignedLinkServesTheSongAudioWithoutASession(t *testing.T) {
	h, srv, owner, _ := newCoverEnv(t)
	g := mkYue2Song(t, srv, "coverable", owner)

	link, err := srv.mintCoverURL(store.CoverLinkAudio, g.ID)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !strings.HasPrefix(link, "https://songs.example.test/signed/") {
		t.Fatalf("link is not built from MM3_PUBLIC_URL: %q", link)
	}

	// Fetched with no cookie at all, exactly as the worker would.
	res := get(h, strings.TrimPrefix(link, "https://songs.example.test"))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the worker has no session", res.Code)
	}
	if body := res.Body.String(); body != "yue2-audio-coverable" {
		t.Errorf("body = %q, want the song's bytes", body)
	}
	// The worker probes the container itself; a browser must not be able to
	// sniff an uploaded file into something scriptable on this origin.
	if ct := res.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	if res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing nosniff on a route that serves attacker-supplied bytes")
	}
}

// An unknown, lapsed, or malformed token is one indistinguishable 404 —
// otherwise the public route becomes a way to probe which tokens are real.
func TestSignedLinkRefusesEverythingElse(t *testing.T) {
	h, srv, owner, _ := newCoverEnv(t)
	mkYue2Song(t, srv, "real-song", owner)

	// A link minted with a one-nanosecond life is expired by the time it is
	// used. CreateCoverLink refuses a non-positive TTL — correctly, since a
	// link that is dead on arrival is a bug rather than a request — so expiry
	// is exercised by letting a real one lapse rather than by faking it.
	expired, err := srv.st.CreateCoverLink(store.CoverLinkAudio, "real-song", time.Nanosecond)
	if err != nil {
		t.Fatalf("minting a short-lived link: %v", err)
	}
	valid, _ := srv.st.CreateCoverLink(store.CoverLinkAudio, "real-song", time.Hour)

	// The expired token must not be usable, and must be refused the same way an
	// unknown one is.
	if _, _, err := srv.st.CoverLink(expired); err != nil {
		t.Fatalf("expired-link lookup errored: %v", err)
	}

	for _, tc := range []struct{ name, token string }{
		{"unknown", strings.Repeat("a", 64)},
		{"expired", expired},
		{"too short to be real", "abc"},
		// A valid token for a song that no longer exists.
		{"dangling", func() string {
			tok, _ := srv.st.CreateCoverLink(store.CoverLinkAudio, "deleted-song", time.Hour)
			return tok
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := get(h, "/signed/"+tc.token)
			if res.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", res.Code)
			}
		})
	}

	// An empty token is a different case: "/signed/" does not match the route
	// pattern at all, so the middleware protects it as an unclassified path and
	// the request never reaches the handler. Asserting a specific code here
	// would be asserting the middleware's behaviour, not this route's — what
	// matters is only that nothing is served.
	if res := get(h, "/signed/"); res.Code == http.StatusOK {
		t.Error("/signed/ with no token served a body")
	}

	// And the valid one still works, so the refusals above are about the token
	// rather than the route being broken.
	if res := get(h, "/signed/"+valid); res.Code != http.StatusOK {
		t.Errorf("a live token was refused: %d", res.Code)
	}
}

// A token is single-purpose: it serves exactly the artifact it was minted for,
// and nothing else. The URL carries no id, so this is the only thing that ties
// a link to a file.
func TestSignedLinkServesOnlyItsOwnArtifact(t *testing.T) {
	h, srv, owner, _ := newCoverEnv(t)
	mkYue2Song(t, srv, "song-one", owner)
	mkYue2Song(t, srv, "song-two", owner)

	one, _ := srv.mintCoverURL(store.CoverLinkAudio, "song-one")
	two, _ := srv.mintCoverURL(store.CoverLinkAudio, "song-two")

	// Distinct tokens, and each serves its own bytes.
	if one == two {
		t.Fatal("two links for two songs are identical")
	}
	if got := get(h, strings.TrimPrefix(one, "https://songs.example.test")).Body.String(); got != "yue2-audio-song-one" {
		t.Errorf("link one served %q", got)
	}
	if got := get(h, strings.TrimPrefix(two, "https://songs.example.test")).Body.String(); got != "yue2-audio-song-two" {
		t.Errorf("link two served %q", got)
	}
}

// The owner-scoped audio route must stay closed. Widening the allowlist to a
// prefix would have been the easy mistake, and this is what catches it.
func TestAudioRouteStillRequiresASession(t *testing.T) {
	h, srv, owner, _ := newCoverEnv(t)
	g := mkYue2Song(t, srv, "still-protected", owner)

	// The unwrapped handler, with no cookie.
	raw, _, _ := newTestEnvWith(t, func(c *config.Config) { c.PublicURL = "https://songs.example.test" })
	res := get(raw, "/audio/"+g.ID)
	if res.Code == http.StatusOK {
		t.Error("/audio served without a session — signing must not have opened it")
	}
	if strings.Contains(res.Body.String(), "yue2-audio-") {
		t.Error("/audio leaked the song's bytes without a session")
	}
	_ = h
}

// A cover queues a job carrying the engine's own mode, the target style, and a
// fetchable URL — with the source song recorded only when the recording really
// is one of ours.
func TestCoverQueuesAJobWithAFetchableSource(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "cover-me", owner)

	res := postCover(t, h, g.ID, url.Values{
		"instructions": {"jazz-funk, Rhodes piano"},
		"input":        {"[Verse]\nthe words I know are better"},
	}, tok)
	if res.Code >= 400 {
		t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
	}

	jobs, err := srv.st.DequeueQueued(10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("queued %d jobs (err %v), want 1", len(jobs), err)
	}
	j := jobs[0]
	if j.Mode != store.ModeCover {
		t.Errorf("Mode = %q, want cover", j.Mode)
	}
	if j.Engine != store.EngineYue2 {
		t.Errorf("Engine = %q, want the source song's engine", j.Engine)
	}
	if !strings.HasPrefix(j.SourceAudio, "https://songs.example.test/signed/") {
		t.Errorf("SourceAudio is not a fetchable absolute link: %q", j.SourceAudio)
	}
	if j.SourceSongID != g.ID {
		t.Errorf("SourceSongID = %q, want %q — the recording is one of ours", j.SourceSongID, g.ID)
	}
	// User-supplied words win over transcription, which is what the worker's
	// lyrics_supplied flag records.
	if j.Lyrics != "[Verse]\nthe words I know are better" {
		t.Errorf("Lyrics = %q", j.Lyrics)
	}
	// cot is left unset: a cover is melody-conditioned, and the worker forces
	// that itself regardless of what is asked for.
	if j.Cot != "" {
		t.Errorf("Cot = %q, want empty so the worker decides", j.Cot)
	}
}

// Omitting the lyrics is the point of a cover — the worker transcribes them
// from the recording — so a blank box must not be rejected as it would be for a
// create.
func TestCoverAllowsOmittedLyrics(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "instrumental-cover", owner)

	res := postCover(t, h, g.ID, url.Values{
		"instructions": {"slow acoustic, fingerpicked"},
	}, tok)
	if res.Code >= 400 {
		t.Fatalf("a cover with no lyrics was refused: %d %s", res.Code, res.Body.String())
	}
	jobs, _ := srv.st.DequeueQueued(10)
	if len(jobs) != 1 || jobs[0].Lyrics != "" {
		t.Errorf("expected one job with empty lyrics, got %+v", jobs)
	}
}

// A pasted URL is passed through unchanged: the worker fetches it itself, so
// re-serving it through this app would move the bytes twice for nothing.
func TestCoverPassesThroughAPastedURL(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "paste-source", owner)

	res := postCover(t, h, g.ID, url.Values{
		"instructions": {"heavy metal"},
		"source_url":   {"https://example.invalid/original.mp3"},
	}, tok)
	if res.Code >= 400 {
		t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
	}
	j := queuedJob(t, srv)
	if j.SourceAudio != "https://example.invalid/original.mp3" {
		t.Errorf("SourceAudio = %q, want the pasted URL unchanged", j.SourceAudio)
	}
	// A recording that is not ours must not be recorded as derived from this song.
	if j.SourceSongID != "" {
		t.Errorf("SourceSongID = %q, want empty for an external recording", j.SourceSongID)
	}
}

// A pasted value that is not a fetchable address is refused before a job is
// queued, because the worker would only fail on it later.
func TestCoverRefusesAnUnfetchableURL(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "bad-url", owner)

	for _, raw := range []string{"not a url", "ftp://x/y.mp3", "javascript:alert(1)", "//no-scheme/x.mp3"} {
		t.Run(raw, func(t *testing.T) {
			res := postCover(t, h, g.ID, url.Values{
				"instructions": {"pop"}, "source_url": {raw},
			}, tok)
			if res.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %q", res.Code, raw)
			}
		})
	}
	if jobs, _ := srv.st.DequeueQueued(10); len(jobs) != 0 {
		t.Errorf("a refused cover still queued %d jobs", len(jobs))
	}
}

// An upload is staged on the data volume and served by signed link, so a
// full-length recording is not capped by the job payload.
func TestCoverUploadsARecordingAndServesItSigned(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "upload-cover", owner)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("instructions", "lo-fi, warm tape")
	part, _ := mw.CreateFormFile("source_upload", "../../evil name.mp3")
	_, _ = part.Write([]byte("uploaded-recording-bytes"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/songs/"+g.ID+"/cover", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookieFor(tok))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("upload status = %d; body: %s", rec.Code, rec.Body.String())
	}

	j := queuedJob(t, srv)
	if !strings.HasPrefix(j.SourceAudio, "https://songs.example.test/signed/") {
		t.Fatalf("uploaded cover did not get a signed link: %q", j.SourceAudio)
	}
	// The file is served back through that link.
	body := get(h, strings.TrimPrefix(j.SourceAudio, "https://songs.example.test")).Body.String()
	if body != "uploaded-recording-bytes" {
		t.Errorf("signed link served %q, want the uploaded bytes", body)
	}

	// The filename the caller sent decided nothing: it is stored under a fresh
	// random name, so a path in it cannot escape the source directory.
	entries, err := os.ReadDir(srv.sourceDir())
	if err != nil {
		t.Fatalf("reading the source dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one staged upload, found %d", len(entries))
	}
	for _, e := range entries {
		if strings.ContainsAny(e.Name(), `/\`) || strings.Contains(e.Name(), "..") {
			t.Errorf("stored name %q came from the uploaded filename", e.Name())
		}
	}
	// And nothing landed outside the directory.
	if _, err := os.Stat(filepath.Join(filepath.Dir(srv.sourceDir()), "evil name.mp3")); err == nil {
		t.Error("the uploaded filename escaped into the data directory")
	}
}

// Cover is YuE2's mode alone. Queuing one under MiniMax would fail at the
// endpoint with a schema error, long after the user was told it worked.
func TestCoverRefusesANonYue2Song(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkSong(t, srv, "minimax-song", owner, false)

	res := postCover(t, h, g.ID, url.Values{"instructions": {"pop"}}, tok)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a non-YuE2 song", res.Code)
	}
	if jobs, _ := srv.st.DequeueQueued(10); len(jobs) != 0 {
		t.Errorf("a refused cover still queued %d jobs", len(jobs))
	}
}

// A style is required: it is the entire instruction for what the cover should
// become, and without one there is nothing to generate.
func TestCoverNeedsAStyle(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "no-style", owner)

	res := postCover(t, h, g.ID, url.Values{"input": {"words"}}, tok)
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 with no style", res.Code)
	}
}

// Covering is owner-only, and someone else's song is a 404 rather than a 403 —
// otherwise the route reports which ids exist.
func TestCoverIsOwnerOnly(t *testing.T) {
	h, srv, aliceID, _ := newCoverEnvAs(t, "cover-alice")
	_, bobTok := mkSession(t, srv, "cover-bob", store.StatusApproved, store.RoleUser)
	g := mkYue2Song(t, srv, "alices-song", aliceID)

	res := postFormAs(h, "/songs/"+g.ID+"/cover", url.Values{"instructions": {"pop"}}, bobTok)
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for another user's song", res.Code)
	}
}

// The cover panel appears only where it can actually work: on a YuE2 song, and
// only when the engine is configured. A control that fails at the endpoint is
// the shape this project keeps rejecting.
func TestCoverPanelAppearsOnlyWhereItCanWork(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)

	yue2 := mkYue2Song(t, srv, "panel-yue2", owner)
	if body := do(h, "GET", "/songs/"+yue2.ID, cookieFor(tok)).Body.String(); !strings.Contains(body, "COVER") {
		t.Error("a YuE2 song offers no cover panel")
	}

	minimax := mkSong(t, srv, "panel-minimax", owner, false)
	if body := do(h, "GET", "/songs/"+minimax.ID, cookieFor(tok)).Body.String(); strings.Contains(body, "Generate the cover") {
		t.Error("a MiniMax song offers a cover, which its endpoint cannot run")
	}

	// And with the engine unconfigured, no YuE2 song offers it either — the
	// endpoint is simply not there.
	// YuE2 deliberately left unset: the engine is not configured at all.
	off, _, offSrv := newTestEnvWith(t, func(c *config.Config) { c.MaxInFlight = 0 })
	offOwner, offTok := mkSession(t, offSrv, "panel-no-engine-user", store.StatusApproved, store.RoleUser)
	g := mkYue2Song(t, offSrv, "panel-no-engine", offOwner.ID)
	if body := do(off, "GET", "/songs/"+g.ID, cookieFor(offTok)).Body.String(); strings.Contains(body, "Generate the cover") {
		t.Error("the cover panel appeared with no YuE2 endpoint configured")
	}
}

// A cover is guided by default, because unguided the transcribed melody wins:
// a rock ballad covered as 80s synth-pop came back sounding like the original.
// Balanced (3) is the preset that took the new sound with every word intact.
func TestCoverIsGuidedByDefault(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, srv, "guided-cover", owner)

	res := postCover(t, h, g.ID, url.Values{"instructions": {"80s synth-pop"}}, tok)
	if res.Code >= 400 {
		t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
	}
	jobs, _ := srv.st.DequeueQueued(10)
	if len(jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(jobs))
	}
	if jobs[0].CfgScale == nil || *jobs[0].CfgScale != 3 {
		t.Errorf("CfgScale = %v, want the balanced preset (3)", jobs[0].CfgScale)
	}
}

// Every preset stores what it names, off stores nothing, and a value the form
// never offers is refused before a paid job is queued for it.
func TestCoverStyleStrengthPresets(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  *float64
	}{
		{"off", nil},
		{"balanced", ptrTo(3.0)},
		{"strong", ptrTo(4.5)},
	} {
		t.Run(tc.value, func(t *testing.T) {
			h, srv, owner, tok := newCoverEnvAs(t, "strength-"+tc.value)
			g := mkYue2Song(t, srv, "strength-"+tc.value, owner)
			res := postCover(t, h, g.ID, url.Values{
				"instructions": {"synth-pop"}, "style_strength": {tc.value},
			}, tok)
			if res.Code >= 400 {
				t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
			}
			jobs, _ := srv.st.DequeueQueued(10)
			if len(jobs) != 1 {
				t.Fatalf("queued %d jobs, want 1", len(jobs))
			}
			got := jobs[0].CfgScale
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("CfgScale = %v, want %v", got, tc.want)
			}
		})
	}

	h, srv, owner, tok := newCoverEnvAs(t, "strength-bogus")
	g := mkYue2Song(t, srv, "strength-bogus", owner)
	res := postCover(t, h, g.ID, url.Values{
		"instructions": {"synth-pop"}, "style_strength": {"11"},
	}, tok)
	if res.Code != http.StatusBadRequest {
		t.Errorf("an unknown strength = %d, want 400", res.Code)
	}
	if jobs, _ := srv.st.DequeueQueued(10); len(jobs) != 0 {
		t.Errorf("an unknown strength still queued %d jobs", len(jobs))
	}
}

// The seed is the source's unless a new one is asked for — with the melody
// locked too, a copied seed left no way to get a different take.
func TestCoverSeedIsCopiedUnlessANewOneIsAsked(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	seed := int64(48)
	g := &store.Song{
		ID: "seeded-cover", JobID: "job-seeded-cover", UserID: owner,
		Lyrics: "[Verse]\nla", Caption: "rock ballad", Seed: &seed,
		Engine: store.EngineYue2, Mode: store.ModeCreate, Delivery: "s3",
		AudioPath: filepath.Join(srv.cfg.AudioDir, "seeded-cover.m4a"),
		CreatedAt: time.Now().UTC(),
	}
	if err := srv.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}

	postCover(t, h, g.ID, url.Values{"instructions": {"synth-pop"}}, tok)
	postCover(t, h, g.ID, url.Values{"instructions": {"synth-pop"}, "new_seed": {"1"}}, tok)
	jobs, _ := srv.st.DequeueQueued(10)
	if len(jobs) != 2 {
		t.Fatalf("queued %d jobs, want 2", len(jobs))
	}
	if jobs[0].Seed == nil || *jobs[0].Seed != seed {
		t.Errorf("default seed = %v, want the source's %d", jobs[0].Seed, seed)
	}
	if jobs[1].Seed == nil || *jobs[1].Seed == seed || *jobs[1].Seed < 0 {
		t.Errorf("new seed = %v, want a fresh non-negative one", jobs[1].Seed)
	}
}
