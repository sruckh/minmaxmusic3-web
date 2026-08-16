package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// Fixture credentials. Nothing here is a real secret.
const (
	fxAdminUser = "test-admin"
	fxAdminPass = "fixture-admin-password"
	fxUserPass  = "fixture-user-password"
)

// useBcryptCost lowers the work factor for one test and restores it after.
// bcrypt at the production cost takes seconds per call under -race, which
// would dominate the suite; the cost is irrelevant to everything except the
// tests that assert it explicitly.
func useBcryptCost(t *testing.T, cost int) {
	t.Helper()
	prev := bcryptCost
	bcryptCost = cost
	t.Cleanup(func() { bcryptCost = prev })
}

// newAuthEnv boots a server with the static administrator configured, with a
// cheap work factor so the test stays fast.
func newAuthEnv(t *testing.T) (http.Handler, *Server) {
	t.Helper()
	useBcryptCost(t, bcrypt.MinCost)
	h, _, s := newTestEnvWith(t, func(c *config.Config) {
		c.AdminUser = fxAdminUser
		c.AdminPassword = fxAdminPass
	})
	return h, s
}

// mkUser inserts an approved-or-otherwise user straight into the store.
func mkUser(t *testing.T, s *Server, username, password, status string) *store.User {
	t.Helper()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{ID: newUserID(), Username: username, PasswordHash: hash,
		Status: status, Role: store.RoleUser}
	if err := s.st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	return u
}

func postFormCookie(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	h.ServeHTTP(res, req)
	return res
}

// sessionCookieFrom returns the mm3_session cookie a response set, or nil.
func sessionCookieFrom(res *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range (&http.Response{Header: res.Header()}).Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	return nil
}

func login(t *testing.T, h http.Handler, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	return postFormCookie(h, "/login", url.Values{
		"username": {username}, "password": {password}})
}

func TestLoginAndRegisterPagesRender(t *testing.T) {
	h, _ := newAuthEnv(t)
	for path, want := range map[string]string{
		"/login":    `action="/login"`,
		"/register": `action="/register"`,
	} {
		res := get(h, path)
		if res.Code != 200 {
			t.Fatalf("GET %s = %d", path, res.Code)
		}
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("GET %s missing %q", path, want)
		}
	}
	// The registered notice is keyed, not reflected from the query string.
	res := get(h, "/login?notice=registered")
	if !strings.Contains(res.Body.String(), "administrator must approve") {
		t.Errorf("registered notice missing: %s", res.Body.String())
	}
}

// TestRegisterCreatesPendingUser covers the contract's registration path.
func TestRegisterCreatesPendingUser(t *testing.T) {
	h, s := newAuthEnv(t)
	useBcryptCost(t, ProductionBcryptCost) // this test checks the stored cost
	res := postForm(h, "/register", url.Values{
		"username": {"newcomer"}, "password": {fxUserPass},
		"confirm_password": {fxUserPass}})
	if res.Code != http.StatusSeeOther {
		t.Fatalf("POST /register = %d, want 303; body=%s", res.Code, res.Body.String())
	}
	if loc := res.Header().Get("Location"); loc != "/login?notice=registered" {
		t.Fatalf("redirect = %q", loc)
	}
	// Registration must never sign the new account in.
	if c := sessionCookieFrom(res); c != nil {
		t.Fatalf("registration issued a session cookie: %#v", c)
	}

	u, err := s.st.GetUserByUsername("newcomer")
	if err != nil || u == nil {
		t.Fatalf("user not stored: %#v, err=%v", u, err)
	}
	if u.Status != store.StatusPending || u.Role != store.RoleUser {
		t.Fatalf("stored user = status:%q role:%q, want pending/user", u.Status, u.Role)
	}
	// The password must be hashed, never stored or echoed in plaintext.
	if strings.Contains(u.PasswordHash, fxUserPass) {
		t.Fatal("password stored in plaintext")
	}
	if !checkPassword(u.PasswordHash, fxUserPass) {
		t.Fatal("stored hash does not verify the password")
	}
	if !strings.HasPrefix(u.PasswordHash, "$2a$12$") {
		t.Fatalf("hash %q is not bcrypt cost 12", u.PasswordHash[:min(7, len(u.PasswordHash))])
	}
}

func TestRegisterValidation(t *testing.T) {
	h, s := newAuthEnv(t)
	cases := []struct {
		name             string
		user, pw, confim string
		wantIn           string
	}{
		{"short username", "ab", fxUserPass, fxUserPass, "at least 3"},
		{"short password", "someone", "short", "short", "at least 8"},
		{"mismatch", "someone", fxUserPass, fxUserPass + "x", "do not match"},
		{"spaces", "some one", fxUserPass, fxUserPass, "cannot contain spaces"},
		{"overlong password", "someone", strings.Repeat("x", 73), strings.Repeat("x", 73), "72 characters or fewer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := postForm(h, "/register", url.Values{
				"username": {c.user}, "password": {c.pw}, "confirm_password": {c.confim}})
			if res.Code != http.StatusBadRequest {
				t.Fatalf("= %d, want 400", res.Code)
			}
			if !strings.Contains(res.Body.String(), c.wantIn) {
				t.Errorf("body missing %q", c.wantIn)
			}
			if u, _ := s.st.GetUserByUsername(c.user); u != nil {
				t.Errorf("invalid signup created a user: %#v", u)
			}
		})
	}
}

// TestRegisterCannotShadowConfigAdmin: a signup under the administrator's
// username could never log in, because the config path wins.
func TestRegisterCannotShadowConfigAdmin(t *testing.T) {
	h, s := newAuthEnv(t)
	for _, name := range []string{fxAdminUser, strings.ToUpper(fxAdminUser)} {
		res := postForm(h, "/register", url.Values{
			"username": {name}, "password": {fxUserPass}, "confirm_password": {fxUserPass}})
		if res.Code != http.StatusConflict {
			t.Fatalf("registering %q = %d, want 409", name, res.Code)
		}
		if u, _ := s.st.GetUserByUsername(name); u != nil {
			t.Fatalf("a user shadowing the admin was created: %#v", u)
		}
	}
}

// TestAdminLoginIssuesSession covers the Infisical credential path.
func TestAdminLoginIssuesSession(t *testing.T) {
	h, s := newAuthEnv(t)
	res := login(t, h, fxAdminUser, fxAdminPass)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("admin login = %d, want 303; body=%s", res.Code, res.Body.String())
	}
	c := sessionCookieFrom(res)
	if c == nil || c.Value == "" {
		t.Fatal("admin login set no session cookie")
	}
	sess, err := s.st.GetSession(c.Value)
	if err != nil || sess == nil {
		t.Fatalf("session not stored: %#v, err=%v", sess, err)
	}
	if !sess.IsAdmin || !sess.ConfigAdmin {
		t.Fatalf("admin session = %#v, want admin", sess)
	}
	if sess.UserID != ConfigAdminUserID || sess.Username != fxAdminUser {
		t.Fatalf("admin session identity = %q/%q", sess.UserID, sess.Username)
	}
	if sess.Status != store.StatusApproved {
		t.Fatalf("admin status = %q", sess.Status)
	}
}

// TestConfigAdminIDCannotCollide pins the invariant the id sentinel rests on.
func TestConfigAdminIDCannotCollide(t *testing.T) {
	if !strings.Contains(ConfigAdminUserID, ":") {
		t.Fatalf("ConfigAdminUserID %q must contain a character hex cannot produce",
			ConfigAdminUserID)
	}
	const hexDigits = "0123456789abcdef"
	for i := 0; i < 2000; i++ {
		id := newUserID()
		if id == ConfigAdminUserID {
			t.Fatalf("generated id collided with the config admin sentinel")
		}
		if len(id) != 32 {
			t.Fatalf("generated id %q is not 32 characters", id)
		}
		if strings.Trim(id, hexDigits) != "" {
			t.Fatalf("generated id %q left the hex alphabet", id)
		}
	}
}

// TestAdminLoginDisabledWithoutBothHalves: a blank half must disable admin
// login, never fall back to a default credential.
func TestAdminLoginDisabledWithoutBothHalves(t *testing.T) {
	cases := []struct{ name, user, pass string }{
		{"both blank", "", ""},
		{"no password", fxAdminUser, ""},
		{"no username", "", fxAdminPass},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := newTestEnvWith(t, func(cfg *config.Config) {
				cfg.AdminUser, cfg.AdminPassword = c.user, c.pass
			})
			res := login(t, h, c.user, c.pass)
			if res.Code == http.StatusSeeOther {
				t.Fatalf("admin login succeeded with a blank half")
			}
			if sessionCookieFrom(res) != nil {
				t.Fatal("a session was issued with admin login disabled")
			}
		})
	}
}

func TestApprovedUserLoginAndLogout(t *testing.T) {
	h, s := newAuthEnv(t)
	u := mkUser(t, s, "approved-user", fxUserPass, store.StatusApproved)

	res := login(t, h, u.Username, fxUserPass)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303; body=%s", res.Code, res.Body.String())
	}
	c := sessionCookieFrom(res)
	if c == nil {
		t.Fatal("no session cookie")
	}
	if c.Name != sessionCookie || !c.HttpOnly ||
		c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Fatalf("cookie attributes wrong: %#v", c)
	}
	sess, err := s.st.GetSession(c.Value)
	if err != nil || sess == nil {
		t.Fatalf("session missing: %#v, err=%v", sess, err)
	}
	if sess.UserID != u.ID || sess.IsAdmin || sess.ConfigAdmin {
		t.Fatalf("ordinary user got an admin session: %#v", sess)
	}

	// Logout revokes server-side, not just in the browser.
	out := postFormCookie(h, "/logout", url.Values{}, c)
	if out.Code != http.StatusSeeOther {
		t.Fatalf("logout = %d, want 303", out.Code)
	}
	if gone, err := s.st.GetSession(c.Value); err != nil || gone != nil {
		t.Fatalf("session survived logout: %#v, err=%v", gone, err)
	}
	if cleared := sessionCookieFrom(out); cleared == nil || cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Fatalf("logout did not clear the cookie: %#v", cleared)
	}
	// Logging out again is harmless.
	if again := postFormCookie(h, "/logout", url.Values{}, c); again.Code != http.StatusSeeOther {
		t.Fatalf("second logout = %d", again.Code)
	}
}

// TestPendingAndDisabledRejected covers the two status notices — and pins
// that they are only reachable with a correct password.
func TestPendingAndDisabledRejected(t *testing.T) {
	h, s := newAuthEnv(t)
	pending := mkUser(t, s, "pending-user", fxUserPass, store.StatusPending)
	disabled := mkUser(t, s, "disabled-user", fxUserPass, store.StatusDisabled)

	for _, c := range []struct{ user, wantIn string }{
		{pending.Username, "awaiting administrator approval"},
		{disabled.Username, "has been disabled"},
	} {
		res := login(t, h, c.user, fxUserPass)
		if res.Code != http.StatusForbidden {
			t.Fatalf("%s login = %d, want 403", c.user, res.Code)
		}
		if !strings.Contains(res.Body.String(), c.wantIn) {
			t.Errorf("%s: body missing %q", c.user, c.wantIn)
		}
		if sessionCookieFrom(res) != nil {
			t.Fatalf("%s was issued a session", c.user)
		}
	}

	// With a WRONG password the status must not leak: an unapproved account
	// answers exactly like a nonexistent one.
	for _, name := range []string{pending.Username, disabled.Username} {
		res := login(t, h, name, "wrong-password-entirely")
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s wrong-password = %d, want 401", name, res.Code)
		}
		body := res.Body.String()
		for _, leak := range []string{noticePending, noticeDisabled} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: status leaked to someone without the password", name)
			}
		}
		if !strings.Contains(body, invalidCredentials) {
			t.Errorf("%s: wrong password did not get the uniform message", name)
		}
	}
}

// TestLoginFailuresAreIndistinguishable is the enumeration guard: a wrong
// password and a nonexistent user must match in status, in body, and roughly
// in time.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	h, s := newAuthEnv(t)
	useBcryptCost(t, 10) // slow enough that a skipped comparison is obvious
	real := mkUser(t, s, "real-user", fxUserPass, store.StatusApproved)

	wrongPass := login(t, h, real.Username, "definitely-not-the-password")
	noSuchUser := login(t, h, "no-such-user-at-all", "definitely-not-the-password")

	if wrongPass.Code != noSuchUser.Code {
		t.Fatalf("status differs: wrong-password=%d unknown-user=%d",
			wrongPass.Code, noSuchUser.Code)
	}
	if wrongPass.Code != http.StatusUnauthorized {
		t.Fatalf("failure status = %d, want 401", wrongPass.Code)
	}
	// Bodies differ only where the form echoes the submitted username back.
	normalise := func(res *httptest.ResponseRecorder, user string) string {
		return strings.ReplaceAll(res.Body.String(), user, "USER")
	}
	if a, b := normalise(wrongPass, real.Username), normalise(noSuchUser, "no-such-user-at-all"); a != b {
		t.Errorf("response bodies differ between wrong-password and unknown-user")
	}
	for _, res := range []*httptest.ResponseRecorder{wrongPass, noSuchUser} {
		if !strings.Contains(res.Body.String(), "Incorrect username or password") {
			t.Errorf("failure body is not the uniform message")
		}
	}

	// Timing: the unknown-user path must not short-circuit. Both paths run one
	// bcrypt comparison, so neither should be dramatically faster. The bound is
	// deliberately loose — this catches a missing decoy check, not jitter.
	median := func(user string) time.Duration {
		var ds []time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			login(t, h, user, "definitely-not-the-password")
			ds = append(ds, time.Since(start))
		}
		if ds[0] > ds[1] {
			ds[0], ds[1] = ds[1], ds[0]
		}
		if ds[1] > ds[2] {
			ds[1], ds[2] = ds[2], ds[1]
		}
		return ds[1]
	}
	// The limiter allows 10 per window; the calls above plus 6 here stay under.
	known, unknown := median(real.Username), median("another-unknown-user")
	if unknown*4 < known {
		t.Errorf("unknown-user path is much faster than wrong-password "+
			"(%v vs %v) — the decoy bcrypt check is not running", unknown, known)
	}
}

// TestAdminLoginBurnsSameWork: the administrator's username must not be
// identifiable by a fast rejection.
func TestAdminLoginBurnsSameWork(t *testing.T) {
	h, s := newAuthEnv(t)
	useBcryptCost(t, 10) // slow enough that a skipped comparison is obvious
	mkUser(t, s, "some-user", fxUserPass, store.StatusApproved)

	timeOne := func(user string) time.Duration {
		start := time.Now()
		login(t, h, user, "wrong-password-here")
		return time.Since(start)
	}
	admin, ordinary := timeOne(fxAdminUser), timeOne("some-user")
	if admin*4 < ordinary {
		t.Errorf("wrong admin password returns much faster than a wrong user "+
			"password (%v vs %v) — the admin username is timing-detectable",
			admin, ordinary)
	}
	res := login(t, h, fxAdminUser, "wrong-password-here")
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("wrong admin password = %d, want 401", res.Code)
	}
	if !strings.Contains(res.Body.String(), "Incorrect username or password") {
		t.Error("admin failure does not use the uniform message")
	}
}

// TestLoginIsRateLimited: an unthrottled password endpoint is brute-forceable
// however good the hashing is.
func TestLoginIsRateLimited(t *testing.T) {
	h, s := newAuthEnv(t)
	u := mkUser(t, s, "target-user", fxUserPass, store.StatusApproved)

	var limited bool
	for i := 0; i < loginLimitPerWindow+3; i++ {
		res := login(t, h, u.Username, "guess-number-"+strings.Repeat("x", i))
		if res.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("no 429 within %d attempts", loginLimitPerWindow+3)
	}
	// Throttling must hold even for the correct password — otherwise the
	// limiter is trivially bypassed by guessing until one lands.
	res := login(t, h, u.Username, fxUserPass)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("correct password bypassed the limiter: %d", res.Code)
	}
	if sessionCookieFrom(res) != nil {
		t.Fatal("a throttled request still issued a session")
	}
}

// TestSessionFixation: a token planted before login must be dead afterwards,
// and the issued token must not be the one the client supplied.
func TestSessionFixation(t *testing.T) {
	h, s := newAuthEnv(t)
	u := mkUser(t, s, "fixation-user", fxUserPass, store.StatusApproved)

	// An attacker plants a session of their own and hands the victim the cookie.
	planted, err := store.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.st.CreateSession(planted, &store.Session{
		UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	res := postFormCookie(h, "/login",
		url.Values{"username": {u.Username}, "password": {fxUserPass}},
		&http.Cookie{Name: sessionCookie, Value: planted})
	if res.Code != http.StatusSeeOther {
		t.Fatalf("login = %d, want 303", res.Code)
	}
	issued := sessionCookieFrom(res)
	if issued == nil {
		t.Fatal("no session cookie issued")
	}
	if issued.Value == planted {
		t.Fatal("login reused the token supplied by the client")
	}
	if gone, err := s.st.GetSession(planted); err != nil || gone != nil {
		t.Fatalf("the planted session survived login: %#v, err=%v", gone, err)
	}
	if live, err := s.st.GetSession(issued.Value); err != nil || live == nil {
		t.Fatalf("the issued session does not resolve: %#v, err=%v", live, err)
	}
}

// TestSessionCookieSecureUnderTLS: Secure is set when the connection is
// HTTPS, including via the reverse proxy's header, and not otherwise.
func TestSessionCookieSecureUnderTLS(t *testing.T) {
	h, s := newAuthEnv(t)
	u := mkUser(t, s, "tls-user", fxUserPass, store.StatusApproved)
	form := url.Values{"username": {u.Username}, "password": {fxUserPass}}

	plain := postFormCookie(h, "/login", form)
	if c := sessionCookieFrom(plain); c == nil || c.Secure {
		t.Fatalf("plain HTTP cookie should not be Secure: %#v", c)
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(res, req)
	if c := sessionCookieFrom(res); c == nil || !c.Secure {
		t.Fatalf("proxied HTTPS cookie should be Secure: %#v", c)
	}
}

// TestPasswordsNeverAppearInResponses is a blunt guard against echoing a
// secret back into a page.
func TestPasswordsNeverAppearInResponses(t *testing.T) {
	h, s := newAuthEnv(t)
	mkUser(t, s, "echo-user", fxUserPass, store.StatusApproved)

	responses := []*httptest.ResponseRecorder{
		postForm(h, "/register", url.Values{"username": {"echo-new"},
			"password": {fxUserPass}, "confirm_password": {fxUserPass}}),
		login(t, h, "echo-user", fxUserPass),
		login(t, h, "echo-user", "wrong-"+fxUserPass),
		login(t, h, fxAdminUser, fxAdminPass),
		login(t, h, fxAdminUser, "wrong-"+fxAdminPass),
	}
	for i, res := range responses {
		body := res.Body.String()
		for _, secret := range []string{fxUserPass, fxAdminPass} {
			if strings.Contains(body, secret) {
				t.Errorf("response %d echoed a password back to the client", i)
			}
		}
	}
}

// TestConfigAdminNeverReachesTheDatabase: the static administrator must not
// exist as a users row.
func TestConfigAdminNeverReachesTheDatabase(t *testing.T) {
	h, s := newAuthEnv(t)
	if res := login(t, h, fxAdminUser, fxAdminPass); res.Code != http.StatusSeeOther {
		t.Fatalf("admin login = %d", res.Code)
	}
	if u, err := s.st.GetUserByUsername(fxAdminUser); err != nil || u != nil {
		t.Fatalf("admin was persisted as a user: %#v, err=%v", u, err)
	}
	users, err := s.st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Fatalf("admin login created %d users", len(users))
	}
}

func TestAdminLoginEnabledFlag(t *testing.T) {
	cases := []struct {
		user, pass string
		want       bool
	}{
		{"", "", false}, {"admin", "", false}, {"", "pw", false}, {"admin", "pw", true},
	}
	for _, c := range cases {
		cfg := &config.Config{AdminUser: c.user, AdminPassword: c.pass}
		if got := cfg.AdminLoginEnabled(); got != c.want {
			t.Errorf("AdminLoginEnabled(%q,%q) = %v, want %v", c.user, present(c.pass), got, c.want)
		}
		// Summary must never carry the password itself.
		if c.pass != "" && strings.Contains(cfg.Summary(), c.pass) {
			t.Error("Summary leaked ADMIN_PASSWORD")
		}
	}
}

func present(v string) string {
	if v == "" {
		return "(unset)"
	}
	return "set"
}
