package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// mkSession inserts a user and a live session, returning the raw token.
func mkSession(t *testing.T, s *Server, username, status, role string) (*store.User, string) {
	t.Helper()
	u := &store.User{ID: newUserID(), Username: username,
		PasswordHash: "$2a$04$" + strings.Repeat("x", 53),
		Status:       status, Role: role}
	if err := s.st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.st.CreateSession(token, &store.Session{
		UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return u, token
}

func cookieFor(token string) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: token}
}

// do issues a request with optional cookies and headers.
func do(h http.Handler, method, path string, c *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	return res
}

// denied reports whether a response refused the request rather than serving it.
func denied(res *httptest.ResponseRecorder) bool {
	switch res.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError:
		return true
	case http.StatusSeeOther, http.StatusFound, http.StatusTemporaryRedirect:
		return strings.HasPrefix(res.Header().Get("Location"), "/login")
	}
	return false
}

// concretePath turns a route pattern into a requestable path.
func concretePath(pattern string) (method, path string) {
	method, path = http.MethodGet, pattern
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		method, path = pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	path = strings.ReplaceAll(path, "{$}", "")
	// Substitute a wildcard for each path parameter.
	for strings.Contains(path, "{") {
		open := strings.IndexByte(path, '{')
		close := strings.IndexByte(path[open:], '}')
		if close < 0 {
			break
		}
		path = path[:open] + "probe-id" + path[open+close+1:]
	}
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasSuffix(path, "/") && len(path) > 1 {
		path += "probe"
	}
	return method, path
}

// TestEveryRouteIsProtectedUnlessExplicitlyPublic is the default-deny proof.
//
// It walks the *real* registered route table — recorded by the router as
// Routes() builds it, so it cannot drift from what the server actually serves
// — and fires an unauthenticated request at each one. Anything not in the
// public allowlist must refuse. A route added tomorrow and forgotten is
// therefore covered by this test the moment it is registered.
func TestEveryRouteIsProtectedUnlessExplicitlyPublic(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	if len(s.routes) == 0 {
		t.Fatal("no routes recorded — the enumeration is vacuous")
	}

	public := map[string]bool{}
	for _, p := range publicPatterns {
		public[p] = true
	}

	var checked int
	for _, pattern := range s.routes {
		method, path := concretePath(pattern)
		checked++
		if public[pattern] {
			// Assert classification rather than the response: a public
			// handler may legitimately answer 401 (bad credentials) or
			// redirect to /login (logout), which is not the middleware
			// refusing. The point here is that it was never asked to.
			req := httptest.NewRequest(method, path, nil)
			if got := s.levelFor(req); got != accessPublic {
				t.Errorf("public route %q classified as %v", pattern, got)
			}
			continue
		}
		if res := do(h, method, path, nil); !denied(res) {
			t.Errorf("route %q is reachable without a session: %d %s",
				pattern, res.Code, res.Header().Get("Location"))
		}
	}
	if checked != len(s.routes) {
		t.Fatalf("checked %d of %d routes", checked, len(s.routes))
	}
	t.Logf("checked %d registered routes", checked)
}

// TestPublicAllowlistIsExactlyThis is the second half of the default-deny
// guard. The middleware reads publicPatterns; this test keeps an independent
// copy. Making a route public therefore requires editing two files, one of
// which is a test a reviewer reads — it can never happen as a silent
// side effect of adding a handler.
func TestPublicAllowlistIsExactlyThis(t *testing.T) {
	want := []string{
		"GET /favicon.ico",
		"GET /healthz",
		"GET /login",
		"GET /register",
		"GET /static/",
		"POST /login",
		"POST /logout",
		"POST /register",
	}
	got := append([]string(nil), publicPatterns...)
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the public allowlist changed.\n got: %v\nwant: %v\n"+
			"If this is intentional, confirm the new route really must be "+
			"reachable without a session, then update this list.", got, want)
	}
}

// TestUnclassifiedRouteDefaultsToProtected proves the default itself, rather
// than the current route table: a pattern nobody classified demands a session.
func TestUnclassifiedRouteDefaultsToProtected(t *testing.T) {
	_, _, s := newTestEnvWith(t, nil)
	for _, path := range []string{
		"/some-route-invented-tomorrow",
		"/admin",
		"/admin/users/x/approve",
		"/api/anything",
		"/deeply/nested/new/thing",
	} {
		r := httptest.NewRequest("GET", path, nil)
		if got := s.levelFor(r); got == accessPublic {
			t.Errorf("unclassified path %q classified as public", path)
		}
	}
	// And admin prefixes demand admin, not merely a session.
	for _, path := range []string{"/admin", "/admin/users/x/approve"} {
		r := httptest.NewRequest("GET", path, nil)
		if got := s.levelFor(r); got != accessAdmin {
			t.Errorf("path %q level = %v, want accessAdmin", path, got)
		}
	}
}

func TestUnauthenticatedBrowserRedirectsToLoginWithNext(t *testing.T) {
	h, _, _ := newTestEnvWith(t, nil)
	res := do(h, "GET", "/history?page=2", nil)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("GET /history = %d, want 303", res.Code)
	}
	loc, err := url.Parse(res.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Path != "/login" {
		t.Fatalf("redirect to %q", loc.Path)
	}
	if got := loc.Query().Get("next"); got != "/history?page=2" {
		t.Fatalf("next = %q, want /history?page=2", got)
	}
	if got := loc.Query().Get("notice"); got != noticeKeySignIn {
		t.Fatalf("notice = %q", got)
	}
}

func TestUnauthenticatedAPIGetsJSON(t *testing.T) {
	h, _, _ := newTestEnvWith(t, nil)
	res := do(h, "POST", "/jobs", nil, "Accept", "application/json")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("= %d, want 401", res.Code)
	}
	if ct := res.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, res.Body.String())
	}
	if body["error"] != "unauthenticated" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestUnauthenticatedHTMXGetsRedirectHeader(t *testing.T) {
	h, _, _ := newTestEnvWith(t, nil)
	res := do(h, "GET", "/jobs/abc", nil, "HX-Request", "true")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("= %d, want 401", res.Code)
	}
	if loc := res.Header().Get("HX-Redirect"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("HX-Redirect = %q", loc)
	}
}

// TestStatusIsEnforcedOnEveryRequest: a user disabled mid-session must be
// refused on their very next request, without waiting for the cookie to lapse.
func TestStatusIsEnforcedOnEveryRequest(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	u, token := mkSession(t, s, "live-user", store.StatusApproved, store.RoleUser)

	if res := do(h, "GET", "/history", cookieFor(token)); res.Code != 200 {
		t.Fatalf("approved user = %d, want 200", res.Code)
	}
	// Disable the account. The session row is left in place deliberately: the
	// point is that status resolves live, not that the row was revoked.
	if err := s.st.UpdateUserStatus(u.ID, store.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	res := do(h, "GET", "/history", cookieFor(token))
	if !denied(res) {
		t.Fatalf("disabled user still served: %d", res.Code)
	}

	// A pending user is refused too, and told why.
	_, ptok := mkSession(t, s, "pending-user", store.StatusPending, store.RoleUser)
	res = do(h, "GET", "/history", cookieFor(ptok))
	if res.Code != http.StatusForbidden && !denied(res) {
		t.Fatalf("pending user = %d", res.Code)
	}
	if loc := res.Header().Get("Location"); !strings.Contains(loc, noticeKeyPending) {
		t.Fatalf("pending redirect = %q, want a pending notice", loc)
	}
}

// TestRevokedSessionIsRefusedAndCookieCleared covers the expired/unknown path.
func TestRevokedSessionIsRefusedAndCookieCleared(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, token := mkSession(t, s, "revoked-user", store.StatusApproved, store.RoleUser)
	if err := s.st.DeleteSession(token); err != nil {
		t.Fatal(err)
	}
	res := do(h, "GET", "/history", cookieFor(token))
	if !denied(res) {
		t.Fatalf("revoked session served: %d", res.Code)
	}
	var cleared bool
	for _, c := range (&http.Response{Header: res.Header()}).Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("dead session cookie was not cleared")
	}
	// A garbage token is refused the same way.
	if res := do(h, "GET", "/history", cookieFor("not-a-real-token-at-all-xxxxxxxx")); !denied(res) {
		t.Fatalf("garbage token served: %d", res.Code)
	}
}

// TestAdminRoutesRequireAdmin: the /admin prefix is admin-only before Stage 06
// registers a single handler there, so those routes cannot ship unprotected.
func TestAdminRoutesRequireAdmin(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, userTok := mkSession(t, s, "plain-user", store.StatusApproved, store.RoleUser)
	_, adminTok := mkSession(t, s, "admin-user", store.StatusApproved, store.RoleAdmin)

	for _, path := range []string{"/admin", "/admin/users/x/approve"} {
		// Anonymous: refused.
		if res := do(h, "GET", path, nil); !denied(res) {
			t.Errorf("anonymous reached %s: %d", path, res.Code)
		}
		// Approved but not admin: 403, and specifically not a 404 that would
		// reveal whether the route exists.
		res := do(h, "GET", path, cookieFor(userTok))
		if res.Code != http.StatusForbidden {
			t.Errorf("ordinary user %s = %d, want 403", path, res.Code)
		}
		// Admin: clears the middleware. No handler is registered yet, so the
		// mux answers 404 — which is exactly the proof that the refusal above
		// came from access control and not from a missing route.
		if res := do(h, "GET", path, cookieFor(adminTok)); res.Code != http.StatusNotFound {
			t.Errorf("admin %s = %d, want 404 (past the middleware)", path, res.Code)
		}
	}
}

// TestNonOwnerCannotReachAnotherUsersSong is the audio proof: /audio/{id}
// streams bytes, so a non-owner must get nothing for a private song.
func TestNonOwnerCannotReachAnotherUsersSong(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "bob", store.StatusApproved, store.RoleUser)

	now := time.Now().UTC()
	song := &store.Song{ID: "private-song", JobID: "private-job", UserID: alice.ID,
		IsPublic: false, Lyrics: "la", Caption: "pop", Duration: 30,
		Engine: "stub", Delivery: "base64", AudioPath: "/tmp/nope.m4a",
		Title: "Alice Private", CreatedAt: now}
	if err := s.st.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	// Bob: every read surface must refuse, identically to a missing song.
	for _, c := range []struct{ method, path string }{
		{"GET", "/audio/private-song"},
		{"GET", "/songs/private-song"},
	} {
		res := do(h, c.method, c.path, cookieFor(bobTok))
		if res.Code != http.StatusNotFound {
			t.Errorf("bob %s %s = %d, want 404", c.method, c.path, res.Code)
		}
		if strings.Contains(res.Body.String(), "Alice Private") {
			t.Errorf("bob %s %s leaked the song title", c.method, c.path)
		}
	}
	// And a missing song answers the same way, so the endpoint cannot be used
	// to learn which ids exist.
	missing := do(h, "GET", "/audio/no-such-song", cookieFor(bobTok))
	present := do(h, "GET", "/audio/private-song", cookieFor(bobTok))
	if missing.Code != present.Code {
		t.Errorf("existence is observable: missing=%d private=%d",
			missing.Code, present.Code)
	}

	// Alice reaches her own song's metadata page.
	if res := do(h, "GET", "/songs/private-song", cookieFor(aliceTok)); res.Code != 200 {
		t.Fatalf("owner denied their own song: %d", res.Code)
	}

	// Bob cannot destroy or rename it either.
	if res := do(h, "DELETE", "/songs/private-song", cookieFor(bobTok)); res.Code == 200 {
		t.Error("bob deleted alice's song")
	}
	if got, err := s.st.Song("private-song", store.UserAccess(alice.ID)); err != nil || got == nil {
		t.Fatalf("alice's song was destroyed: %#v, err=%v", got, err)
	}

	// A song alice shared publicly *is* readable by bob — the public rule
	// applies, so the 404s above are ownership working, not the route being
	// broken for everyone.
	shared := &store.Song{ID: "shared-song", JobID: "shared-job", UserID: alice.ID,
		IsPublic: true, Lyrics: "la", Caption: "pop", Duration: 30,
		Engine: "stub", Delivery: "base64", AudioPath: "/tmp/shared.m4a",
		Title: "Alice Shared", CreatedAt: now}
	if err := s.st.CreateSong(shared); err != nil {
		t.Fatal(err)
	}
	if res := do(h, "GET", "/songs/shared-song", cookieFor(bobTok)); res.Code != 200 {
		t.Errorf("shared song not readable by another user: %d", res.Code)
	}
	// But sharing grants reading, never writing.
	if res := do(h, "DELETE", "/songs/shared-song", cookieFor(bobTok)); res.Code == 200 {
		t.Error("bob deleted a song that was merely shared with him")
	}
	if got, err := s.st.Song("shared-song", store.UserAccess(alice.ID)); err != nil || got == nil {
		t.Fatalf("shared song was destroyed by a non-owner: %#v, err=%v", got, err)
	}
}

func TestSafeNextRejectsOffsiteTargets(t *testing.T) {
	bad := []string{
		"https://evil.com",
		"http://evil.com/path",
		"//evil.com",
		"//evil.com/path",
		`/\evil.com`,
		`/\/evil.com`,
		"/%2f%2fevil.com",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"evil.com",
		"../etc/passwd",
		"/..//evil.com",
		"/history\n\rLocation: https://evil.com",
		"/history\x00",
		"\t//evil.com",
		"/login",    // loops
		"/register", // loops
		"/logout",   // would sign the user straight back out
		strings.Repeat("/a", 400),
		"",
	}
	for _, in := range bad {
		if got := safeNext(in); got != "" {
			t.Errorf("safeNext(%q) = %q, want \"\"", in, got)
		}
	}

	good := map[string]string{
		"/":                   "/",
		"/history":            "/history",
		"/history?page=2":     "/history?page=2",
		"/songs/abc123":       "/songs/abc123",
		"/audio/abc?x=1&y=2":  "/audio/abc?x=1&y=2",
		"/songs/with%20space": "/songs/with%20space",
	}
	for in, want := range good {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLoginHonoursOnlySafeNext: the round trip, not just the helper.
func TestLoginHonoursOnlySafeNext(t *testing.T) {
	h, s := newAuthEnv(t)
	u := mkUser(t, s, "next-user", fxUserPass, store.StatusApproved)

	res := postFormCookie(h, "/login", url.Values{
		"username": {u.Username}, "password": {fxUserPass},
		"next": {"/history?page=3"}})
	if res.Code != http.StatusSeeOther {
		t.Fatalf("login = %d", res.Code)
	}
	if loc := res.Header().Get("Location"); loc != "/history?page=3" {
		t.Fatalf("Location = %q, want /history?page=3", loc)
	}

	// A hostile next is dropped and the user lands on the app root.
	for _, hostile := range []string{"https://evil.com", "//evil.com", `/\evil.com`} {
		res := postFormCookie(h, "/login", url.Values{
			"username": {u.Username}, "password": {fxUserPass},
			"next": {hostile}})
		loc := res.Header().Get("Location")
		if loc != "/" {
			t.Errorf("next=%q redirected to %q, want /", hostile, loc)
		}
	}
}

// TestLoginPageDoesNotReflectHostileNext guards the hidden form field.
func TestLoginPageDoesNotReflectHostileNext(t *testing.T) {
	h, _, _ := newTestEnvWith(t, nil)
	for _, hostile := range []string{"https://evil.com", "//evil.com"} {
		res := do(h, "GET", "/login?next="+url.QueryEscape(hostile), nil)
		if strings.Contains(res.Body.String(), "evil.com") {
			t.Errorf("login page reflected %q into the form", hostile)
		}
	}
	// A safe next does survive into the form.
	res := do(h, "GET", "/login?next=%2Fhistory", nil)
	if !strings.Contains(res.Body.String(), `name="next" value="/history"`) {
		t.Error("safe next not carried into the login form")
	}
}

// TestCallerScopeMatchesSession pins the seam: the Access a handler sees is
// derived from the session, and is admin only for an admin.
func TestCallerScopeMatchesSession(t *testing.T) {
	_, _, s := newTestEnvWith(t, nil)

	// No user in context at all: the zero Access, which owns nothing.
	bare := httptest.NewRequest("GET", "/", nil)
	if got := s.caller(bare); got.UserID != "" || got.Admin {
		t.Fatalf("caller without a session = %#v, want the zero Access", got)
	}

	user := httptest.NewRequest("GET", "/", nil)
	user = user.WithContext(withUser(user.Context(),
		&UserContext{UserID: "u1", Username: "u", IsAdmin: false}))
	if got := s.caller(user); got.UserID != "u1" || got.Admin {
		t.Fatalf("ordinary caller = %#v", got)
	}

	admin := httptest.NewRequest("GET", "/", nil)
	admin = admin.WithContext(withUser(admin.Context(),
		&UserContext{UserID: "a1", Username: "a", IsAdmin: true}))
	if got := s.caller(admin); got.UserID != "a1" || !got.Admin {
		t.Fatalf("admin caller = %#v", got)
	}
}
