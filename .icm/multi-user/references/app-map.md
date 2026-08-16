# Application Route & Access Control Map

> Layer 3 · Endpoint routing and authorization matrix
>
> **Status: reconciled with the shipped router.** This file originally carried
> the pre-build sketch, whose paths (`/api/jobs`, `/api/assistant`,
> `POST /songs/{id}/delete`) and middleware names (`RequireAuth`,
> `RequireAdmin`) were never built. The table below is the real route table as
> registered in `internal/server/{server,auth,handlers,admin}.go`.
> `TestEveryRouteIsProtectedUnlessExplicitlyPublic` enumerates the live router,
> not this file, so treat the code as the authority and keep this in step.

## Route Definitions & Access Levels

| Pattern | Auth Level | Purpose |
|---------|------------|---------|
| `GET /healthz` | Public | Health check. Returns `200 OK` even when administrator sign-in is disabled. |
| `GET /login` | Public | Sign-in page. |
| `POST /login` | Public | Verify credentials, issue a session. Rate limited per IP. |
| `GET /register` | Public | Account request page. |
| `POST /register` | Public | Create a `pending` account. Never issues a session. Rate limited per IP. |
| `POST /logout` | Public | Revoke the presented token server-side and clear the cookie. |
| `GET /static/` | Public | Static assets (`web/static`). |
| `GET /favicon.ico` | Public | Allowlisted but **not registered** — a browser's automatic probe gets a plain 404 instead of a redirect to `/login`. Real icons are under `/static/favicon/`. |
| `GET /{$}` | Authenticated | Main studio & generation UI. |
| `POST /assistant` | Authenticated | AI assistant prompt proxy. Rate limited per IP. |
| `POST /jobs` | Authenticated | Submit a generation job, owned by the session user. Rate limited per IP. |
| `GET /jobs/{id}` | Owner / Admin | htmx polling fragment. Scoped by `store.Job(id, Access)`. |
| `GET /history` | Authenticated | Partitioned library (`?mine=`, `?public=`). |
| `GET /history/personal` | Authenticated | htmx fragment, personal songs (`?page=`). |
| `GET /history/public` | Authenticated | htmx fragment, community songs (`?page=`). |
| `GET /songs/{id}` | Owner / Shared / Admin | Song detail view. |
| `POST /songs/{id}/toggle-public` | Owner / Admin | Set `is_public` explicitly (`public=1` / `public=0`), not a flip. |
| `POST /songs/{id}/regenerate` | Owner / Admin | Re-queue with the same inputs and seed. |
| `POST /songs/{id}/title` | Owner / Admin | Rename a song. |
| `DELETE /songs/{id}` | Owner / Admin | Delete the song, its job row, and its audio file. |
| `GET /audio/{id}` | Owner / Shared / Admin | Audio stream/playback. |
| `GET /admin` | Admin Only | User administration dashboard with pending badge. |
| `POST /admin/users/{id}/approve` | Admin Only | Approve an account. |
| `POST /admin/users/{id}/disable` | Admin Only | Disable an account and revoke its sessions. |
| `POST /admin/users/{id}/delete` | Admin Only | Delete an account, its content, and its audio. |

"Owner / Shared / Admin" means the ownership test lives in SQL, not in the
handler: a caller who is not entitled gets the same `404` as for an id that does
not exist, so no endpoint can be used to discover which ids are real.

## Middleware Enforcement

There are no `RequireAuth` / `RequireAdmin` wrappers to attach per route.
`Server.Routes()` wraps the **entire mux** in `Server.protect`, and access level
is decided by classification rather than by registration:

- **`protect`** — the single access-control surface. It refuses cross-origin
  writes first (for public routes too), then requires an approved session for
  anything not on `publicPatterns`, then requires administrator privilege for
  anything on `adminPatterns` (`GET /admin` and the `/admin/` prefix, so a new
  admin route is protected from the moment it is added).
- **`levelFor`** — classification via a second `http.ServeMux` used purely as a
  pattern matcher, so it shares exact matching semantics with the dispatching
  mux. The zero `accessLevel` is `accessAuth`: anything unmatched — including a
  method mismatch — is authenticated-only. **Forgetting to classify a new route
  makes it unreachable, never public.**
- **`authenticate`** — resolves the session, denies on every branch that is not
  a fully approved account, and injects `UserContext` (`UserID`, `Username`,
  `IsAdmin`, `ConfigAdmin`, `Status`, `PendingCount`). Status and privilege come
  from the store's live join with `users`, never from the session row.
- **`sameOriginWrite`** — the CSRF defence, an origin check rather than a token.
  `Sec-Fetch-Site` is authoritative when present, then `Origin`; a request with
  no browser signal at all is allowed, because CSRF needs a browser.
- **`Server.caller`** — the one place a request becomes a `store.Access`. The
  zero value owns nothing, so a handler somehow reached without a user reads and
  deletes nothing.
- **`requireAdmin`** — a second, in-handler check on the four admin handlers.
  Redundant with the middleware on purpose: these delete accounts and audio, so
  they do not assume the routing that reached them.
