package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// postFormAs issues a form POST carrying a session cookie.
func postFormAs(h http.Handler, path string, form url.Values, token string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.AddCookie(cookieFor(token))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

// mkSong writes a song owned by userID with a real file on disk, so audio
// streaming is exercised rather than 404ing on a missing file.
func mkSong(t *testing.T, s *Server, id, userID string, public bool) *store.Song {
	t.Helper()
	if err := os.MkdirAll(s.cfg.AudioDir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.cfg.AudioDir, id+".m4a")
	if err := os.WriteFile(path, []byte("fake-audio-bytes-"+id), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &store.Song{
		ID: id, JobID: "job-" + id, UserID: userID, IsPublic: public,
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "stub",
		Delivery: "base64", AudioPath: path, Title: "Song " + id,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	return g
}

func setPublic(h http.Handler, id, token string, public string) *httptest.ResponseRecorder {
	return postFormAs(h, "/songs/"+id+"/toggle-public", url.Values{"public": {public}}, token)
}

// storedSong reads a song back as its owner, failing the test if it is gone.
func storedSong(t *testing.T, s *Server, id, ownerID string) *store.Song {
	t.Helper()
	g, err := s.st.Song(id, store.UserAccess(ownerID))
	if err != nil || g == nil {
		t.Fatalf("song %s is gone: %#v, err=%v", id, g, err)
	}
	return g
}

// nonOwnerWritesRefused tries every write on a song as someone who does not own
// it. Each must answer 404 — the same as a song that is not there — and public
// is the sharing target to attempt.
func nonOwnerWritesRefused(t *testing.T, h http.Handler, id, token, public string) {
	t.Helper()
	for what, res := range map[string]*httptest.ResponseRecorder{
		"delete": do(h, "DELETE", "/songs/"+id, cookieFor(token)),
		"rename": postFormAs(h, "/songs/"+id+"/title", url.Values{"title": {"Bob Was Here"}}, token),
		"share":  setPublic(h, id, token, public),
	} {
		if res.Code != http.StatusNotFound {
			t.Errorf("non-owner %s of %s = %d, want 404", what, id, res.Code)
		}
	}
}

// TestSongIsolationBetweenUsers: one user's songs are invisible and immutable
// to another across every surface.
func TestSongIsolationBetweenUsers(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "iso-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "iso-bob", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "alice-private", alice.ID, false)

	// Every read surface answers 404 — the same as a song that is not there.
	for _, path := range []string{
		"/songs/alice-private", "/audio/alice-private",
	} {
		res := do(h, "GET", path, cookieFor(bobTok))
		if res.Code != http.StatusNotFound {
			t.Errorf("bob GET %s = %d, want 404", path, res.Code)
		}
		if strings.Contains(res.Body.String(), "fake-audio-bytes") {
			t.Errorf("bob GET %s streamed the audio", path)
		}
	}

	// Bob's own library never contains it.
	res := do(h, "GET", "/history", cookieFor(bobTok))
	if res.Code != 200 {
		t.Fatalf("bob /history = %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "alice-private") {
		t.Error("alice's song appeared in bob's history")
	}

	// And no write surface touches it.
	nonOwnerWritesRefused(t, h, "alice-private", bobTok, "1")

	// Nothing changed.
	if g := storedSong(t, s, "alice-private", alice.ID); g.Title != "Song alice-private" || g.IsPublic {
		t.Fatalf("alice's song was modified: %#v", g)
	}
}

// TestToggleSharesAndUnshares is the happy path plus the revocation the lead
// called out: un-publishing must take effect on the very next request.
func TestToggleSharesAndUnshares(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "tog-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "tog-bob", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "song-x", alice.ID, false)

	// Private: bob cannot stream it.
	if res := do(h, "GET", "/audio/song-x", cookieFor(bobTok)); res.Code != http.StatusNotFound {
		t.Fatalf("bob streamed a private song: %d", res.Code)
	}

	// Alice shares it.
	if res := setPublic(h, "song-x", aliceTok, "1"); res.Code != http.StatusSeeOther {
		t.Fatalf("share = %d, want 303; body=%s", res.Code, res.Body.String())
	}
	if !storedSong(t, s, "song-x", alice.ID).IsPublic {
		t.Fatal("share did not set is_public")
	}

	// Bob can now stream it, bytes and all.
	res := do(h, "GET", "/audio/song-x", cookieFor(bobTok))
	if res.Code != 200 {
		t.Fatalf("bob GET shared audio = %d, want 200", res.Code)
	}
	if !strings.Contains(res.Body.String(), "fake-audio-bytes-song-x") {
		t.Fatal("shared audio did not stream")
	}

	// Alice un-shares it.
	if res := setPublic(h, "song-x", aliceTok, "0"); res.Code != http.StatusSeeOther {
		t.Fatalf("unshare = %d", res.Code)
	}

	// Revocation is immediate: bob's very next request is refused, with no
	// cached handle and no still-valid URL.
	after := do(h, "GET", "/audio/song-x", cookieFor(bobTok))
	if after.Code != http.StatusNotFound {
		t.Fatalf("bob still streamed after un-publish: %d", after.Code)
	}
	if strings.Contains(after.Body.String(), "fake-audio-bytes") {
		t.Fatal("audio bytes served after un-publish")
	}
	if res := do(h, "GET", "/songs/song-x", cookieFor(bobTok)); res.Code != http.StatusNotFound {
		t.Fatalf("bob still read the detail page after un-publish: %d", res.Code)
	}
}

// TestAudioIsNotReachableOutsideTheAuthorisedRoute closes the "still-valid
// URL" question at its root: the audio directory must not be served by any
// other route, or un-publishing would be cosmetic.
func TestAudioIsNotReachableOutsideTheAuthorisedRoute(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "path-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "path-bob", store.StatusApproved, store.RoleUser)
	g := mkSong(t, s, "hidden-song", alice.ID, false)

	// The audio directory must not live under the static root.
	staticRoot, err := filepath.Abs(filepath.Join(s.cfg.WebDir, "static"))
	if err != nil {
		t.Fatal(err)
	}
	audioPath, err := filepath.Abs(g.AudioPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(audioPath, staticRoot+string(filepath.Separator)) {
		t.Fatalf("audio %q lives under the static root %q and is world-readable",
			audioPath, staticRoot)
	}

	// And guessing at static paths gets nothing.
	base := filepath.Base(g.AudioPath)
	for _, path := range []string{
		"/static/" + base,
		"/static/audio/" + base,
		"/static/../audio/" + base,
	} {
		res := do(h, "GET", path, cookieFor(bobTok))
		if res.Code == 200 && strings.Contains(res.Body.String(), "fake-audio-bytes") {
			t.Errorf("audio reachable at %s", path)
		}
	}
}

// TestSharingGrantsReadingNeverWriting: a public song is readable by anyone
// signed in, and mutable by nobody but its owner.
func TestSharingGrantsReadingNeverWriting(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "share-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "share-bob", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "public-song", alice.ID, true)

	// Bob reads it.
	if res := do(h, "GET", "/songs/public-song", cookieFor(bobTok)); res.Code != 200 {
		t.Fatalf("bob cannot read a public song: %d", res.Code)
	}
	if res := do(h, "GET", "/audio/public-song", cookieFor(bobTok)); res.Code != 200 {
		t.Fatalf("bob cannot stream a public song: %d", res.Code)
	}

	// But cannot write to it — including un-sharing it, which would be a
	// denial-of-service on someone else's song.
	nonOwnerWritesRefused(t, h, "public-song", bobTok, "0")

	if g := storedSong(t, s, "public-song", alice.ID); !g.IsPublic || g.Title != "Song public-song" {
		t.Fatalf("a non-owner changed a public song: %#v", g)
	}

	// The rendered page offers bob no owner controls.
	body := do(h, "GET", "/songs/public-song", cookieFor(bobTok)).Body.String()
	for _, control := range []string{"toggle-public", "hx-delete"} {
		if strings.Contains(body, control) {
			t.Errorf("a non-owner was shown the %q control", control)
		}
	}
}

// TestToggleIsIdempotentAndExplicit covers the concurrency semantics: the
// endpoint sets a target rather than flipping, so repeating a request is a
// no-op instead of an undo.
func TestToggleIsIdempotentAndExplicit(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "idem-alice", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "idem-song", alice.ID, false)

	isPublic := func() bool { return storedSong(t, s, "idem-song", alice.ID).IsPublic }

	// Two clients both sharing leaves it shared — not shared then un-shared,
	// which is what a blind flip would produce. Then the same for un-sharing.
	for _, target := range []string{"1", "1", "1", "0", "0", "0"} {
		if res := setPublic(h, "idem-song", aliceTok, target); res.Code != http.StatusSeeOther {
			t.Fatalf("public=%s = %d", target, res.Code)
		}
		if isPublic() != (target == "1") {
			t.Fatalf("after public=%s the song is public=%v", target, isPublic())
		}
	}

	// The target is required, not inferred — a missing field included.
	for _, form := range []url.Values{
		{"public": {""}}, {"public": {"maybe"}}, {"public": {"2"}}, {"public": {"null"}}, {},
	} {
		res := postFormAs(h, "/songs/idem-song/toggle-public", form, aliceTok)
		if res.Code != http.StatusBadRequest {
			t.Errorf("form %q = %d, want 400", form.Encode(), res.Code)
		}
	}
	if isPublic() {
		t.Fatal("a rejected request still changed the song")
	}

	// Accepted spellings, each reset to private before the next.
	for _, ok := range []string{"1", "true", "on", "yes"} {
		if res := setPublic(h, "idem-song", aliceTok, ok); res.Code != http.StatusSeeOther {
			t.Errorf("public=%q = %d", ok, res.Code)
		}
		if !isPublic() {
			t.Errorf("public=%q did not share", ok)
		}
		if res := setPublic(h, "idem-song", aliceTok, "0"); res.Code != http.StatusSeeOther {
			t.Fatalf("reset = %d", res.Code)
		}
	}
}

// TestToggleConcurrentWritesConverge: many simultaneous requests to the same
// target must not corrupt the row or lose an update.
func TestToggleConcurrentWritesConverge(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "race-alice", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "race-song", alice.ID, false)

	const n = 16
	done := make(chan int, n)
	for i := 0; i < n; i++ {
		go func() { done <- setPublic(h, "race-song", aliceTok, "1").Code }()
	}
	for i := 0; i < n; i++ {
		if code := <-done; code != http.StatusSeeOther {
			t.Errorf("concurrent share = %d", code)
		}
	}
	if !storedSong(t, s, "race-song", alice.ID).IsPublic {
		t.Fatal("16 concurrent 'share' requests left the song private")
	}
}

// TestAdminCanToggleAnySong: administrators are the documented exception.
func TestAdminCanToggleAnySong(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "adm-alice", store.StatusApproved, store.RoleUser)
	_, adminTok := mkSession(t, s, "adm-root", store.StatusApproved, store.RoleAdmin)
	mkSong(t, s, "adm-song", alice.ID, false)

	if res := setPublic(h, "adm-song", adminTok, "1"); res.Code != http.StatusSeeOther {
		t.Fatalf("admin share = %d", res.Code)
	}
	if !storedSong(t, s, "adm-song", alice.ID).IsPublic {
		t.Fatal("admin share did not take effect")
	}
	if res := do(h, "GET", "/audio/adm-song", cookieFor(adminTok)); res.Code != 200 {
		t.Fatalf("admin cannot stream: %d", res.Code)
	}
}

// TestToggleRequiresASession: the new route is covered by default-deny.
func TestToggleRequiresASession(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "anon-alice", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "anon-song", alice.ID, false)

	res := postFormAs(h, "/songs/anon-song/toggle-public", url.Values{"public": {"1"}}, "")
	if !denied(res) {
		t.Fatalf("anonymous toggle = %d, want a refusal", res.Code)
	}
	if storedSong(t, s, "anon-song", alice.ID).IsPublic {
		t.Fatal("an anonymous request shared a song")
	}
}

// TestGeneratedSongInheritsItsOwner verifies end to end what Stages 01 and 03
// wired: the job carries the creator, and the worker copies it onto the song.
func TestGeneratedSongInheritsItsOwner(t *testing.T) {
	h, up, s := newTestEnvWith(t, nil)
	u, token := mkSession(t, s, "gen-user", store.StatusApproved, store.RoleUser)

	form := url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop. Vocal Details: soft. Arrangement: guitar."},
		"audio_duration": {"30"},
		"seed":           {"7"},
	}
	res := postFormAs(h, "/jobs", form, token)
	if res.Code != 200 {
		t.Fatalf("POST /jobs = %d: %s", res.Code, res.Body.String())
	}
	up.completeFirstRun(t)

	// The job is owned by the creator, not by the legacy owner.
	waitUntil(t, 20*time.Second, func() bool {
		songs, err := s.st.Songs(10, 0, store.UserAccess(u.ID))
		return err == nil && len(songs) == 1
	}, "song persisted with the creator as owner")

	songs, err := s.st.Songs(10, 0, store.UserAccess(u.ID))
	if err != nil || len(songs) != 1 {
		t.Fatalf("songs for creator = %d, err=%v", len(songs), err)
	}
	if songs[0].UserID != u.ID {
		t.Fatalf("song owner = %q, want %q", songs[0].UserID, u.ID)
	}
	if songs[0].IsPublic {
		t.Fatal("a newly generated song defaulted to public")
	}
	// And it is invisible to anyone else.
	_, otherTok := mkSession(t, s, "gen-other", store.StatusApproved, store.RoleUser)
	if res := do(h, "GET", "/songs/"+songs[0].ID, cookieFor(otherTok)); res.Code != http.StatusNotFound {
		t.Fatalf("another user read a freshly generated song: %d", res.Code)
	}
}

// TestCrossOriginWritesAreRefused covers the CSRF defence, including the
// same-site case that SameSite=Lax alone does not stop.
func TestCrossOriginWritesAreRefused(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "csrf-alice", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "csrf-song", alice.ID, false)

	refused := []struct{ name, header, value string }{
		{"cross-site fetch metadata", "Sec-Fetch-Site", "cross-site"},
		{"same-site sibling host", "Sec-Fetch-Site", "same-site"},
		{"foreign origin", "Origin", "https://evil.example.com"},
	}
	for _, c := range refused {
		res := postFormAs(h, "/songs/csrf-song/toggle-public",
			url.Values{"public": {"1"}}, aliceTok, c.header, c.value)
		if res.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", c.name, res.Code)
		}
		if storedSong(t, s, "csrf-song", alice.ID).IsPublic {
			t.Fatalf("%s changed the song", c.name)
		}
	}

	allowed := []struct{ name, header, value string }{
		{"same-origin fetch metadata", "Sec-Fetch-Site", "same-origin"},
		{"typed navigation", "Sec-Fetch-Site", "none"},
		{"matching origin", "Origin", "http://example.com"},
	}
	for _, c := range allowed {
		res := postFormAs(h, "/songs/csrf-song/toggle-public",
			url.Values{"public": {"1"}}, aliceTok, c.header, c.value)
		if res.Code != http.StatusSeeOther {
			t.Errorf("%s = %d, want 303", c.name, res.Code)
		}
	}

	// Reads are never refused by the origin check.
	if res := do(h, "GET", "/history", cookieFor(aliceTok), "Sec-Fetch-Site", "cross-site"); res.Code != 200 {
		t.Errorf("a cross-site GET was refused: %d", res.Code)
	}
	// Login is protected too — forging a login is a real attack.
	if res := postFormAs(h, "/login",
		url.Values{"username": {"x"}, "password": {"y"}}, "",
		"Sec-Fetch-Site", "cross-site"); res.Code != http.StatusForbidden {
		t.Errorf("cross-site login = %d, want 403", res.Code)
	}
}
