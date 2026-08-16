# Stage 02 — Authentication & Session Management

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/infisical-admin.md` | Admin credentials integration contract |
| 3 | `../../references/feature-spec.md` | Login and registration requirements |
| 4 | `../01-data-model/output/data-model-spec.md` | Store models and session methods |

## Process
1. Update `internal/config/config.go` to load `AdminUser` and `AdminPassword` from Infisical environment variables.
2. Implement password hashing and verification helpers using `golang.org/x/crypto/bcrypt`.
3. Create `handleRegister` in `internal/server/handlers.go` to validate inputs and insert users with `status = 'pending'`.
4. Create `handleLogin` to verify credentials against `AdminUser`/`AdminPassword` (Infisical) or database users, rejecting `pending`/`disabled` statuses.
5. Issue secure, HTTP-only session cookies (`mm3_session`) upon successful login.
6. Create `handleLogout` to invalidate the active session in SQLite and clear the cookie.
7. Write tests in `internal/server/server_test.go` covering registration, admin login, pending rejection, and logout.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Auth & Session Contract | `output/auth-spec.md` | Markdown |
