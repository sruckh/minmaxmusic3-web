# Project Brief — MossTTS-v1.5 Web ("Timbre")

Single source of truth for what is being built. Every `.goals/goal-*.md` references this. **Nothing is implemented in this skill run** — this file + the goals are the deliverable.

---

## 1. What it is

A web front-end for text-to-speech. A signed-in user pastes a script, picks (or clones) a voice, queues one or more render jobs, and downloads the resulting WAV files. Inference runs on a **RunPod serverless** endpoint (`https://api.runpod.ai/v2/10qdttbr4x3l9g`); the web app owns the job queue, submission, polling, audio storage, and download/delete.

## 2. Stack

Go · **templ** · **HTMX v4** · **Alpine.js** · **Tailwind v4** · **SQLite** (pure-Go `modernc.org/sqlite`) · **LiteStream** (replication sidecar) · Docker. See `.goals/recon.md §3` for the rationale (esp. pure-Go SQLite = no CGO/toolchain in the image).

## 3. Functional requirements

1. **Auth** — gate the entire UI behind login. Bootstrap a single admin user (bcrypt-hashed, stored in SQLite). Session cookie, auth middleware on every route except login + static assets.
2. **Reference audio upload (one-shot cloning)** — the user supplies a reference sample as a **file** (drag/drop or file picker, never a URL); authenticated upload stores the blob + metadata in SQLite (or on a volume). The reference is delivered to RunPod as **base64 inline** in the submission payload (confirmed working from testing) — it is **never served at a public URL**.
3. **Voice library** — list stock models and uploaded (cloned) references; select one per render. (UI from DESIGN.md "Voice library".)
4. **Job queue** — user can enqueue multiple jobs; each is a row in SQLite with state `queued`. A **background worker** dequeues and submits to RunPod.
5. **Submission** — worker POSTs `{input:{text, language, reference_audio_base64?, stream:false}}` to `/run` (async); for a cloned voice, the stored reference bytes are base64-encoded inline (exact field name confirmed against the handler). Stores the returned RunPod `id`, moves state → `submitted`/`in_progress`.
6. **Polling** — a **background poller** hits `/status/{id}` until `COMPLETED`/`FAILED` (and/or consumes `/stream/{id}`). The **browser** only polls the Go app (HTMX, ~2s) — never RunPod — so the Cloudflare 90s cap is never in play.
7. **Audio capture** — on `COMPLETED`, decode `output.audio_base64`, write a WAV to storage, associate the path with the job, set state → `ready`.
8. **Download** — authenticated route to download a ready job's WAV to the user's device.
9. **Delete** — delete a queued job (cancels/not-submits) **or** a completed job; deleting a completed job **also removes the saved audio file**.
10. **Concurrency** — as jobs complete, queued jobs are submitted (worker drains the queue; configurable max in-flight).

## 4. Non-functional / infrastructure requirements

- **Docker**, on network `shared_net`, **no host ports**. Behind **NGINX Proxy Manager** (same network) → **Cloudflare** (TLS). Host `cloudflared` unused.
- **No host toolchain.** All build/generate/test runs in Docker. No `go`/`templ`/`npm`/`npx`/`sqlite3` on the host.
- **Cloudflare 90s** — no request blocks longer; the async architecture in `recon.md §1` is mandatory.
- **Secrets** — `RUNPOD_API_KEY` injected from **Infisical** at runtime (mechanism per Outline docs; see `recon.md §5`).
- **Durability** — SQLite replicated via **LiteStream** sidecar.
- **Design fidelity** — UI built strictly from DESIGN.md + `index.html`; the 10-color palette is exhaustive (deterministic CSS grep check). WCAG AA, focus rings, reduced-motion all honored.

## 5. Data model (sketch — finalize in Goal 0/1)

- `users(id, username, password_hash, created_at)`
- `voices(id, kind [stock|cloned], name, model, license_label, reference_path, created_at)` — `reference_path` points at the stored reference bytes (BLOB or volume file); there is no public URL — references are base64-encoded inline at submit time.
- `jobs(id, user_id, voice_id, text, language, params_json, status [queued|submitted|in_progress|ready|failed], runpod_id, audio_path, format, sample_rate, delay_ms, exec_ms, error, created_at, updated_at)`

## 6. Definition of done (project-level)

- `docker compose build` and `docker compose up -d` succeed; container joins `shared_net`, answers on NPM, no host ports.
- Auth blocks all UI routes until login; correct creds work, wrong rejected.
- Upload → reference bytes stored and base64-deliverable to RunPod inline (no public URL).
- Enqueue → worker submits to RunPod → poller marks ready → WAV saved and downloadable → delete removes row + file.
- Queue drains as jobs complete; multiple jobs supported.
- UI matches DESIGN.md; compiled assets contain no color outside the 10 palette hexes.
- `docker compose run --rm app go test ./...` exits 0.
- `RUNPOD_API_KEY` present in-container (verified without leaking the value).

## 7. Explicit non-goals (this skill run)

- No implementation code is written now. Only `.goals/` artifacts are produced.
- No Outline plan is written now — that is **Goal 0**'s job when the user runs it.
- Streaming playback UI is secondary; non-streaming (`stream:false`) capture is the required path.
