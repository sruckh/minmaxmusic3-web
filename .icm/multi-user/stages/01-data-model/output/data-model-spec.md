# Stage 01 Output — Data Model & Store Contract

> Layer 4 · Input contract for stages 02–07

Everything below is implemented in `internal/store/store.go` and covered by
`internal/store/store_test.go`. Later stages consume this surface; they should
not re-derive it from the schema.

## Constants

```go
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDisabled = "disabled"
)

const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// LegacyUserID owns jobs and songs created before the multi-user migration.
// It is deliberately NOT a real users row, so nothing can log in as it.
const LegacyUserID = "legacy"

var (
	ErrTransition    = errors.New("store: state transition rejected")
	ErrUsernameTaken = errors.New("store: username already taken")
)
```

## Structs

```go
type User struct {
	ID           string
	Username     string
	PasswordHash string   // stage 02 hashes; the store only sees the finished hash
	Status       string   // StatusPending | StatusApproved | StatusDisabled
	Role         string   // RoleUser | RoleAdmin
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Session struct {
	Token     string
	UserID    string
	Username  string  // denormalised: request auth is one row read
	IsAdmin   bool    // denormalised
	CreatedAt time.Time
	ExpiresAt time.Time
}
```

`Job` gains `UserID string`. `Song` gains `UserID string` and `IsPublic bool`.
Both are populated on every read, including `DeleteSong`'s returned metadata.

## Schema

```sql
CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  password_hash TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending',
  role          TEXT NOT NULL DEFAULT 'user',
  created_at    TIMESTAMP NOT NULL,
  updated_at    TIMESTAMP NOT NULL
);
CREATE TABLE sessions (
  token      TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL,
  username   TEXT NOT NULL,
  is_admin   INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMP NOT NULL,
  expires_at TIMESTAMP NOT NULL
);

ALTER TABLE jobs  ADD COLUMN user_id   TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE songs ADD COLUMN user_id   TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE songs ADD COLUMN is_public INTEGER NOT NULL DEFAULT 0;
```

Indexes: `idx_users_status`, `idx_sessions_user_id`, `idx_sessions_expires_at`,
`idx_jobs_user_id`, `idx_songs_user_created (user_id, created_at DESC)`,
`idx_songs_public_created (is_public, created_at DESC)`, plus the pre-existing
job/song indexes.

### Deviations from `references/schema.md`, and why

1. **No `DEFAULT CURRENT_TIMESTAMP` on any timestamp column.** SQLite renders
   `CURRENT_TIMESTAMP` as `"2006-01-02 15:04:05"`, while the `modernc.org/sqlite`
   driver renders a Go `time.Time` as `"2006-01-02 15:04:05.999999999 +0000 UTC"`.
   Both land in the same TEXT column, and range comparison is lexicographic — so
   mixing them silently corrupts the `expires_at > ?` test that session validity
   depends on. Every timestamp is written from Go in UTC instead, matching the
   convention the existing `jobs`/`songs` tables already follow. **Stage 02+ must
   keep writing timestamps through the store, never via raw SQL defaults.**
2. **`idx_users_username` omitted** — the `UNIQUE` constraint on a `COLLATE NOCASE`
   column already creates an equivalent index; a second one only costs writes.
3. **`idx_songs_user_id` and `idx_songs_is_public` omitted** — the composite
   `idx_songs_user_created` / `idx_songs_public_created` serve those lookups as
   leftmost-prefix indexes.

### Migration idempotency

`migrate()` is safe to run repeatedly on a populated database. Tables and
indexes use `IF NOT EXISTS`; `ALTER TABLE ADD COLUMN` has no such form, so each
is guarded by a `pragma_table_info` probe. Verified by `TestMigrateIsIdempotent`
(three successive `Open`s on one growing DB) and `TestMigrateBackfillsLegacyRows`
(a database built on the pre-multi-user schema migrates without data loss).

## Store method signatures

### Users

```go
func (s *Store) CreateUser(u *User) error            // ErrUsernameTaken on case-insensitive collision
func (s *Store) GetUserByID(id string) (*User, error)          // (nil, nil) if absent
func (s *Store) GetUserByUsername(name string) (*User, error)  // (nil, nil) if absent; case-insensitive
func (s *Store) UpdateUserStatus(id, status string) error      // sql.ErrNoRows if absent
func (s *Store) DeleteUser(id string) error                    // sql.ErrNoRows if absent
func (s *Store) ListUsers() ([]*User, error)                   // newest signup first
func (s *Store) CountPendingUsers() (int, error)
```

- `CreateUser` defaults empty `Status` to `pending` and empty `Role` to `user`,
  and writes back `CreatedAt`/`UpdatedAt` on the passed struct.
- `UpdateUserStatus` rejects any status outside the enum.
- **`UpdateUserStatus` revokes the user's sessions in the same transaction
  whenever the new status is not `approved`.** Disabling or un-approving an
  account logs it out immediately rather than at cookie expiry.
- **`DeleteUser` deletes the account and its sessions in one transaction.** It
  leaves `jobs` and `songs` in place, retaining `user_id`, so audio is never
  lost to a cascade — reassignment or purge is an explicit stage 06 action.

### Sessions

```go
func (s *Store) CreateSession(sess *Session) error       // error if ExpiresAt is zero
func (s *Store) GetSession(token string) (*Session, error) // (nil, nil) if unknown OR expired
func (s *Store) DeleteSession(token string) error          // idempotent
func (s *Store) DeleteUserSessions(userID string) (int64, error) // log out everywhere
func (s *Store) DeleteExpiredSessions() (int64, error)           // periodic sweep
```

- **`GetSession` enforces expiry on read** (`WHERE token = ? AND expires_at > ?`),
  so a token is dead the moment it lapses and correctness never depends on the
  sweep having run. Callers treat `(nil, nil)` as "not authenticated" and must
  not distinguish unknown from expired.
- The lookup is a primary-key seek. Measured at **31µs** against 5 000 sessions
  (budget: 10ms); `TestGetSessionIsIndexed` asserts both the query plan uses an
  index and the average stays under 10ms.
- `DeleteSession` on an already-revoked token is not an error, so double logout
  is safe.
- `DeleteUserSessions` is beyond the contract's four session operations. It was
  needed internally for the status/delete cascade and is exported because
  stage 06 (admin disable) and stage 02 (log out everywhere) both need it.

### Jobs and songs

`CreateJob(j *Job) error` and `CreateSong(g *Song) error` keep their existing
signatures and now persist `user_id` (and `is_public` for songs). An empty
`UserID` is stored as `LegacyUserID`, never as an empty string that would match
no real account.

`Job`, `DequeueQueued`, `SubmittingJobs`, `ActiveJobs`, `Song`, `SongForJob`,
`Songs`, and `DeleteSong` all populate the new fields.

`Songs(limit, offset)` remains **unscoped** — it still lists every owner's songs.
Ownership scoping is stage 04/05's contract; changing it here would have broken
the existing history page mid-pipeline.

## What stage 02 inherits

- Hash passwords itself (bcrypt cost ≥ 12 per conventions) and hand
  `CreateUser` the finished hash.
- Generate the 32-byte token and choose the session TTL; the store only
  enforces that some expiry exists.
- Treat `GetSession` returning `(nil, nil)` as unauthenticated, then check
  `User.Status == StatusApproved` before granting access — the store does not
  reject unapproved users at session lookup, because the session may legitimately
  exist while approval is pending.
