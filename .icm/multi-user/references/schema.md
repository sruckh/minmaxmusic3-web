# Database Schema & Migrations — Multi-User System

> Layer 3 · SQLite tables and migrations

## SQLite Schema Definitions

### 1. `users` Table
```sql
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending', -- 'pending', 'approved', 'disabled'
    role TEXT NOT NULL DEFAULT 'user',      -- 'user', 'admin'
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);
```

### 2. `sessions` Table
```sql
CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    username TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);
```

### 3. Migrations for Existing Tables (`jobs`, `songs`)
```sql
-- Add user_id to jobs for tracking generation ownership
ALTER TABLE jobs ADD COLUMN user_id TEXT NOT NULL DEFAULT 'legacy';
CREATE INDEX IF NOT EXISTS idx_jobs_user_id ON jobs(user_id);

-- Add user_id and is_public to songs
ALTER TABLE songs ADD COLUMN user_id TEXT NOT NULL DEFAULT 'legacy';
ALTER TABLE songs ADD COLUMN is_public INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_songs_user_id ON songs(user_id);
CREATE INDEX IF NOT EXISTS idx_songs_is_public ON songs(is_public);
CREATE INDEX IF NOT EXISTS idx_songs_user_created ON songs(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_songs_public_created ON songs(is_public, created_at DESC);
```

## Migration Strategy
- Store migration checks existing columns with `PRAGMA table_info` before executing `ALTER TABLE`.
- Legacy single-user songs default to `user_id = 'legacy'` and `is_public = 0` (or claimable by admin).
