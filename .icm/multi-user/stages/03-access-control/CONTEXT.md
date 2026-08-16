# Stage 03 — Access Control & Route Protection

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/app-map.md` | Endpoint routing and authorization matrix |
| 4 | `../02-auth-sessions/output/auth-spec.md` | Auth handlers and session verification |

## Process
1. Implement `RequireAuth` middleware in `internal/server/server.go` to validate session cookies and redirect unauthenticated requests to `/login`.
2. Implement `RequireAdmin` middleware to restrict administrative routes to sessions with `is_admin = true`.
3. Create `UserContext` and inject current user ID, username, admin status, and pending user count into request contexts.
4. Protect generation endpoints (`/`, `/api/jobs`, `/api/assistant`), history (`/history`), and audio streams (`/audio/{id}`).
5. Ensure public endpoints (`/login`, `/register`, `/static/*`, `/favicon*`) bypass auth middleware.
6. Handle unauthorized API requests with structured 401/403 JSON responses.
7. Write middleware tests verifying route access enforcement and redirection logic.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Access Control Contract | `output/access-control-spec.md` | Markdown |
