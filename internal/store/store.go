// Package store persists jobs and songs in SQLite (pure-Go driver, no CGO).
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Job states — the closed machine from stage 02's contract.
const (
	StateQueued     = "queued"
	StateSubmitting = "submitting" // local claim made; remote id not durable yet
	StateSubmitted  = "submitted"
	StateRunning    = "running"
	StateSucceeded  = "succeeded"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
)

// User statuses. A user exists from signup but cannot act until approved;
// disabling is reversible, deletion is not.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDisabled = "disabled"
)

// User roles.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// LegacyUserID owns jobs and songs created before the multi-user migration.
// It is deliberately not a real users row, so nothing can log in as it.
const LegacyUserID = "legacy"

var (
	ErrTransition    = errors.New("store: state transition rejected")
	ErrUsernameTaken = errors.New("store: username already taken")
)

type Job struct {
	ID        string
	State     string
	RunPodID  string
	UserID    string
	Lyrics    string
	Caption   string
	Duration  float64
	Seed      *int64
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Song struct {
	ID        string
	JobID     string
	UserID    string
	IsPublic  bool
	Lyrics    string
	Caption   string
	Duration  float64
	Seed      *int64
	Engine    string
	Delivery  string
	AudioPath string
	Title     string
	CreatedAt time.Time
}

// User is an account. Password hashing is the caller's job (stage 02); the
// store only ever sees the finished hash.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Status       string
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Session is a bearer token with a hard expiry. Username and IsAdmin are
// denormalised so request auth is a single row read; UpdateUserStatus and
// DeleteUser revoke sessions rather than let a stale copy outlive the change.
type Session struct {
	Token     string
	UserID    string
	Username  string
	IsAdmin   bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Open creates the database and schema.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: single writer
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

type Store struct{ db *sql.DB }

func (s *Store) Close() error { return s.db.Close() }

// migrate is idempotent: safe to run on a fresh database and on a populated
// one that has already been through it. Timestamps are always written from Go
// in UTC — no column takes DEFAULT CURRENT_TIMESTAMP, because SQLite renders
// that as "2006-01-02 15:04:05" while the driver renders time.Time as
// "2006-01-02 15:04:05.999999999 +0000 UTC". Mixing the two in one column
// breaks the range comparison that expires_at depends on.
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS jobs (
  id         TEXT PRIMARY KEY,
  state      TEXT NOT NULL,
  runpod_id  TEXT NOT NULL DEFAULT '',
  lyrics     TEXT NOT NULL,
  caption    TEXT NOT NULL,
  duration_s REAL NOT NULL,
  seed       INTEGER,
  error      TEXT NOT NULL DEFAULT '',
  retries    INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS songs (
  id         TEXT PRIMARY KEY,
  job_id     TEXT NOT NULL,
  lyrics     TEXT NOT NULL,
  caption    TEXT NOT NULL,
  duration_s REAL NOT NULL,
  seed       INTEGER,
  engine     TEXT NOT NULL,
  delivery   TEXT NOT NULL,
  audio_path TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  password_hash TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending',
  role          TEXT NOT NULL DEFAULT 'user',
  created_at    TIMESTAMP NOT NULL,
  updated_at    TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token      TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  username   TEXT NOT NULL,
  is_admin   INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
CREATE INDEX IF NOT EXISTS idx_songs_created ON songs(created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_songs_job ON songs(job_id);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
`); err != nil {
		return err
	}

	// ADD COLUMN has no IF NOT EXISTS, so each is guarded by a table_info
	// probe. Existing rows fall to the legacy owner and stay private.
	for _, c := range []struct{ table, col, ddl string }{
		{"jobs", "user_id", `ALTER TABLE jobs ADD COLUMN user_id TEXT NOT NULL DEFAULT '` + LegacyUserID + `'`},
		{"songs", "user_id", `ALTER TABLE songs ADD COLUMN user_id TEXT NOT NULL DEFAULT '` + LegacyUserID + `'`},
		{"songs", "is_public", `ALTER TABLE songs ADD COLUMN is_public INTEGER NOT NULL DEFAULT 0`},
	} {
		has, err := s.hasColumn(c.table, c.col)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := s.db.Exec(c.ddl); err != nil {
			return err
		}
	}

	// Indexes over the added columns, so they must follow the ALTERs.
	_, err := s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_jobs_user_id ON jobs(user_id);
CREATE INDEX IF NOT EXISTS idx_songs_user_created ON songs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_songs_public_created ON songs(is_public, created_at DESC);
`)
	return err
}

// hasColumn reports whether table already has the named column.
func (s *Store) hasColumn(table, col string) (bool, error) {
	rows, err := s.db.Query(`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, table, col)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// CreateJob inserts a queued job owned by j.UserID (legacy when unset).
func (s *Store) CreateJob(j *Job) error {
	_, err := s.db.Exec(
		`INSERT INTO jobs (id, state, user_id, lyrics, caption, duration_s, seed, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, StateQueued, owner(j.UserID), j.Lyrics, j.Caption, j.Duration, j.Seed,
		j.CreatedAt.UTC(), j.CreatedAt.UTC())
	return err
}

// owner defaults an unset owner to the legacy user so the NOT NULL column
// never silently becomes an empty string that matches no real account.
func owner(userID string) string {
	if userID == "" {
		return LegacyUserID
	}
	return userID
}

// TransitionJob moves a job from one state to another (compare-and-set).
// Terminal states never transition again.
func (s *Store) TransitionJob(id, from, to string, set func(assignments map[string]any)) error {
	sets := map[string]any{"state": to, "updated_at": time.Now().UTC()}
	if set != nil {
		set(sets)
	}
	cols, args := make([]string, 0, len(sets)), make([]any, 0, len(sets)+2)
	for k, v := range sets {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id, from)
	res, err := s.db.Exec(
		"UPDATE jobs SET "+join(cols)+" WHERE id = ? AND state = ?", args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s %s -> %s", ErrTransition, id, from, to)
	}
	return nil
}

// FailJob marks a job failed from any non-terminal state.
func (s *Store) FailJob(id, reason string) error {
	res, err := s.db.Exec(
		`UPDATE jobs SET state = ?, error = ?, updated_at = ?
		 WHERE id = ? AND state NOT IN ('succeeded','failed','cancelled')`,
		StateFailed, reason, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s already terminal", ErrTransition, id)
	}
	return nil
}

func (s *Store) Job(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, state, runpod_id, user_id, lyrics, caption,
		duration_s, seed, error, created_at, updated_at FROM jobs WHERE id = ?`, id)
	var j Job
	err := row.Scan(&j.ID, &j.State, &j.RunPodID, &j.UserID, &j.Lyrics, &j.Caption,
		&j.Duration, &j.Seed, &j.Error, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// DequeueQueued returns queued jobs (worker submits them).
func (s *Store) DequeueQueued(limit int) ([]*Job, error) {
	return s.jobsByState(StateQueued, limit)
}

// SubmittingJobs returns jobs claimed locally before the remote id became
// durable. On restart these are failed as ambiguous — never resubmitted.
func (s *Store) SubmittingJobs() ([]*Job, error) {
	return s.jobsByState(StateSubmitting, 100)
}

// ActiveJobs returns submitted/running jobs (worker polls them).
func (s *Store) ActiveJobs() ([]*Job, error) {
	js, err := s.jobsByState(StateSubmitted, 100)
	if err != nil {
		return nil, err
	}
	rs, err := s.jobsByState(StateRunning, 100)
	return append(js, rs...), err
}

func (s *Store) jobsByState(state string, limit int) ([]*Job, error) {
	rows, err := s.db.Query(`SELECT id, state, runpod_id, user_id, lyrics, caption,
		duration_s, seed, error, created_at, updated_at
		FROM jobs WHERE state = ? ORDER BY created_at LIMIT ?`, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.State, &j.RunPodID, &j.UserID, &j.Lyrics, &j.Caption,
			&j.Duration, &j.Seed, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, rows.Err()
}

// OrphanSubmission atomically records a remote id (when known) and fails a
// locally-claimed submission. This prevents a CAS error from losing the id
// and prevents restart from resubmitting a billable remote generation.
func (s *Store) OrphanSubmission(id, runpodID, reason string) error {
	res, err := s.db.Exec(`UPDATE jobs SET state = ?, runpod_id = ?, error = ?, updated_at = ?
		WHERE id = ? AND state = ?`, StateFailed, runpodID, reason,
		time.Now().UTC(), id, StateSubmitting)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: job %s is not submitting", ErrTransition, id)
	}
	return nil
}

// BumpRetries increments the transient-retry counter, returning the new value.
func (s *Store) BumpRetries(id string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`UPDATE jobs SET retries = retries + 1, updated_at = ?
		 WHERE id = ? RETURNING retries`, time.Now().UTC(), id).Scan(&n)
	return n, err
}

// CreateSong persists a finished song. OR IGNORE + the unique job_id index
// make it idempotent — the worker may retry after a crash mid-finish.
func (s *Store) CreateSong(g *Song) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO songs (id, job_id, user_id, is_public, lyrics, caption,
		  duration_s, seed, engine, delivery, audio_path, title, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.JobID, owner(g.UserID), g.IsPublic, g.Lyrics, g.Caption, g.Duration, g.Seed,
		g.Engine, g.Delivery, g.AudioPath, g.Title, g.CreatedAt.UTC())
	return err
}

// songCols is the column list every Song read shares, so a new column can
// never be added to one query and forgotten in another.
const songCols = `id, job_id, user_id, is_public, lyrics, caption, duration_s, seed,
	engine, delivery, audio_path, title, created_at`

func scanSong(sc interface{ Scan(...any) error }, g *Song) error {
	return sc.Scan(&g.ID, &g.JobID, &g.UserID, &g.IsPublic, &g.Lyrics, &g.Caption,
		&g.Duration, &g.Seed, &g.Engine, &g.Delivery, &g.AudioPath, &g.Title, &g.CreatedAt)
}

func (s *Store) Song(id string) (*Song, error) {
	var g Song
	err := scanSong(s.db.QueryRow(`SELECT `+songCols+` FROM songs WHERE id = ?`, id), &g)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// SongForJob returns the (unique) song produced by a job, or nil.
func (s *Store) SongForJob(jobID string) (*Song, error) {
	var g Song
	err := scanSong(s.db.QueryRow(`SELECT `+songCols+` FROM songs WHERE job_id = ?`, jobID), &g)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// Songs pages the library newest-first, across all owners. Ownership scoping
// is stage 04/05's job — this stays the unscoped read the worker and the
// existing history page already use.
func (s *Store) Songs(limit, offset int) ([]*Song, error) {
	rows, err := s.db.Query(`SELECT `+songCols+`
		FROM songs ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Song
	for rows.Next() {
		var g Song
		if err := scanSong(rows, &g); err != nil {
			return nil, err
		}
		out = append(out, &g)
	}
	return out, rows.Err()
}

// DeleteSong removes a song and its associated job in a single transaction.
// It returns the deleted song metadata (or nil if not found) so the caller can
// clean up the audio file from disk.
func (s *Store) DeleteSong(id string) (*Song, error) {
	g, err := s.Song(id)
	if err != nil || g == nil {
		return g, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM songs WHERE id = ?`, id); err != nil {
		return nil, err
	}
	if g.JobID != "" {
		if _, err := tx.Exec(`DELETE FROM jobs WHERE id = ?`, g.JobID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return g, nil
}

// UpdateSongTitle updates the title of a song.
func (s *Store) UpdateSongTitle(id, title string) error {
	res, err := s.db.Exec(`UPDATE songs SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const userCols = `id, username, password_hash, status, role, created_at, updated_at`

func scanUser(sc interface{ Scan(...any) error }, u *User) error {
	return sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Status, &u.Role,
		&u.CreatedAt, &u.UpdatedAt)
}

// CreateUser inserts an account. Username uniqueness is case-insensitive and
// enforced by the DB, so a race between two signups fails one of them here
// rather than producing two accounts that differ only in case.
func (s *Store) CreateUser(u *User) error {
	now := time.Now().UTC()
	if !u.CreatedAt.IsZero() {
		now = u.CreatedAt.UTC()
	}
	if u.Status == "" {
		u.Status = StatusPending
	}
	if u.Role == "" {
		u.Role = RoleUser
	}
	_, err := s.db.Exec(
		`INSERT INTO users (id, username, password_hash, status, role, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Status, u.Role, now, now)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return fmt.Errorf("%w: %s", ErrUsernameTaken, u.Username)
	}
	if err != nil {
		return err
	}
	u.CreatedAt, u.UpdatedAt = now, now
	return nil
}

func (s *Store) GetUserByID(id string) (*User, error) {
	var u User
	err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByUsername looks up an account case-insensitively — the column's
// NOCASE collation makes this an index seek, not a scan.
func (s *Store) GetUserByUsername(username string) (*User, error) {
	var u User
	err := scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, username), &u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserStatus sets a user's status and, when that status is no longer
// approved, revokes their live sessions in the same transaction. Without this
// a disabled user keeps working until their cookie happens to expire.
func (s *Store) UpdateUserStatus(id, status string) error {
	switch status {
	case StatusPending, StatusApproved, StatusDisabled:
	default:
		return fmt.Errorf("store: unknown user status %q", status)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE users SET status = ?, updated_at = ? WHERE id = ?`,
		status, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if status != StatusApproved {
		if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteUser removes an account and its sessions together. Jobs and songs are
// left in place — their user_id is retained so an admin can reassign or purge
// them deliberately rather than losing audio to a cascade.
func (s *Store) DeleteUser(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ListUsers returns every account, newest signup first.
func (s *Store) ListUsers() ([]*User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		if err := scanUser(rows, &u); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// CountPendingUsers powers the admin approval badge.
func (s *Store) CountPendingUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE status = ?`, StatusPending).Scan(&n)
	return n, err
}

// CreateSession stores a token. ExpiresAt is required — a session without one
// would never expire.
func (s *Store) CreateSession(sess *Session) error {
	if sess.ExpiresAt.IsZero() {
		return errors.New("store: session requires an expiry")
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(
		`INSERT INTO sessions (token, user_id, username, is_admin, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.Token, sess.UserID, sess.Username, sess.IsAdmin,
		sess.CreatedAt.UTC(), sess.ExpiresAt.UTC())
	return err
}

// GetSession returns a live session, or nil if the token is unknown or the
// session has already expired. Expiry is enforced on read rather than left to
// the sweep, so a token is dead the moment it lapses. The lookup is a primary
// key seek.
func (s *Store) GetSession(token string) (*Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT token, user_id, username, is_admin, created_at, expires_at
		 FROM sessions WHERE token = ? AND expires_at > ?`, token, time.Now().UTC()).
		Scan(&sess.Token, &sess.UserID, &sess.Username, &sess.IsAdmin,
			&sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// DeleteSession revokes one token (logout). Revoking an already-dead token is
// not an error.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteUserSessions revokes every session for a user (log out everywhere),
// returning how many were removed.
func (s *Store) DeleteUserSessions(userID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteExpiredSessions prunes lapsed rows, returning how many were removed.
// GetSession already refuses expired tokens; this only reclaims space.
func (s *Store) DeleteExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
