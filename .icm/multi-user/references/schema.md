# Database Schema & Migrations — Multi-User System

> Layer 3 · SQLite tables and migrations
>
> **Status: reconciled with the shipped schema.** This file originally carried
> the pre-build sketch. Where Stage 01 deviated from that sketch it did so for
> reasons recorded in
> `stages/01-data-model/output/data-model-spec.md`, and the code is the
> authority. `internal/store/store.go` `migrate()` is the executable version of
> everything below.

## Binding rule — no column takes `DEFAULT CURRENT_TIMESTAMP`

Every timestamp is written from Go in UTC. SQLite renders
`CURRENT_TIMESTAMP` as `2006-01-02 15:04:05`, while the `modernc.org/sqlite`
driver renders a `time.Time` as `2006-01-02 15:04:05.999999999 +0000 UTC`.
Mixing both formats in one column breaks the string range comparison that
`sessions.expires_at` depends on, so a session could outlive its expiry or die
on issue. The earlier draft of this file specified
`DEFAULT CURRENT_TIMESTAMP` on four columns; that was wrong and is not what
ships. Do not reintroduce it.

## SQLite Schema Definitions

### 1. `users` Table
```sql
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL COLLATE NOCASE UNIQUE,
  password_hash TEXT NOT NULL,
  status        TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'approved', 'disabled'
  role          TEXT NOT NULL DEFAULT 'user',    -- 'user', 'admin'
  created_at    TIMESTAMP NOT NULL,
  updated_at    TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
```

No `idx_users_username`: the `UNIQUE` constraint already creates an index, and
the `COLLATE NOCASE` on the column makes the case-insensitive lookup in
`GetUserByUsername` an index seek rather than a scan.

### 2. `sessions` Table
```sql
CREATE TABLE IF NOT EXISTS sessions (
  token_hash   TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL,
  username     TEXT NOT NULL,
  config_admin INTEGER NOT NULL DEFAULT 0,
  created_at   TIMESTAMP NOT NULL,
  expires_at   TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
```

Two deliberate departures from the sketch:

- **`token_hash`, not `token`.** The raw bearer token is never persisted, only
  its SHA-256, so a database read, a backup, or a leaked query log yields
  nothing replayable. `migrate()` detects a legacy `sessions.token` column and
  **drops and rebuilds the table** — every row in it is directly replayable, so
  those sessions are invalidated on purpose. No user, job, or song data is
  touched.
- **`config_admin`, not `is_admin`.** Privilege is not stored on the session.
  `GetSession` LEFT JOINs `users` and resolves `is_admin` and `status` live on
  every read, so a demotion, disable, or deletion takes effect on the very next
  request. `config_admin` marks the one identity that has no `users` row to
  resolve against — the static Infisical administrator.

### 3. Migrations for Existing Tables (`jobs`, `songs`)
```sql
ALTER TABLE jobs  ADD COLUMN user_id   TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE songs ADD COLUMN user_id   TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE songs ADD COLUMN is_public INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_jobs_user_id       ON jobs(user_id);
CREATE INDEX IF NOT EXISTS idx_songs_user_created ON songs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_songs_public_created ON songs(is_public, created_at DESC);
```

`idx_songs_user_id` and `idx_songs_is_public` from the sketch are **not**
created: the two composite indexes are strict supersets by leading column, and
the partitioned library's two queries (`PersonalSongs`, `PublicSongs`) are
written to match them exactly so each page is an index range scan with no sort.
`TestPartitionQueriesUseTheirIndexes` pins that.

## Migration Strategy
- `migrate()` is idempotent — safe on a fresh database and on one already
  through it. `ALTER TABLE` has no `IF NOT EXISTS`, so each added column is
  guarded by a `pragma_table_info` probe.
- Legacy single-user rows fall to `user_id = 'legacy'` and stay `is_public = 0`.
  `store.LegacyUserID` is deliberately **not** a `users` row, so nothing can log
  in as it and no personal library ever shows those songs; an administrator can
  still reach one by id. What an operator should do about them is documented in
  the repository `README.md` § *Upgrading from the single-user release*.
