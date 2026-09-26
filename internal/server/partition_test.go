package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// libraryBodies returns the three surfaces that render songs, for one viewer.
func libraryBodies(t *testing.T, h http.Handler, token string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, path := range []string{"/history", "/history/personal", "/history/public"} {
		out[path] = getAs(t, h, path, token)
	}
	return out
}

// getAs fetches path as the token's user, failing unless it answers 200. A
// check for what a page must not contain means nothing on an error page.
func getAs(t *testing.T, h http.Handler, path, token string) string {
	t.Helper()
	res := do(h, "GET", path, cookieFor(token))
	if res.Code != http.StatusOK {
		t.Fatalf("GET %s = %d", path, res.Code)
	}
	return res.Body.String()
}

// wantNone reports every string in secrets that body contains.
func wantNone(t *testing.T, where, body string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Errorf("%s exposed %q", where, secret)
		}
	}
}

// pagedPaths is every history URL that takes a page number, at page v.
func pagedPaths(v string) []string {
	return []string{
		"/history/personal?page=" + v,
		"/history/public?page=" + v,
		"/history?mine=" + v + "&public=" + v,
	}
}

// TestPersonalSongsNeverLeak is the point of this stage: nothing of user A's
// reaches user B through any history surface, at any page, unless A shares it.
func TestPersonalSongsNeverLeak(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "leak-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "leak-bob", store.StatusApproved, store.RoleUser)

	// Alice owns enough songs to span several pages; bob owns one.
	const aliceSongs = 45
	for i := 0; i < aliceSongs; i++ {
		mkSong(t, s, fmt.Sprintf("alice-%02d", i), alice.ID, false)
	}
	bobSong := mkSong(t, s, "bob-only", mkUserFor(t, s, bobTok), false)

	// Page 1 of every surface, for bob.
	for path, body := range libraryBodies(t, h, bobTok) {
		wantNone(t, path, body, "alice-", alice.ID)
	}

	// ...and every page bob can reach, including past the end of his own list
	// and deep into the range where alice's songs live.
	for _, page := range []string{"1", "2", "3", "4", "5", "99", "10000"} {
		for _, path := range pagedPaths(page) {
			wantNone(t, "GET "+path, getAs(t, h, path, bobTok), "alice-")
		}
	}

	// Bob's own song is present, so the absence above is isolation and not a
	// broken query.
	if !strings.Contains(getAs(t, h, "/history/personal", bobTok), bobSong.ID) {
		t.Fatal("bob cannot see his own song")
	}

	// Alice sees all of hers and none of bob's.
	aliceBody := getAs(t, h, "/history/personal", aliceTok)
	wantNone(t, "alice's library", aliceBody, "bob-only")
	if !strings.Contains(aliceBody, "alice-44") {
		t.Error("alice's newest song is missing from page 1")
	}

	sharingExposesOnlyThatSong(t, h, s, alice.ID, bobTok, aliceSongs)
}

// sharingExposesOnlyThatSong shares alice-00, checks it alone reaches bob's
// community library, then un-shares it and checks it is gone again.
func sharingExposesOnlyThatSong(t *testing.T, h http.Handler, s *Server, aliceID, bobTok string, songs int) {
	t.Helper()
	if _, err := s.st.SetSongPublic("alice-00", true, store.UserAccess(aliceID)); err != nil {
		t.Fatal(err)
	}
	pub := getAs(t, h, "/history/public", bobTok)
	if !strings.Contains(pub, "alice-00") {
		t.Error("a shared song did not reach the community library")
	}
	others := make([]string, 0, songs-1)
	for i := 1; i < songs; i++ {
		others = append(others, fmt.Sprintf("alice-%02d", i))
	}
	wantNone(t, "the community library after sharing alice-00", pub, others...)

	// And un-sharing removes it again.
	if _, err := s.st.SetSongPublic("alice-00", false, store.UserAccess(aliceID)); err != nil {
		t.Fatal(err)
	}
	wantNone(t, "the community library after un-sharing", getAs(t, h, "/history/public", bobTok), "alice-00")
}

// mkUserFor returns the user id behind a session token.
func mkUserFor(t *testing.T, s *Server, token string) string {
	t.Helper()
	sess, err := s.st.GetSession(token)
	if err != nil || sess == nil {
		t.Fatalf("session lookup: %#v, err=%v", sess, err)
	}
	return sess.UserID
}

// TestPersonalSectionIsOwnSongsForAdminToo: AdminAccess lifts the ownership
// predicate everywhere else, so "My Songs" is the one place it must not.
func TestPersonalSectionIsOwnSongsForAdminToo(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "adm-owner", store.StatusApproved, store.RoleUser)
	admin, adminTok := mkSession(t, s, "adm-viewer", store.StatusApproved, store.RoleAdmin)

	mkSong(t, s, "someone-elses", alice.ID, false)
	mkSong(t, s, "admins-own", admin.ID, false)

	body := do(h, "GET", "/history/personal", cookieFor(adminTok)).Body.String()
	if !strings.Contains(body, "admins-own") {
		t.Error("the admin's own song is missing from My Songs")
	}
	if strings.Contains(body, "someone-elses") {
		t.Error("My Songs showed the admin every song in the system")
	}

	// The community section is still only shared songs, even for an admin.
	pub := do(h, "GET", "/history/public", cookieFor(adminTok)).Body.String()
	if strings.Contains(pub, "someone-elses") || strings.Contains(pub, "admins-own") {
		t.Error("the community library showed private songs to an admin")
	}
}

// TestCommunityCardsWithholdOwnerIdentity pins the disclosure decision: a
// community card names the song, never the person. Usernames are the login
// identifier here, so publishing them would hand out half of every credential
// pair and undo the enumeration defence.
func TestCommunityCardsWithholdOwnerIdentity(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "identity-alice", store.StatusApproved, store.RoleUser)
	_, bobTok := mkSession(t, s, "identity-bob", store.StatusApproved, store.RoleUser)
	g := mkSong(t, s, "shared-one", alice.ID, true)

	body := do(h, "GET", "/history/public", cookieFor(bobTok)).Body.String()
	if !strings.Contains(body, g.ID) {
		t.Fatal("the shared song is not in the community library")
	}
	for _, secret := range []string{"identity-alice", alice.ID} {
		if strings.Contains(body, secret) {
			t.Errorf("the community library exposed %q", secret)
		}
	}
	// Owner-only columns are withheld too.
	if strings.Contains(body, "hx-delete") {
		t.Error("a non-owner was shown a delete control in the community library")
	}
}

// TestPagingParametersAreClamped: limit and offset reach SQL, so hostile page
// values must be corrected rather than trusted.
func TestPagingParametersAreClamped(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	u, token := mkSession(t, s, "page-user", store.StatusApproved, store.RoleUser)
	for i := 0; i < 25; i++ {
		mkSong(t, s, fmt.Sprintf("p-%02d", i), u.ID, false)
	}

	hostile := []string{
		"-1", "0", "abc", "", "1e9", "99999999999999999999",
		"9223372036854775807", "1; DROP TABLE songs", "2 OR 1=1",
		"../../etc/passwd", "NaN", "+1", "1.5", "-9223372036854775808",
	}
	for _, raw := range hostile {
		for _, path := range pagedPaths(url.QueryEscape(raw)) {
			if res := do(h, "GET", path, cookieFor(token)); res.Code != http.StatusOK {
				t.Errorf("GET %s = %d, want 200", path, res.Code)
			}
		}
	}
	// The table survived every one of those.
	songs, err := s.st.PersonalSongs(u.ID, 100, 0)
	if err != nil || len(songs) != 25 {
		t.Fatalf("songs after hostile paging = %d, err=%v; want 25", len(songs), err)
	}

	// A page never returns more than pageSize rows.
	if n := strings.Count(getAs(t, h, "/history/personal?page=1", token), "hx-delete"); n != pageSize {
		t.Fatalf("page 1 rendered %d rows, want %d", n, pageSize)
	}
	// Page 2 holds the remainder and offers no "Older" link.
	body2 := getAs(t, h, "/history/personal?page=2", token)
	if n := strings.Count(body2, "hx-delete"); n != 5 {
		t.Fatalf("page 2 rendered %d rows, want 5", n)
	}
	if strings.Contains(body2, "page=3") {
		t.Error("a phantom Older link appeared on the last page")
	}
	// Paging past the end is empty, not an error.
	getAs(t, h, "/history/personal?page=500", token)
}

// TestHistoryFragmentsRequireASession: the fragments are real routes and carry
// the page's authorisation. The route-enumeration test covers them too; this
// asserts it directly, including the unapproved case.
func TestHistoryFragmentsRequireASession(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, _ := mkSession(t, s, "frag-alice", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "frag-song", alice.ID, true)

	for _, path := range []string{"/history/personal", "/history/public"} {
		if res := do(h, "GET", path, nil); !denied(res) {
			t.Errorf("anonymous reached %s: %d", path, res.Code)
		}
		if body := do(h, "GET", path, nil).Body.String(); strings.Contains(body, "frag-song") {
			t.Errorf("%s leaked a song to an anonymous caller", path)
		}
	}

	// A pending user is refused too — approval gates the library.
	_, pendingTok := mkSession(t, s, "frag-pending", store.StatusPending, store.RoleUser)
	for _, path := range []string{"/history/personal", "/history/public"} {
		if res := do(h, "GET", path, cookieFor(pendingTok)); !denied(res) {
			t.Errorf("a pending user reached %s: %d", path, res.Code)
		}
	}
}

// TestHistoryFragmentsAreEnumerated confirms the Stage 03 default-deny test
// actually covers the two routes this stage added.
func TestHistoryFragmentsAreEnumerated(t *testing.T) {
	_, _, s := newTestEnvWith(t, nil)
	want := map[string]bool{
		"GET /history/personal": false,
		"GET /history/public":   false,
	}
	for _, p := range s.routes {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for pattern, found := range want {
		if !found {
			t.Errorf("%q is not in the recorded route table, so the "+
				"default-deny enumeration does not cover it", pattern)
		}
	}
	for _, p := range publicPatterns {
		if strings.Contains(p, "/history") {
			t.Errorf("a history route was added to the public allowlist: %q", p)
		}
	}
}

// TestEmptyStatesRender: a new user with no songs, and an empty community
// library, must say something rather than render a blank panel.
func TestEmptyStatesRender(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	_, token := mkSession(t, s, "empty-user", store.StatusApproved, store.RoleUser)

	body := getAs(t, h, "/history", token)
	wantAll(t, "empty history", body,
		"No songs in your library yet",
		"Nothing has been shared yet",
		"My Songs", "Community Songs")
	// No paging controls on an empty section.
	if strings.Contains(body, "page=2") {
		t.Error("an empty section offered a next page")
	}

	// Each fragment stands alone.
	for _, c := range []struct{ path, want string }{
		{"/history/personal", "No songs in your library yet"},
		{"/history/public", "Nothing has been shared yet"},
	} {
		wantAll(t, "GET "+c.path, getAs(t, h, c.path, token), c.want)
	}

	// A user with songs but nothing shared still gets the community message.
	u := mkUserFor(t, s, token)
	mkSong(t, s, "private-only", u, false)
	body = getAs(t, h, "/history", token)
	if !strings.Contains(body, "Nothing has been shared yet") {
		t.Error("community empty state missing when only private songs exist")
	}
	if strings.Contains(body, "No songs in your library yet") {
		t.Error("personal empty state shown when the user has a song")
	}
}

// TestOwnSharedSongIsControllableFromTheCommunityList: a user's own song shown
// in the community section keeps its owner controls, and other people's do not.
func TestOwnSharedSongIsControllableFromTheCommunityList(t *testing.T) {
	h, _, s := newTestEnvWith(t, nil)
	alice, aliceTok := mkSession(t, s, "ctl-alice", store.StatusApproved, store.RoleUser)
	bob, bobTok := mkSession(t, s, "ctl-bob", store.StatusApproved, store.RoleUser)
	mkSong(t, s, "alice-shared", alice.ID, true)
	mkSong(t, s, "bob-shared", bob.ID, true)

	// Alice sees a control for hers and none for bob's.
	body := do(h, "GET", "/history/public", cookieFor(aliceTok)).Body.String()
	if !strings.Contains(body, `hx-target="#share-alice-shared"`) {
		t.Error("alice has no sharing control for her own song in the community list")
	}
	if strings.Contains(body, `hx-target="#share-bob-shared"`) {
		t.Error("alice was given a sharing control for bob's song")
	}
	// And symmetrically for bob.
	body = do(h, "GET", "/history/public", cookieFor(bobTok)).Body.String()
	if !strings.Contains(body, `hx-target="#share-bob-shared"`) {
		t.Error("bob has no control for his own song")
	}
	if strings.Contains(body, `hx-target="#share-alice-shared"`) {
		t.Error("bob was given a control for alice's song")
	}

	// The control is cosmetic; the endpoint is what enforces it.
	if res := setPublic(h, "alice-shared", bobTok, "0"); res.Code != http.StatusNotFound {
		t.Errorf("bob un-shared alice's song: %d", res.Code)
	}
	if !storedSong(t, s, "alice-shared", alice.ID).IsPublic {
		t.Fatal("bob's request changed alice's song")
	}
}
