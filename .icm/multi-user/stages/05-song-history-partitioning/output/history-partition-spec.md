# Stage 05 Output — Partitioned History Contract

> Layer 4 · Input contract for stages 06–07

Implemented in `internal/store/store.go` (`PersonalSongs`, `PublicSongs`,
`clampPage`), `internal/server/history.go`, and three templates. Covered by
`internal/server/partition_test.go` and `internal/store/store_test.go`.

## Store queries

```go
const MaxPageSize = 100

func (s *Store) PersonalSongs(userID string, limit, offset int) ([]*Song, error)
func (s *Store) PublicSongs(limit, offset int) ([]*Song, error)
```

**`PersonalSongs` takes a user id, not an `Access`.** That is the whole answer
to the admin question: `AdminAccess` lifts the ownership predicate everywhere
else in this codebase, and if "My Songs" went through it an administrator's
personal library would silently become every song in the system. There is no
admin variant of this query, so the mistake is not available.

`PublicSongs` takes neither — ownership does not narrow it, because that is
what "shared" means. It is the one list query with no owner predicate, and its
`is_public = 1` filter is the entire authorization, in the SQL.

### Index alignment

| Query | Index | Plan |
|---|---|---|
| `WHERE user_id = ? ORDER BY created_at DESC` | `idx_songs_user_created (user_id, created_at DESC)` | index range scan |
| `WHERE is_public = 1 ORDER BY created_at DESC` | `idx_songs_public_created (is_public, created_at DESC)` | index range scan |

`TestPartitionQueriesUseTheirIndexes` builds 2 000 songs and asserts each plan
names its index **and** contains no `TEMP B-TREE` — an index that is used for
the filter but not the ordering would still sort the whole partition per page.

### Clamping

`clampPage` bounds every paged read in the store — `PersonalSongs`,
`PublicSongs`, and the pre-existing `Songs`:

- `limit < 1` → 1, `limit > MaxPageSize` (100) → 100.
- `offset < 0` → 0. SQLite errors on a negative offset, so this is correctness
  as well as safety.

This is the last line, not the only one: the handler clamps the page number
first. The store clamps anyway because it cannot see who called it.

## Handler paging

```go
const maxPage = 10000
func pageParam(r *http.Request, key string) int
```

`pageParam` returns a **valid page for any input**. Unparseable, empty, zero,
negative, and absurd values all become a real page rather than being trusted:
`limit` and `offset` reach SQL, and an unbounded page number makes the database
walk the table on demand. `maxPage` caps the offset at 200 000 rows.

The page number is the only user-supplied paging input — `limit` is always
`pageSize` and is never taken from the URL, so there is no `limit=1000000`
surface at all.

Paging parameters, by route:

| Route | Parameters |
|---|---|
| `GET /history` | `?mine=N&public=N` — both sections, independently |
| `GET /history/personal` | `?page=N` |
| `GET /history/public` | `?page=N` |

Each query asks for `pageSize+1` rows so `HasNext` is exact — no phantom
"Older" link on a boundary page, and no separate `COUNT`, which would be a
second query and a second thing to scope.

## Routes and authorization

`/history/personal` and `/history/public` are **real routes**, not includes.
They are registered on the same mux as everything else and are absent from
`publicPatterns`, so Stage 03's whole-mux default-deny requires an approved
session — verified rather than assumed by `TestHistoryFragmentsRequireASession`
(anonymous *and* pending are both refused) and by
`TestHistoryFragmentsAreEnumerated`, which asserts both patterns appear in the
recorded route table that
`TestEveryRouteIsProtectedUnlessExplicitlyPublic` walks, and that neither
crept into the public allowlist.

Authorization is doubled deliberately: the middleware requires a session, and
the query is scoped to the session user regardless. Neither depends on the
other.

## What a community card exposes — and what it does not

**A community card names the song, never the person.** It shows title, length,
engine, created-at, a sharing badge, and a download link. It withholds:

- **The owner's username.** This is a deliberate product choice, and the reason
  is specific to this application: the username *is* the login identifier.
  Publishing a directory of them would hand an attacker a validated list of
  half of every credential pair and undo the enumeration defence Stage 02 built
  — where a wrong password and an unknown user are indistinguishable in status,
  body, and time. A community library that names its authors would make that
  work pointless. If attribution is wanted later, the right shape is a separate
  display name that a user opts into, not the login username.
- **The owner's user id**, for the same reason plus it being an internal key.
- **The seed**, which is an owner-facing generation detail rather than
  something a listener needs.
- **Owner controls** — no delete, no rename — for anyone but the owner.

`TestCommunityCardsWithholdOwnerIdentity` asserts the rendered body contains
neither the username nor the user id, and no delete control.

Lyrics and caption remain visible on the *detail* page of a shared song, which
is the reading grant Stages 03/04 already established; the card itself does not
carry them.

## Sections and controls

`songSection` is one half of the library; `songCard` is one row, carrying a
per-song `CanEdit`. Per-song is necessary because the community section mixes
other people's songs with the viewer's own — a user who shares a song sees it
in both sections and keeps its control in both.

`CanEdit` is:

- **personal section**: always true, by construction — the query returned the
  caller's own rows.
- **community section**: `admin || song.UserID == viewer`.

**`CanEdit` is presentation only.** Every mutating endpoint re-derives
ownership in its own SQL, so hiding a control is never what stops a non-owner —
`TestOwnSharedSongIsControllableFromTheCommunityList` shows the control
asymmetry and then proves the endpoint refuses regardless.

Status badges (`Private` / `Shared`) render for every song in both sections via
the `share-toggle.html` fragment, which now uses a per-song element id
(`#share-{{.Song.ID}}`) so a list of rows produces no duplicate ids.

## Templates

| File | Role |
|---|---|
| `history.html` | page shell, both sections |
| `songs-section.html` | one section — table, empty state, pager. Used by the page **and** by both fragment endpoints, so the two can never drift |
| `share-toggle.html` | per-song badge + control (updated for per-song ids) |

Fragment pagers swap `#section-personal` / `#section-public` via
`hx-get` + `hx-swap="outerHTML"`, so each tab pages without touching the other.

## Empty states

Both sections render a message rather than a blank panel, and neither shows a
pager when there is nothing to page:

- personal: "No songs in your library yet — describe a sound on the console and
  press Generate." plus a link to `/`.
- community: "Nothing has been shared yet. Publish one of your own songs to
  start the community library."

`TestEmptyStatesRender` covers a brand-new user on the page and on each
fragment, and the mixed case where a user has private songs but nothing is
shared.

## What Stage 06–07 inherit

- `store.PersonalSongs(userID, limit, offset)` and `store.PublicSongs(limit,
  offset)`; both clamp. Do not add an `Access`-taking personal variant.
- `store.Songs(limit, offset, a)` remains the unscoped/admin-lifting list and
  is now unused by the server — it is what an admin dashboard would want.
- `pageParam(r, key)` for any new paged surface; never read a `limit` from a URL.
- `songSection` / `songCard` and `songs-section.html` are reusable for any new
  song list.
- The community card's disclosure set is a decision, not an accident. Adding a
  field to it — especially a username — should be a deliberate change with the
  enumeration argument above reconsidered.
- Stage 07's visual pass can restyle `songs-section.html` freely; the
  `#section-personal` / `#section-public` ids and the `#share-<id>` ids are
  load-bearing for htmx swaps.
