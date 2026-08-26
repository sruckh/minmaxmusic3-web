package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// journey drives the app the way a browser does: one cookie jar per person,
// following nothing automatically so every hop is asserted explicitly.
type journey struct {
	t     *testing.T
	h     http.Handler
	name  string
	token string
}

func newJourney(t *testing.T, h http.Handler, name string) *journey {
	return &journey{t: t, h: h, name: name}
}

func (j *journey) req(method, path string, form url.Values) *httptest.ResponseRecorder {
	j.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	// A real browser sends this on same-origin navigation; the origin check
	// depends on it, so the journey behaves like one.
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if j.token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: j.token})
	}
	res := httptest.NewRecorder()
	j.h.ServeHTTP(res, req)
	// Adopt any session the response hands back, as a browser would.
	for _, c := range (&http.Response{Header: res.Header()}).Cookies() {
		if c.Name == sessionCookie {
			if c.MaxAge < 0 {
				j.token = ""
			} else {
				j.token = c.Value
			}
		}
	}
	return res
}

func (j *journey) get(path string) *httptest.ResponseRecorder {
	return j.req("GET", path, nil)
}

func (j *journey) post(path string, form url.Values) *httptest.ResponseRecorder {
	if form == nil {
		form = url.Values{}
	}
	return j.req("POST", path, form)
}

func (j *journey) mustGet(path string, want int) string {
	j.t.Helper()
	res := j.get(path)
	if res.Code != want {
		j.t.Fatalf("[%s] GET %s = %d, want %d", j.name, path, res.Code, want)
	}
	return res.Body.String()
}

func (j *journey) mustPost(path string, form url.Values, want int) *httptest.ResponseRecorder {
	j.t.Helper()
	res := j.post(path, form)
	if res.Code != want {
		j.t.Fatalf("[%s] POST %s = %d, want %d; body=%s",
			j.name, path, res.Code, want, res.Body.String())
	}
	return res
}

func (j *journey) redirectedTo(res *httptest.ResponseRecorder) string {
	loc := res.Header().Get("Location")
	if loc == "" {
		loc = res.Header().Get("HX-Redirect")
	}
	return loc
}

// TestAcceptanceFullUserLifecycle is the end-to-end arc. It exercises every
// stage of the feature in one continuous story against the real handler stack,
// with no store calls used to set up state that a user could perform through
// the UI.
//
//	register → blocked at the approval gate → admin approves → login →
//	generate → song is private → share → second user sees it in Community →
//	un-share → second user refused → admin disables the first user →
//	their session dies mid-flight
func TestAcceptanceFullUserLifecycle(t *testing.T) {
	h, up, s := newTestEnvWith(t, func(c *config.Config) {
		c.AdminUser = fxAdminUser
		c.AdminPassword = fxAdminPass
	})
	useBcryptCost(t, bcrypt.MinCost)

	const (
		alicePass = "alice-fixture-password"
		bobPass   = "bob-fixture-password"
	)
	alice := newJourney(t, h, "alice")
	bob := newJourney(t, h, "bob")
	admin := newJourney(t, h, "admin")

	// ---- 1. Anonymous: the app is closed. ----
	for _, path := range []string{"/", "/history", "/history/personal", "/admin"} {
		res := alice.get(path)
		if !denied(res) {
			t.Fatalf("anonymous reached %s: %d", path, res.Code)
		}
	}
	// ...but the front door is open, and says how to get in.
	page := alice.mustGet("/login", 200)
	if !strings.Contains(page, `action="/login"`) || !strings.Contains(page, `href="/register"`) {
		t.Fatal("the login page does not offer a way in or a way to register")
	}

	// ---- 2. Register. ----
	res := alice.mustPost("/register", url.Values{
		"username": {"alice"}, "password": {alicePass},
		"confirm_password": {alicePass}}, http.StatusSeeOther)
	if got := alice.redirectedTo(res); got != "/login?notice="+noticeKeyRegistered {
		t.Fatalf("registration redirect = %q", got)
	}
	if alice.token != "" {
		t.Fatal("registration signed the new user straight in")
	}
	page = alice.mustGet("/login?notice="+noticeKeyRegistered, 200)
	if !strings.Contains(page, noticeRegistered) {
		t.Fatal("the registration notice is not shown")
	}

	// ---- 3. The approval gate holds. ----
	res = alice.post("/login", url.Values{"username": {"alice"}, "password": {alicePass}})
	if res.Code != http.StatusForbidden {
		t.Fatalf("pending login = %d, want 403", res.Code)
	}
	if !strings.Contains(res.Body.String(), noticePending) {
		t.Fatalf("pending login did not explain why: %s", res.Body.String())
	}
	if alice.token != "" {
		t.Fatal("a pending account was issued a session")
	}
	// A wrong password on the same account says nothing about its status.
	res = alice.post("/login", url.Values{"username": {"alice"}, "password": {"wrong"}})
	if res.Code != http.StatusUnauthorized || strings.Contains(res.Body.String(), noticePending) {
		t.Fatal("a wrong password leaked the account's pending status")
	}

	// ---- 4. The administrator approves. ----
	admin.mustPost("/login", url.Values{
		"username": {fxAdminUser}, "password": {fxAdminPass}}, http.StatusSeeOther)
	if admin.token == "" {
		t.Fatal("admin login issued no session")
	}
	board := admin.mustGet("/admin", 200)
	for _, want := range []string{"Pending Registration Requests", "alice", "Approve User"} {
		if !strings.Contains(board, want) {
			t.Fatalf("the dashboard is missing %q", want)
		}
	}
	// The badge is visible to the admin and counts the one pending request.
	if !strings.Contains(board, "1 Pending") {
		t.Fatal("the pending badge does not show the waiting request")
	}

	aliceUser, err := s.st.GetUserByUsername("alice")
	if err != nil || aliceUser == nil {
		t.Fatalf("alice was not stored: %#v, err=%v", aliceUser, err)
	}
	if aliceUser.Status != store.StatusPending {
		t.Fatalf("alice's status = %q, want pending", aliceUser.Status)
	}
	admin.mustPost("/admin/users/"+aliceUser.ID+"/approve", nil, http.StatusSeeOther)

	// ---- 5. Alice can now log in. ----
	res = alice.mustPost("/login", url.Values{
		"username": {"alice"}, "password": {alicePass}}, http.StatusSeeOther)
	if alice.token == "" {
		t.Fatal("approved login issued no session")
	}
	if got := alice.redirectedTo(res); got != "/" {
		t.Fatalf("login redirect = %q, want /", got)
	}
	home := alice.mustGet("/", 200)
	if !strings.Contains(home, "alice") {
		t.Error("the signed-in username is not shown")
	}
	if strings.Contains(home, `href="/admin"`) {
		t.Error("an ordinary user was shown the Admin tab")
	}

	// ---- 6. Generate a song, end to end through the worker. ----
	res = alice.mustPost("/jobs", url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop. Vocal Details: soft. Arrangement: guitar."},
		"audio_duration": {"30"},
		"seed":           {"7"},
	}, 200)
	_ = res
	waitUntil(t, 10*time.Second, func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.RunCalls == 1
	}, "worker submitted the job")
	up.mu.Lock()
	up.Completed = true
	up.mu.Unlock()
	waitUntil(t, 20*time.Second, func() bool {
		songs, err := s.st.PersonalSongs(aliceUser.ID, 10, 0)
		return err == nil && len(songs) == 1
	}, "the song landed owned by alice")

	songs, err := s.st.PersonalSongs(aliceUser.ID, 10, 0)
	if err != nil || len(songs) != 1 {
		t.Fatalf("alice's songs = %d, err=%v", len(songs), err)
	}
	song := songs[0]
	if song.IsPublic {
		t.Fatal("a new song defaulted to public")
	}

	// It is in her library, and she can stream it.
	lib := alice.mustGet("/history", 200)
	if !strings.Contains(lib, song.ID) {
		t.Fatal("the new song is not in alice's library")
	}
	if !strings.Contains(lib, "My Songs") || !strings.Contains(lib, "Community Songs") {
		t.Fatal("the library is not partitioned")
	}
	alice.mustGet("/audio/"+song.ID, 200)

	// ---- 7. Bob registers, is approved, and cannot see it. ----
	bob.mustPost("/register", url.Values{
		"username": {"bob"}, "password": {bobPass},
		"confirm_password": {bobPass}}, http.StatusSeeOther)
	bobUser, err := s.st.GetUserByUsername("bob")
	if err != nil || bobUser == nil {
		t.Fatalf("bob was not stored: %v", err)
	}
	admin.mustPost("/admin/users/"+bobUser.ID+"/approve", nil, http.StatusSeeOther)
	bob.mustPost("/login", url.Values{
		"username": {"bob"}, "password": {bobPass}}, http.StatusSeeOther)

	bobLib := bob.mustGet("/history", 200)
	if strings.Contains(bobLib, song.ID) {
		t.Fatal("alice's private song appeared in bob's library")
	}
	for _, path := range []string{"/songs/" + song.ID, "/audio/" + song.ID} {
		if res := bob.get(path); res.Code != http.StatusNotFound {
			t.Fatalf("bob reached %s: %d, want 404", path, res.Code)
		}
	}
	// Bob cannot destroy or rename it either.
	if res := bob.req("DELETE", "/songs/"+song.ID, nil); res.Code == 200 {
		t.Fatal("bob deleted alice's song")
	}

	// ---- 8. Alice shares it; bob sees it in Community. ----
	alice.mustPost("/songs/"+song.ID+"/toggle-public",
		url.Values{"public": {"1"}}, http.StatusSeeOther)

	bobLib = bob.mustGet("/history", 200)
	if !strings.Contains(bobLib, song.ID) {
		t.Fatal("a shared song did not reach the community library")
	}
	bob.mustGet("/songs/"+song.ID, 200)
	audio := bob.mustGet("/audio/"+song.ID, 200)
	if audio == "" {
		t.Fatal("the shared audio streamed nothing")
	}
	// Reading is granted; writing is not.
	if res := bob.post("/songs/"+song.ID+"/toggle-public",
		url.Values{"public": {"0"}}); res.Code != http.StatusNotFound {
		t.Fatalf("bob un-shared alice's song: %d", res.Code)
	}

	// ---- 9. Alice un-shares; bob is refused on his very next request. ----
	alice.mustPost("/songs/"+song.ID+"/toggle-public",
		url.Values{"public": {"0"}}, http.StatusSeeOther)
	if res := bob.get("/audio/" + song.ID); res.Code != http.StatusNotFound {
		t.Fatalf("bob still streamed after un-publish: %d", res.Code)
	}
	if res := bob.get("/songs/" + song.ID); res.Code != http.StatusNotFound {
		t.Fatalf("bob still read the detail page after un-publish: %d", res.Code)
	}
	bobLib = bob.mustGet("/history", 200)
	if strings.Contains(bobLib, song.ID) {
		t.Fatal("the un-shared song stayed in the community library")
	}

	// ---- 10. The administrator disables alice; her session dies in flight. ----
	if res := alice.get("/history"); res.Code != 200 {
		t.Fatalf("alice was already locked out: %d", res.Code)
	}
	admin.mustPost("/admin/users/"+aliceUser.ID+"/disable", nil, http.StatusSeeOther)

	res = alice.get("/history")
	if !denied(res) {
		t.Fatalf("a disabled user was still served: %d", res.Code)
	}
	if res := alice.get("/audio/" + song.ID); !denied(res) {
		t.Fatalf("a disabled user still streamed audio: %d", res.Code)
	}
	// And she cannot log back in, with the reason given.
	alice.token = ""
	res = alice.post("/login", url.Values{"username": {"alice"}, "password": {alicePass}})
	if res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), noticeDisabled) {
		t.Fatalf("disabled login = %d: %s", res.Code, res.Body.String())
	}

	// Bob is unaffected throughout.
	bob.mustGet("/history", 200)

	// ---- 11. The administrator deletes alice; her content goes with her. ----
	admin.mustPost("/admin/users/"+aliceUser.ID+"/delete", nil, http.StatusSeeOther)
	if u, err := s.st.GetUserByID(aliceUser.ID); err != nil || u != nil {
		t.Fatalf("alice survived deletion: %#v, err=%v", u, err)
	}
	if g, err := s.st.Song(song.ID, store.AdminAccess("root")); err != nil || g != nil {
		t.Fatalf("alice's song survived her deletion: %#v, err=%v", g, err)
	}
	// Bob is still fine, and the dashboard no longer lists alice.
	bob.mustGet("/history", 200)
	board = admin.mustGet("/admin", 200)
	if strings.Contains(board, ">alice<") {
		t.Error("a deleted user is still listed on the dashboard")
	}

	// ---- 12. Logging out ends the session server-side. ----
	bobToken := bob.token
	bob.mustPost("/logout", nil, http.StatusSeeOther)
	if sess, err := s.st.GetSession(bobToken); err != nil || sess != nil {
		t.Fatalf("bob's session survived logout: %#v, err=%v", sess, err)
	}
	if res := bob.get("/history"); !denied(res) {
		t.Fatalf("bob still served after logout: %d", res.Code)
	}
}

// TestAcceptanceHostileContentRendersInert: titles, usernames, and notices are
// user-influenced and now render in the admin table, community cards, and
// badges. html/template escapes by default — this proves nothing bypasses it.
func TestAcceptanceHostileContentRendersInert(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	useBcryptCost(t, bcrypt.MinCost)

	const payload = `<script>alert("xss")</script>`
	const attrPayload = `" onmouseover="alert(1)`

	// A hostile username, registered through the real endpoint.
	hostileName := "ev" + payload
	if len(hostileName) > maxUsernameLen {
		hostileName = hostileName[:maxUsernameLen]
	}
	admin := newJourney(t, h, "admin")
	_, adminTok := mkSession(t, s, "render-admin", store.StatusApproved, store.RoleAdmin)
	admin.token = adminTok

	evil := &store.User{ID: newUserID(), Username: hostileName,
		PasswordHash: "$2a$04$" + strings.Repeat("x", 53),
		Status:       store.StatusPending, Role: store.RoleUser}
	if err := s.st.CreateUser(evil); err != nil {
		t.Fatal(err)
	}

	// A hostile song title, on a shared song so it reaches a community card.
	owner, ownerTok := mkSession(t, s, "render-owner", store.StatusApproved, store.RoleUser)
	g := mkSong(t, s, "render-song", owner.ID, true)
	if err := s.st.UpdateSongTitle(g.ID, payload+attrPayload,
		store.UserAccess(owner.ID)); err != nil {
		t.Fatal(err)
	}

	pages := map[string]string{
		"admin dashboard":   admin.mustGet("/admin", 200),
		"community library": func() string { j := newJourney(t, h, "v"); j.token = adminTok; return j.mustGet("/history", 200) }(),
		"song detail":       func() string { j := newJourney(t, h, "o"); j.token = ownerTok; return j.mustGet("/songs/"+g.ID, 200) }(),
		"owner library":     func() string { j := newJourney(t, h, "o"); j.token = ownerTok; return j.mustGet("/history", 200) }(),
	}
	for where, body := range pages {
		// The raw payload must never appear unescaped.
		if strings.Contains(body, "<script>alert") {
			t.Errorf("%s rendered an executable script tag", where)
		}
		if strings.Contains(body, `onmouseover="alert`) {
			t.Errorf("%s rendered an executable event handler", where)
		}
	}
	// It did reach the page — escaped — so the assertions above are not
	// passing merely because the content was absent.
	if !strings.Contains(pages["admin dashboard"], "&lt;script&gt;") {
		t.Error("the hostile username never reached the dashboard, so nothing was proven")
	}
	if !strings.Contains(pages["owner library"], "&lt;script&gt;") &&
		!strings.Contains(pages["owner library"], "\\u003cscript\\u003e") {
		t.Error("the hostile title never reached the library, so nothing was proven")
	}

	// A hostile notice key is not reflected at all — the key is mapped
	// server-side, so unknown input renders no notice rather than echoing it
	// even in escaped form.
	body := newJourney(t, h, "anon").mustGet(
		"/login?notice="+url.QueryEscape(payload), 200)
	for _, bad := range []string{"alert(", "&lt;script&gt;", "<script>alert"} {
		if strings.Contains(body, bad) {
			t.Errorf("the login page reflected the notice value (%q)", bad)
		}
	}
	// The dashboard legitimately renders the escaped hostile *username*, so
	// here the test is that nothing executable appears and that no notice
	// element was produced for the unknown key.
	adminBody := admin.mustGet("/admin?notice="+url.QueryEscape(payload), 200)
	if strings.Contains(adminBody, "<script>alert") {
		t.Error("the dashboard rendered an executable script tag")
	}
	if strings.Contains(adminBody, `class="notice"`) {
		t.Error("the dashboard rendered a notice for an unknown key")
	}
}

// TestAcceptanceEmptyAndErrorStates walks the states a user actually hits when
// nothing has happened yet or something went wrong.
func TestAcceptanceEmptyAndErrorStates(t *testing.T) {
	h, _, s := newTestEnvWith(t, func(c *config.Config) {
		c.AdminUser = fxAdminUser
		c.AdminPassword = fxAdminPass
	})
	useBcryptCost(t, bcrypt.MinCost)

	admin := newJourney(t, h, "admin")
	admin.mustPost("/login", url.Values{
		"username": {fxAdminUser}, "password": {fxAdminPass}}, http.StatusSeeOther)

	// Nothing pending, nobody registered.
	board := admin.mustGet("/admin", 200)
	if !strings.Contains(board, "No accounts are waiting for approval") {
		t.Error("the empty pending state is missing")
	}
	if strings.Contains(board, "nav-badge") {
		t.Error("a badge appeared with nothing pending")
	}

	// No songs, no community songs.
	lib := admin.mustGet("/history", 200)
	for _, want := range []string{
		"No songs in your library yet", "Nothing has been shared yet",
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("the library is missing the empty state %q", want)
		}
	}

	// A failed login says one thing, whoever you are.
	anon := newJourney(t, h, "anon")
	res := anon.post("/login", url.Values{"username": {"nobody"}, "password": {"nothing"}})
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), invalidCredentials) {
		t.Errorf("failed login = %d: %s", res.Code, res.Body.String())
	}

	// A rejected registration explains itself and keeps the username typed in.
	res = anon.post("/register", url.Values{
		"username": {"ab"}, "password": {"short"}, "confirm_password": {"short"}})
	if res.Code != http.StatusBadRequest {
		t.Errorf("short registration = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "at least 3") {
		t.Error("the registration error does not say what is wrong")
	}

	// A taken username is reported rather than silently failing.
	mkSession(t, s, "taken-name", store.StatusApproved, store.RoleUser)
	res = anon.post("/register", url.Values{
		"username": {"taken-name"}, "password": {"a-good-password"},
		"confirm_password": {"a-good-password"}})
	if res.Code != http.StatusConflict {
		t.Errorf("duplicate registration = %d, want 409", res.Code)
	}

	// A throttled login says so and offers Retry-After.
	var throttled bool
	for i := 0; i < loginLimitPerWindow+5; i++ {
		res := anon.post("/login", url.Values{
			"username": {"nobody"}, "password": {"nope"}})
		if res.Code == http.StatusTooManyRequests {
			throttled = true
			if res.Header().Get("Retry-After") == "" {
				t.Error("the throttled response has no Retry-After")
			}
			if !strings.Contains(res.Body.String(), "Too many attempts") {
				t.Error("the throttled response does not explain itself")
			}
			break
		}
	}
	if !throttled {
		t.Error("login was never throttled")
	}
}

// TestAcceptanceAccessibilityBasics checks the affordances a keyboard or
// screen-reader user depends on, on the pages this feature added.
func TestAcceptanceAccessibilityBasics(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	useBcryptCost(t, bcrypt.MinCost)
	_, adminTok := mkSession(t, s, "a11y-admin", store.StatusApproved, store.RoleAdmin)
	mkSession(t, s, "a11y-pending", store.StatusPending, store.RoleUser)
	owner, ownerTok := mkSession(t, s, "a11y-owner", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "a11y-song", owner.ID, false)

	anon := newJourney(t, h, "anon")
	admin := newJourney(t, h, "admin")
	admin.token = adminTok
	user := newJourney(t, h, "user")
	user.token = ownerTok

	// Every form control is labelled and reachable.
	for _, path := range []string{"/login", "/register"} {
		body := anon.mustGet(path, 200)
		for _, id := range []string{"username", "password"} {
			if !strings.Contains(body, `for="`+id+`"`) {
				t.Errorf("%s: no label bound to %q", path, id)
			}
			if !strings.Contains(body, `id="`+id+`"`) {
				t.Errorf("%s: input %q has no id to bind to", path, id)
			}
		}
		if !strings.Contains(body, "autocomplete=") {
			t.Errorf("%s: inputs carry no autocomplete hints", path)
		}
	}

	// The login and register forms expose their headings, guidance, and password control state
	// without coupling this test to the decorative rack-console classes.
	login := anon.mustGet("/login", 200)
	for _, want := range []string{
		`id="login-title"`,
		`aria-labelledby="login-title"`,
		`aria-describedby="login-guidance"`,
		`aria-controls="password"`,
		`aria-pressed="false"`,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("/login is missing semantic hook %q", want)
		}
	}

	register := anon.mustGet("/register", 200)
	for _, want := range []string{
		`id="register-title"`,
		`aria-labelledby="register-title"`,
		`aria-describedby="register-guidance"`,
		`aria-controls="password"`,
		`aria-controls="confirm_password"`,
		`aria-pressed="false"`,
	} {
		if !strings.Contains(register, want) {
			t.Errorf("/register is missing semantic hook %q", want)
		}
	}

	// The badge reads as words, not a bare number.
	board := admin.mustGet("/admin", 200)
	if !strings.Contains(board, "1 Pending") {
		t.Error("the badge is not readable as text")
	}
	// Tables are titled and their action column is named for assistive tech.
	if !strings.Contains(board, `aria-labelledby="pending-heading"`) {
		t.Error("the pending table is not associated with its heading")
	}
	if !strings.Contains(board, `<span class="sr-only">Actions</span>`) {
		t.Error("the action column has no accessible name")
	}
	// Buttons say what they do.
	for _, label := range []string{"Approve User", "Disable User", "Delete User"} {
		if !strings.Contains(board, label) {
			t.Errorf("the dashboard is missing the %q button", label)
		}
	}
	// Destructive actions confirm first.
	if strings.Count(board, "hx-confirm") < 2 {
		t.Error("destructive admin actions do not confirm")
	}

	// The sharing control names the song it acts on.
	lib := user.mustGet("/history", 200)
	if !strings.Contains(lib, `aria-label="Share &#34;`) {
		t.Error("the sharing control has no accessible name naming its song")
	}
	// The theme mechanism is present on an authenticated page.
	if !strings.Contains(lib, "data-theme") {
		t.Error("the theme script is missing from an authenticated page")
	}
}

// TestAcceptanceThemeAndResponsiveHooksArePresent: every page this feature
// added inherits the existing theme mechanism rather than inventing one.
func TestAcceptanceThemeAndResponsiveHooksArePresent(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	useBcryptCost(t, bcrypt.MinCost)
	_, adminTok := mkSession(t, s, "theme-admin", store.StatusApproved, store.RoleAdmin)

	anon := newJourney(t, h, "anon")
	admin := newJourney(t, h, "admin")
	admin.token = adminTok

	bodies := map[string]string{
		"/login":    anon.mustGet("/login", 200),
		"/register": anon.mustGet("/register", 200),
		"/admin":    admin.mustGet("/admin", 200),
		"/history":  admin.mustGet("/history", 200),
	}
	for path, body := range bodies {
		if !strings.Contains(body, "mm3-theme") {
			t.Errorf("%s does not use the existing theme mechanism", path)
		}
		if !strings.Contains(body, `name="viewport"`) {
			t.Errorf("%s has no viewport meta, so it will not scale on mobile", path)
		}
		if !strings.Contains(body, "/static/app.css") {
			t.Errorf("%s does not load the shared stylesheet", path)
		}
		// No second theming mechanism crept in.
		if strings.Contains(body, "prefers-color-scheme") && !strings.Contains(body, "matchMedia") {
			t.Errorf("%s embeds its own colour-scheme handling", path)
		}
	}
}

// TestAcceptanceGenerateDraftSurvivesNavigation covers the generate form being a
// scratchpad a song is written in rather than a fill-and-submit form: its fields
// are saved under a per-user key so leaving for History and coming back does not
// wipe work in progress, a Clear button resets it deliberately, and logging out
// drops the draft so a shared browser does not hand it to the next account.
func TestAcceptanceGenerateDraftSurvivesNavigation(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	useBcryptCost(t, bcrypt.MinCost)
	_, tok := mkSession(t, s, "drafter", store.StatusApproved, store.RoleUser)

	j := newJourney(t, h, "drafter")
	j.token = tok
	body := j.mustGet("/", 200)

	// The draft itself lives in one shared module, because the song detail page
	// writes the same draft when it hands a song back to be reworked.
	draftJS := j.mustGet("/static/draft.js", 200)

	// Namespaced per user, so two accounts on one browser never share a draft.
	if !strings.Contains(draftJS, `'mm3-draft:' + (window.MM3_USER`) {
		t.Error("the draft key is not scoped to the signed-in user")
	}
	if !strings.Contains(body, `window.MM3_USER = "drafter"`) {
		t.Error("generate page does not name the signed-in user its draft belongs to")
	}
	// Every field a song is written into is kept, not just the lyrics.
	for _, f := range []string{"idea", "title", "lyrics", "caption", "dur", "seed", "bpm", "key", "vocals"} {
		if !strings.Contains(draftJS, `'`+f+`'`) {
			t.Errorf("draft does not persist the %q field", f)
		}
	}
	// Restoring on load and saving on change are what make navigating away safe.
	for _, hook := range []string{"this.restore()", "$watch", "mm3WriteDraft(d)"} {
		if !strings.Contains(body, hook) {
			t.Errorf("generate page is missing %q, so the draft will not survive navigation", hook)
		}
	}
	// Clearing is an explicit user action, never a side effect of navigating.
	// Clear sits in the accordion header beside Assistant, whose own @click
	// toggles the section — so .stop is load-bearing, not decoration.
	if !strings.Contains(body, `@click.stop="clear()"`) {
		t.Error("generate page offers no Clear button, or its click is not stopped " +
			"from also collapsing the lyrics accordion")
	}
	if !strings.Contains(body, `:disabled="!dirty"`) {
		t.Error("Clear is not disabled on an untouched form")
	}

	// Logging out drops this account's draft from every page carrying the nav.
	for _, path := range []string{"/", "/history"} {
		if !strings.Contains(j.mustGet(path, 200), `onsubmit="mm3ClearDraft()"`) {
			t.Errorf("%s: logging out does not drop the saved draft", path)
		}
	}
}

// TestAcceptanceUserNamesTheirSong covers naming a song where it is written
// rather than renaming it afterwards in History: the generate form offers an
// optional title, and a song submitted without one is still filed under a
// readable name.
func TestAcceptanceUserNamesTheirSong(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	useBcryptCost(t, bcrypt.MinCost)
	_, tok := mkSession(t, s, "namer", store.StatusApproved, store.RoleUser)

	j := newJourney(t, h, "namer")
	j.token = tok
	body := j.mustGet("/", 200)

	if !strings.Contains(body, `name="title"`) {
		t.Error("generate form offers no title field")
	}
	// Optional, and said so — the alternative is a user who thinks it is required.
	if !strings.Contains(body, "optional") {
		t.Error("the title field does not read as optional")
	}
	if !strings.Contains(body, `x-model="title"`) {
		t.Error("the title is not bound to the draft, so it will not survive navigation")
	}
}

// TestAcceptanceSongOpensInGenerator covers reworking a past song: its detail
// page hands the song to the generate form through that same per-user draft, so
// the seed, lyrics, caption and length all arrive editable. Re-running a stored
// seed unchanged only reproduces the song, which is why this replaced the old
// regenerate-with-the-same-seed button.
func TestAcceptanceSongOpensInGenerator(t *testing.T) {
	h, id := completeOneSong(t)
	body := get(h, "/songs/"+id).Body.String()

	if !strings.Contains(body, `onclick="mm3EditInGenerator()"`) {
		t.Error("song detail offers no way to open the song in the generator")
	}
	// It writes the draft and navigates — nothing is queued behind the user's back.
	for _, hook := range []string{"mm3WriteDraft(d)", "location.href = '/'"} {
		if !strings.Contains(body, hook) {
			t.Errorf("song detail is missing %q", hook)
		}
	}
	// Work already on the generate page is not silently replaced.
	if !strings.Contains(body, "mm3DraftIsDirty(mm3ReadDraft())") {
		t.Error("song detail overwrites an existing draft without confirming")
	}
	if strings.Contains(body, "/regenerate") {
		t.Error("song detail still offers the regenerate-with-the-same-seed action")
	}
}
