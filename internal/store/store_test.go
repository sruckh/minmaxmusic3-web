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
	got, err := s.Job(j.ID)
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
	got, err := s.Job(j.ID)
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
	songs, err := s.Songs(10, 0)
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
	got, err := s.Song(song.ID)
	if err != nil || got == nil {
		t.Fatalf("expected song to exist, got %v, err=%v", got, err)
	}

	// Delete song
	deleted, err := s.DeleteSong(song.ID)
	if err != nil {
		t.Fatalf("DeleteSong failed: %v", err)
	}
	if deleted == nil || deleted.ID != song.ID {
		t.Fatalf("expected deleted song %s, got %#v", song.ID, deleted)
	}

	// Verify song is removed
	gotSong, err := s.Song(song.ID)
	if err != nil {
		t.Fatalf("Song lookup after delete error: %v", err)
	}
	if gotSong != nil {
		t.Fatalf("expected song to be deleted, found: %#v", gotSong)
	}

	// Verify job is removed
	gotJob, err := s.Job(j.ID)
	if err != nil {
		t.Fatalf("Job lookup after delete error: %v", err)
	}
	if gotJob != nil {
		t.Fatalf("expected job to be deleted, found: %#v", gotJob)
	}

	// Deleting a non-existent song returns nil, nil
	missing, err := s.DeleteSong("non-existent")
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

	if err := s.UpdateSongTitle(song.ID, "New Custom Title"); err != nil {
		t.Fatalf("UpdateSongTitle failed: %v", err)
	}

	got, err := s.Song(song.ID)
	if err != nil || got == nil {
		t.Fatalf("Song lookup error: %v", err)
	}
	if got.Title != "New Custom Title" {
		t.Fatalf("expected title 'New Custom Title', got %q", got.Title)
	}

	// Update non-existent song
	if err := s.UpdateSongTitle("non-existent", "Title"); err == nil {
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

	g, err := s.Song("old-song")
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
	j, err := s.Job("old-job")
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

	live := &Session{Token: "tok-live", UserID: u.ID, Username: u.Username,
		IsAdmin: true, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreateSession(live); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession("tok-live")
	if err != nil || got == nil {
		t.Fatalf("GetSession = %#v, err=%v", got, err)
	}
	if got.UserID != u.ID || got.Username != "alice" || !got.IsAdmin {
		t.Fatalf("session round-trip lost fields: %#v", got)
	}
	if !got.ExpiresAt.Equal(live.ExpiresAt) {
		t.Fatalf("expires_at = %v, want %v", got.ExpiresAt, live.ExpiresAt)
	}

	if got, err := s.GetSession("no-such-token"); err != nil || got != nil {
		t.Fatalf("unknown token = %#v, err=%v; want nil, nil", got, err)
	}
	if err := s.CreateSession(&Session{Token: "t", UserID: u.ID}); err == nil {
		t.Fatalf("expected a session without an expiry to be rejected")
	}

	// An expired session must not resolve, even before the sweep runs.
	expired := &Session{Token: "tok-dead", UserID: u.ID, Username: u.Username,
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := s.CreateSession(expired); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession("tok-dead"); err != nil || got != nil {
		t.Fatalf("expired session resolved: %#v, err=%v", got, err)
	}

	// The sweep removes only the expired row.
	n, err := s.DeleteExpiredSessions()
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, err=%v; want 1", n, err)
	}
	if got, err := s.GetSession("tok-live"); err != nil || got == nil {
		t.Fatalf("sweep removed a live session: %#v, err=%v", got, err)
	}

	// Explicit logout, and revoking an already-dead token is not an error.
	if err := s.DeleteSession("tok-live"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession("tok-live"); err != nil || got != nil {
		t.Fatalf("session survived logout: %#v", got)
	}
	if err := s.DeleteSession("tok-live"); err != nil {
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
		if err := s.CreateSession(&Session{Token: tok, UserID: uid, Username: uid,
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
	if got, err := s.GetSession("a1"); err != nil || got == nil {
		t.Fatalf("approval revoked a session: %#v, err=%v", got, err)
	}

	// Disabling revokes every session for that user, and only that user.
	if err := s.UpdateUserStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"a1", "a2"} {
		got, err := s.GetSession(tok)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("session %s survived the account being disabled", tok)
		}
	}
	if got, err := s.GetSession("b1"); err != nil || got == nil {
		t.Fatalf("disabling alice revoked bob's session: %#v, err=%v", got, err)
	}

	// Deleting an account drops its sessions too.
	if err := s.DeleteUser(other.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession("b1"); err != nil || got != nil {
		t.Fatalf("session survived account deletion: %#v, err=%v", got, err)
	}

	// Reverting to pending also revokes.
	mkSession("a3", u.ID)
	if err := s.UpdateUserStatus(u.ID, StatusPending); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSession("a3"); err != nil || got != nil {
		t.Fatalf("session survived a revert to pending: %#v", got)
	}
}

func TestDeleteUserSessions(t *testing.T) {
	s := openTemp(t)
	u := mustCreateUser(t, s, testUser("u1", "alice"))
	now := time.Now().UTC()
	for _, tok := range []string{"t1", "t2", "t3"} {
		if err := s.CreateSession(&Session{Token: tok, UserID: u.ID, Username: "alice",
			CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteUserSessions(u.ID)
	if err != nil || n != 3 {
		t.Fatalf("DeleteUserSessions = %d, err=%v; want 3", n, err)
	}
	if got, err := s.GetSession("t2"); err != nil || got != nil {
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
	got, err := s.Job(j.ID)
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
	if got, _ := s.Job("j-bare"); got.UserID != LegacyUserID {
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

	gotPub, err := s.Song("s-pub")
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

	songs, err := s.Songs(10, 0)
	if err != nil || len(songs) != 2 {
		t.Fatalf("Songs = %d, err=%v; want 2", len(songs), err)
	}
	for _, g := range songs {
		if g.UserID == "" {
			t.Fatalf("song %s listed with no owner", g.ID)
		}
	}

	// A song created without an owner falls to legacy and stays private.
	if err := s.CreateSong(&Song{ID: "s-bare", JobID: "j-bare-2", Lyrics: "la",
		Caption: "pop", Duration: 30, Engine: "stub", Delivery: "base64",
		AudioPath: "/tmp/c.m4a", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	bare, _ := s.Song("s-bare")
	if bare.UserID != LegacyUserID || bare.IsPublic {
		t.Fatalf("unowned song = %#v", bare)
	}

	// Ownership survives the delete path that returns metadata for cleanup.
	deleted, err := s.DeleteSong("s-pub")
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
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := tx.Exec(`INSERT INTO sessions (token, user_id, username,
			is_admin, created_at, expires_at) VALUES (?, ?, ?, 0, ?, ?)`,
			fmt.Sprintf("tok-%05d", i), fmt.Sprintf("u%d", i), "user",
			now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var plan string
	if err := s.db.QueryRow(`EXPLAIN QUERY PLAN SELECT token FROM sessions
		WHERE token = ? AND expires_at > ?`, "tok-04999", now).
		Scan(new(int), new(int), new(int), &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "USING INDEX") && !strings.Contains(plan, "USING PRIMARY KEY") {
		t.Fatalf("GetSession does not use an index: %s", plan)
	}

	start := time.Now()
	const reads = 200
	for i := range reads {
		got, err := s.GetSession(fmt.Sprintf("tok-%05d", i*17%n))
		if err != nil || got == nil {
			t.Fatalf("GetSession: %#v, err=%v", got, err)
		}
	}
	if avg := time.Since(start) / reads; avg > 10*time.Millisecond {
		t.Fatalf("GetSession averaged %v over %d rows, want well under 10ms", avg, n)
	}
}


