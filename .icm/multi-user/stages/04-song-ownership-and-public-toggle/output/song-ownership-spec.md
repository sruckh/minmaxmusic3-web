# Stage 04 Output — Song Ownership & Public Sharing Contract

> Layer 4 · Input contract for stages 05–07

Implemented in `internal/store/store.go` (`SetSongPublic`),
`internal/server/history.go` (`handleToggleSongPublic`), and
`internal/server/access.go` (origin check). Covered by
`internal/server/ownership_test.go` and `internal/store/store_test.go`.

## What this stage inherited already working

Four of the six Process items landed in Stages 01/03. They were verified end to
end here rather than rewritten, and each now has an integration test:

| Item | Where it already lived | Test added now |
|---|---|---|
| `handleCreateJob` attaches the user id | `handlers.go:108`, `UserID: s.caller(r).UserID` | `TestGeneratedSongInheritsItsOwner` |
| Worker persists `user_id` onto the song | `worker.go:313`, `UserID: j.UserID` | same |
| `handleDeleteSong` is owner-or-admin | `DeleteSong(id, s.caller(r))`, scoped in SQL | `TestSongIsolationBetweenUsers` |
| `handleAudio` allows owner/admin/public | `readableSong` (Stage 03) | `TestToggleSharesAndUnshares` |

The genuinely new surface is the toggle, plus the CSRF defence it made
necessary.

## The rule lives in the query

The lesson from `pocketbase/core/record_query.go` — the rule is injected into
the SQL, not evaluated in a handler branch that a second code path can reach
around — is what every mutating song operation here does:

```sql
UPDATE songs SET is_public = ? WHERE id = ? AND (user_id = ? OR ?) RETURNING …
```

There is no `Song()` read, no `if` in Go, and no window between checking and
writing. A handler cannot forget the check because there is no version of the
statement without it.

## `SetSongPublic`

```go
func (s *Store) SetSongPublic(id string, public bool, a Access) (*Song, error)
```

- Returns the row **as it now stands**, via `RETURNING`, so the caller needs no
  follow-up read.
- Returns `(nil, nil)` when the song does not exist **or** is not the caller's —
  deliberately indistinguishable, so the endpoint cannot confirm that an id
  belongs to someone.
- `Access{}` (the zero value) matches nothing, so a caller that lost its user
  changes nothing.

## `POST /songs/{id}/toggle-public`

**The endpoint sets an explicit target; it does not flip.** The caller sends:

| Field | Values | Meaning |
|---|---|---|
| `public` | `1`, `true`, `on`, `yes` | share |
| `public` | `0`, `false`, `off`, `no` | make private |
| absent or anything else | — | **400**, never a guess |

### Why not a flip

`is_public = NOT is_public` is atomic in SQLite, so it would not corrupt the
row — but the *outcome* depends on the order two requests happen to arrive in.
A user with the song open in two tabs, or double-clicking a slow control,
cannot say what state their song ended in, and one of those two clicks silently
undid the other. Setting a target removes the question: the last writer wins,
every writer agrees on the result, and repeating a request is a no-op rather
than an undo.

That also makes the operation safely retryable — an htmx retry or a refreshed
POST cannot invert the user's intent.

`TestToggleIsIdempotentAndExplicit` fires the same target three times and
asserts the state each round; `TestToggleConcurrentWritesConverge` fires 16
simultaneous shares and asserts the song ends shared.

### Responses

| Outcome | Status |
|---|---|
| Success (htmx) | 200 + re-rendered `share-toggle.html` fragment |
| Success (form post) | 303 → `/songs/{id}` |
| Not the caller's song, or no such song | **404** — identical answers |
| Missing/invalid `public` | 400 |
| Cross-origin write | 403 |
| No session | Stage 03 default-deny |

A non-owner never sees 403, because 403 would confirm the id exists.

## Un-publishing revokes immediately

`readableSong` re-reads `is_public` from the database on every request, and
nothing caches a decision or hands out a durable URL. The moment a song goes
private the next request for it 404s.

`TestToggleSharesAndUnshares` proves the whole arc: bob is refused, alice
shares, bob streams the actual bytes, alice un-shares, bob's very next request
is refused and no bytes are served.

`TestAudioIsNotReachableOutsideTheAuthorisedRoute` closes the other half of
that question — if audio files were reachable by any other route,
un-publishing would be cosmetic. It asserts the audio directory is not under
the static root (`AudioDir` is `/data/audio`; the static root is
`<WebDir>/static`) and that guessed static paths, including traversal attempts,
return nothing.

## Sharing grants reading, never writing

A public song is readable by any signed-in user and mutable by nobody but its
owner or an administrator. That includes **un-sharing**: letting a non-owner
flip someone else's song private would be a denial of service on their content.
It falls out of the SQL — every write carries `(user_id = ? OR ?)` — rather
than from a separate check.

`TestSharingGrantsReadingNeverWriting` asserts bob can read and stream a public
song but cannot delete, rename, or re-toggle it, that the row is unchanged
afterwards, and that the rendered page offers him no owner controls at all.

## CSRF — origin check, not tokens

This is the first user-aimed state-changing POST beyond auth, so the honest
answer to "is SameSite=Lax enough?" is **no, not by itself**, and this stage
adds a defence rather than deferring one.

`SameSite=Lax` stops a *cross-site* attacker: their forged POST arrives without
the session cookie and is simply unauthenticated. What it does **not** stop is a
*same-site* attacker. Any sibling host under the same registrable domain is
"same-site" to a cookie, so an XSS or open redirect in a neighbouring service on
this domain could forge a write here with the cookie attached.

`sameOriginWrite` runs before anything else for every unsafe method, on public
routes too — forging a login is as real an attack as forging a toggle:

1. `Sec-Fetch-Site`, when present, is authoritative: `same-origin` and `none`
   pass; `same-site` and `cross-site` are refused. This is what closes the
   sibling-subdomain gap.
2. Otherwise `Origin`, when present, must match `r.Host` or `cfg.PublicURL`.
3. Otherwise allow. No browser signal means no browser, and CSRF needs a browser
   to supply the ambient credentials. Refusing here would break curl and
   server-to-server callers while stopping no attack, since every browser sends
   `Origin` on a cross-origin write.

Safe methods are never refused by this check.

### Residual risk

- A browser that sends neither `Sec-Fetch-Site` nor `Origin` on a cross-origin
  POST would slip through rule 3. Every current browser sends `Origin`; a
  browser old enough to omit it also predates `SameSite`, so it was already
  unprotected.
- This is not a defence against XSS *in this application*. Same-origin script
  passes every check. Tokens would not help there either.

If the team wants a token regardless, the natural place is a per-session value
in the session row plus a hidden field — but it should be a deliberate stage,
not bolted onto this one.

## UI

`share-toggle.html` is a small fragment rendered on the song detail page and
returned by the toggle endpoint for htmx swaps. It shows the current state to
everyone who can see the song and the control only to those who can change it.

`handleSongDetail` now passes `CanEdit` (`s.owns(r, g)`), which also gates the
existing delete and regenerate buttons. **This is presentation only** — every
mutating endpoint re-derives ownership in its own SQL, so a hidden control is
never what stops a non-owner.

The song *card* toggle in `/history` is deliberately not built: it belongs with
the partitioned library, which is Stage 05's.

## What Stage 05–07 inherit

- `store.SetSongPublic(id, public, a)` for any sharing change; do not add a
  flip variant.
- `store.PublicSong(id)` reads a song only if shared; `readableSong(r, id)`
  applies the full owner/admin/public rule.
- `s.owns(r, g)` for showing owner-only controls.
- Send `public=1` / `public=0` from any new toggle control; an absent value is
  a 400 by design.
- Any new state-changing route inherits the origin check automatically — do not
  add per-route CSRF logic.
- Stage 05's community section should list `is_public = 1` songs; the songs it
  links to are already readable through `readableSong`, so no new authorization
  is needed there.
