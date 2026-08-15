# Stage 02 — api-contracts

> Layer 2 · "What do I do?"

**Purpose:** Pin the exact request/response contracts for the two upstream
APIs the Go server calls: the RunPod worker and the LLM chat endpoint.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Blueprint | ../01-project-blueprint/output/blueprint.md | full | features to wire |
| Worker API | ../../shared/runpod-worker-api.md | full | canonical RunPod contract |
| Assistant prompt | ../../shared/llm-assistant-system-prompt.md | Step 4 | assistant output shape |
| Goal spec | ../../.goals/goal-mm3-icm-scaffold.md | Infisical table | credentials in play |

## Process
1. RunPod worker — **async primary** (Cloudflare ~100 s proxy cap forbids
   sync in the request path): `POST {RUNPOD_ENDPOINT}/run` → `{id, status}`;
   background worker polls `GET /status/{id}` (3 s → 10 s backoff) until
   COMPLETED/FAILED; `output` branches on `delivery`: `s3` (fetch + store
   before URL expiry) or `base64` (decode + store). `/runsync` = smoke
   tests only. See `../../shared/runpod-worker-api.md`.
2. Define the Go-side error taxonomy: validation errors, worker 4xx/5xx,
   job FAILED, poll timeout (give up after e.g. 15 min), s3 URL expiry,
   base64 oversize (18 MB cap ⇒ prefer s3).
3. Job-state machine: queued → submitted → running → succeeded | failed |
   cancelled; transitions persisted in SQLite (blueprint §5).
4. LLM assistant: OpenAI-compatible chat completions at `LLM_BASE_URL`
   with `LLM_API_KEY`, model `LLM_MODEL_ID`; the Go server **proxies** so
   the key never reaches the browser. Parse the assistant reply into
   `input` / `instructions` / `audio_duration` / `seed`.
5. Write both contracts as request/response schemas with field tables and
   error enums.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| API contract spec | output/api-contracts.md | markdown |

## Audits
- [ ] Both contracts round-trip against a stub server (schema-valid fixtures).
- [ ] No secret value appears in the spec — names only.
