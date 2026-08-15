# Generation + assistant — stage 05 output

## What was built

- `internal/store` — SQLite (pure-Go `modernc.org/sqlite`, WAL, single
  writer): `jobs` + `songs` tables, compare-and-set `TransitionJob`
  (terminal states never transition), `FailJob`, retry counter, paged
  library queries.
- `internal/runpod` — async client: `Submit` (POST /run), `Status`
  (GET /status/{id}), `OutputOf`, best-effort `Cancel`; error taxonomy with
  `IsPermanent` (400/401/403/404 + decode + missing-config permanent;
  429/5xx/network transient), 2 KiB error cap.
- `internal/llm` — assistant proxy: OpenAI-compatible chat, 60 s timeout,
  one network retry, `max_tokens` 2000, temp 0.8, idea capped 4,000 chars;
  `ParseDraft` takes the LAST fenced JSON block, validates non-empty
  fields, clamps duration 10–300.
- `internal/worker` — the only RunPod caller: 2 s tick drains queued
  (max-in-flight from active-count budget; queued→submitting is claimed
  before POST /run; restart fails ambiguous submissions rather than risking
  duplicate billing; returned ids are durably recorded/cancelled on CAS
  failure), polls active
  (3 s→10 s backoff, 15 min budget → best-effort cancel → failed(timeout)),
  every status maps to exactly one transition, transient×3 → failed,
  restart recovery via failExpired. Audio: s3 fetch (64 MiB cap) or base64
  decode → `<audioDir>/<song-id>.wav` + songs row.
- `internal/server` — handlers: `POST /assistant` (JSON draft; error map
  502/503/504), `POST /jobs` (validate → enqueue → queued fragment in <1 s;
  **no RunPod in the request path**), `GET /jobs/{id}` (poll fragment;
  terminal swap stops polling by replacing the trigger element),
  `GET /audio/{id}` (serves stored WAV). Per-IP fixed-window limiters:
  6 gens/hour, 20 assistant/day, 429 + Retry-After.
- `web/templates` — index (assistant panel, tag-inserter buttons, form with
  Alpine state, disabled-until-valid submit), job fragment (spinner/badge/
  player), error fragment. Theme-before-paint + toggle in layout.

## Validation rules enforced (blueprint §3.1–3.2)

Tags-alone-on-line check with one-click fix path; duration 10–300; caption
hard cap far above the 5,000-token advisory; field-level next-step copy.

## Evidence

`go test ./...` — 8/8 pass, including TestEndToEnd (stub RunPod + stub
LLM): valid submit → worker submits once → COMPLETED → base64 stored →
player fragment → `GET /audio/{id}` 200. Rate-limit tests prove both
limiters trip exactly at their budgets. Assistant tests cover round-trip,
unparseable-502, and the LLM-call count.

## Deviations from contract

None material. `songForJob` scans rather than a job_id index (v1 scale);
index can be added in stage 06 if pagination needs it.
