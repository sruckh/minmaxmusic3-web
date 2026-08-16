# Multi-User Acceptance Report

> Layer 4 · Stage 07 output — what the suite proves about the shipped feature

This is a report on evidence, not a description of code. Every claim below is
backed by a named test that runs in `go test -race ./...`.

## Verdict

The multi-user feature behaves correctly end to end across all seven stages.
The full user lifecycle runs as one continuous journey against the real handler
stack, and the isolation, sharing, and revocation properties hold at every step.

`go test -race ./...` — **zero failures**, all packages green.

## The acceptance arc

`TestAcceptanceFullUserLifecycle` is the primary evidence. It drives three
independent browser-like sessions (`alice`, `bob`, `admin`) through one
continuous story. Each session keeps its own cookie jar, adopts whatever
`Set-Cookie` the server returns, sends `Sec-Fetch-Site: same-origin` as a real
browser would, and follows nothing automatically — every hop is asserted.

State is only ever created the way a user could create it. The test never
reaches into the store to fabricate an approval, a session, or a shared song.

| # | Step | What is proven |
|---|---|---|
| 1 | Anonymous hits `/`, `/history`, `/history/personal`, `/admin` | all four refused; `/login` is reachable and offers registration |
| 2 | Register as `alice` | 303 to the registration notice, **no session issued** |
| 3 | Log in while pending | 403 with the pending notice; **no session issued** |
| 3b | Wrong password on the same pending account | 401 with the uniform message — the pending status does **not** leak to someone without the password |
| 4 | Config admin logs in, opens `/admin` | dashboard lists alice under "Pending Registration Requests"; badge reads "1 Pending" |
| 5 | Admin approves; alice logs in | 303 to `/`; her username renders in the nav; **no Admin tab for a non-admin** |
| 6 | Alice generates a song through the worker | song lands owned by alice and **private by default**; appears in "My Songs"; she can stream it |
| 7 | Bob registers, is approved, logs in | alice's private song is absent from bob's library; `/songs/{id}` and `/audio/{id}` both 404; `DELETE` refused |
| 8 | Alice shares the song | it appears in bob's "Community Songs"; he reads the detail page and **streams the actual bytes**; his attempt to un-share it 404s |
| 9 | Alice un-shares | bob's **very next** request 404s for both audio and detail; the song leaves the community library |
| 10 | Admin disables alice | her in-flight session is refused on the next request; audio refused; she cannot log back in, and is told why |
| 11 | Admin deletes alice | account gone, **song gone even to an admin**, bob unaffected, dashboard no longer lists her |
| 12 | Bob logs out | session row destroyed server-side, not merely the cookie; his next request is refused |

Step 3b, step 9, and step 10 are the three that a naive implementation fails:
status leaking to an unauthenticated prober, revocation that waits for a cache
or a cookie to expire, and a disabled account that keeps working until logout.

## Isolation and sharing

Beyond the arc, isolation is pinned by the earlier stages' suites, which all
still pass:

- `TestPersonalSongsNeverLeak` — 45 songs across three pages; none reach a
  second user on any of the three history surfaces, at pages 1–5, 99, or 10000.
  Sharing one song exposes exactly that one.
- `TestNonOwnerCannotReachAnotherUsersSong` — a private song and a nonexistent
  id return the **same** status, so `/audio/{id}` cannot be used to discover
  which ids are real.
- `TestSharingGrantsReadingNeverWriting` — a public song is readable by anyone
  signed in and mutable by nobody but its owner, including un-sharing.
- `TestAudioIsNotReachableOutsideTheAuthorisedRoute` — the audio directory is
  not under the static root and traversal attempts return nothing, so
  un-publishing is real rather than cosmetic.

## Escaping

`TestAcceptanceHostileContentRendersInert` registers a user whose **username**
contains `<script>alert("xss")</script>` and gives a shared song a **title**
carrying both a script tag and an attribute-breakout payload
(`" onmouseover="alert(1)`). It then renders four surfaces — the admin
dashboard, the community library, the song detail page, and the owner's own
library — and asserts none contains an executable script tag or event handler.

Critically, it also asserts the payload **did** reach the page in escaped form
(`&lt;script&gt;`), so the test cannot pass merely because the content was
missing. Nothing in the codebase uses `template.HTML`, `template.JS`, or
`template.URL`; `html/template`'s contextual escaping is the only mechanism and
it is not bypassed anywhere.

Notice values are keys mapped server-side, so a hostile `?notice=` renders
**nothing at all** — not even escaped — on `/login`, and produces no notice
element on `/admin`.

## Empty and error states

`TestAcceptanceEmptyAndErrorStates` covers what a user meets when nothing has
happened yet or something went wrong:

| State | Evidence |
|---|---|
| No pending accounts | "No accounts are waiting for approval", and **no badge** |
| Empty personal library | "No songs in your library yet" + a link to write one |
| Empty community library | "Nothing has been shared yet…" |
| Failed login | 401, single uniform message |
| Rejected registration | 400, says what is wrong, keeps the typed username |
| Duplicate username | 409 |
| Throttled login | 429 with `Retry-After` and an explanation |

## Accessibility

`TestAcceptanceAccessibilityBasics` asserts, on the pages this feature added:

- Every form control has a bound `<label for>` and a matching `id`, plus
  `autocomplete` hints so password managers work.
- The pending badge reads **"1 Pending"** — words, not a bare number a screen
  reader would announce without context.
- Both admin tables are associated with their heading via `aria-labelledby`,
  and the action column has an `sr-only` accessible name.
- Buttons say what they do: "Approve User", "Disable User", "Delete User".
- Destructive admin actions confirm before firing.
- The sharing control carries an `aria-label` naming the song it acts on, so a
  list of identical "Make Public" buttons is distinguishable.
- `:focus-visible` styling already exists in `app.css` and is inherited.

## Theme and responsiveness

`TestAcceptanceThemeAndResponsiveHooksArePresent` asserts every new page
(`/login`, `/register`, `/admin`, `/history`) loads the shared stylesheet, has a
viewport meta, and uses the **existing** theme mechanism — the `mm3-theme`
localStorage key with the `data-theme` attribute set before first paint. It also
asserts no page embeds its own `prefers-color-scheme` handling, so a second
theming mechanism cannot creep in.

New pages inherit theming for free because they all include
`{{template "head" .}}`; no new theming code was written.

## Copy

All user-facing strings now match `_config/voice.md` exactly — registration,
pending, disabled, invalid-credentials and logout notices; the
"Pending Registration Requests" heading; the "{N} Pending" badge;
"Approve User" / "Disable User" / "Delete User"; the delete confirmation
wording; the "My Songs" / "Community Songs" headings and their subtitles; and
the "Publicly Visible" sharing label.

Tests now assert against the copy **constants** rather than string literals, so
`voice.md` is the single source of wording and a future copy change does not
require hunting through test files.

## Regression surface

Every guard built in earlier stages still passes unchanged:

| Guard | Test |
|---|---|
| Default-deny route enumeration | `TestEveryRouteIsProtectedUnlessExplicitlyPublic` |
| Public allowlist is exactly eight routes | `TestPublicAllowlistIsExactlyThis` |
| Login enumeration (status, body, timing) | `TestLoginFailuresAreIndistinguishable`, `TestAdminLoginBurnsSameWork` |
| Session fixation | `TestSessionFixation` |
| Raw tokens never stored | `TestSessionTokenIsNotStoredRaw` |
| Live privilege resolution | `TestSessionPrivilegeIsResolvedLive` |
| Open redirect via `next=` | `TestSafeNextRejectsOffsiteTargets` |
| CSRF origin check | `TestCrossOriginWritesAreRefused`, `TestAdminRoutesAreEnumeratedAndOriginChecked` |
| Last-admin guard | `TestLastAdminGuard`, `TestLastAdminIsProtected` |
| Self-destruction guard | `TestAdminCannotActOnSelf`, `TestConfigAdminCannotBeTargeted` |
| Paging clamps | `TestPagingParametersAreClamped` |
| Partition index plans | `TestPartitionQueriesUseTheirIndexes` |

## Known limitations

These are deliberate and documented, not oversights:

1. **No role-promotion endpoint.** `role` is only ever `user` from
   registration, so a database administrator can currently only be created by
   seeding the row directly. The static Infisical admin is the working
   administrator. The last-admin guard is built and tested ahead of promotion
   existing, so adding it later is safe by default.
2. **CSRF is an origin check, not a token.** It closes the cross-site and
   sibling-subdomain cases; it is not a defence against XSS within this origin,
   where tokens would not help either.
3. **The community library withholds author identity.** Usernames are the login
   identifier, so publishing them would undo the enumeration defence. Attribution
   would need an opt-in display name.
4. **No audit log.** Admin actions are logged via `slog` but there is no
   queryable trail.
5. **The admin dashboard is unpaged.** `ListUsers` returns every account; fine
   at present scale, and it is an admin-only surface.
6. **The two library sections are stacked, not tabbed.** The fragment endpoints
   support either presentation with no server change.
