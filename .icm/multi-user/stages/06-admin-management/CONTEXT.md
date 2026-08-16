# Stage 06 — Administrator Management & Badges

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/feature-spec.md` | Admin user lifecycle and badge requirements |
| 4 | `../05-song-history-partitioning/output/history-partition-spec.md` | Partitioned library and user data |

## Process
1. Implement `handleAdminDashboard` (`GET /admin`) to display all registered users, their status (`pending`, `approved`, `disabled`), and timestamps.
2. Implement pending user request query on `store.Store` (`CountPendingUsers`) and inject the count into template layout context.
3. Add pending requests badge to the Admin navigation tab when count > 0.
4. Implement `handleApproveUser` (`POST /admin/users/{id}/approve`) to transition user status to `approved`.
5. Implement `handleDisableUser` (`POST /admin/users/{id}/disable`) to transition user status to `disabled` and terminate active sessions.
6. Implement `handleDeleteUser` (`POST /admin/users/{id}/delete`) to delete user records and cleanup/reassign associated jobs and songs.
7. Write tests for admin endpoints verifying permission checks, status transitions, and user deletion.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Admin Management Contract | `output/admin-spec.md` | Markdown |
