# Stage 01 — Data Model & Schema Migrations

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/schema.md` | Table definitions for users, sessions, jobs, songs |
| 3 | `../../_config/conventions.md` | Data ownership and status enum rules |

## Process
1. Define `User` and `Session` structs in `internal/store/store.go` with ID, username, password hash, status, and timestamps.
2. Update `Job` and `Song` structs to include `UserID string` and `IsPublic bool`.
3. Add migration queries in `migrate()` to create `users` and `sessions` tables and add `user_id` and `is_public` columns to `jobs` and `songs`.
4. Implement user CRUD operations on `Store`: `CreateUser`, `GetUserByID`, `GetUserByUsername`, `UpdateUserStatus`, `DeleteUser`, `ListUsers`, `CountPendingUsers`.
5. Implement session operations: `CreateSession`, `GetSession`, `DeleteSession`, `DeleteExpiredSessions`.
6. Update `CreateJob`, `CreateSong`, and `Song` methods to accept and persist `user_id` and `is_public`.
7. Write unit tests in `internal/store/store_test.go` verifying schema migrations and user/session/song persistence.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Schema & Store Contract | `output/data-model-spec.md` | Markdown |
