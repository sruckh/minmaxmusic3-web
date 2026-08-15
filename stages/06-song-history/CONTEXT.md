# Stage 06 — song-history

> Layer 2 · "What do I do?"

**Purpose:** Persist every generated song and make the library browsable and
replayable.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Feature notes | ../05-generation-and-assistant/output/generation.md | result shape | what to persist |
| API contracts | ../02-api-contracts/output/api-contracts.md | response schema | metadata fields |
| Design system | ../03-design-system/output/design-system.md | tables, cards | list UI |

## Process
1. Storage choice (SQLite vs JSON-on-disk) — decide and record; single
   table `songs`: id, created_at, lyrics, caption, duration, seed, engine,
   delivery, audio_url, local_path.
2. On successful generation: save metadata + audio (downloaded blob or the
   presigned URL + fetch time) before showing the result.
3. `GET /history`: server-rendered table (htmx pagination), newest first —
   title/idea, date, duration, seed, engine, play/download actions.
4. Replay: click-through detail view with full lyrics + caption and the
   audio player; "regenerate with same seed" action.
5. Pruning rule for local audio (size cap + eviction order), documented.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| History feature | ../../internal/store/, ../../web/templates/ | Go + templates |
| Storage notes | output/history.md | markdown |

## Audits
- [ ] Songs survive a container restart (volume, not layer).
- [ ] Regenerate-with-same-seed reproduces the request exactly.
