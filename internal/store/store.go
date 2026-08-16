// Package store persists jobs and songs in SQLite (pure-Go driver, no CGO).
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	// ErrLastAdmin refuses an operation that would leave the database with no
	// approved administrator.
	ErrLastAdmin = errors.New("store: refusing to remove the last administrator")
)

// Access is who is asking. Every query that can read or destroy a tenant's
// data takes one, and the ownership test lives in the SQL rather than in a
// check the caller has to remember to write.
//
// The zero Access owns nothing and is not an admin, so a caller that forgets
// to fill it in reads nothing and deletes nothing — the failure mode is an
// empty result, never a leak. Build one with UserAccess or AdminAccess so the
// privileged case is spelled out at the call site.
type Access struct {
	UserID string
	Admin  bool
}

// UserAccess scopes an operation to one owner's rows.
func UserAccess(userID string) Access { return Access{UserID: userID} }

// AdminAccess lifts the ownership restriction. userID is still recorded as the
// acting account so callers cannot pass an anonymous god object.
func AdminAccess(userID string) Access { return Access{UserID: userID, Admin: true} }

// ownedBy is the ownership predicate. Admin is a bound parameter, never
// interpolated, so no caller input can reshape the query. Supply the arguments
// with Access.args().
const ownedBy = `(user_id = ? OR ?)`

func (a Access) args() []any { return []any{a.UserID, a.Admin} }

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

// Session is a login with a hard expiry.
//
// The raw bearer token is never stored — only its SHA-256 — so a database
// read, a backup, or a leaked query log yields nothing that can be replayed.
// TokenHash is the stored key; the raw token exists only in the cookie and in
// the caller's memory, and the store cannot hand it back.
//
// Username, IsAdmin and Status are resolved from the users table on every
// read, never cached in the session row: a demotion, a disable, or a deletion
// takes effect on the very next request instead of waiting for the cookie to
// lapse. ConfigAdmin is the sole exception and is a stored flag, because the
// static Infisical admin has no users row to resolve against.
type Session struct {
	TokenHash   string
	UserID      string
	Username    string
	ConfigAdmin bool   // input: the static config admin, which has no users row
	IsAdmin     bool   // output: resolved live from users.role
	Status      string // output: resolved live from users.status
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// MinTokenLen is the shortest bearer token CreateSession accepts. A 32-byte
// random value is 64 hex characters, so this floor still rejects a truncated
// or hand-written token without forcing an encoding on the caller. Length is
// the only entropy proxy the store can check — use NewSessionToken.
const MinTokenLen = 32

// NewSessionToken returns a 32-byte cryptographically random token, hex
// encoded. Callers should use this rather than rolling their own.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
	// An earlier revision keyed sessions by the raw bearer token. Every row in
	// such a table is directly replayable, so the table is rebuilt rather than
	// migrated — the sessions are invalidated on purpose and those users log
	// in again. No user, job, or song data is touched.
	rawTokens, err := s.hasColumn("sessions", "token")
	if err != nil {
		return err
	}
	if rawTokens {
		if _, err := s.db.Exec(`DROP TABLE sessions`); err != nil {
			return err
		}
	}

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
  token_hash   TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL,
  username     TEXT NOT NULL,
  config_admin INTEGER NOT NULL DEFAULT 0,
  created_at   TIMESTAMP NOT NULL,
  expires_at   TIMESTAMP NOT NULL
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
	_, err = s.db.Exec(`
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

// Job returns a job the caller owns, or nil. It is scoped for the same reason
// Song is: the job-status route takes an id straight from the URL, and the
// song it renders is reached through this lookup.
func (s *Store) Job(id string, a Access) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, state, runpod_id, user_id, lyrics, caption,
		duration_s, seed, error, created_at, updated_at
		FROM jobs WHERE id = ? AND `+ownedBy, append([]any{id}, a.args()...)...)
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

// Song returns a song the caller is allowed to see, or nil. A non-owner is
// indistinguishable from a missing row, so the API cannot be used to probe for
// the existence of another tenant's songs.
func (s *Store) Song(id string, a Access) (*Song, error) {
	var g Song
	err := scanSong(s.db.QueryRow(`SELECT `+songCols+`
		FROM songs WHERE id = ? AND `+ownedBy, append([]any{id}, a.args()...)...), &g)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// PublicSong returns a song only if it has been explicitly shared. It is the
// one song read that is not ownership-scoped, so the is_public test is the
// entire authorisation check and it lives in the SQL rather than in a caller
// that might forget it.
func (s *Store) PublicSong(id string) (*Song, error) {
	var g Song
	err := scanSong(s.db.QueryRow(`SELECT `+songCols+`
		FROM songs WHERE id = ? AND is_public = 1`, id), &g)
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

// MaxPageSize bounds any paged read. limit and offset reach SQL, and an
// unbounded limit is both a denial of service and a way to sweep the table in
// one request, so the store clamps rather than trusting its caller.
const MaxPageSize = 100

// clampPage bounds a page window. A limit below one would return nothing and a
// negative offset is a SQLite error, so both are corrected rather than passed
// through.
func clampPage(limit, offset int) (int, int) {
	if limit < 1 {
		limit = 1
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// PersonalSongs pages one owner's library, newest first.
//
// It takes a user id rather than an Access on purpose: "my songs" means the
// caller's own songs even for an administrator, whose AdminAccess would
// otherwise lift the predicate and turn their personal library into every
// song in the system. There is no admin variant of this query.
//
// The predicate and ordering match idx_songs_user_created (user_id,
// created_at DESC), so paging is an index range scan with no sort.
func (s *Store) PersonalSongs(userID string, limit, offset int) ([]*Song, error) {
	limit, offset = clampPage(limit, offset)
	rows, err := s.db.Query(`SELECT `+songCols+`
		FROM songs WHERE user_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectSongs(rows)
}

// PublicSongs pages every explicitly shared song, newest first. Ownership does
// not narrow it — that is what "shared" means — so it takes no Access.
//
// Matches idx_songs_public_created (is_public, created_at DESC).
func (s *Store) PublicSongs(limit, offset int) ([]*Song, error) {
	limit, offset = clampPage(limit, offset)
	rows, err := s.db.Query(`SELECT `+songCols+`
		FROM songs WHERE is_public = 1
		ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	return collectSongs(rows)
}

func collectSongs(rows *sql.Rows) ([]*Song, error) {
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

// Songs pages the caller's library newest-first; an admin sees every owner's.
// The two shapes are separate statements rather than one predicate because
// SQLite will not use idx_songs_user_created through an OR, and the library
// page must not degrade into a full table scan as the song count grows.
//
// The server has no caller for this today: Stage 05 split the library page
// into PersonalSongs (never lifted by admin, so an administrator's "My Songs"
// stays their own) and PublicSongs. It is kept rather than deleted because it
// is the only Access-scoped catalogue read in the store — the shape an admin
// catalogue view would need — and because its tests pin the two properties the
// Access type exists for: AdminAccess lifts the ownership predicate, and the
// zero Access reads nothing. Delete it only together with an equivalent
// home for those two assertions.
func (s *Store) Songs(limit, offset int, a Access) ([]*Song, error) {
	limit, offset = clampPage(limit, offset)
	q := `SELECT ` + songCols + ` FROM songs WHERE user_id = ?
		ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args := []any{a.UserID, limit, offset}
	if a.Admin {
		q = `SELECT ` + songCols + ` FROM songs
			ORDER BY created_at DESC LIMIT ? OFFSET ?`
		args = []any{limit, offset}
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return collectSongs(rows)
}

// DeleteSong removes a song the caller owns, together with its job, in a
// single transaction. It returns the deleted song (or nil if it does not exist
// or is not the caller's) so the caller can unlink the audio file.
//
// The ownership predicate is repeated on the DELETE rather than trusting the
// preceding read, so the destructive statement is safe read in isolation.
func (s *Store) DeleteSong(id string, a Access) (*Song, error) {
	g, err := s.Song(id, a)
	if err != nil || g == nil {
		return g, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`DELETE FROM songs WHERE id = ? AND `+ownedBy,
		append([]any{id}, a.args()...)...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	// g.JobID comes from a row the caller was just authorised to delete, not
	// from user input, so the job goes with it unconditionally.
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

// SetSongPublic shares or unshares a song the caller owns, returning the row
// as it now stands (or nil if there is no such song of theirs).
//
// It takes the target state rather than flipping, and it is a single
// statement: there is no read-then-check-then-write for a concurrent request
// to interleave with, and no lost update when two clients act at once. Two
// callers both sharing a song both succeed and the outcome is unambiguous.
// Ownership is in the WHERE clause, so a non-owner updates nothing and gets
// the same answer as for a song that does not exist.
func (s *Store) SetSongPublic(id string, public bool, a Access) (*Song, error) {
	var g Song
	err := scanSong(s.db.QueryRow(
		`UPDATE songs SET is_public = ? WHERE id = ? AND `+ownedBy+
			` RETURNING `+songCols,
		append([]any{public, id}, a.args()...)...), &g)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateSongTitle renames a song the caller owns. Renaming someone else's is
// reported as sql.ErrNoRows — the same answer as a song that does not exist.
func (s *Store) UpdateSongTitle(id, title string, a Access) error {
	res, err := s.db.Exec(`UPDATE songs SET title = ? WHERE id = ? AND `+ownedBy,
		append([]any{title, id}, a.args()...)...)
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

	// Moving an administrator off approved is a removal for this purpose.
	if status != StatusApproved {
		if err := guardLastAdmin(tx, id); err != nil {
			return err
		}
	}

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

// guardLastAdmin returns ErrLastAdmin when removing the account, or moving it
// off approved, would leave the database with no approved administrator.
//
// It must run inside the caller's transaction. Checking in one statement and
// writing in another leaves a window where two concurrent removals each see
// the other as the survivor and both succeed, stranding the system.
//
// An account that is not currently an approved administrator protects nothing,
// so the guard passes immediately.
func guardLastAdmin(tx *sql.Tx, id string) error {
	var role, status string
	err := tx.QueryRow(`SELECT role, status FROM users WHERE id = ?`, id).
		Scan(&role, &status)
	if err != nil {
		return err // sql.ErrNoRows included: no such user
	}
	if role != RoleAdmin || status != StatusApproved {
		return nil
	}
	var others int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM users WHERE role = ? AND status = ? AND id <> ?`,
		RoleAdmin, StatusApproved, id).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// DeleteUser removes an account and everything belonging to it — sessions,
// jobs, and songs — in a single transaction, and returns the audio paths of
// the deleted songs so the caller can unlink the files.
//
// Content is destroyed rather than reassigned. Reassigning a departed user's
// private songs to a shared owner would quietly make them browsable by
// administrators forever, which is a privacy regression dressed up as
// preservation; leaving them owned by an id with no users row makes them
// unreachable garbage. Deleting is the outcome that matches what was asked.
//
// The files are deliberately not touched here: a filesystem unlink cannot join
// the transaction, so the database commits first and the caller unlinks after.
// A file left behind is recoverable garbage; a row pointing at a file that is
// already gone is not.
//
// Returns ErrLastAdmin rather than stranding the system, and sql.ErrNoRows if
// there is no such user. Either way nothing is written.
func (s *Store) DeleteUser(id string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := guardLastAdmin(tx, id); err != nil {
		return nil, err
	}

	rows, err := tx.Query(`SELECT audio_path FROM songs WHERE user_id = ?`, id)
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, err
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close() // the single sqlite connection cannot Exec while this is open

	for _, q := range []string{
		`DELETE FROM songs WHERE user_id = ?`,
		`DELETE FROM jobs WHERE user_id = ?`,
		`DELETE FROM sessions WHERE user_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return nil, err
		}
	}
	res, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return paths, nil
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

// CreateSession stores the SHA-256 of a bearer token. The raw token is taken
// as an argument and never persisted, so there is no field on Session that
// could carry a live secret back out of the store.
//
// Inputs read from sess: UserID, Username, ConfigAdmin, CreatedAt, ExpiresAt.
// IsAdmin and Status are outputs of GetSession and are ignored here. On
// success sess.TokenHash is filled in.
func (s *Store) CreateSession(token string, sess *Session) error {
	if len(token) < MinTokenLen {
		return fmt.Errorf("store: session token must be at least %d characters, got %d",
			MinTokenLen, len(token))
	}
	if sess.ExpiresAt.IsZero() {
		return errors.New("store: session requires an expiry")
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = time.Now().UTC()
	}
	sess.TokenHash = hashToken(token)
	_, err := s.db.Exec(
		`INSERT INTO sessions (token_hash, user_id, username, config_admin, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.Username, sess.ConfigAdmin,
		sess.CreatedAt.UTC(), sess.ExpiresAt.UTC())
	return err
}

// GetSession takes the raw bearer token, hashes it, and returns the live
// session it identifies — or nil if the token is unknown, the session has
// expired, or the account behind it no longer exists.
//
// Expiry is enforced on read rather than left to the sweep, so a token is dead
// the moment it lapses. Privilege and status are joined from the users table
// rather than read from the session row, so a demoted, disabled, or deleted
// account cannot keep acting on a cached copy. The lookup is a primary key
// seek on the hash.
func (s *Store) GetSession(token string) (*Session, error) {
	var (
		sess                       Session
		uid, uname, urole, ustatus sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT s.token_hash, s.user_id, s.username, s.config_admin,
		        s.created_at, s.expires_at, u.id, u.username, u.role, u.status
		 FROM sessions s LEFT JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.expires_at > ?`,
		hashToken(token), time.Now().UTC()).
		Scan(&sess.TokenHash, &sess.UserID, &sess.Username, &sess.ConfigAdmin,
			&sess.CreatedAt, &sess.ExpiresAt, &uid, &uname, &urole, &ustatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	switch {
	case uid.Valid:
		// A real account: everything that governs authorisation comes from the
		// users row as it is now, not from what was true at login.
		sess.Username = uname.String
		sess.IsAdmin = urole.String == RoleAdmin
		sess.Status = ustatus.String
	case sess.ConfigAdmin:
		// The static config admin has no users row to resolve against.
		sess.IsAdmin = true
		sess.Status = StatusApproved
	default:
		// The account is gone; the session dies with it.
		return nil, nil
	}
	return &sess, nil
}

// DeleteSession revokes one token (logout). Revoking an already-dead or
// unknown token is not an error.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
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
