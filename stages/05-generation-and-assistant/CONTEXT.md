# Stage 05 — generation-and-assistant

> Layer 2 · "What do I do?"

**Purpose:** Build the core flow — generate a song from the form, watch it
progress, play/download it — plus the AI assistant that drafts the form's
contents from a rough idea.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| API contracts | ../02-api-contracts/output/api-contracts.md | full | both upstream calls |
| Go skeleton | ../04-go-foundation/output/go-foundation.md | routes, layout | where code lands |
| Assistant prompt | ../../shared/llm-assistant-system-prompt.md | full | system prompt to install |
| Glossary | ../../_config/glossary.md | input/instructions | field semantics |

## Process
1. Generation form: lyrics textarea (`input`) with tag helper, caption
   textarea (`instructions`), `audio_duration` slider (10–300 s), `seed`
   input, submit via htmx (`hx-post`).
2. Assistant panel: rough-idea textarea → `POST /assistant` (Go proxies
   `LLM_BASE_URL`, model `LLM_MODEL_ID`, key server-side only) → parse the
   reply into the four fields → prefill the form for **human review**
   before submit (per the system prompt's Step 4 shape).
3. Submit: Go validates (tags on own lines, duration ≤ 300, non-empty
   caption), enqueues a job row (blueprint §5) — returns in < 1 s with the
   job id. A background worker submits async `POST /run` and polls
   `/status/{id}` (per stage 02); on success it fetches s3 audio (or decodes
   base64) into local storage and writes the `songs` row.
4. Progress + result: the browser htmx-polls `/jobs/{id}` — queued/running
   chip with elapsed time, then inline audio player (`<audio controls>`)
   + download button + metadata line (duration, seed, engine, delivery).
5. Errors map to the stage-02 taxonomy with next-step copy (voice rules).

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Feature code | ../../internal/, ../../web/templates/ | Go + templates |
| Feature notes | output/generation.md | markdown |

## Audits
- [ ] End-to-end against a stub worker + stub LLM: idea → assistant → form
      → submit → playable audio.
- [ ] Assistant output never auto-submits; the user confirms.
- [ ] `LLM_API_KEY` appears in no template, script, or response body.
