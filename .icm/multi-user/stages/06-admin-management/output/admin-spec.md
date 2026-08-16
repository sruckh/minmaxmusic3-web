# Stage 06 Output — Administrator Management Contract

> Layer 4 · Input contract for stage 07

Implemented in `internal/server/admin.go`, `internal/store/store.go`
(`guardLastAdmin`, `DeleteUser`, `ErrLastAdmin`), and three templates. Covered
by `internal/server/admin_test.go` and `internal/store/store_test.go`.

These are the first destructive endpoints in the application, and each takes an
id straight from the URL. The design follows from that.

## Routes

| Route | Level | Effect |
|---|---|---|
| `GET /admin` | admin | dashboard: pending requests first, then all accounts |
| `POST /admin/users/{id}/approve` | admin | status → `approved` |
| `POST /admin/users/{id}/disable` | admin | status → `disabled`, sessions revoked |
| `POST /admin/users/{id}/delete` | admin | account, sessions, jobs, songs, audio |

All four are classified `accessAdmin` by the `/admin` prefix that Stage 03
pre-wired, all four are absent from `publicPatterns`, and all three POSTs
inherit Stage 04's `sameOriginWrite` check.
`TestAdminRoutesAreEnumeratedAndOriginChecked` asserts each pattern is in the
recorded route table the default-deny enumeration walks, that none reached the
public allowlist, and that cross-origin and same-site writes are refused with
nothing changed.

## Two walls, deliberately

The middleware gates `/admin`, and `requireAdmin` re-derives administrator
status **inside every handler**. The second wall is not redundancy theatre:
these handlers delete accounts and unlink audio, so they do not assume the
routing that reached them. If a future change registers one outside the
`/admin` prefix, or edits `adminPatterns`, the handler still refuses.

`TestAdminEndpointsRefuseNonAdmins` covers anonymous, approved-but-not-admin
(403), and pending (refused before privilege is even considered) on all four
routes, and asserts the target account is unchanged afterwards.

## Self-destruction

An administrator **cannot disable or delete their own account**. Approving
yourself is allowed, because it is a no-op rather than a lockout.

The guard is `id == uc.UserID` in `adminAction`, with `allowSelf` true only for
approve. `TestAdminCannotActOnSelf` asserts both refusals, that the account is
still present and approved afterwards, and that the admin is still able to
reach the dashboard.

### The configured administrator

`/admin/users/config:admin/{approve,disable,delete}` is refused **before any
other check**, with its own message: the static administrator has no `users`
row, so there is nothing to act on.

The ordering matters. For the config admin both the self guard and this one
apply, and "not a database account" is the true reason — reporting "you cannot
do that to your own account" would be misleading. Either way **nothing is
written and nothing 500s**: the request never reaches the store.

`TestConfigAdminCannotBeTargeted` signs in as the configured administrator,
fires all three actions at `config:admin`, asserts a 303 with the right notice
each time, asserts the user count is unchanged, and asserts the admin is still
signed in with dashboard access.

## The last-admin problem

**Decision: refuse, unconditionally, and do not rely on the config admin as the
floor.**

`store.ErrLastAdmin` is returned when deleting an account, or moving it off
`approved`, would leave the database with no approved administrator.

The alternative was to allow it whenever `AdminLoginEnabled()` is true, on the
grounds that the config admin is a permanent way back in. That was rejected
because it makes a safety invariant depend on a runtime configuration value
that can change underneath it: if `ADMIN_PASSWORD` is later cleared or rotated
badly, a deletion that was legal at the time has retroactively stranded the
system, and nothing detects it. The unconditional rule is checkable, has no
environmental input, and its cost is small — an administrator who genuinely
wants no DB admins must promote someone first, which is the same click ordering
they would want anyway.

The config admin remains the recovery path if the invariant is ever defeated
out-of-band, but it is a backstop, not the design.

### Why the guard lives in the transaction

```go
func guardLastAdmin(tx *sql.Tx, id string) error
```

It reads the target's role and status and counts the other approved
administrators **inside the caller's transaction**. Checking in one statement
and writing in another leaves a window where two concurrent removals each see
the other as the survivor and both commit — the exact TOCTOU shape the critic
found in the reference's `recordUpdate`.

An account that is not currently an approved administrator protects nothing, so
the guard passes immediately. Notably a **disabled** administrator does not
count as cover for removing the live one, and can itself be removed.

`TestLastAdminGuard` covers: delete, disable, and un-approve all refused for
the sole admin; nothing written by the refusal; approve unaffected; ordinary
users unaffected; removable once a second approved admin exists; the new last
admin protected in turn; a disabled admin not counted as a survivor.

## Deletion

```go
func (s *Store) DeleteUser(id string) ([]string, error)
```

**One transaction**, in order: guard, collect audio paths, delete songs, delete
jobs, delete sessions, delete the user, commit. Returns the audio paths for the
caller to unlink.

### What happens to their content: it is destroyed

The three options were cascade, reassign to `legacy`, and orphan.

- **Reassign** was rejected as a privacy regression dressed up as preservation:
  a departed user's *private* songs would quietly become permanently browsable
  by administrators under a shared owner.
- **Orphan** (leaving `user_id` pointing at a row that no longer exists) is
  strictly worse — the songs stay on disk and in the database, reachable by
  admins via `AdminAccess`, belonging to nobody, forever.
- **Cascade** is what "delete this account" means in an admin panel, and it is
  the only option with no surprising residue.

A deleted user's **shared** songs leave the community library with them, rather
than lingering there attributed to nobody.

### Transaction boundary vs the filesystem

The database commits **first**; files are unlinked **after**, by the handler. An
unlink cannot join a SQL transaction, so one of the two orderings must be
chosen deliberately:

- DB first: a crash between the two leaves an orphaned file on disk. That is
  recoverable garbage — no row references it and nothing serves it.
- Files first: a crash or rollback leaves rows pointing at audio that is gone,
  which is a broken library.

DB-first is the only ordering where a partial failure is harmless. A failed
unlink is logged and reported to the administrator as
"some audio files could not be removed" rather than being swallowed.

`TestDeleteUserRemovesEverything` asserts the account, session, both songs
(private and shared), the job, and both audio files are gone, that the shared
one is no longer public, and that another user's account, song, and audio file
are untouched. `TestDeleteUserCascadesInOneTransaction` pins the same at the
store level, including the returned paths, an empty user, and `sql.ErrNoRows`
for a missing one.

## Status transitions are explicit and idempotent

Both endpoints **set a state**; neither toggles. Approving an approved account
succeeds as a no-op — `TestApproveIsIdempotent` fires it three times and checks
the state each round. Acting on an account that has since vanished is a notice,
not a 500.

Disabling revokes the account's sessions in the **same transaction** as the
status change (Stage 01's `UpdateUserStatus`), so the user is signed out at
once; and Stage 01's live status join would refuse them on their next request
even if a session row survived. `TestDisableTerminatesSessions` asserts the
session row is gone, the next request is refused, and that re-approving does
**not** resurrect the revoked token.

## The badge

`UserContext.PendingCount` is populated by the Stage 03 middleware **for
administrators only**, so the count never enters a non-admin context in the
first place — the layout could not leak it even if the template were wrong.

`pageData(r, data)` injects `.User` into full-page renders; the nav shows the
Admin tab under `{{if .User}}{{if .User.IsAdmin}}` (nested, so a page with no
user cannot nil-dereference) and the badge only when the count is above zero.

`TestPendingBadge` asserts: the tab with no badge at zero pending, the badge
with the count at three, neither tab nor badge for a non-admin on `/` or
`/history`, and that `/login` — which has no user at all — still renders.

## Notices

Admin actions redirect with a **notice key**, mapped to a message server-side
by `adminNoticeFor`, exactly as the login page does. Nothing a caller supplies
is reflected into the dashboard.

## What Stage 07 inherits

- `.User` (a `*UserContext`) is available on every full-page render: `UserID`,
  `Username`, `IsAdmin`, `ConfigAdmin`, `Status`, `PendingCount`.
- Guard any admin-only markup with nested `{{if .User}}{{if .User.IsAdmin}}` —
  login and register render with no user.
- `admin.html` and `admin-actions.html` are functional and unstyled beyond the
  existing skeleton; the `nav-badge` class is the badge hook.
- The `dict` template function builds an inline map for sub-templates that need
  more than one value.
- Adding an admin route under `/admin/…` inherits admin gating, the origin
  check, and the enumeration test automatically.
- **There is no role-promotion endpoint.** `role` is only ever `user` from
  registration, so a database administrator can currently only be created by
  seeding the row directly. The last-admin guard is therefore defensive for a
  state the application cannot yet reach on its own — deliberately, so that
  adding promotion later is safe by default.
