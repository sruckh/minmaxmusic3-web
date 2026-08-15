# Blueprint — minmaxmusic3-web (stage 01 output)

Single source of truth for what is being built. Every later stage references
this. Produced under the gauntlet (bar: timbre's project brief).

## 1. What it is

A web front-end for AI music generation (**MM3** / MiniMax Music 3). A visitor describes a song — or asks
the built-in AI assistant to draft it — reviews the tagged lyrics and style
caption, and generates a complete song via the **MiniMax Music 3** model on a
RunPod serverless endpoint. The app owns assistant proxying, an async job
queue, submission, polling, audio storage, playback, download, and a
persisted song library. Public at `https://mm3.gemneye.xyz` (Cloudflare → NPM
→ shared_net, zero host ports). No accounts in v1 — the site is open but
rate-limited (§3.8).

## 2. Stack (pins decided)

Go · html/template · **htmx 4.0.0-beta4** (timbre's proven pin; ESM build,
vendored locally, not CDN) · **Alpine.js 3.14.8** (vendored) · **Tailwind CSS
v4** (`@tailwindcss/cli@4` in the Docker build stage) · SQLite (pure-Go
`modernc.org/sqlite`, no CGO) · Docker (alpine secretbase + Infisical
entrypoint, env **dev**).

## 2.1 External dependencies (pinned)

- **Inference**: RunPod serverless endpoint running the worker at
  github.com/sruckh/minmaxmusic3-serverless, model `MiniMaxAI/MiniMax-Music3`
  (32 kHz 16-bit stereo WAV out). Endpoint URL + API key are secrets —
  `RUNPOD_ENDPOINT` / `RUNPOD_API_KEY` from Infisical project
  `mini-max-music3-z96r`, env **dev** — never literal in this repo. The
  request/response schema (async `/run` + `/status`, delivery modes, caps)
  is pinned canonically in `shared/runpod-worker-api.md`.
- **Assistant LLM**: OpenAI-compatible chat completions; base URL, key, and
  model id from Infisical (`LLM_BASE_URL` / `LLM_API_KEY` / `LLM_MODEL_ID`,
  env dev), system prompt `shared/llm-assistant-system-prompt.md`.
- Both are the only external runtime dependencies; everything else is local.

## 3. Functional requirements (each = pass/fail)

1. **Generation form** — vertical stacked accordion layout:
   - **Lyrics & Style**: lyrics textarea with section-tag inserters (`[Intro] [Verse] … [Outro]`), style caption textarea.
   - **Configuration**: `audio_duration` slider & centered step input 15–300 s (up to 5 minutes max); BPM, Key, Model, Seed, Vocals toggle.
   - **Master Out & Generate**: Peak meter, Generate button, status readout & timer.
2. **Validation** — tag-on-own-line check with one-click fix; duration
   clamped 15 ≤ duration ≤ 300 s; caption length advisory (5,000-token model cap).
3. **AI assistant** — rough idea → `POST /assistant` → server-side LLM call
   (OpenAI-compatible; base URL/key/model from Infisical `LLM_*`; 60 s
   timeout, response capped ~2,000 tokens) → parses
   `{input, instructions, audio_duration, seed}` → prefills the form. Never
   auto-submits. Site fully usable without it.
4. **Async generation** — submit returns in < 1 s with a job id; a background
   worker calls RunPod **async** `POST /run`, polls `GET /status/{id}`, and
   writes the result. The browser polls our job fragment via htmx. **No
   synchronous `/runsync` in the request path** — Cloudflare caps proxied
   requests at ~100 s and a 300 s generation would 524. `/runsync` is
   reserved for offline smoke tests.
5. **Result** — inline `<audio controls>` player, download button, metadata
   (duration, seed, engine, delivery); s3 audio fetched and stored locally
   before the presigned URL expires; base64 delivery decoded and stored.
6. **Errors** — the stage-02 taxonomy inlined here: form-validation (400,
   field-level), rate-limited (429, when resets), worker-failed (job page
   shows reason + retry button), assistant-timeout (panel message, form
   still usable). No raw API text to the browser.
7. **Song library** — `/history` newest-first, htmx pagination, replay,
   download, regenerate-with-same-seed. Songs survive restart (named volume).
8. **Abuse limits** — per-IP token bucket: ≤ 6 generations/hour, ≤ 20
   assistant calls/day; ≤ 2 jobs in flight globally; over-limit → 429 page.
9. **Themes** — dark/light toggle, `prefers-color-scheme` default,
   localStorage persistence; every page in both themes.
10. **Favicons & Header** — single topbar header with MM3 icon logo and brand mark.

## 4. Page map

`/` generate (single topbar header + assistant + vertical accordion rack + job status + result) · `/history` library +
replay · `/jobs/{id}` job status fragment (htmx poll target) · `/healthz`.

## 5. Data model (SQLite)

- `jobs(id TEXT PK, state TEXT queued|running|succeeded|failed, runpod_id,
  error, created_at, updated_at)`
- `songs(id TEXT PK, job_id, lyrics, caption, duration_s REAL, seed INT,
  engine, delivery, audio_path, title, created_at)`
- No users table in v1.

## 6. Definition of done

Every requirement above has a check: form/validation via handler tests;
assistant + generation via stub-server end-to-end tests (idea → prefill →
job → playable file); limits via 429 tests; persistence via restart test.
`go test ./...`, `go vet ./...`, `gofmt -l .` clean; `icm audit` exit 0;
secret-leak grep clean; `docker inspect` shows shared_net + zero published
ports; both themes screenshot-checked against the reference pages.

## 7. Non-goals (v1)

No auth/user accounts, no per-user isolation, no waveform editing, no
section-level regeneration, no streaming playback, no payment anything.

## 8. Hard invariants

Zero published host ports (`shared_net` only, NPM sole ingress) · no secret
literal in repo/image/compose (Infisical entrypoint injection, env dev) ·
`DESIGN.md` palette & MM3 branding only · design assets never modified
(`sha256sum -c .goals/baseline.sha256`).
