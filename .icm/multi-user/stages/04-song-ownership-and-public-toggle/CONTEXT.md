# Stage 04 — Song Ownership & Public Toggle

> Layer 2 · Stage Contract

## Inputs
| Layer | Path | Purpose |
|-------|------|---------|
| 3 | `../../references/feature-spec.md` | Public sharing and song isolation rules |
| 4 | `../03-access-control/output/access-control-spec.md` | Context-injected user identity |

## Process
1. Update `handleCreateJob` to attach the authenticated user's ID (`user_id`) to newly enqueued jobs.
2. Ensure the background worker persists `user_id` when converting finished jobs into songs.
3. Update `handleDeleteSong` in `internal/server/history.go` to enforce that only the song owner or an administrator can delete a song.
4. Implement `handleToggleSongPublic` endpoint (`POST /songs/{id}/toggle-public`) allowing the owner or admin to toggle `is_public` between 0 and 1.
5. Update audio stream handler (`handleAudio`) to allow playback if the requester is the owner, an admin, or if the song is public.
6. Write integration tests verifying song ownership isolation, unauthorized delete prevention, and public visibility toggle behavior.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Song Ownership Contract | `output/song-ownership-spec.md` | Markdown |
