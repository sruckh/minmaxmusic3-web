# Stage 01 Output — Data Model & Store Contract

> Layer 4 · Input contract for stages 02–07

Everything below is implemented in `internal/store/store.go` and covered by
`internal/store/store_test.go`. Later stages consume this surface; they should
not re-derive it from the schema.

Two rules govern this layer, and later stages must not undo them:

1. **Ownership lives in the SQL, not in the caller.** Every query that can read
   or destroy a tenant's data takes an `Access` and carries the owner into the
   `WHERE` clause. A handler that forgets to check ownership gets an empty
   result, not another tenant's data.
2. **Secrets and privilege are never stored in a form that can be replayed or
   go stale.** Session tokens are hashed at rest; role and status are resolved
   from the `users` table on every request.

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

// MinTokenLen is the shortest bearer token CreateSession accepts.
const MinTokenLen = 32

var (
	ErrTransition    = errors.New("store: state transition rejected")
	ErrUsernameTaken = errors.New("store: username already taken")
)
```

## Access — the ownership scope

```go
type Access struct {
	UserID string
	Admin  bool
}

func UserAccess(userID string) Access  // scope to one owner
func AdminAccess(userID string) Access // lift the restriction, recording who acted
```

The **zero `Access` owns nothing and is not admin**, so a caller that forgets to
fill it in reads nothing and deletes nothing — the failure mode is an empty
result, never a leak. There is no empty-string-means-god sentinel. The admin
override is only reachable by typing `AdminAccess` at the call site, so a
privileged call is visible in review; it is never a default.

Internally the predicate is `(user_id = ? OR ?)` with the admin flag **bound as
a parameter**, never interpolated, so no caller input can reshape the query.

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
	TokenHash   string // SHA-256 of the bearer token; the raw token is never stored
	UserID      string
	Username    string
	ConfigAdmin bool   // input:  the static config admin, which has no users row
	IsAdmin     bool   // output: resolved live from users.role
	Status      string // output: resolved live from users.status
	CreatedAt   time.Time
	ExpiresAt   time.Time
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
  token_hash   TEXT PRIMARY KEY,   -- SHA-256 hex, never the raw token
  user_id      TEXT NOT NULL,
  username     TEXT NOT NULL,
  config_admin INTEGER NOT NULL DEFAULT 0,
  created_at   TIMESTAMP NOT NULL,
  expires_at   TIMESTAMP NOT NULL
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
2. **`sessions` is keyed by `token_hash`, not the raw token**, and the
   `is_admin` column is now `config_admin` with a narrower meaning (see
   "Session privilege" below).
3. **`idx_users_username` omitted** — the `UNIQUE` constraint on a `COLLATE NOCASE`
   column already creates an equivalent index; a second one only costs writes.
4. **`idx_songs_user_id` and `idx_songs_is_public` omitted** — the composite
   `idx_songs_user_created` / `idx_songs_public_created` serve those lookups as
   leftmost-prefix indexes.

### Migration idempotency

`migrate()` is safe to run repeatedly on a populated database. Tables and
indexes use `IF NOT EXISTS`; `ALTER TABLE ADD COLUMN` has no such form, so each
is guarded by a `pragma_table_info` probe.

`migrate()` also detects a `sessions` table left over from the revision that
keyed rows by the **raw** bearer token and rebuilds it. Every such row is
directly replayable, so the sessions are invalidated on purpose and those users
log in again; users, jobs, and songs are untouched.

Covered by `TestMigrateIsIdempotent`, `TestMigrateBackfillsLegacyRows`, and
`TestMigrateRebuildsRawTokenSessions`.

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
- `UpdateUserStatus` rejects any status outside the enum, and **revokes the
  user's sessions in the same transaction** whenever the new status is not
  `approved`.
- `DeleteUser` deletes the account and its sessions in one transaction. It
  leaves `jobs` and `songs` in place, retaining `user_id`, so audio is never
  lost to a cascade — reassignment or purge is an explicit stage 06 action.

### Sessions

```go
func NewSessionToken() (string, error)                   // 32 crypto/rand bytes, hex
func (s *Store) CreateSession(token string, sess *Session) error
func (s *Store) GetSession(token string) (*Session, error)       // (nil, nil) if unknown, expired, or orphaned
func (s *Store) DeleteSession(token string) error                // idempotent
func (s *Store) DeleteUserSessions(userID string) (int64, error) // log out everywhere
func (s *Store) DeleteExpiredSessions() (int64, error)           // periodic sweep
```

**The raw token is a parameter, never a struct field.** `CreateSession` hashes
it with SHA-256 and stores only the hex digest, so there is no field on
`Session` that could carry a live secret back out of the store, and a database
dump, backup, or leaked query log yields nothing replayable. `GetSession` and
`DeleteSession` take the raw token and hash it before looking up. Verified by
`TestSessionTokenIsNotStoredRaw`, which scans every column of every row for the
token and also asserts the stored hash is itself rejected as a credential.

- `CreateSession` rejects a token shorter than `MinTokenLen` rather than
  trusting the caller. Length is the only entropy proxy the store can check —
  **use `NewSessionToken`**, do not roll your own.
- `CreateSession` inputs read from `sess`: `UserID`, `Username`, `ConfigAdmin`,
  `CreatedAt`, `ExpiresAt`. On success `sess.TokenHash` is filled in.
- `GetSession` enforces expiry on read (`expires_at > ?`), so a token is dead
  the moment it lapses and correctness never depends on the sweep having run.
- The lookup is a primary-key seek on the hash, **measured at 85µs against
  5 000 sessions** (budget: 10ms) with the users join included.
  `TestGetSessionIsIndexed` asserts the plan contains no `SCAN` and that the
  average stays under 10ms.

#### Session privilege — resolved live, not cached

`GetSession` joins `users` and derives `Username`, `IsAdmin`, and `Status` from
the users row **as it is now**. Nothing about authorisation is read from the
session row. Consequences, all covered by `TestSessionPrivilegeIsResolvedLive`:

- A **demotion** takes effect on the next request; there is no stale admin bit.
- A **status change** is visible immediately, even if the session row survives.
- A **deleted account** makes its sessions resolve to `nil` at once, so an
  orphaned row is never usable.

`ConfigAdmin` is the single stored flag and the single exception: the static
Infisical admin has no `users` row to resolve against, so its session carries
`config_admin = 1` and resolves to `IsAdmin: true, Status: approved`. Stage 02
must give that session a `UserID` that cannot collide with a real `users.id`.
A session that matches neither case — no users row and not the config admin —
is treated as invalid.

The `UpdateUserStatus` / `DeleteUser` session revocation is therefore defence in
depth rather than the only line of defence. Keep both.

### Jobs and songs — all ownership-scoped

```go
func (s *Store) CreateJob(j *Job) error                        // persists j.UserID
func (s *Store) Job(id string, a Access) (*Job, error)         // (nil, nil) if absent or not yours
func (s *Store) CreateSong(g *Song) error                      // persists g.UserID and g.IsPublic
func (s *Store) Song(id string, a Access) (*Song, error)       // (nil, nil) if absent or not yours
func (s *Store) Songs(limit, offset int, a Access) ([]*Song, error)
func (s *Store) UpdateSongTitle(id, title string, a Access) error   // sql.ErrNoRows if absent or not yours
func (s *Store) DeleteSong(id string, a Access) (*Song, error)      // (nil, nil) if absent or not yours
```

- A non-owner gets **exactly the same answer as for a row that does not
  exist**, so the API cannot be used to probe for another tenant's songs.
- `DeleteSong` repeats the ownership predicate on the `DELETE` rather than
  trusting the preceding read, so the destructive statement is safe read in
  isolation. Critically, a refused delete returns `nil` — the caller never
  receives an `AudioPath` and so cannot be tricked into unlinking another
  tenant's file. The job is then deleted by an id taken from an already
  authorised row, not from user input.
- `Songs` issues two statements rather than one `OR` predicate: SQLite will not
  use `idx_songs_user_created` through an `OR`, and the library page must not
  degrade into a full scan. Verified — user-scoped paging is an index `SEARCH`
  at 175µs over 5 000 songs.
- `Job` is scoped because the job-status route takes an id straight from the
  URL and reaches the song through it.
- An empty `UserID` on create is stored as `LegacyUserID`, never as an empty
  string that would match no real account.
- The worker copies `Job.UserID` onto the song it creates, so a generated song
  belongs to whoever asked for it.

`SongForJob(jobID)` remains **unscoped**: it is a system-level lookup used by
the worker, which has no user context. Its one server call site is gated by a
scoped `Job` lookup that 404s first. Stage 04 should keep that ordering.

Covered by `TestCrossTenantAccessIsDenied`, which asserts user B can neither
read, list, rename, nor delete user A's song, that the zero `Access` is not a
skeleton key, and that the owner and an admin still get through.

## What stage 02 inherits

- Hash passwords itself (bcrypt cost ≥ 12 per conventions) and hand
  `CreateUser` the finished hash.
- Mint tokens with `store.NewSessionToken()` and choose the session TTL.
- Treat `GetSession` returning `(nil, nil)` as unauthenticated, then check
  `Session.Status == StatusApproved` before granting access. `Status` and
  `IsAdmin` on the returned session are already live — do not re-read them from
  anywhere else, and do not cache them across requests.

## What stage 03/04 inherits

`internal/server` currently has a single placeholder:

```go
func (s *Server) caller(r *http.Request) store.Access {
	return store.UserAccess(store.LegacyUserID)
}
```

Every scoped store call in the server goes through it. **Stage 03 replaces its
body with the session user** and every handler is scoped at once. It returns a
user scope, not an admin scope, so failing to replace it fails closed.
