package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// legacy is the scope every pre-multi-user row lives in.
var legacy = UserAccess(LegacyUserID)

// testToken pads a label out to MinTokenLen so CreateSession accepts it while
// the token stays readable in failure messages.
func testToken(label string) string {
	return label + strings.Repeat("0", MinTokenLen)
}

func testJob(id string) *Job {
	now := time.Now().UTC()
	return &Job{ID: id, State: StateQueued, Lyrics: "la", Caption: "pop",
		Duration: 30, CreatedAt: now, UpdatedAt: now}
}

func TestAmbiguousSubmittingJobNeverRequeues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	j := testJob("j1")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionJob(j.ID, StateQueued, StateSubmitting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate process restart in the remote/local dual-write window.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ambiguous, err := s.SubmittingJobs()
	if err != nil || len(ambiguous) != 1 {
		t.Fatalf("submitting jobs = %d, err=%v", len(ambiguous), err)
	}
	if err := s.OrphanSubmission(j.ID, "", "submit-ambiguous"); err != nil {
		t.Fatal(err)
	}
	queued, err := s.DequeueQueued(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 0 {
		t.Fatalf("ambiguous job became eligible for resubmit: %d queued", len(queued))
	}
	got, err := s.Job(j.ID, legacy)
	if err != nil || got.State != StateFailed || got.Error != "submit-ambiguous" {
		t.Fatalf("recovered job = %#v, err=%v", got, err)
	}
}

func TestOrphanSubmissionDurablyRecordsRemoteID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	j := testJob("j2")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.TransitionJob(j.ID, StateQueued, StateSubmitting, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.OrphanSubmission(j.ID, "rp-123", "submit-cas"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(j.ID, legacy)
	if err != nil || got.RunPodID != "rp-123" || got.State != StateFailed {
		t.Fatalf("orphan = %#v, err=%v", got, err)
	}
}

func TestOneSongPerJob(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, id := range []string{"song-a", "song-b"} {
		if err := s.CreateSong(&Song{ID: id, JobID: "job-one", Lyrics: "la",
			Caption: "pop", Duration: 30, Engine: "stub", Delivery: "base64",
			AudioPath: "/tmp/x.wav", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	songs, err := s.Songs(10, 0, legacy)
	if err != nil || len(songs) != 1 {
		t.Fatalf("songs = %d, err=%v", len(songs), err)
	}
}

func TestDeleteSong(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	j := testJob("job-del")
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	song := &Song{
		ID: "song-del", JobID: j.ID, Lyrics: "lyrics", Caption: "caption",
		Duration: 45, Engine: "diffusers", Delivery: "base64",
		AudioPath: "/tmp/del.m4a", Title: "Delete Me", CreatedAt: now,
	}
	if err := s.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	// Verify song exists
	got, err := s.Song(song.ID, legacy)
	if err != nil || got == nil {
		t.Fatalf("expected song to exist, got %v, err=%v", got, err)
	}

	// Delete song
	deleted, err := s.DeleteSong(song.ID, legacy)
	if err != nil {
		t.Fatalf("DeleteSong failed: %v", err)
	}
	if deleted == nil || deleted.ID != song.ID {
		t.Fatalf("expected deleted song %s, got %#v", song.ID, deleted)
	}

	// Verify song is removed
	gotSong, err := s.Song(song.ID, legacy)
	if err != nil {
		t.Fatalf("Song lookup after delete error: %v", err)
	}
	if gotSong != nil {
		t.Fatalf("expected song to be deleted, found: %#v", gotSong)
	}

	// Verify job is removed
	gotJob, err := s.Job(j.ID, legacy)
	if err != nil {
		t.Fatalf("Job lookup after delete error: %v", err)
	}
	if gotJob != nil {
		t.Fatalf("expected job to be deleted, found: %#v", gotJob)
	}

	// Deleting a non-existent song returns nil, nil
	missing, err := s.DeleteSong("non-existent", legacy)
	if err != nil {
		t.Fatalf("DeleteSong non-existent error: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for non-existent song, got %#v", missing)
	}
}

func TestUpdateSongTitle(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().UTC()
	song := &Song{
		ID: "song-upd", JobID: "j-upd", Lyrics: "la", Caption: "pop",
		Duration: 30, Engine: "diffusers", Delivery: "base64",
		AudioPath: "/tmp/u.m4a", Title: "Original Title", CreatedAt: now,
	}
	if err := s.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateSongTitle(song.ID, "New Custom Title", legacy); err != nil {
		t.Fatalf("UpdateSongTitle failed: %v", err)
	}

	got, err := s.Song(song.ID, legacy)
	if err != nil || got == nil {
		t.Fatalf("Song lookup error: %v", err)
	}
	if got.Title != "New Custom Title" {
		t.Fatalf("expected title 'New Custom Title', got %q", got.Title)
	}

	// Update non-existent song
	if err := s.UpdateSongTitle("non-existent", "Title", legacy); err == nil {
		t.Fatalf("expected error when updating non-existent song, got nil")
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testUser(id, name string) *User {
	return &User{ID: id, Username: name, PasswordHash: "hash-" + id,
		Status: StatusPending, Role: RoleUser}
}

func mustCreateUser(t *testing.T, s *Store, u *User) *User {
	t.Helper()
	if err := s.CreateUser(u); err != nil {
		t.Fatalf("CreateUser(%s): %v", u.Username, err)
	}
	return u
}

// TestMigrateIsIdempotent reopens a populated database repeatedly. Each Open
// re-runs migrate(), so a non-idempotent ALTER would fail here.
func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := range 3 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		u := testUser(fmt.Sprintf("u%d", i), fmt.Sprintf("user%d", i))
		mustCreateUser(t, s, u)
		if err := s.CreateJob(testJob(fmt.Sprintf("j%d", i))); err != nil {
			t.Fatalf("CreateJob #%d: %v", i+1, err)
		}
		users, err := s.ListUsers()
		if err != nil {
			t.Fatal(err)
		}
		if len(users) != i+1 {
			t.Fatalf("after Open #%d: %d users, want %d", i+1, len(users), i+1)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigrateBackfillsLegacyRows builds a database on the pre-multi-user
// schema, then migrates it. Existing songs must survive, land on the legacy
// owner, and stay private.
func TestMigrateBackfillsLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE jobs (
  id TEXT PRIMARY KEY, state TEXT NOT NULL, runpod_id TEXT NOT NULL DEFAULT '',
  lyrics TEXT NOT NULL, caption TEXT NOT NULL, duration_s REAL NOT NULL,
  seed INTEGER, error TEXT NOT NULL DEFAULT '', retries INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL);
CREATE TABLE songs (
  id TEXT PRIMARY KEY, job_id TEXT NOT NULL, lyrics TEXT NOT NULL,
  caption TEXT NOT NULL, duration_s REAL NOT NULL, seed INTEGER,
  engine TEXT NOT NULL, delivery TEXT NOT NULL, audio_path TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '', created_at TIMESTAMP NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.Exec(`INSERT INTO jobs (id, state, lyrics, caption, duration_s,
		created_at, updated_at) VALUES ('old-job', 'succeeded', 'la', 'pop', 30, ?, ?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO songs (id, job_id, lyrics, caption, duration_s,
		engine, delivery, audio_path, title, created_at)
		VALUES ('old-song', 'old-job', 'la', 'pop', 30, 'diffusers', 'base64',
		'/tmp/old.m4a', 'Old Song', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrating a populated legacy database: %v", err)
	}
	defer s.Close()

	g, err := s.Song("old-song", legacy)
	if err != nil || g == nil {
		t.Fatalf("legacy song lost by migration: %#v err=%v", g, err)
	}
	if g.UserID != LegacyUserID {
		t.Fatalf("legacy song user_id = %q, want %q", g.UserID, LegacyUserID)
	}
	if g.IsPublic {
		t.Fatalf("legacy song became public by default")
	}
	if g.Title != "Old Song" {
		t.Fatalf("legacy song title = %q, want %q", g.Title, "Old Song")
	}
	j, err := s.Job("old-job", legacy)
	if err != nil || j == nil {
		t.Fatalf("legacy job lost by migration: %#v err=%v", j, err)
	}
	if j.UserID != LegacyUserID {
		t.Fatalf("legacy job user_id = %q, want %q", j.UserID, LegacyUserID)
	}
}

func TestUsernameUniquenessIsCaseInsensitive(t *testing.T) {
	s := openTemp(t)
	mustCreateUser(t, s, testUser("u1", "Alice"))

	err := s.CreateUser(testUser("u2", "ALICE"))
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("second signup as ALICE = %v, want ErrUsernameTaken", err)
	}
	users, err := s.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("case-variant signup created %d accounts, want 1", len(users))
	}

	// ...and lookup finds the account regardless of the case supplied.
	for _, name := range []string{"Alice", "alice", "ALICE", "aLiCe"} {
		got, err := s.GetUserByUsername(name)
		if err != nil || got == nil {
			t.Fatalf("GetUserByUsername(%q) = %#v, err=%v", name, got, err)
		}
		if got.ID != "u1" {
			t.Fatalf("GetUserByUsername(%q) resolved to %q", name, got.ID)
		}
	}
}

func TestUserCRUD(t *testing.T) {
	s := openTemp(t)

	if got, err := s.GetUserByID("nope"); err != nil || got != nil {
		t.Fatalf("missing user = %#v, err=%v; want nil, nil", got, err)
	}
	if got, err := s.GetUserByUsername("nope"); err != nil || got != nil {
		t.Fatalf("missing username = %#v, err=%v; want nil, nil", got, err)
	}

	u := mustCreateUser(t, s, testUser("u1", "alice"))
	got, err := s.GetUserByID(u.ID)
	if err != nil || got == nil {
		t.Fatalf("GetUserByID = %#v, err=%v", got, err)
	}
	if got.PasswordHash != "hash-u1" || got.Status != StatusPending || got.Role != RoleUser {
		t.Fatalf("round-tripped user = %#v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not persisted: %#v", got)
	}

	// Defaults are applied when the caller leaves status/role unset.
	bare := &User{ID: "u2", Username: "bob", PasswordHash: "h"}
	mustCreateUser(t, s, bare)
	got2, err := s.GetUserByID("u2")
	if err != nil || got2 == nil {
		t.Fatal(err)
	}
	if got2.Status != StatusPending || got2.Role != RoleUser {
		t.Fatalf("defaults not applied: status=%q role=%q", got2.Status, got2.Role)
	}

	if n, err := s.CountPendingUsers(); err != nil || n != 2 {
		t.Fatalf("CountPendingUsers = %d, err=%v; want 2", n, err)
	}
	if err := s.UpdateUserStatus("u1", StatusApproved); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountPendingUsers(); err != nil || n != 1 {
		t.Fatalf("after approval CountPendingUsers = %d, err=%v; want 1", n, err)
	}
	if got, _ := s.GetUserByID("u1"); got.Status != StatusApproved {
		t.Fatalf("status = %q, want approved", got.Status)
	}

	if err := s.UpdateUserStatus("u1", "bogus"); err == nil {
		t.Fatalf("expected an error for an unknown status")
	}
	if err := s.UpdateUserStatus("ghost", StatusApproved); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("status update on a missing user = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteUser("ghost"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("DeleteUser on a missing user = %v, want sql.ErrNoRows", err)
	}

	if err := s.DeleteUser("u2"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetUserByID("u2"); err != nil || got != nil {
		t.Fatalf("deleted user still readable: %#v", got)
	}
	users, err := s.ListUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("ListUsers = %d, err=%v; want 1", len(users), err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := openTemp(t)
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	now := time.Now().UTC()

	liveTok, deadTok := testToken("tok-live"), testToken("tok-dead")
	live := &Session{UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(liveTok, live); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(liveTok)
	if err != nil || got == nil {
		t.Fatalf("GetSession = %#v, err=%v", got, err)
	}
	if got.UserID != u.ID || got.Username != "alice" {
		t.Fatalf("session round-trip lost fields: %#v", got)
	}
	if !got.ExpiresAt.Equal(live.ExpiresAt) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, live.ExpiresAt)
	}
	// A plain user is never admin, and status is resolved from the users row.
	if got.IsAdmin || got.Status != StatusPending {
		t.Fatalf("resolved privilege = admin:%v status:%q, want false/pending",
			got.IsAdmin, got.Status)
	}

	if got, err := s.GetSession(testToken("no-such")); err != nil || got != nil {
		t.Fatalf("unknown token = %#v, err=%v; want nil, nil", got, err)
	}
	if err := s.CreateSession(testToken("x"), &Session{UserID: u.ID}); err == nil {
		t.Fatalf("expected a session without an expiry to be rejected")
	}
	// A short token is rejected rather than trusted.
	if err := s.CreateSession("short", &Session{UserID: u.ID,
		ExpiresAt: now.Add(time.Hour)}); err == nil {
		t.Fatalf("expected a token under MinTokenLen to be rejected")
	}

	// An expired session must not resolve, even before the sweep runs.
	expired := &Session{UserID: u.ID, Username: u.Username,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := s.CreateSession(deadTok, expired); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(deadTok); err != nil || got != nil {
		t.Fatalf("expired session resolved: %#v, err=%v", got, err)
	}

	// The sweep removes only the expired row.
	n, err := s.DeleteExpiredSessions()
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, err=%v; want 1", n, err)
	}
	if got, err := s.GetSession(liveTok); err != nil || got == nil {
		t.Fatalf("sweep removed a live session: %#v, err=%v", got, err)
	}

	// Explicit logout, and revoking an already-dead token is not an error.
	if err := s.DeleteSession(liveTok); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(liveTok); err != nil || got != nil {
		t.Fatalf("session survived logout: %#v", got)
	}
	if err := s.DeleteSession(liveTok); err != nil {
		t.Fatalf("re-revoking a token: %v", err)
	}
}

// TestDisablingUserRevokesSessions is the cascade a naive implementation
// forgets: a disabled or deleted account must lose its live tokens at once,
// not whenever the cookie happens to lapse.
func TestDisablingUserRevokesSessions(t *testing.T) {
	s := openTemp(t)
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	other := mustCreateUser(t, s, testUser("u2", "bob"))
	now := time.Now().UTC()

	mkSession := func(tok, uid string) {
		t.Helper()
		if err := s.CreateSession(testToken(tok), &Session{UserID: uid, Username: uid,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	mkSession("a1", u.ID)
	mkSession("a2", u.ID)
	mkSession("b1", other.ID)

	// Approving keeps sessions alive.
	if err := s.UpdateUserStatus(u.ID, StatusApproved); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(testToken("a1")); err != nil || got == nil {
		t.Fatalf("approval revoked a session: %#v, err=%v", got, err)
	}

	// Disabling revokes every session for that user, and only that user.
	if err := s.UpdateUserStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"a1", "a2"} {
		got, err := s.GetSession(testToken(tok))
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("session %s survived the account being disabled", tok)
		}
	}
	if got, err := s.GetSession(testToken("b1")); err != nil || got == nil {
		t.Fatalf("disabling alice revoked bob's session: %#v, err=%v", got, err)
	}

	// Deleting an account drops its sessions too.
	if err := s.DeleteUser(other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(testToken("b1")); err != nil || got != nil {
		t.Fatalf("session survived account deletion: %#v, err=%v", got, err)
	}

	// Reverting to pending also revokes.
	mkSession("a3", u.ID)
	if err := s.UpdateUserStatus(u.ID, StatusPending); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(testToken("a3")); err != nil || got != nil {
		t.Fatalf("session survived a revert to pending: %#v", got)
	}
}

func TestDeleteUserSessions(t *testing.T) {
	s := openTemp(t)
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	now := time.Now().UTC()
	for _, tok := range []string{"t1", "t2", "t3"} {
		if err := s.CreateSession(testToken(tok), &Session{UserID: u.ID, Username: "alice",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteUserSessions(u.ID)
	if err != nil || n != 3 {
		t.Fatalf("DeleteUserSessions = %d, err=%v; want 3", n, err)
	}
	if got, err := s.GetSession(testToken("t2")); err != nil || got != nil {
		t.Fatalf("session survived log-out-everywhere: %#v", got)
	}
}

func TestJobAndSongOwnership(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()

	j := testJob("j-owned")
	j.UserID = "u1"
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	got, err := s.Job(j.ID, UserAccess("u1"))
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" {
		t.Fatalf("job user_id = %q, want u1", got.UserID)
	}
	// An unset owner falls to the legacy id, never the empty string.
	if err := s.CreateJob(testJob("j-bare")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Job("j-bare", legacy); got.UserID != LegacyUserID {
		t.Fatalf("unowned job user_id = %q, want %q", got.UserID, LegacyUserID)
	}
	queued, err := s.DequeueQueued(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queued {
		if q.UserID == "" {
			t.Fatalf("job %s came back from the queue with no owner", q.ID)
		}
	}

	pub := &Song{ID: "s-pub", JobID: "j-owned", UserID: "u1", IsPublic: true,
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "diffusers",
		Delivery: "base64", AudioPath: "/tmp/a.m4a", CreatedAt: now}
	priv := &Song{ID: "s-priv", JobID: "j-bare", UserID: "u2", IsPublic: false,
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "diffusers",
		Delivery: "base64", AudioPath: "/tmp/b.m4a", CreatedAt: now}
	for _, g := range []*Song{pub, priv} {
		if err := s.CreateSong(g); err != nil {
			t.Fatal(err)
		}
	}

	gotPub, err := s.Song("s-pub", UserAccess("u1"))
	if err != nil || gotPub == nil {
		t.Fatal(err)
	}
	if gotPub.UserID != "u1" || !gotPub.IsPublic {
		t.Fatalf("public song = %#v", gotPub)
	}
	gotPriv, err := s.SongForJob("j-bare")
	if err != nil || gotPriv == nil {
		t.Fatal(err)
	}
	if gotPriv.UserID != "u2" || gotPriv.IsPublic {
		t.Fatalf("private song = %#v", gotPriv)
	}

	// An admin sees every owner's songs; a user sees only their own.
	songs, err := s.Songs(10, 0, AdminAccess("root"))
	if err != nil || len(songs) != 2 {
		t.Fatalf("admin Songs = %d, err=%v; want 2", len(songs), err)
	}
	for _, g := range songs {
		if g.UserID == "" {
			t.Fatalf("song %s listed with no owner", g.ID)
		}
	}
	mine, err := s.Songs(10, 0, UserAccess("u1"))
	if err != nil || len(mine) != 1 || mine[0].ID != "s-pub" {
		t.Fatalf("u1 Songs = %#v, err=%v; want only s-pub", mine, err)
	}

	// A song created without an owner falls to legacy and stays private.
	if err := s.CreateSong(&Song{ID: "s-bare", JobID: "j-bare-2", Lyrics: "la",
		Caption: "pop", Duration: 30, Engine: "stub", Delivery: "base64",
		AudioPath: "/tmp/c.m4a", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	bare, _ := s.Song("s-bare", legacy)
	if bare.UserID != LegacyUserID || bare.IsPublic {
		t.Fatalf("unowned song = %#v", bare)
	}

	// Ownership survives the delete path that returns metadata for cleanup.
	deleted, err := s.DeleteSong("s-pub", UserAccess("u1"))
	if err != nil || deleted == nil {
		t.Fatal(err)
	}
	if deleted.UserID != "u1" || !deleted.IsPublic {
		t.Fatalf("deleted song lost ownership: %#v", deleted)
	}
}

// TestGetSessionIsIndexed guards the auth hot path: every request resolves a
// session, so the lookup must stay a key seek rather than a table scan.
func TestGetSessionIsIndexed(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	const n = 5000
	u := mustCreateUser(t, s, testUser("u1", "alice"))

	tok := func(i int) string { return testToken(fmt.Sprintf("tok-%05d", i)) }
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := tx.Exec(`INSERT INTO sessions (token_hash, user_id, username,
			config_admin, created_at, expires_at) VALUES (?, ?, ?, 0, ?, ?)`,
			hashToken(tok(i)), u.ID, u.Username,
			now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The users join must not cost the sessions seek.
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN
		SELECT s.token_hash FROM sessions s LEFT JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(tok(4999)), now)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var plan string
		if err := rows.Scan(new(int), new(int), new(int), &plan); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(plan, "SCAN") {
			t.Fatalf("GetSession falls back to a scan: %s", plan)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	const reads = 200
	for i := range reads {
		got, err := s.GetSession(tok(i * 17 % n))
		if err != nil || got == nil {
			t.Fatalf("GetSession: %#v, err=%v", got, err)
		}
	}
	if avg := time.Since(start) / reads; avg > 10*time.Millisecond {
		t.Fatalf("GetSession averaged %v over %d rows, want well under 10ms", avg, n)
	}
}

// TestCrossTenantAccessIsDenied is the negative path the store API must make
// unrepresentable: user B holds a valid id for user A's song and still cannot
// read, rename, or destroy it — with no ownership check written by the caller.
func TestCrossTenantAccessIsDenied(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	alice, bob := UserAccess("alice"), UserAccess("bob")

	j := testJob("job-a")
	j.UserID = "alice"
	if err := s.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	song := &Song{ID: "song-a", JobID: "job-a", UserID: "alice",
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "diffusers",
		Delivery: "base64", AudioPath: "/tmp/alice.m4a", Title: "Alice's Song",
		CreatedAt: now}
	if err := s.CreateSong(song); err != nil {
		t.Fatal(err)
	}

	// Read: bob gets the same answer as for a song that does not exist, so the
	// API cannot even be used to confirm it is there.
	got, err := s.Song("song-a", bob)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("bob read alice's song: %#v", got)
	}
	if gotJob, err := s.Job("job-a", bob); err != nil || gotJob != nil {
		t.Fatalf("bob read alice's job: %#v, err=%v", gotJob, err)
	}

	// List: alice's song never appears in bob's library.
	list, err := s.Songs(50, 0, bob)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range list {
		if g.ID == "song-a" {
			t.Fatalf("alice's song appeared in bob's library")
		}
	}

	// Rename: refused, and the stored title is untouched.
	if err := s.UpdateSongTitle("song-a", "Bob Was Here", bob); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("bob renamed alice's song: err=%v, want sql.ErrNoRows", err)
	}
	still, err := s.Song("song-a", alice)
	if err != nil || still == nil {
		t.Fatal(err)
	}
	if still.Title != "Alice's Song" {
		t.Fatalf("title changed to %q by a non-owner", still.Title)
	}

	// Delete: refused, and — the part that matters most — the audio path is
	// not handed back, so a caller cannot be tricked into unlinking the file.
	deleted, err := s.DeleteSong("song-a", bob)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("bob deleted alice's song and received %q for unlinking", deleted.AudioPath)
	}
	survived, err := s.Song("song-a", alice)
	if err != nil || survived == nil {
		t.Fatalf("alice's song was destroyed by bob: %#v, err=%v", survived, err)
	}
	if gotJob, err := s.Job("job-a", alice); err != nil || gotJob == nil {
		t.Fatalf("alice's job was destroyed by bob: %#v, err=%v", gotJob, err)
	}

	// The zero Access is not a skeleton key — it owns nothing.
	var zero Access
	if got, err := s.Song("song-a", zero); err != nil || got != nil {
		t.Fatalf("the zero Access read a song: %#v, err=%v", got, err)
	}
	if got, err := s.DeleteSong("song-a", zero); err != nil || got != nil {
		t.Fatalf("the zero Access deleted a song: %#v, err=%v", got, err)
	}
	if list, err := s.Songs(50, 0, zero); err != nil || len(list) != 0 {
		t.Fatalf("the zero Access listed %d songs, want 0 (err=%v)", len(list), err)
	}

	// The owner, and an admin, still get through.
	if got, err := s.Song("song-a", alice); err != nil || got == nil {
		t.Fatalf("owner denied their own song: %#v, err=%v", got, err)
	}
	if got, err := s.Song("song-a", AdminAccess("root")); err != nil || got == nil {
		t.Fatalf("admin denied: %#v, err=%v", got, err)
	}
	if err := s.UpdateSongTitle("song-a", "Renamed By Admin", AdminAccess("root")); err != nil {
		t.Fatalf("admin rename: %v", err)
	}
	if got, err := s.DeleteSong("song-a", AdminAccess("root")); err != nil || got == nil {
		t.Fatalf("admin delete: %#v, err=%v", got, err)
	}
}

// TestSessionTokenIsNotStoredRaw proves the bearer token never reaches the
// database: a dump of every column of every row must not contain it.
func TestSessionTokenIsNotStoredRaw(t *testing.T) {
	s := openTemp(t)
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	now := time.Now().UTC()

	token, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Fatalf("NewSessionToken returned %d chars, want 64 (32 bytes hex)", len(token))
	}
	if err := s.CreateSession(token, &Session{UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.db.Query(`SELECT token_hash, user_id, username, config_admin,
		CAST(created_at AS TEXT), CAST(expires_at AS TEXT) FROM sessions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var cols [6]string
		if err := rows.Scan(&cols[0], &cols[1], &cols[2], &cols[3], &cols[4], &cols[5]); err != nil {
			t.Fatal(err)
		}
		for i, c := range cols {
			if strings.Contains(c, token) {
				t.Fatalf("raw bearer token found in sessions column %d: %q", i, c)
			}
		}
		if cols[0] != hashToken(token) {
			t.Fatalf("stored key = %q, want the SHA-256 of the token", cols[0])
		}
		found++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("sessions rows = %d, want 1", found)
	}

	// The hash is not accepted in place of the token, so a database reader
	// gains nothing by replaying what they found.
	if got, err := s.GetSession(hashToken(token)); err != nil || got != nil {
		t.Fatalf("the stored hash authenticated as a token: %#v, err=%v", got, err)
	}
	// The real token still works, and never comes back out of the store.
	got, err := s.GetSession(token)
	if err != nil || got == nil {
		t.Fatalf("GetSession with the real token: %#v, err=%v", got, err)
	}
	if got.TokenHash != hashToken(token) {
		t.Fatalf("TokenHash = %q, want the hash", got.TokenHash)
	}

	// Two tokens never collide onto one row.
	other, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if other == token {
		t.Fatal("NewSessionToken returned the same value twice")
	}
	if got, err := s.GetSession(other); err != nil || got != nil {
		t.Fatalf("an unrelated token resolved: %#v, err=%v", got, err)
	}
}

// TestSessionPrivilegeIsResolvedLive proves privilege is never read from a
// stale copy in the session row: promotion, demotion, disabling, and deletion
// all take effect on the next lookup.
func TestSessionPrivilegeIsResolvedLive(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	u := mustCreateUser(t, s, &User{ID: "u1", Username: "alice",
		PasswordHash: "h", Status: StatusApproved, Role: RoleUser})

	token := testToken("live")
	if err := s.CreateSession(token, &Session{UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(token)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.IsAdmin || got.Status != StatusApproved {
		t.Fatalf("initial resolve = admin:%v status:%q", got.IsAdmin, got.Status)
	}

	// Promotion is visible without touching the session row.
	if _, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, RoleAdmin, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(token); got == nil || !got.IsAdmin {
		t.Fatalf("promotion not visible on the existing session: %#v", got)
	}
	// ...and so is demotion, which is the direction that matters.
	if _, err := s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, RoleUser, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(token); got == nil || got.IsAdmin {
		t.Fatalf("demoted user still resolves as admin: %#v", got)
	}

	// A status change is visible even when the session row is left in place.
	if _, err := s.db.Exec(`UPDATE users SET status = ? WHERE id = ?`, StatusDisabled, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(token); got == nil || got.Status != StatusDisabled {
		t.Fatalf("status not resolved live: %#v", got)
	}

	// A session whose account has been removed resolves to nothing, even if
	// the row outlives the user.
	if _, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession(token); err != nil || got != nil {
		t.Fatalf("orphaned session still resolves: %#v, err=%v", got, err)
	}

	// The config admin has no users row and is still admin.
	cfgTok := testToken("cfg")
	if err := s.CreateSession(cfgTok, &Session{UserID: "config-admin",
		Username: "root", ConfigAdmin: true,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	cfg, err := s.GetSession(cfgTok)
	if err != nil || cfg == nil {
		t.Fatalf("config admin session: %#v, err=%v", cfg, err)
	}
	if !cfg.IsAdmin || cfg.Status != StatusApproved || cfg.Username != "root" {
		t.Fatalf("config admin resolved wrong: %#v", cfg)
	}
}

// TestMigrateRebuildsRawTokenSessions covers the upgrade from the revision
// that keyed sessions by the raw bearer token. Those rows are replayable, so
// migrate() rebuilds the table and the sessions are invalidated on purpose —
// while users, jobs, and songs are left untouched.
func TestMigrateRebuildsRawTokenSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r1.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	now := time.Now().UTC()
	if err := s.CreateSong(&Song{ID: "s1", JobID: "j1", UserID: u.ID,
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "stub",
		Delivery: "base64", AudioPath: "/tmp/a.m4a", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Recreate the round-1 sessions table, raw token as the primary key.
	if _, err := s.db.Exec(`DROP TABLE sessions;
CREATE TABLE sessions (
  token TEXT PRIMARY KEY, user_id TEXT NOT NULL, username TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL, expires_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions VALUES ('plaintext-token', ?, 'alice', 0, ?, ?)`,
		u.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("migrating a raw-token sessions table: %v", err)
	}
	defer s2.Close()

	// The replayable row is gone, and the column it lived in with it.
	if has, err := s2.hasColumn("sessions", "token"); err != nil || has {
		t.Fatalf("raw token column survived migration (has=%v, err=%v)", has, err)
	}
	if got, err := s2.GetSession("plaintext-token"); err != nil || got != nil {
		t.Fatalf("a raw-token session survived the rebuild: %#v, err=%v", got, err)
	}
	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("sessions rows after rebuild = %d, want 0", n)
	}

	// User and song data is untouched.
	if got, err := s2.GetUserByID(u.ID); err != nil || got == nil {
		t.Fatalf("user lost by the sessions rebuild: %#v, err=%v", got, err)
	}
	if got, err := s2.Song("s1", UserAccess(u.ID)); err != nil || got == nil {
		t.Fatalf("song lost by the sessions rebuild: %#v, err=%v", got, err)
	}

	// The rebuilt table works.
	tok := testToken("fresh")
	if err := s2.CreateSession(tok, &Session{UserID: u.ID, Username: "alice",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got, err := s2.GetSession(tok); err != nil || got == nil {
		t.Fatalf("new session after rebuild: %#v, err=%v", got, err)
	}
}

// TestSetSongPublicIsScopedAndIdempotent pins the authorisation at the SQL
// level rather than through a handler: a non-owner updates nothing, and the
// target state is set rather than flipped.
func TestSetSongPublicIsScopedAndIdempotent(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	alice, bob := UserAccess("alice"), UserAccess("bob")

	if err := s.CreateSong(&Song{ID: "s1", JobID: "j1", UserID: "alice",
		Lyrics: "la", Caption: "pop", Duration: 30, Engine: "stub",
		Delivery: "base64", AudioPath: "/tmp/a.m4a", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// A non-owner gets (nil, nil) — the same answer as a missing song — and
	// changes nothing.
	got, err := s.SetSongPublic("s1", true, bob)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("bob shared alice's song: %#v", got)
	}
	if g, _ := s.Song("s1", alice); g.IsPublic {
		t.Fatal("a non-owner's write took effect")
	}
	if got, err := s.SetSongPublic("no-such-song", true, alice); err != nil || got != nil {
		t.Fatalf("missing song = %#v, err=%v; want nil, nil", got, err)
	}

	// The owner sets an explicit target, and the returned row reflects it.
	got, err = s.SetSongPublic("s1", true, alice)
	if err != nil || got == nil {
		t.Fatalf("owner share = %#v, err=%v", got, err)
	}
	if !got.IsPublic || got.ID != "s1" || got.UserID != "alice" {
		t.Fatalf("returned row = %#v", got)
	}

	// Repeating it is a no-op, not an undo — the property a blind flip lacks.
	for i := 0; i < 3; i++ {
		if got, err := s.SetSongPublic("s1", true, alice); err != nil || got == nil || !got.IsPublic {
			t.Fatalf("repeat #%d = %#v, err=%v", i, got, err)
		}
	}
	if got, err := s.SetSongPublic("s1", false, alice); err != nil || got == nil || got.IsPublic {
		t.Fatalf("unshare = %#v, err=%v", got, err)
	}

	// An admin may act on anyone's song.
	if got, err := s.SetSongPublic("s1", true, AdminAccess("root")); err != nil || got == nil || !got.IsPublic {
		t.Fatalf("admin share = %#v, err=%v", got, err)
	}
	// The zero Access is not a skeleton key.
	if got, err := s.SetSongPublic("s1", false, Access{}); err != nil || got != nil {
		t.Fatalf("zero Access shared a song: %#v, err=%v", got, err)
	}

	// PublicSong only ever returns a shared song.
	if g, err := s.PublicSong("s1"); err != nil || g == nil {
		t.Fatalf("PublicSong on a shared song = %#v, err=%v", g, err)
	}
	if _, err := s.SetSongPublic("s1", false, alice); err != nil {
		t.Fatal(err)
	}
	if g, err := s.PublicSong("s1"); err != nil || g != nil {
		t.Fatalf("PublicSong on a private song = %#v, err=%v", g, err)
	}
}

// TestPartitionedReadsAndClamping pins the two partition queries at the SQL
// level: personal is one owner's rows and takes no admin lift, public is only
// shared rows, and both clamp their window.
func TestPartitionedReadsAndClamping(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	mk := func(id, owner string, public bool, age time.Duration) {
		t.Helper()
		if err := s.CreateSong(&Song{ID: id, JobID: "j-" + id, UserID: owner,
			IsPublic: public, Lyrics: "la", Caption: "pop", Duration: 30,
			Engine: "stub", Delivery: "base64", AudioPath: "/tmp/" + id,
			CreatedAt: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a-new", "alice", false, 1*time.Second)
	mk("a-mid", "alice", true, 2*time.Second)
	mk("a-old", "alice", false, 3*time.Second)
	mk("b-one", "bob", true, 4*time.Second)

	ids := func(songs []*Song) []string {
		out := make([]string, len(songs))
		for i, g := range songs {
			out[i] = g.ID
		}
		return out
	}

	// Personal: one owner's rows, newest first, nobody else's.
	got, err := s.PersonalSongs("alice", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a-new,a-mid,a-old"; strings.Join(ids(got), ",") != want {
		t.Fatalf("PersonalSongs(alice) = %v, want %s", ids(got), want)
	}
	if got, _ := s.PersonalSongs("bob", 10, 0); len(got) != 1 || got[0].ID != "b-one" {
		t.Fatalf("PersonalSongs(bob) = %v", ids(got))
	}
	// An unknown owner gets nothing rather than everything.
	if got, err := s.PersonalSongs("nobody", 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("PersonalSongs(nobody) = %v, err=%v", ids(got), err)
	}
	if got, err := s.PersonalSongs("", 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("PersonalSongs(\"\") = %v, err=%v", ids(got), err)
	}

	// Public: only shared rows, across owners, newest first.
	pub, err := s.PublicSongs(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := "a-mid,b-one"; strings.Join(ids(pub), ",") != want {
		t.Fatalf("PublicSongs = %v, want %s", ids(pub), want)
	}

	// Paging is a window, not a filter.
	if page2, _ := s.PersonalSongs("alice", 2, 2); len(page2) != 1 || page2[0].ID != "a-old" {
		t.Fatalf("PersonalSongs page 2 = %v", ids(page2))
	}

	// Clamping: an absurd limit is capped, a negative one still returns rows,
	// and a negative offset does not error.
	if got, err := s.PersonalSongs("alice", 1_000_000, 0); err != nil || len(got) != 3 {
		t.Fatalf("huge limit = %v, err=%v", ids(got), err)
	}
	if got, err := s.PersonalSongs("alice", -5, -5); err != nil || len(got) != 1 {
		t.Fatalf("negative limit/offset = %v, err=%v", ids(got), err)
	}
	if got, err := s.PublicSongs(-1, -1); err != nil || len(got) != 1 {
		t.Fatalf("PublicSongs negative = %v, err=%v", ids(got), err)
	}
	if l, o := clampPage(1_000_000, -3); l != MaxPageSize || o != 0 {
		t.Fatalf("clampPage(1000000, -3) = %d, %d", l, o)
	}
	// Paging past the end is empty, not an error.
	if got, err := s.PersonalSongs("alice", 10, 500); err != nil || len(got) != 0 {
		t.Fatalf("deep offset = %v, err=%v", ids(got), err)
	}
}

// TestPartitionQueriesUseTheirIndexes: the library must not degrade into a
// full table scan as the song count grows.
func TestPartitionQueriesUseTheirIndexes(t *testing.T) {
	s := openTemp(t)
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		if _, err := tx.Exec(`INSERT INTO songs (id, job_id, user_id, is_public,
			lyrics, caption, duration_s, seed, engine, delivery, audio_path,
			title, created_at) VALUES (?,?,?,?,'la','pop',30,NULL,'stub','base64','/tmp/x','t',?)`,
			fmt.Sprintf("s%04d", i), fmt.Sprintf("j%04d", i),
			fmt.Sprintf("u%d", i%40), i%2, now.Add(-time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, query, wantIndex string }{
		{"personal", `SELECT id FROM songs WHERE user_id = ? ORDER BY created_at DESC LIMIT 21 OFFSET 0`, "idx_songs_user_created"},
		{"public", `SELECT id FROM songs WHERE is_public = 1 ORDER BY created_at DESC LIMIT 21 OFFSET 0`, "idx_songs_public_created"},
	} {
		var plan string
		args := []any{}
		if strings.Contains(c.query, "user_id = ?") {
			args = append(args, "u3")
		}
		if err := s.db.QueryRow("EXPLAIN QUERY PLAN "+c.query, args...).
			Scan(new(int), new(int), new(int), &plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, c.wantIndex) {
			t.Errorf("%s query does not use %s: %s", c.name, c.wantIndex, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s query sorts instead of walking the index: %s", c.name, plan)
		}
	}
}
