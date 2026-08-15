# Goal — Scaffold the ICM workspace for minmaxmusic3-web

Build out the ICM (Interpretable Context Methodology) folder structure that will
drive the implementation of **minmaxmusic3-web**: a web front-end for the
MiniMax Music 3 model, calling the RunPod serverless worker for inference.

**This goal produces the workspace spec (folders, stage contracts, config) —
NOT the application code.** Implementation happens later, one stage at a time,
via `icm run` (each of those can be its own /goal run).

## Facts you start from (already verified — do not re-derive)

- Working dir: `/opt/docker/minmaxmusic3-web` (not a git repo). Current
  contents: `DESIGN.md`, `index-dark.html`, `index-light.html`,
  `favicon_io.zip`, `AGENTS.md`/`CLAUDE.md` (Outline-managed), `.outline/`,
  `.goals/`.
- The `icm` skill is installed: `/root/.claude/skills/icm/scripts/{new,stage,run,audit,sync}`
  (Python 3 stdlib, extensionless executables). `icm new <dir>` scaffolds a
  five-layer workspace; `audit <dir>` validates it and exits 0 on conformance.
- Audit rules (what "valid" means mechanically): Layer 0 needs `IDENTITY.md`
  OR `CLAUDE.md`; root `CONTEXT.md` required; every `CONTEXT.md` ≤ 80 lines;
  every file under `references/` and `_config/` ≤ 200 lines; every
  `stages/NN-*/CONTEXT.md` must contain `## Inputs`, `## Process`,
  `## Outputs`; stage references must point only to LOWER-numbered stages.
- The MiniMax Music 3 model surface (from the HF model card): lyrics with
  section tags (`[Intro] [Verse] [Pre-Chorus] [Chorus] [Post-Chorus]
  [Bridge] [Instrumental] [Solo] [Outro]`), music description as a
  Structured Caption (**Global Metadata / Vocal Details / Arrangement**),
  `audio_duration` (songs up to 5 min; hard caps: 5,000-token prompt,
  9,000 acoustic frames), `seed`, output 32 kHz 16-bit stereo WAV.
- The RunPod worker API (github.com/sruckh/minmaxmusic3-serverless):
  `POST /runsync` with `{"input": {"input": <lyrics>, "instructions":
  <description>, "audio_duration": <sec>, "seed": <int>}}`; response branches
  on `output.delivery` = `s3` (presigned `audio_url`, expires) or inline
  base64; engines `diffusers` / `sglang-omni`; env-configured S3/Backblaze
  delivery. RunPod caps `/runsync` responses at 20 MB.
- Deployment targets: dockerized container, **no ports published to the
  host**, attached to the existing docker network `shared_net` (verified
  present), behind Nginx Proxy Manager (also on `shared_net`), public URL
  `mm3.gemneye.xyz`.
- Stack: **Go** (server + html/template), **htmx 4.x**, **Alpine.js**,
  **Tailwind CSS v4**. Note: as of Aug 2026 htmx 4.0 ("The Fetchening") is
  in alpha/beta — htmx 2.x is still the `latest` stable. Pin the exact
  version the stage contracts will use and record that decision in the
  blueprint stage's output.
- Design system: `DESIGN.md` defines the 7-color palette and both themes
  (dark canvas `#5E1226`, light canvas `#E5DCDF`, etc.);
  `index-dark.html` / `index-light.html` are working theme examples;
  `favicon_io.zip` contains 7 favicon files (favicon.ico, PNGs,
  site.webmanifest) to be extracted into static assets during implementation.
- Secrets/config (host convention, verified in Outline — see the dub ADR
  "All configuration in Infisical, no .env file" and the shlink
  "Infisical machine identity" task): **everything lives in Infisical**, no
  `.env` file ever, no secret literal in any file under this repo or in any
  image layer. This project's Infisical parameters:
  | Parameter | Value |
  |-----------|-------|
  | Host | `https://secrets.gemneye.xyz` |
  | Project ID | `bd1ff4df-ddf9-4037-a183-4d7a92b232f4` |
  | Project slug | `mini-max-music3-z96r` |
  | Environment | `dev` |
  | Client ID (machine identity, Universal Auth) | `e71fea5c-499d-4b9d-a91c-086ca9abcfc0` |
  | Client secret | NOT in this file — seeded once via the seed script (below) |
  Five variables are already configured in the project, environment `dev`:
  `LLM_API_KEY`, `LLM_BASE_URL`, `LLM_MODEL_ID`, `RUNPOD_API_KEY`,
  `RUNPOD_ENDPOINT`. The three `LLM_*` variables drive the in-app **AI
  assistant** (below) — an OpenAI-compatible chat endpoint + model id.
- Injection pattern to follow (shlink precedent, 2026-08-13): project-owned
  gpg keyring at `~/.config/mm3-web-infisical/gnupg` (uid `mm3-web-infisical`,
  rsa3072, no passphrase) — separate from every other credential store on
  this host; a seed script that gpg-encrypts the client secret into it; a
  bring-up path: gpg decrypt → `infisical login --method=universal-auth` →
  `infisical export --format=dotenv-export` → shell env → docker compose
  interpolation. The Infisical CLI runs from the `infisical/cli:latest`
  container (no host install), secrets passed to it via `-e NAME` only, so
  they never appear in host process args. Note the dub ADR's open concern:
  env vars land in `docker inspect` output — the containerization stage
  contract must decide (and record) runtime delivery: compose env
  interpolation vs mounted docker secret at `/run/secrets`.

## Deliverable

An ICM workspace at `/opt/docker/minmaxmusic3-web` containing:

1. `IDENTITY.md` (Layer 0 — project identity; do NOT overwrite the existing
   `CLAUDE.md`, which is Outline-managed).
2. Root `CONTEXT.md` (Layer 1 — routing table to all stages, ≤ 80 lines).
3. `stages/` with at least these numbered contracts (naming may differ, the
   coverage must not):
   - `01-` project blueprint / requirements — every user-facing feature of
     the model surfaced in the UI (lyrics editor with section tags,
     Structured Caption builder or free-text instructions, duration, seed,
     generation submit, job progress, audio playback + download, song
     history/replay, **AI assistant for writing lyrics + style captions**,
     dark/light theme toggle), plus the htmx 4 pin decision.
   - `02-` API contracts — RunPod worker (request/response schemas, delivery
     modes, polling/timeout strategy, error taxonomy) AND the LLM assistant
     endpoint (chat completions via `LLM_BASE_URL` + `LLM_API_KEY` +
     `LLM_MODEL_ID`, server-side proxy so the API key never reaches the
     browser, response parsed into the `input` / `instructions` form fields).
   - `03-` design system — Tailwind v4 tokens derived from `DESIGN.md`,
     dark/light theming, favicons from `favicon_io.zip`.
   - `04-` Go application foundation — server layout, templates, htmx +
     Alpine.js integration.
   - `05-` song generation feature — the full create→poll→play flow,
     including the AI assistant panel: rough idea in → assistant returns
     tagged `lyrics` + three-heading `instructions` caption (per the system
     prompt below) → fields populate the generation form for human review
     before submit.
   - `06-` song history/library — persistence and replay.
   - `07-` containerization & deployment — Dockerfile, `shared_net`, no
     host port bindings, NPM proxy host `mm3.gemneye.xyz`, **Infisical
     runtime config**: how the four variables (`LLM_API_KEY`, `LLM_BASE_URL`,
     `RUNPOD_API_KEY`, `RUNPOD_ENDPOINT`) reach the container at startup
     (compose env interpolation vs mounted docker secret — decide and record,
     citing the dub ADR's `docker inspect` concern), the bring-up flow
     (gpg decrypt → infisical login → export → compose), and the no-secrets-
     on-disk invariant.
   - `08-` acceptance — verification checklist + acceptance report.
   Each stage `CONTEXT.md` has `## Inputs` / `## Process` / `## Outputs`
   (Inputs as a table naming exact files; Outputs name artifact + location).
   Creative stages (01, 03) include a human checkpoint.
4. `_config/` with the design tokens (palette, typography, component
   patterns) distilled from `DESIGN.md` — each file ≤ 200 lines.
5. `shared/` with the RunPod worker API reference doc used by stages 02+,
   AND `shared/llm-assistant-system-prompt.md` — the AI assistant's system
   prompt, adapted from the draft at
   `.goals/llm-assistant-system-prompt.md` (which is already tailored to this
   app's field names: `input` / `instructions` / `audio_duration` / `seed`).
   Copy/adapt it; keep the tag-on-own-line rule, the three-heading caption
   (Global Metadata / Vocal Details / Arrangement, 250–450 words), the
   18 style families, the pre-flight checklist, and the exact four-field
   JSON payload shape. Mind the ≤200-line budget for `shared/` files — split
   into `shared/llm-assistant/` files if needed.
6. `setup/` with a one-time onboarding questionnaire if appropriate.
7. **Seed script (explicit deliverable exception — tooling, not app code):**
   `scripts/seed-infisical-secret.sh` at the repo root. It must:
   - accept the Infisical client secret from an argument, `--file`, or stdin
     (never echo it);
   - on first run, create the project-owned gpg keyring
     `~/.config/mm3-web-infisical/gnupg` (uid `mm3-web-infisical`, rsa3072,
     no passphrase, `--homedir` scoped to that directory) if absent;
   - write `~/.config/mm3-web-infisical/client_secret.gpg` (mode 0600) and a
     non-secret `~/.config/mm3-web-infisical/infisical.env` (mode 0600)
     containing `INFISICAL_HOST`, `INFISICAL_CLIENT_ID`,
     `INFISICAL_PROJECT_ID`, `INFISICAL_ENV=dev`;
   - support `--verify` to decrypt and check the round-trip without printing
     the secret (print only a fingerprint/sha256 prefix);
   - be idempotent (re-running re-seeds the ciphertext).
   `bash -n scripts/seed-infisical-secret.sh` must pass, and a dry-run that
   exercises keyring creation + encrypt/decrypt round-trip with a throwaway
   test value must succeed **without contacting Infisical** (no network in
   the test; the real secret is seeded by the operator afterwards).

## Success criteria (all must hold)

1. `python3 /root/.claude/skills/icm/scripts/audit /opt/docker/minmaxmusic3-web`
   exits 0 and prints `icm audit: OK`.
2. `ls /opt/docker/minmaxmusic3-web/stages/` shows ≥ 8 zero-padded numbered
   stage directories, in the coverage order above.
3. Coverage greps — each of these strings is found somewhere under `stages/`,
   `shared/`, or `setup/` (case-insensitive):
   `audio_duration`, `seed`, `[verse]` (section tags), `structured caption`
   (or `vocal details`), `runsync`, `delivery`, `base64`, `alpine`, `htmx`,
   `tailwind`, `shared_net`, `mm3.gemneye.xyz`, `favicon`, `#5E1226`,
   `infisical`, `llm_api_key`, `llm_model_id`, `runpod_api_key`,
   `runpod_endpoint`, `universal-auth`, `mini-max-music3-z96r`,
   `assistant` (the LLM lyric/caption helper). Also
   `shared/llm-assistant-system-prompt.md` (or equivalent under
   `shared/llm-assistant/`) exists and contains `Global Metadata`,
   `Vocal Details`, `Arrangement`, `[Verse]`, and `audio_duration`.
4. Seed script: `scripts/seed-infisical-secret.sh` exists, `bash -n` passes,
   and its offline dry-run (throwaway value, keyring + encrypt/decrypt
   round-trip, no network) exits 0. Scratch from the dry-run goes in
   `.goals/`.
5. No secret leakage: `grep -rniE '(client_secret *= *["'"'"'][^"'\"']+|sk-[a-z0-9]{20,})'`
   over the repo (excluding `.goals/` and `~/.config`) finds nothing; the
   real client secret appears nowhere in the repo.
6. Baseline assets unmodified:
   `sha256sum -c .goals/baseline.sha256` exits 0 (DESIGN.md,
   index-dark.html, index-light.html, favicon_io.zip untouched). The
   existing `CLAUDE.md`/`AGENTS.md`/`.outline/` are also untouched.
7. No implementation code written: no `*.go` files, no `Dockerfile`, no
   `go.mod`, no `package.json` outside `.goals/` (the seed script is the
   one sanctioned exception).

## Working rules for the run

- Write ALL scratch files, notes, and drafts to `.goals/` — never the repo
  root. The workspace files listed under "Deliverable" are the product, not
  scratch.
- Use the `icm` skill scripts (`new`, `stage`, `audit`) rather than
  hand-rolling the structure; audit after every stage addition.
- Confirm htmx 4 / Tailwind v4 / Alpine.js API details against Context7 docs
  before writing them into contracts — do not trust memory for versions.
- Keep every `CONTEXT.md` ≤ 80 lines and every `_config/`/`references/`
  file ≤ 200 lines (audit enforces this; design for it, don't fight it).
- Never write a secret literal anywhere in the repo — the seed script takes
  the secret from the operator; the goal run only proves it works with a
  throwaway value. The Infisical client ID and project ID are not secrets
  and may appear in contracts/docs.

## Turn cap & failure path

Stop after 8 tries. If still failing, stop, leave the workspace as-is (do
NOT delete it), and report: which audit violation or coverage grep is still
failing, and what was tried.
