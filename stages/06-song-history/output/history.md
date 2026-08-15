# Song history — stage 06 output

## Storage decision

SQLite (pure-Go `modernc.org/sqlite`, WAL, single writer) per the
blueprint — same driver timbre uses, no CGO, no toolchain in the image.
Tables `jobs`/`songs` with a **unique index on `songs.job_id`** (one song
per job — the idempotency backstop for the worker's finish path).
Regeneration writes a NEW job row reusing the song's exact inputs, so
"same seed" reproduces the request without touching history.

## What was built

- `GET /history` — newest-first table (title, length, seed, engine, date,
  download), 20/page with prev/next links, invitation empty state.
- `GET /songs/{id}` — replay detail: player, metadata line, download,
  "Generate again with this seed" (htmx POST), full lyrics + caption.
- `POST /songs/{id}/regenerate` — rate-limited like any generation;
  re-submits identical lyrics/caption/duration/seed.
- `GET /audio/{id}` — serves the stored WAV (`Content-Disposition`
  filename = song id).

## Persistence

Audio under `MM3_AUDIO_DIR` (default `/data/audio`), DB at
`MM3_DB_PATH` (default `/data/mm3.db`), `/data` is a named volume —
songs survive container replacement. Pruning: not needed at v1 scale
(a 5-minute stereo WAV ≈ 19 MB; 6 jobs/hour/IP bounds growth); revisit
when the volume crosses ~10 GB.

## Evidence

`go test ./...` 11/11, including: history lists the completed song with
title/engine/seed; detail shows lyrics, caption, player, regenerate;
regenerate queues identical inputs (Queued fragment). Persistence across
restart is structural (named volume + WAL) and is re-proven live in
stage 08.
