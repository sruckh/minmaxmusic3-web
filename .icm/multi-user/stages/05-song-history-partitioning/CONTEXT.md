# Stage 05 — Song History Partitioning

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/app-map.md` | History routes and HTMX fragment contracts |
| 4 | `../04-song-ownership-and-public-toggle/output/song-ownership-spec.md` | Scoped song queries and visibility flags |

## Process
1. Implement `PersonalSongs(userID string, limit, offset int)` on `store.Store` returning songs where `user_id = ?`.
2. Implement `PublicSongs(limit, offset int)` on `store.Store` returning all songs where `is_public = 1`.
3. Update `handleHistory` in `internal/server/history.go` to support dual-section viewing: Personal Songs ("My Songs") and Public Songs ("Community Songs").
4. Create HTMX endpoints `/history/personal` and `/history/public` to allow independent tab switching and pagination.
5. Add public toggle controls and status badges directly on song cards in the history views.
6. Write unit and handler tests ensuring personal songs are never leaked to other users unless marked public.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Partitioned History Contract | `output/history-partition-spec.md` | Markdown |
