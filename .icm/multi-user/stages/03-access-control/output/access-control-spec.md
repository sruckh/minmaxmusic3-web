# Stage 03 Output — Access Control Contract

> Layer 4 · Input contract for stages 04–07

Implemented in `internal/server/access.go`, with wiring in `server.go` and the
`next` round-trip in `auth.go`. Covered by `internal/server/access_test.go`.

## The central decision: protection is the default

Both production references this stage was measured against are **opt-in**:
GoTrue writes `r.With(api.requireAuthentication).Post(...)` per route, and
PocketBase attaches a `RequireAuth()` hook per route. In both, a route added
without the wrapper ships public, and nothing fails.

This implementation inverts that. `Routes()` wraps the **entire** mux:

```go
return s.protect(rt.mux)
```

`protect` classifies every request and only lets an explicitly public pattern
through unauthenticated. Consequences:

- A route added and never thought about is **authenticated-only**.
- Forgetting to classify a route makes it *unreachable*, never public — the
  failure mode is a broken feature you notice immediately, not a silent leak.
- Making something public requires editing `publicPatterns`, which is a
  deliberate, greppable, reviewable act.

```go
type accessLevel int
const (
	accessAuth   accessLevel = iota // valid, approved session — the DEFAULT
	accessPublic                    // must be listed explicitly
	accessAdmin                     // approved session with admin privilege
)
```

`accessAuth` is the zero value on purpose: classification is a map lookup, and
a miss returns the zero value.

### Classification

`newClassifier()` builds a second `http.ServeMux` whose handlers encode the
level. Using a ServeMux — rather than prefix string matching — means the
classifier shares **exact** pattern semantics with the mux that dispatches, so
there is no second, subtly different path matcher to drift out of agreement.

Anything the classifier does not match falls to `accessAuth`. That includes a
method mismatch and a path-cleaning redirect, both of which therefore fail
closed.

### The public allowlist

```go
var publicPatterns = []string{
	"GET /healthz",
	"GET /login", "POST /login",
	"GET /register", "POST /register",
	"POST /logout",
	"GET /static/", "GET /favicon.ico",
}
```

**Deliberate deviation from app-map.md:** it lists `/logout` as Authenticated;
here it is public. Logout only revokes the token the caller presents. Requiring
an approved session to log out strands a user disabled mid-session with a
cookie they cannot clear, and protects nothing — there is no state to leak.
`SameSite=Lax` already blocks the cross-site POST.

```go
var adminPatterns = []string{"GET /admin", "/admin/"}
```

The `/admin` prefix is classified **before Stage 06 registers a single handler
there**. Those routes are admin-only from the moment they exist, rather than
from the moment someone remembers to protect them.

## UserContext

```go
type UserContext struct {
	UserID       string
	Username     string
	IsAdmin      bool
	ConfigAdmin  bool
	Status       string
	PendingCount int // administrators only — it drives the approval badge
}

func userFrom(ctx context.Context) (*UserContext, bool)
```

Injected into the request context by `protect` for every request that clears
the middleware. `PendingCount` is computed only for administrators; a failure
to count is logged and left at zero, because a badge is not worth failing a
request over.

## The `caller` seam is now filled

```go
func (s *Server) caller(r *http.Request) store.Access {
	uc, ok := userFrom(r.Context())
	if !ok {
		return store.Access{} // owns nothing
	}
	if uc.IsAdmin {
		return store.AdminAccess(uc.UserID)
	}
	return store.UserAccess(uc.UserID)
}
```

This is the seam Stage 01 left and Stage 02 preserved. Every ownership-scoped
store call in the server routes through it, so one function decides what a
request may touch. A request that somehow reaches a handler without a user gets
the zero `Access`, which reads and deletes nothing.

## Authorization matrix as implemented

| Route (current path) | app-map name | Level |
|---|---|---|
| `GET /{$}` | `/` | Authenticated |
| `GET /login`, `POST /login` | same | Public |
| `GET /register`, `POST /register` | same | Public |
| `POST /logout` | same | Public (see above) |
| `GET /history` | `/history` | Authenticated, scoped by `caller` |
| `GET /songs/{id}` | same | Owner / Public / Admin |
| `DELETE /songs/{id}` | `/songs/{id}/delete` | Owner / Admin |
| `POST /songs/{id}/title` | — | Owner / Admin |
| `POST /songs/{id}/regenerate` | — | Owner / Admin |
| `POST /assistant` | `/api/assistant` | Authenticated |
| `POST /jobs` | `/api/jobs` | Authenticated |
| `GET /jobs/{id}` | `/api/jobs/{id}/fragment` | Owner / Admin |
| `GET /audio/{id}` | same | Owner / Public / Admin |
| `GET /healthz`, `GET /static/` | same | Public |
| `/admin`, `/admin/…` | same | Admin only (no handlers yet) |

**Path naming:** app-map.md uses `/api/jobs`, `/api/assistant`, and
`/api/jobs/{id}/fragment`. The application serves these as `/jobs`,
`/assistant`, and `/jobs/{id}`. Renaming them is a feature change that would
break templates and existing tests, and Stage 03 owns authorization, not route
naming. The *levels* are exactly as specified. `wantsJSON` already treats an
`/api/` prefix as an API client, so a future rename inherits the JSON error
shape automatically.

Routes in app-map.md that do not exist yet — `/history/personal`,
`/history/public`, `/songs/{id}/toggle-public`, and every `/admin/*` handler —
are **not stubbed**. Their protection is pre-wired (the `/admin/` prefix
classification, and default-deny for the rest), so they arrive protected.

## Refusal shapes

`deny` answers in the shape the caller can use:

| Caller | Response |
|---|---|
| API (`/api/` prefix, or JSON `Accept`/`Content-Type`) | `401`/`403` + `{"error":…,"message":…}` |
| htmx (`HX-Request: true`) | status + `HX-Redirect: /login?…` so the browser navigates instead of swapping a login page into a fragment |
| Browser GET | `303 → /login?next=…&notice=…` |
| Browser non-GET | plain status + message (nowhere useful to land) |

| Condition | Status | Error code |
|---|---|---|
| No cookie, unknown/expired/orphaned session | 401 | `unauthenticated` |
| Session valid, status not approved | 403 | `account-not-approved` |
| Approved but not admin, on `/admin` | 403 | `forbidden` |
| Session lookup failed | 500 | `internal` |

### Fail closed

`authenticate` has no path that continues with a nil user. In particular a
**store error denies with 500** rather than falling through as anonymous. A
dead cookie is additionally cleared on the way out, so the browser stops
presenting it.

### Status is enforced per request, never cached

`store.GetSession` resolves role and status by live join (Stage 01), and
`authenticate` re-checks `Status == StatusApproved` on **every** request. A user
disabled mid-session is refused on their next request without the session row
being touched — `TestStatusIsEnforcedOnEveryRequest` disables an account and
leaves its session row in place precisely to prove the check is live.

## `next=` — open-redirect defence

`safeNext` accepts only a local path and drops everything else. Rejected:
absolute URLs, any scheme, scheme-relative `//evil.com`, the backslash variants
browsers normalise into it (`/\evil.com`), percent-encodings that decode into
one (`/%2f%2fevil.com`), userinfo or host components, control characters
(browsers strip some, and would then act on a target that was never validated),
empty segments and dot segments (`/..//evil.com`), anything over 512 bytes, and
`/login`, `/register`, `/logout` — which merely loop.

It is applied at **three** points, because the value is attacker-controlled at
each: when the middleware builds the login URL, when the login page renders it
into a hidden field, and when `handleLogin` acts on the submitted form. A
rejected value becomes `""` and the user lands on `/`.

## `/audio/{id}` — the sharpest edge

It streams bytes, so it gets the strictest treatment:

```go
func (s *Server) readableSong(r *http.Request, id string) (*store.Song, error) {
	g, err := s.st.Song(id, s.caller(r))   // owner, or anything if admin
	if err != nil || g != nil { return g, err }
	return s.st.PublicSong(id)             // else only if explicitly shared
}
```

A refusal is a plain **404 — byte-identical to a song that does not exist** —
so the endpoint cannot be used to enumerate which ids are real.
`TestNonOwnerCannotReachAnotherUsersSong` asserts the private and missing cases
return the same status, that no metadata leaks into the body, and that a shared
song *is* readable (so the 404s are ownership working, not the route being
broken). It also asserts that sharing grants reading and never writing.

`store.PublicSong(id)` is the one additive store method this stage introduces:
the `is_public = 1` test lives in the SQL, not in a caller that might forget it.
It is not the public/personal library split — that remains Stage 05's.

`SongForJob` remains unscoped and remains gated by the scoped `Job` lookup in
`handleJobFragment`, which 404s first. **Stage 04 must not reorder that.**

## Tests

| Test | Guards |
|---|---|
| `TestEveryRouteIsProtectedUnlessExplicitlyPublic` | walks the **real recorded route table** and fires an unauthenticated request at every non-public route |
| `TestPublicAllowlistIsExactlyThis` | independent copy of the allowlist — publishing a route requires editing a test too |
| `TestUnclassifiedRouteDefaultsToProtected` | the default itself, on paths that do not exist yet |
| `TestNonOwnerCannotReachAnotherUsersSong` | the audio/detail proof |
| `TestSafeNextRejectsOffsiteTargets` | 19 hostile `next` values |
| `TestLoginHonoursOnlySafeNext`, `TestLoginPageDoesNotReflectHostileNext` | the round trip, not just the helper |
| `TestStatusIsEnforcedOnEveryRequest` | live status, disabled and pending |
| `TestRevokedSessionIsRefusedAndCookieCleared` | revoked and garbage tokens |
| `TestAdminRoutesRequireAdmin` | anonymous / user / admin on `/admin*` |
| `TestUnauthenticatedAPIGetsJSON`, `…HTMXGetsRedirectHeader`, `…BrowserRedirectsToLoginWithNext` | the three refusal shapes |
| `TestCallerScopeMatchesSession` | the ownership seam |

The route enumeration is driven by `Server.routes`, recorded by the `router`
wrapper as `Routes()` registers each pattern — so it is the table the server
actually serves, not a hand-maintained copy that drifts.

## What Stage 04–06 inherit

- **Add routes freely; they are protected on arrival.** Only edit
  `publicPatterns` (and its test copy) if a route genuinely must be anonymous.
- Register admin handlers under `/admin/…` and they are admin-gated already.
- Use `s.caller(r)` for every ownership-scoped store call, and
  `s.readableSong(r, id)` where the public rule applies.
- Read the caller with `userFrom(r.Context())`; it is always present in a
  handler, since no unauthenticated request reaches one.
- `UserContext.PendingCount` is populated for admins — Stage 06's badge needs
  no extra query.
- Do not reorder the `Job`-then-`SongForJob` gate in `handleJobFragment`.
