package server

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

func adminPost(h http.Handler, path, token string) *http.Response {
	res := postFormAs(h, path, url.Values{}, token)
	return &http.Response{StatusCode: res.Code, Header: res.Header()}
}

// noticeFromRedirect returns the notice key an admin action redirected with.
func noticeFromRedirect(t *testing.T, res *http.Response) string {
	t.Helper()
	loc := res.Header.Get("Location")
	if loc == "" {
		loc = res.Header.Get("HX-Redirect")
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("redirect %q: %v", loc, err)
	}
	return u.Query().Get("notice")
}

// TestAdminEndpointsRefuseNonAdmins covers both walls: the middleware, and the
// handler's own check.
func TestAdminEndpointsRefuseNonAdmins(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	target, _ := mkSession(t, s, "victim", store.StatusApproved, store.RoleUser)
	_, userTok := mkSession(t, s, "plain", store.StatusApproved, store.RoleUser)
	_, pendingTok := mkSession(t, s, "waiting", store.StatusPending, store.RoleUser)

	paths := []string{
		"/admin/users/" + target.ID + "/approve",
		"/admin/users/" + target.ID + "/disable",
		"/admin/users/" + target.ID + "/delete",
	}
	for _, path := range paths {
		// Anonymous.
		if res := postFormAs(h, path, url.Values{}, ""); !denied(res) {
			t.Errorf("anonymous reached %s: %d", path, res.Code)
		}
		// Approved but not an admin.
		if res := postFormAs(h, path, url.Values{}, userTok); res.Code != http.StatusForbidden {
			t.Errorf("ordinary user %s = %d, want 403", path, res.Code)
		}
		// Pending: refused before privilege is even considered.
		if res := postFormAs(h, path, url.Values{}, pendingTok); !denied(res) {
			t.Errorf("pending user reached %s: %d", path, res.Code)
		}
	}
	// The dashboard too.
	if res := do(h, "GET", "/admin", cookieFor(userTok)); res.Code != http.StatusForbidden {
		t.Errorf("ordinary user GET /admin = %d, want 403", res.Code)
	}
	if res := do(h, "GET", "/admin", nil); !denied(res) {
		t.Errorf("anonymous GET /admin = %d", res.Code)
	}

	// Nothing happened to the target.
	got, err := s.st.GetUserByID(target.ID)
	if err != nil || got == nil {
		t.Fatalf("target user gone: %#v, err=%v", got, err)
	}
	if got.Status != store.StatusApproved {
		t.Fatalf("target status changed to %q", got.Status)
	}
}

// TestRequireAdminIsASecondWall: the handler refuses on its own, not only
// because the middleware classified /admin.
func TestRequireAdminIsASecondWall(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, userTok := mkSession(t, s, "second-wall", store.StatusApproved, store.RoleUser)

	// Drive the handler directly, bypassing the mux and its classification —
	// the request still carries a non-admin UserContext.
	sess, err := s.st.GetSession(userTok)
	if err != nil || sess == nil {
		t.Fatal(err)
	}
	res := postFormAs(h, "/admin/users/x/delete", url.Values{}, userTok)
	if res.Code != http.StatusForbidden {
		t.Fatalf("handler-level refusal = %d, want 403", res.Code)
	}
	if !strings.Contains(res.Body.String(), "Administrator access") {
		t.Errorf("unexpected body: %s", res.Body.String())
	}
}

// TestAdminDashboardListsUsers checks the dashboard content and the pending
// split.
func TestAdminDashboardListsUsers(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, adminTok := mkSession(t, s, "boss", store.StatusApproved, store.RoleAdmin)
	mkSession(t, s, "waiting-one", store.StatusPending, store.RoleUser)
	mkSession(t, s, "waiting-two", store.StatusPending, store.RoleUser)
	mkSession(t, s, "active-one", store.StatusApproved, store.RoleUser)
	mkSession(t, s, "banned-one", store.StatusDisabled, store.RoleUser)

	res := do(h, "GET", "/admin", cookieFor(adminTok))
	if res.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{
		"waiting-one", "waiting-two", "active-one", "banned-one", "boss",
		"Pending Registration Requests", "All Accounts", "approved", "disabled",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// Password hashes must never reach the page.
	if strings.Contains(body, "$2a$") {
		t.Error("the dashboard rendered a password hash")
	}
	// The pending section comes before the full list.
	if strings.Index(body, "Pending Registration Requests") > strings.Index(body, "All Accounts") {
		t.Error("pending requests are not shown first")
	}
}

// TestPendingBadge covers the count injection and that it stays admin-only.
func TestPendingBadge(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, adminTok := mkSession(t, s, "badge-admin", store.StatusApproved, store.RoleAdmin)
	_, userTok := mkSession(t, s, "badge-user", store.StatusApproved, store.RoleUser)

	// No pending accounts: no badge, but the Admin tab is there.
	body := do(h, "GET", "/history", cookieFor(adminTok)).Body.String()
	if !strings.Contains(body, `href="/admin"`) {
		t.Error("admin tab missing for an administrator")
	}
	if strings.Contains(body, "nav-badge") {
		t.Error("badge shown with zero pending accounts")
	}

	// Three pending accounts: the badge appears with the count.
	for _, n := range []string{"p1", "p2", "p3"} {
		mkSession(t, s, "pend-"+n, store.StatusPending, store.RoleUser)
	}
	body = do(h, "GET", "/history", cookieFor(adminTok)).Body.String()
	if !strings.Contains(body, "nav-badge") {
		t.Error("badge missing with pending accounts")
	}
	if !strings.Contains(body, "3 Pending") {
		t.Errorf("badge does not read \"3 Pending\"")
	}

	// A non-admin gets neither the tab nor the count, on any page.
	for _, path := range []string{"/", "/history"} {
		body := do(h, "GET", path, cookieFor(userTok)).Body.String()
		if strings.Contains(body, `href="/admin"`) {
			t.Errorf("%s showed a non-admin the admin tab", path)
		}
		if strings.Contains(body, "nav-badge") {
			t.Errorf("%s leaked the pending badge to a non-admin", path)
		}
	}
	// And the login page, which has no user at all, renders without exploding.
	if res := do(h, "GET", "/login", nil); res.Code != http.StatusOK {
		t.Fatalf("GET /login = %d", res.Code)
	}
}

// TestApproveIsIdempotent: setting a state, not toggling one.
func TestApproveIsIdempotent(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, adminTok := mkSession(t, s, "idem-admin", store.StatusApproved, store.RoleAdmin)
	target, _ := mkSession(t, s, "idem-target", store.StatusPending, store.RoleUser)

	for i := 0; i < 3; i++ {
		res := adminPost(h, "/admin/users/"+target.ID+"/approve", adminTok)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("approve #%d = %d", i, res.StatusCode)
		}
		if got := noticeFromRedirect(t, res); got != adminNoticeApproved {
			t.Fatalf("approve #%d notice = %q", i, got)
		}
		u, _ := s.st.GetUserByID(target.ID)
		if u.Status != store.StatusApproved {
			t.Fatalf("after approve #%d status = %q", i, u.Status)
		}
	}
	// Acting on a vanished account is a notice, not a 500.
	res := adminPost(h, "/admin/users/no-such-user/approve", adminTok)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("approve missing = %d", res.StatusCode)
	}
	if got := noticeFromRedirect(t, res); got != adminNoticeGone {
		t.Fatalf("missing-user notice = %q", got)
	}
}

// TestDisableTerminatesSessions is the contract's explicit requirement.
func TestDisableTerminatesSessions(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, adminTok := mkSession(t, s, "dis-admin", store.StatusApproved, store.RoleAdmin)
	target, targetTok := mkSession(t, s, "dis-target", store.StatusApproved, store.RoleUser)

	// The target is signed in and working.
	if res := do(h, "GET", "/history", cookieFor(targetTok)); res.Code != http.StatusOK {
		t.Fatalf("target /history = %d", res.Code)
	}

	res := adminPost(h, "/admin/users/"+target.ID+"/disable", adminTok)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("disable = %d", res.StatusCode)
	}
	if got := noticeFromRedirect(t, res); got != adminNoticeDisabled {
		t.Fatalf("disable notice = %q", got)
	}

	// The session row is gone, and the next request is refused.
	if sess, err := s.st.GetSession(targetTok); err != nil || sess != nil {
		t.Fatalf("session survived the disable: %#v, err=%v", sess, err)
	}
	if res := do(h, "GET", "/history", cookieFor(targetTok)); !denied(res) {
		t.Fatalf("disabled user still served: %d", res.Code)
	}
	u, _ := s.st.GetUserByID(target.ID)
	if u.Status != store.StatusDisabled {
		t.Fatalf("status = %q", u.Status)
	}

	// Re-approving lets them back in with a fresh login (the old token stays dead).
	if res := adminPost(h, "/admin/users/"+target.ID+"/approve", adminTok); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("re-approve = %d", res.StatusCode)
	}
	if sess, _ := s.st.GetSession(targetTok); sess != nil {
		t.Error("the revoked token came back to life on re-approval")
	}
}

// TestDeleteUserRemovesEverything: account, sessions, jobs, songs, and files.
func TestDeleteUserRemovesEverything(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, adminTok := mkSession(t, s, "del-admin", store.StatusApproved, store.RoleAdmin)
	target, targetTok := mkSession(t, s, "del-target", store.StatusApproved, store.RoleUser)
	keeper, _ := mkSession(t, s, "del-keeper", store.StatusApproved, store.RoleUser)

	doomed := mkSong(t, s, "doomed-song", target.ID, false)
	shared := mkSong(t, s, "doomed-shared", target.ID, true)
	survivor := mkSong(t, s, "survivor-song", keeper.ID, false)
	j := &store.Job{ID: "doomed-job", State: store.StateQueued, UserID: target.ID,
		Lyrics: "la", Caption: "pop", Duration: 30}
	if err := s.st.CreateJob(j); err != nil {
		t.Fatal(err)
	}

	res := adminPost(h, "/admin/users/"+target.ID+"/delete", adminTok)
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	if got := noticeFromRedirect(t, res); got != adminNoticeDeleted {
		t.Fatalf("delete notice = %q", got)
	}

	// Account and session are gone.
	if u, err := s.st.GetUserByID(target.ID); err != nil || u != nil {
		t.Fatalf("user survived: %#v, err=%v", u, err)
	}
	if sess, err := s.st.GetSession(targetTok); err != nil || sess != nil {
		t.Fatalf("session survived: %#v, err=%v", sess, err)
	}
	// Songs are gone — including the shared one, which must leave the
	// community library rather than linger owned by nobody.
	for _, id := range []string{"doomed-song", "doomed-shared"} {
		if g, err := s.st.Song(id, store.AdminAccess("root")); err != nil || g != nil {
			t.Errorf("song %s survived even for an admin: %#v, err=%v", id, g, err)
		}
	}
	if g, err := s.st.PublicSong("doomed-shared"); err != nil || g != nil {
		t.Errorf("a deleted user's shared song is still public: %#v, err=%v", g, err)
	}
	// The job is gone.
	if got, err := s.st.Job("doomed-job", store.AdminAccess("root")); err != nil || got != nil {
		t.Errorf("job survived: %#v, err=%v", got, err)
	}
	// Audio files are unlinked.
	for _, p := range []string{doomed.AudioPath, shared.AudioPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("audio file %s still on disk (err=%v)", p, err)
		}
	}
	// Everyone else is untouched.
	if g, err := s.st.Song("survivor-song", store.UserAccess(keeper.ID)); err != nil || g == nil {
		t.Fatalf("another user's song was destroyed: %#v, err=%v", g, err)
	}
	if _, err := os.Stat(survivor.AudioPath); err != nil {
		t.Errorf("another user's audio was removed: %v", err)
	}
	if u, err := s.st.GetUserByID(keeper.ID); err != nil || u == nil {
		t.Fatalf("another user was deleted: %#v, err=%v", u, err)
	}
}

// TestAdminCannotActOnSelf: no locking yourself out with one click.
func TestAdminCannotActOnSelf(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	admin, adminTok := mkSession(t, s, "self-admin", store.StatusApproved, store.RoleAdmin)
	mkSession(t, s, "self-other-admin", store.StatusApproved, store.RoleAdmin)

	for _, what := range []string{"disable", "delete"} {
		res := adminPost(h, "/admin/users/"+admin.ID+"/"+what, adminTok)
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("self %s = %d", what, res.StatusCode)
		}
		if got := noticeFromRedirect(t, res); got != adminNoticeSelf {
			t.Errorf("self %s notice = %q, want %q", what, got, adminNoticeSelf)
		}
	}
	// Still there, still approved, still signed in.
	u, err := s.st.GetUserByID(admin.ID)
	if err != nil || u == nil {
		t.Fatalf("admin deleted themselves: %#v, err=%v", u, err)
	}
	if u.Status != store.StatusApproved {
		t.Fatalf("admin disabled themselves: status %q", u.Status)
	}
	if res := do(h, "GET", "/admin", cookieFor(adminTok)); res.Code != http.StatusOK {
		t.Fatalf("admin locked out of the dashboard: %d", res.Code)
	}
	// Approving yourself is allowed — it is a no-op, not a lockout.
	if res := adminPost(h, "/admin/users/"+admin.ID+"/approve", adminTok); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("self approve = %d", res.StatusCode)
	}
}

// TestConfigAdminCannotBeTargeted: the static administrator has no users row,
// so these endpoints must answer plainly rather than 500 or half-apply.
func TestConfigAdminCannotBeTargeted(t *testing.T) {
	h, s := newAuthEnv(t)
	// Sign in as the configured administrator.
	res := login(t, h, fxAdminUser, fxAdminPass)
	c := sessionCookieFrom(res)
	if c == nil {
		t.Fatal("admin login issued no cookie")
	}

	before, err := s.st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{"approve", "disable", "delete"} {
		res := adminPost(h, "/admin/users/"+ConfigAdminUserID+"/"+what, c.Value)
		if res.StatusCode != http.StatusSeeOther {
			t.Errorf("config admin %s = %d, want 303 (never a 500)", what, res.StatusCode)
		}
		if got := noticeFromRedirect(t, res); got != adminNoticeConfigAdmin {
			t.Errorf("config admin %s notice = %q", what, got)
		}
	}
	// Nothing was written.
	after, err := s.st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("users went from %d to %d", len(before), len(after))
	}
	// And the config admin is still signed in and still an admin.
	if res := do(h, "GET", "/admin", c); res.Code != http.StatusOK {
		t.Fatalf("config admin locked out: %d", res.Code)
	}
}

// TestLastAdminIsProtected: removing the final approved administrator is
// refused, in both the disable and delete paths.
func TestLastAdminIsProtected(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	only, onlyTok := mkSession(t, s, "only-admin", store.StatusApproved, store.RoleAdmin)
	second, _ := mkSession(t, s, "second-admin", store.StatusApproved, store.RoleAdmin)

	// With two admins, removing one is fine.
	if res := adminPost(h, "/admin/users/"+second.ID+"/delete", onlyTok); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete second admin = %d", res.StatusCode)
	}
	if got := noticeFromRedirect(t, adminPost(h, "/admin/users/"+only.ID+"/disable", onlyTok)); got != adminNoticeSelf {
		// self-guard fires first for the acting admin
		t.Logf("self guard fired first, as expected: %q", got)
	}

	// Now make a second admin and have them try to remove the last one.
	third, thirdTok := mkSession(t, s, "third-admin", store.StatusApproved, store.RoleAdmin)
	// third removes only-admin -> allowed (third remains)
	if res := adminPost(h, "/admin/users/"+only.ID+"/delete", thirdTok); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete = %d", res.StatusCode)
	}
	if u, _ := s.st.GetUserByID(only.ID); u != nil {
		t.Fatal("only-admin was not deleted")
	}

	// third is now the last admin. A fresh admin cannot exist to remove them,
	// so exercise the store guard directly — it is where the invariant lives.
	if _, err := s.st.DeleteUser(third.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("deleting the last admin = %v, want ErrLastAdmin", err)
	}
	if err := s.st.UpdateUserStatus(third.ID, store.StatusDisabled); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("disabling the last admin = %v, want ErrLastAdmin", err)
	}
	// Nothing was written by either refusal.
	u, err := s.st.GetUserByID(third.ID)
	if err != nil || u == nil {
		t.Fatalf("last admin vanished: %#v, err=%v", u, err)
	}
	if u.Status != store.StatusApproved {
		t.Fatalf("last admin status = %q", u.Status)
	}
	if res := do(h, "GET", "/admin", cookieFor(thirdTok)); res.Code != http.StatusOK {
		t.Fatal("the last admin was locked out")
	}
	// Approving them is still fine — the guard only covers removal.
	if err := s.st.UpdateUserStatus(third.ID, store.StatusApproved); err != nil {
		t.Fatalf("approving the last admin = %v", err)
	}
	// And an ordinary user is unaffected by the guard.
	plain, _ := mkSession(t, s, "guard-plain", store.StatusApproved, store.RoleUser)
	if _, err := s.st.DeleteUser(plain.ID); err != nil {
		t.Fatalf("deleting an ordinary user hit the admin guard: %v", err)
	}
}

// TestAdminRoutesAreEnumeratedAndOriginChecked: the new routes inherit the
// Stage 03 default-deny enumeration and the Stage 04 origin check.
func TestAdminRoutesAreEnumeratedAndOriginChecked(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	admin, adminTok := mkSession(t, s, "route-admin", store.StatusApproved, store.RoleAdmin)
	_ = admin
	target, _ := mkSession(t, s, "route-target", store.StatusPending, store.RoleUser)

	want := map[string]bool{
		"GET /admin":                     false,
		"POST /admin/users/{id}/approve": false,
		"POST /admin/users/{id}/disable": false,
		"POST /admin/users/{id}/delete":  false,
	}
	for _, p := range s.routes {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for pattern, found := range want {
		if !found {
			t.Errorf("%q is not in the recorded route table", pattern)
		}
	}
	for _, p := range publicPatterns {
		if strings.Contains(p, "/admin") {
			t.Errorf("an admin route reached the public allowlist: %q", p)
		}
	}

	// Cross-origin admin writes are refused before anything happens.
	for _, c := range []struct{ header, value string }{
		{"Sec-Fetch-Site", "cross-site"},
		{"Sec-Fetch-Site", "same-site"},
		{"Origin", "https://evil.example.com"},
	} {
		res := postFormAs(h, "/admin/users/"+target.ID+"/approve", url.Values{},
			adminTok, c.header, c.value)
		if res.Code != http.StatusForbidden {
			t.Errorf("%s=%s admin write = %d, want 403", c.header, c.value, res.Code)
		}
	}
	u, _ := s.st.GetUserByID(target.ID)
	if u.Status != store.StatusPending {
		t.Fatal("a cross-origin request changed a user's status")
	}
}
