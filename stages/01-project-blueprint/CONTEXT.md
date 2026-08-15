# Stage 01 — project-blueprint

> Layer 2 · "What do I do?"

**Purpose:** Define every user-facing feature of the MiniMax Music 3 front-end
and the decisions the later stages depend on.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Goal spec | ../../.goals/goal-mm3-icm-scaffold.md | full | verified facts + deliverables |
| Model card | https://huggingface.co/MiniMaxAI/MiniMax-Music3 | features, limits | the feature surface |
| Worker README | https://github.com/sruckh/minmaxmusic3-serverless | request/response | what the UI can call |
| Glossary | ../../_config/glossary.md | all | shared vocabulary |

## Process
1. Enumerate every model capability the UI exposes: lyrics editor with
   section tags, style-caption editor (free text + the AI assistant),
   `audio_duration` (≤ 300 s) and `seed` controls, generate submit, job
   progress, audio playback + download (s3/base64 delivery), song history
   and replay, dark/light theme toggle.
2. Decide and record the **htmx pin**: htmx 4.0 is alpha/beta as of
   2026-08-14 (2.x is npm `latest`). Pick the exact version, justify, note
   swap-in risk. Verify against Context7 before deciding.
3. Fix the page map (home/generate, history, assistant panel placement) and
   the non-goals (no user accounts in v1, no waveform editing).
4. Write the feature list with acceptance-sized bullets — one feature, one
   checkable behavior.

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Feature blueprint + decisions | output/blueprint.md | markdown |

## Checkpoints
- [ ] Human approves the feature list and the htmx pin before stage 03.

## Audits
- [ ] Every feature maps to a model capability that actually exists.
- [ ] htmx pin decision cites current version status, not memory.
