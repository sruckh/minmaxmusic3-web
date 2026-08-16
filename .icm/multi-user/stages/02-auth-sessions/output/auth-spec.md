# Stage 02 Output — Authentication & Session Contract

> Layer 4 · Input contract for stages 03–07

Implemented in `internal/server/auth.go`, `internal/config/config.go`, and the
`login.html` / `register.html` templates. Covered by `internal/server/auth_test.go`.

Stage 02 owns **credential verification and session issue/revoke only**. It
does not protect any route — no middleware, no redirects on unauthenticated
access. That is Stage 03's contract.

## Configuration

```go
// internal/config
type Config struct {
	// ...
	AdminUser     string // ADMIN_USER
	AdminPassword string // ADMIN_PASSWORD
}

// AdminLoginEnabled reports whether the static administrator can sign in.
func (c *Config) AdminLoginEnabled() bool
```

Both halves must be present. A blank half **disables administrator login
outright** — there is no default credential, so a misconfigured deploy has no
administrator rather than a guessable one. `server.New` logs a warning at
startup when it is disabled; the rest of the app still boots.

`Config.Summary()` reports `admin_user=set|(unset) admin_password=true|false
admin_login=true|false`. It never prints either value. Nothing in this stage
logs a password, a raw token, or `ADMIN_PASSWORD`.

## Password hashing

```go
const ProductionBcryptCost = 12
func hashPassword(password string) (string, error)
func checkPassword(hash, password string) bool
```

bcrypt at cost 12, satisfying the conventions' "cost >= 12". `checkPassword`
treats any error, including a malformed hash, as a failure — never a pass.

`bcryptCost` is a package var initialised to `ProductionBcryptCost`. It is a
var **only** so tests can lower the work factor (cost 12 costs seconds per call
under `-race`); production never changes it, and
`TestRegisterCreatesPendingUser` runs at the production cost and asserts the
stored hash carries the `$2a$12$` prefix.

Passwords are capped at **72 bytes** at validation time, because bcrypt
silently ignores bytes past 72 — a longer password must be rejected, never
truncated into a weaker one.

## Routes

| Method | Path | Behaviour |
|---|---|---|
| GET | `/login` | Login form. `?notice=registered\|signed-out` selects a **keyed** message; nothing from the query string is reflected into the page. |
| POST | `/login` | Verifies credentials, issues a session, `303 → /` |
| GET | `/register` | Signup form |
| POST | `/register` | Validates, stores a pending user, `303 → /login?notice=registered` |
| POST | `/logout` | Revokes the session server-side, clears the cookie, `303 → /login?notice=signed-out` |

### Registration

Username >= 3 runes (<= 64, no whitespace), password >= 8 bytes (<= 72),
`confirm_password` must match. The account is stored with
`status = pending`, `role = user`. **Registration never signs the new user
in** — approval is an administrator's decision.

A signup whose username matches `ADMIN_USER` (case-insensitively) is refused
with 409. The config path wins at login, so such an account could never be
used and the collision would only ever confuse.

Ids come from `newUserID()`: 16 random bytes, hex encoded — 32 lowercase hex
characters.

### Login

Order of operations is load-bearing:

1. Rate limit (below).
2. If `AdminLoginEnabled()` and the username constant-time-equals `AdminUser`,
   take the **config path**: `subtle.ConstantTimeCompare` against
   `AdminPassword`. Otherwise look the user up and use bcrypt.
3. **Verify the password.**
4. Only then check status.
5. Issue the session.

Status is checked *after* the password verifies because the pending and
disabled notices confirm an account exists. Reachable before verification, they
would be an enumeration oracle; reachable only after, they leak nothing to
anyone who does not already hold the password.

Responses:

| Outcome | Status | Body |
|---|---|---|
| Success | 303 → `/` | — |
| Wrong password, unknown user, blank field, over-long password | 401 | `Incorrect username or password.` |
| Correct password, status pending | 403 | pending notice |
| Correct password, status disabled | 403 | disabled notice |
| Throttled | 429 + `Retry-After: 900` | throttle notice |

## Session issue

```go
func (s *Server) startSession(w, r, userID, username string, configAdmin bool) error
```

- The token comes from `store.NewSessionToken()` — 32 crypto/rand bytes, hex.
  It is **never** taken from the request.
- Any session the client arrived with is **revoked server-side** before the new
  one is issued.
- TTL is 7 days, written as an absolute `ExpiresAt` in UTC. No column takes
  `DEFAULT CURRENT_TIMESTAMP`; the Stage 01 timestamp rule is intact.

Cookie `mm3_session`: `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` when the
connection is HTTPS.

`isTLS` accepts `r.TLS != nil` **or** `X-Forwarded-Proto: https`. Behind the
reverse proxy `r.TLS` is always nil, so without the header check `Secure` would
never be set in production. Unlike the client IP in `ratelimit.go`, trusting
this header is safe: a forged value can only *add* `Secure`, never remove it —
the worst an attacker achieves by lying is that their own cookie stops being
sent over plain HTTP.

### The static administrator's identity

```go
const ConfigAdminUserID = "config:admin"
```

The administrator has no `users` row, so its session's `user_id` must never
collide with a real one. Generated ids are 32 lowercase hex characters and
**hex cannot produce a colon**. `TestConfigAdminIDCannotCollide` pins all three
halves of that argument: the sentinel contains a colon, generated ids are 32
characters, and generated ids never leave the hex alphabet.

The admin session carries `ConfigAdmin: true`, which is what makes Stage 01's
`GetSession` resolve it to `IsAdmin: true, Status: approved` without a users
row. Admin credentials never touch the database
(`TestConfigAdminNeverReachesTheDatabase`).

## Logout

Revokes the session row via `store.DeleteSession` **and** clears the cookie.
Clearing only the cookie would leave a token that still authenticates if it
had leaked. Logging out twice is harmless.

## Attack surface — how each is handled

**Username enumeration.** A wrong password and a nonexistent user are
indistinguishable in status (401), in body (one shared
`Incorrect username or password.`), and in time. The unknown-user path runs
`burnPasswordCheck()`, a real bcrypt comparison against a decoy hash at the
current cost, so it cannot return early. The **config-admin path burns the same
work**: `subtle.ConstantTimeCompare` is orders of magnitude cheaper than
bcrypt, so without the decoy the administrator's username would be identifiable
by a fast rejection. Every login path performs exactly one bcrypt-cost
comparison. Asserted by `TestLoginFailuresAreIndistinguishable` and
`TestAdminLoginBurnsSameWork`.

*Bounded, deliberate exception:* `POST /register` answers 409 "That username is
already taken." A username-based signup cannot avoid disclosing that, and
hiding it would leave users unable to understand a failed signup. It is
throttled to 5/hour/IP, and the login path leaks nothing.

**Brute force.** `POST /login` is limited to 10 attempts per 15 minutes per IP,
`POST /register` to 5 per hour, using the existing `limiter`. Every attempt
counts, including successful ones — otherwise a limiter is bypassed by guessing
until one lands (`TestLoginIsRateLimited` asserts the correct password is
throttled too).

**Session fixation.** The issued token is always freshly generated and never
the one the client supplied, and any pre-existing session is revoked
server-side rather than merely overwritten in the browser. A token planted
before login is dead, not promoted (`TestSessionFixation`).

**Credential leakage.** `TestPasswordsNeverAppearInResponses` sweeps every auth
response for the fixture passwords. Logs record the username and IP on login
events but never a credential or token.

## What Stage 03 inherits

- `sessionCookie` (`"mm3_session"`) and `sessionToken(r) string` to read the
  raw token off a request.
- `store.GetSession(token)` returns `(nil, nil)` for unknown, expired, or
  orphaned sessions, and otherwise a session whose `IsAdmin` and `Status` are
  resolved live. Middleware should require `Status == store.StatusApproved`.
- `ConfigAdminUserID` identifies the static administrator; `Session.ConfigAdmin`
  distinguishes it from a database admin.
- `s.caller(r)` in `internal/server/history.go` is still the Stage 01
  placeholder returning `store.UserAccess(store.LegacyUserID)`. **Stage 03
  replaces its body with the session user**, and every scoped store call in the
  server is scoped at once.
- Both `/login` and `/register` must stay reachable unauthenticated, and
  `/logout` must stay reachable by an unapproved session so a pending user can
  clear their own cookie.
