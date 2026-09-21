<p align="center">
  <img src="assets/readme/hero.svg" alt="MiniMax Music 3 Web Interface Hero Banner" width="100%">
</p>

<h1 align="center">MiniMax Music 3 Web Interface</h1>

<p align="center">
  <strong>A self-hosted web console for text-to-song inference.</strong><br>
  Turn text ideas, tagged lyrics, and style captions into full stereo M4A audio tracks (192 kbps AAC) —
  on <strong>MiniMax Music 3</strong>, or on <strong>YuE2</strong>, which plans the song as an editable
  score before it writes any audio.
</p>

<p align="center">
  <a href="#overview">Overview</a> •
  <a href="#features">Features</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#authentication--administration">Authentication</a> •
  <a href="#api-reference">API Reference</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#development">Development</a>
</p>

---

## Overview

**minmaxmusic3-web** is a self-hosted web application for generating music from text. Users can describe a sound, draft structured lyrics, set style parameters, and generate high-quality audio files.

It drives **two inference engines**, and the model is a property of each song rather than of the deployment:

| Engine | Modes | What it is |
|---|---|---|
| **MiniMax Music 3** | create | Sings tagged lyrics at a length you choose, from a structured style caption. |
| **YuE2** | create, cover, edit | Plans the song as an ABC score first, then realizes it as audio — so the score can be re-rendered with a different arrangement or tempo, and an existing recording can be re-styled. |

The engine is chosen on the generate form and recorded with the job. It decides what the rest of the form offers, because the two do not accept the same section tags, the same parameters, or the same shape of style. Each engine also has **its own AI assistant**: the prompts ask for different documents — a JSON block for MiniMax, labelled plain text for YuE2 — and the reply is parsed by the engine that produced it.

YuE2 is **offered only when its endpoint is configured**. With `YUE2_RUNPOD_ENDPOINT` unset, the selector shows MiniMax alone and the rest of the application is unchanged.

The application is **multi-user and closed by default**: every route except sign-in, registration, the signed-cover route, and the health check requires an approved account, and every song is private to the account that generated it until its owner explicitly shares it. See [Authentication & Administration](#authentication--administration) before deploying — an instance with no administrator credentials configured starts and reports healthy, but nobody can ever be approved.

---

## Features

### 🎵 Text-to-Song Music Generation
- Generate full-length music tracks (up to 300 seconds) from text lyrics and style captions.
- Outputs high-quality **stereo M4A (192 kbps AAC)** audio files.

### 🎛 Two Engines, Chosen Per Song
- **MiniMax Music 3** — tagged lyrics and a structured style caption, at a length you pick.
- **YuE2** — style and lyrics in, with the model planning an ABC score before any audio. It sets its own length, so the duration control is not offered.
- The engine is the **first choice on the form**, because it governs everything below it: the section-tag vocabulary, the shape of the style field, and which configuration options appear. MiniMax's tags include `Post-Chorus` and `Solo`; YuE2's prompt names a narrower set that includes `Interlude`. Showing one list for both would teach a model tags it has no concept of.
- An **Instrumental** toggle replaces inferring it from an empty lyric box — an empty box cannot distinguish "no words" from "not typed yet", and the worker treats those differently.
- A song **records which engine made it**, and the History list shows it.

### ✂️ Edit — Re-render a Song From Its Score *(YuE2)*
- A YuE2 song carries the score the model planned. Editing re-renders from that score with a new arrangement, tempo or words.
- **Only tempo is a genuine performance change.** Key does not re-key anything — ABC note tokens are relative, so changing `K:` respells the same letters rather than transposing — and meter makes the bars the wrong length, which nothing validates, because the worker checks score *format* only and defers measure arithmetic to a tokenizer it cannot run without a GPU.
- So there is **no raw-score editor**: tempo is editable, key and meter are read-only, and the score travels from the stored copy rather than from a text box.
- Editing re-renders the whole song. The waveform outside the change is not preserved, and the form says so.

### 🎨 Cover — Re-style a Recording *(YuE2)*
- Supply a recording three ways: a song already in the library, an upload, or a pasted URL. All three become one thing — a URL the worker can fetch.
- YuE2 transcribes the melody and the lyrics, then generates a new version. Supplying your own lyrics is **optional** and takes precedence over the transcription.
- The recording reaches RunPod through a **short-lived signed link this app mints**, not through shared object storage: 32 random bytes, stored only as a SHA-256 hash, single-purpose, and expiring in two hours. That TTL has to outlive the queue budget, because the worker does not fetch it until a GPU picks the job up.

### 🪄 AI Songwriting & Style Assistant
- Integrated AI assistant (`POST /assistant`) that drafts the generate form's contents from a rough idea. It **prefills for review** and never submits.
- **One assistant per engine.** The prompts ask for different documents and the replies are parsed by the engine that produced them:
  - **MiniMax** — `shared/llm-assistant-system-prompt.md`, answered as a fenced JSON block, parsed by `llm.ParseDraft`.
  - **YuE2** — `shared/llm-assistant-system-prompt-yue2.md`, answered as labelled plain text (`STYLE:` / `LYRICS:` / `COT:` / `NOTES:`), parsed by `llm.ParseYue2Draft`. Its `NOTES` are shown to the user as the assumptions behind the draft.
- **Thinking disabled by default**: Sends `thinking: {"type": "disabled"}` and `reasoning_effort: "none"` so reasoning models (e.g. `deepseek-v4-flash`) skip internal thinking delays — without these they can take well over a minute to reply. The call itself is still budgeted at a 120-second timeout.
- **Resilient parsing**: both parsers strip `<think>`/`<reasoning>` blocks and fold server-sent-event replies into a single message. The JSON parser then accepts closed code fences, unclosed code fences, or a raw object anywhere in the reply; the labelled parser slices between its labels, tolerates fences the prompt forbids, and normalises an unusable `COT` rather than discarding the whole draft.

### ⚡ RunPod Serverless GPU Inference
- Asynchronous worker queue, **one client per configured engine**. MiniMax runs on [sruckh/minmaxmusic3-serverless](https://github.com/sruckh/minmaxmusic3-serverless); YuE2 runs on its own serverless worker, which is a private repository and is deliberately not linked from here. Both endpoints share one RunPod account and API key.
- Handles job queueing, polling, and audio file downloading with automatic error handling.
- **Per-mode time budgets.** A YuE2 cover transcribes the recording with two model families before it generates anything, so it gets a far longer leash than a create — a single flat budget would kill it mid-transcription and report a timeout for work that was progressing normally.
- **A completed generation is not discarded over a transient failure.** Artifact downloads retry with backoff, and a storage error leaves the job alive for the next poll instead of failing it.
- **A refused submission returns to the queue.** RunPod answers `409 ENDPOINT_PAUSED` when an endpoint has scaled to zero; that is a definitive refusal, so the job waits for capacity rather than failing instantly.

### 👥 Multi-User Accounts & Access Control
- **Approval-gated registration**: anyone can request an account at `/register`; the account is created `pending` and cannot sign in until an administrator approves it.
- **Default-deny routing**: the whole HTTP mux is wrapped in the access middleware, so a route is authenticated-only unless its pattern is on an explicit public allowlist.
- **Partitioned library**: `/history` shows *My Songs* (yours alone, even for an administrator) beside *Community Songs* (everything explicitly shared), each paged independently.
- **Explicit sharing**: songs are `is_public = 0` on creation. Sharing grants reading, never writing — only the owner (or an administrator) can rename, un-share, rework, or delete.
- **Admin dashboard** at `/admin`: approve, disable, or delete accounts, with a pending-request badge in the nav. Disabling revokes live sessions in the same transaction; deleting removes the account's sessions, jobs, songs, and audio files — including any cover recordings it uploaded, and the share links its songs were being served under.
- **Session hardening**: session tokens are stored only as SHA-256; privilege and account status are resolved from the `users` table on every request, so a disable or delete takes effect on the very next request rather than at cookie expiry.

### 💾 Song Library & Playback
- **SQLite Database**: Persists job states and song metadata in `/data/mm3.db` using WAL mode.
- **Local Timezone Display**: Creation timestamps automatically format in the user's local browser timezone.
- **Named by you**: give a song a title on the generate form; leave it blank and one is derived from the style caption. Either way it can be renamed in place from the history list.
- **Playback & Management**: Dedicated song playback page with audio player, lyrics display, style caption, inline title editing, deletion, and — for the owner — *Edit in generator*, which copies the song back into the form to be reworked.
- **Score metadata, read from the score.** A YuE2 song shows the key, meter and tempo the model chose, read out of the ABC score it wrote. These are outputs, not settings: YuE2 has no request field for them, and putting them in the style line does not control them — measured over six GPU jobs, two of three seeds returned identical tempos with and without the hint, and key and meter were ignored outright.
- **Truncated generations are flagged.** When a stage hits its token cap the song is marked as shorter than it was asked for, so a cut-short take is not mistaken for a finished one.

### 🔒 Secure Deployment
- **Zero Hardcoded Secrets**: Secrets (`RUNPOD_API_KEY`, `LLM_API_KEY`, `ADMIN_USER`, `ADMIN_PASSWORD`) are injected via Infisical Universal Auth machine identities.
- **Administrator credentials never touch the database**: the static administrator is a config-only identity; there is no seeded admin row and no default password.

---

## Architecture

<p align="center">
  <img src="assets/readme/architecture.svg" alt="MiniMax Music 3 System Architecture" width="100%">
</p>

### Execution Flow
1. **User Request**: The user picks an engine, then enters a song concept into the AI assistant or fills in the generation form. The engine is dispatched to the assistant, which selects that engine's prompt and reply format.
2. **AI Assistant (`POST /assistant`)**: Proxies to OmniRoute / LLM gateway with thinking disabled (`thinking: disabled`), returning a draft the user reviews and edits before submitting.
3. **Job Queue (`POST /jobs`)**: Validates input against **that engine's** rules — MiniMax requires lyrics and a 10–300 s length; YuE2 requires a style and permits an instrumental — and stores a queued job in SQLite (`/data/mm3.db`), recording the engine and mode.
4. **Background Worker**: Dequeues jobs, resolves the RunPod client and request shape **from the job's engine**, submits asynchronously (`POST /run`), polls status (`GET /status/{id}`), fetches the artifact and transcodes it to stereo 192 kbps M4A (`/data/audio/`), stores the planned score beside it, and updates the database. A janitor loop on a slower ticker reaps expired cover links and stale staged uploads.
5. **htmx Polling**: Browser polls `GET /jobs/{id}` and updates the player once generation is complete.

A cover runs a longer path — the form mints a signed link to the recording, and the worker transcribes it before generating:

```text
POST /songs/{id}/cover
  → mint a signed link to the recording        (or pass a pasted URL through)
  → queue job (mode=cover, engine=yue2)
  → worker fetches the link
  → SheetSage2 → melody.abc     ┐ transcription
  → Qwen3-ASR  → lyrics.txt     ┘ then generate
  → generate (melody-conditioned)
  → store the new song
```

---

## Quick Start

### Prerequisites
- **Docker** &amp; **Docker Compose**
- Infisical environment configuration &amp; client secret
- `ADMIN_USER` and `ADMIN_PASSWORD` present in the Infisical project — **without both, the deployment has no administrator and no account can ever be approved.** See [Authentication & Administration](#authentication--administration).
- `RUNPOD_ENDPOINT` and `RUNPOD_API_KEY` for MiniMax. To offer YuE2 as well, add `YUE2_RUNPOD_ENDPOINT`; without it the engine is simply not offered.

### Bring Up the Stack
Run the bring-up script to decrypt secrets into RAM (`/dev/shm`) and start the application:

```bash
./scripts/up.sh --build
```

### Verify Container Logs
```bash
docker logs mm3-app --tail 20
```

Expected startup output:
```text
time=2026-08-15T19:04:58.009Z level=INFO msg="config loaded" summary="addr=:8080 web=/app/web db=/data/mm3.db audio=/data/audio in_flight=2 runpod_endpoint=set runpod_key=true llm_base=set llm_model=set llm_key=true llm_thinking=disabled llm_reasoning_effort=none admin_user=set admin_password=true admin_login=true"
time=2026-08-15T19:04:58.012Z level=INFO msg=listening addr=:8080
```

The summary never prints a secret's value — only whether it is present. **Check `admin_login=true` on every deploy.** If either administrator credential is missing you will instead see:

```text
level=WARN msg="administrator login disabled: ADMIN_USER and ADMIN_PASSWORD must both be set"
```

`GET /healthz` still returns `200 OK` in that state and the container is reported healthy, so this warning is the only signal that the instance cannot be administered.

---

## Authentication & Administration

Every route other than `/login`, `/register`, `/logout`, `/healthz`, `/static/`, `/favicon.ico`, and the signed-cover route below requires an approved session. There is no anonymous access to generation, history, audio, or the admin dashboard. Sessions expire after 7 days, and the account's status is re-resolved from the database on every request.

The one public route that serves content is `GET /signed/{token}`, which exists because the RunPod worker has no session and must be able to fetch a cover's recording. **The token is the whole of its authorisation** — 32 random bytes, stored only as a SHA-256 hash, single-purpose, expiring in two hours, and answering an indistinguishable `404` to anything unknown, lapsed or malformed. It is scoped to that single pattern; `/audio/{id}` remains session-only, and there is a test asserting it.

### The administrator account

The administrator is **not** a database record. It is a pair of Infisical secrets read at startup:

| Secret | Meaning |
|---|---|
| `ADMIN_USER` | The administrator's login name. |
| `ADMIN_PASSWORD` | The administrator's password, compared in constant time. |

Both must be set in the Infisical project (`dev` environment) alongside `RUNPOD_*` and `LLM_*`; the entrypoint injects them with `infisical run`. They are never written to SQLite, never logged, and have **no default** — if either is blank, administrator sign-in is disabled outright rather than falling back to a guessable credential. Registration and the rest of the app keep working, which is exactly why the failure is easy to miss: new users pile up in `pending` with nobody able to approve them.

Recovering from that state means setting both secrets in Infisical and restarting the container. There is no CLI, no bootstrap flag, and no self-service escape hatch.

### First sign-in

1. Open `https://<your-host>/login`.
2. Sign in with `ADMIN_USER` / `ADMIN_PASSWORD`. The **Admin** tab appears in the nav.
3. Have your users open `/register` and request accounts. Registration never issues a session — a new account is created with status `pending` and is told so.
4. Approve them from `/admin`. The nav badge shows how many requests are waiting.

A signup cannot claim the `ADMIN_USER` name; that registration is refused.

### What an administrator can do

| Action | Effect |
|---|---|
| **Approve User** | Moves a `pending` (or `disabled`) account to `approved`. Idempotent. |
| **Disable User** | Moves an account to `disabled` **and revokes its live sessions in the same transaction** — the user is signed out immediately, not at cookie expiry. Reversible by approving again. |
| **Delete User** | Permanently removes the account, its sessions, its jobs, its songs, its cover recordings, and the files behind all of them. **Not reversible**, and the songs are destroyed rather than reassigned. |

Deletion is one transaction for the database rows, and the files are unlinked afterwards — an unlink cannot join the transaction, and a leftover file is recoverable garbage where a row pointing at a missing file is not.

Administrators additionally may read, rename, un-share, and delete **any** song via its URL, and their `Access` lifts the ownership predicate on every scoped store query. They do **not** get a catalogue view: an administrator's *My Songs* is deliberately their own library, not everyone's.

Three guards are deliberate and will refuse you:

- An administrator cannot disable or delete **their own** account.
- The store refuses any change that would leave the database with **no approved administrator**.
- The configured (Infisical) administrator has no `users` row, so admin actions aimed at it are refused with an explanatory notice.

### Known limitation — no role promotion

There is no endpoint that grants the `admin` role to a database account. `role` is `user` for every registered account, so the configured Infisical administrator is the only working administrator. Creating a second one currently means writing the `users` row directly. The last-administrator guard is already implemented and tested ahead of that.

---

## Upgrading from the single-user release

The migration is automatic and idempotent — `store.Open` runs it on start, guarding every `ALTER TABLE` with a `pragma_table_info` probe.

Two things an operator should know:

1. **Pre-existing jobs and songs are assigned `user_id = 'legacy'`** and remain `is_public = 0`. `legacy` is deliberately **not** a `users` row, so nobody can log in as it and no personal library will ever show those songs. They are reachable only by an administrator who already knows a song's id, via `/songs/{id}`. If you want your old library back in a real account, re-own the rows yourself after the first migration has run:

   ```sql
   -- <new-owner-id> is the `id` from the users table for the account
   -- that should own the pre-multi-user library.
   UPDATE songs SET user_id = '<new-owner-id>' WHERE user_id = 'legacy';
   UPDATE jobs  SET user_id = '<new-owner-id>' WHERE user_id = 'legacy';
   ```

   Leaving them as `legacy` is a supported outcome — they simply stay invisible. Deleting them (`DELETE FROM songs WHERE user_id = 'legacy'`) is the third option; remember to unlink the corresponding files under `MM3_AUDIO_DIR`, which no `DELETE` will do for you.

2. **Every existing session is invalidated.** An earlier revision keyed the `sessions` table by the raw bearer token; the migration drops and rebuilds that table rather than migrating replayable rows. Users sign in again once. No user, job, or song data is touched.

---

## Upgrading to two engines

Also automatic, and also additive. The engine and mode columns arrive with defaults that state what the existing rows already mean — every job recorded before them was a MiniMax create, because that was the only engine and the only mode. No backfill, and nothing to re-run.

Adding a second engine does not change the first. With `YUE2_RUNPOD_ENDPOINT` unset the selector shows MiniMax alone; with it set, a YuE2 song is a new row and the MiniMax library is untouched.

Both endpoints run under one RunPod account, and **the existing `RUNPOD_API_KEY` covers both** — there is nothing extra to configure beyond adding the endpoint URL. `YUE2_RUNPOD_API_KEY` exists only as an escape hatch: RunPod can scope a key to named endpoints, so if yours ever cannot reach YuE2 it answers `403` for that endpoint alone while continuing to work for MiniMax, and a key set here takes precedence.

---

## API Reference

Access is enforced by one middleware wrapping the entire mux: anything not listed as **Public** below requires an approved session, and anything under `/admin` additionally requires administrator privilege. State-changing requests are also origin-checked. Beyond the credential endpoints, `/jobs` accepts at most 6 generations per hour per IP and `/assistant` at most 20 drafts per day per IP — both answered with `429 Too Many Requests` and a `Retry-After` header.

| Endpoint | Method | Access | Description |
|---|---|---|---|
| `GET /healthz` | `GET` | Public | Healthcheck endpoint — HTTP 200 with body `ok`, probed by the Docker `HEALTHCHECK`. Returns healthy even when administrator login is disabled. |
| `GET /login` | `GET` | Public | Sign-in page. |
| `POST /login` | `POST` | Public | Verify credentials and issue a session. Rate limited (10 / 15 min / IP). |
| `GET /register` | `GET` | Public | Account request page. |
| `POST /register` | `POST` | Public | Create a `pending` account. Never issues a session. Rate limited (5 / hour / IP). |
| `POST /logout` | `POST` | Public | Revoke the presented session server-side and clear the cookie. |
| `GET /static/` | `GET` | Public | Static assets, including the favicons under `/static/favicon/`. |
| `GET /favicon.ico` | `GET` | Public | Allowlisted but unregistered, so a browser's automatic probe gets a plain `404` rather than a redirect to `/login`. |
| `GET /signed/{token}` | `GET` | Public (signed) | Serves one cover recording to a holder of a valid link. The RunPod worker has no session, so **the token is the entire authorisation** — 32 random bytes, stored only as a SHA-256 hash, single-purpose, expiring in two hours. Unknown, lapsed and malformed tokens all answer the same `404`, so it cannot be used to probe which exist. Scoped to this one pattern: the owner-scoped `/audio/{id}` stays session-only. |
| `GET /` | `GET` | Authenticated | Web console homepage with engine selector, generation form &amp; assistant panel. |
| `POST /assistant` | `POST` | Authenticated | AI assistant proxy, using the selected engine's prompt and reply format. |
| `POST /jobs` | `POST` | Authenticated | Validate form and queue a text-to-song generation job, owned by the caller. |
| `GET /jobs/{id}` | `GET` | Owner / Admin | htmx polling endpoint returning job status or player HTML. |
| `GET /history` | `GET` | Authenticated | Partitioned library: *My Songs* and *Community Songs*, each paged independently (`?mine=`, `?public=`). |
| `GET /history/personal` | `GET` | Authenticated | htmx fragment for the caller's own songs (`?page=`). |
| `GET /history/public` | `GET` | Authenticated | htmx fragment for the community library (`?page=`). |
| `GET /songs/{id}` | `GET` | Owner / Shared / Admin | Playback detail page with lyrics, caption, seed, score metadata, history navigation, and — for the owner — *Edit in generator*, plus the Edit and Cover panels where the song's engine supports them. |
| `POST /songs/{id}/edit` | `POST` | Owner | Re-render a YuE2 song from its stored score with a new arrangement, tempo or words. Blank fields fall back to the song's own, so a tempo-only edit does not blank the lyrics. Queues a **new** song and records `mode=edit` with its source. |
| `POST /songs/{id}/cover` | `POST` | Owner | Re-style a recording with YuE2's cover mode. Accepts a pasted `source_url`, a multipart `source_upload`, or neither — in which case the song itself is the recording. Queues a job with `mode=cover`. |
| `POST /songs/{id}/toggle-public` | `POST` | Owner / Admin | Set sharing explicitly — send `public=1` or `public=0`. Not a blind flip. |
| `POST /songs/{id}/title` | `POST` | Owner / Admin | Update song title from the library. |
| `DELETE /songs/{id}` | `DELETE` | Owner / Admin | Delete song, purge database records, and remove audio file. |
| `GET /audio/{id}` | `GET` | Owner / Shared / Admin | Stream generated audio in the browser — M4A, with legacy `.wav` files passed through as-is. |
| `GET /admin` | `GET` | Admin | User administration dashboard with pending-request badge. |
| `POST /admin/users/{id}/approve` | `POST` | Admin | Approve an account. |
| `POST /admin/users/{id}/disable` | `POST` | Admin | Disable an account and revoke its sessions. |
| `POST /admin/users/{id}/delete` | `POST` | Admin | Delete an account, its songs, and its audio. |

A refusal on an unauthorised song id is a plain `404` — identical to a song that does not exist — so these endpoints cannot be used to discover which ids are real.

---

## Configuration

Values marked *(Infisical)* have no default. They are stored in the Infisical project and injected into the container's environment by `infisical run` at start — never in `docker-compose.yml`, never in the image, never in this repository.

| Environment Variable | Default Value | Description |
|---|---|---|
| `MM3_ADDR` | `:8080` | Server listen address. |
| `MM3_PUBLIC_URL` | *(unset)* | Optional canonical external origin, trusted as a same-origin source for state-changing requests behind the reverse proxy. Unset, a same-origin write must present this request's own Host — which any correctly forwarded proxy already does. Bring-up loads it automatically from the operator's local `~/.config/mm3-web-infisical/infisical.env`; set it there rather than in any tracked file. |
| `MM3_WEB_DIR` | `/app/web` | Directory containing web templates and static assets. |
| `MM3_DB_PATH` | `/data/mm3.db` | SQLite database file path. |
| `MM3_AUDIO_DIR` | `/data/audio` | Output directory for audio M4A files. |
| `MM3_MAX_IN_FLIGHT` | `2` | Global concurrent job limit. |
| `LLM_BASE_URL` | *(Infisical)* | OpenAI-compatible LLM gateway URL. |
| `LLM_API_KEY` | *(Infisical)* | LLM authorization key. |
| `LLM_MODEL_ID` | *(Infisical)* | LLM model ID (e.g. `deepseek-v4-flash`). |
| `LLM_THINKING` | `disabled` | LLM thinking mode (`disabled`, `enabled`, `off`). |
| `LLM_REASONING_EFFORT` | `none` | LLM reasoning effort (`none`, `low`, `medium`, `high`). |
| `RUNPOD_ENDPOINT` | *(Infisical)* | MiniMax RunPod serverless endpoint URL. |
| `RUNPOD_API_KEY` | *(Infisical)* | RunPod authorization key. Both endpoints run under one RunPod account, so this one key covers both. |
| `YUE2_RUNPOD_ENDPOINT` | *(Infisical)* | YuE2 RunPod serverless endpoint URL. **Unset, the engine is simply not offered** — the selector shows MiniMax alone and nothing else changes. |
| `YUE2_RUNPOD_API_KEY` | *(Infisical)* | Optional, and normally unnecessary — `RUNPOD_API_KEY` covers both endpoints. Set it only if your key is scoped to named endpoints and cannot reach YuE2; that shows up as a `403` for the YuE2 endpoint alone. Set, it takes precedence. |
| `ADMIN_USER` | *(Infisical)* | **Required.** Static administrator login name. Blank disables administrator sign-in — see [Authentication & Administration](#authentication--administration). |
| `ADMIN_PASSWORD` | *(Infisical)* | **Required.** Static administrator password, compared in constant time. Blank disables administrator sign-in. |

> ⚠️ `ADMIN_USER` and `ADMIN_PASSWORD` are the two secrets whose absence does **not** break the health check. A deploy missing them starts, serves, accepts registrations, and reports healthy — with no way to approve anyone. Confirm `admin_login=true` in the `config loaded` log line after every deploy.

---

## Development

### Run Unit & Integration Tests
```bash
go test -race ./...
```

`-race` is the supported invocation: the suite exercises the real handler stack with concurrent browser-like sessions, and the store's single-writer assumption is only meaningfully checked under the detector.

### Build Binary
```bash
go build ./...
```

### Live probes — they spend real GPU time

Some behaviour can only be checked against the running system: that the endpoint accepts the request this app builds, that the worker can fetch a signed link, that a cover actually completes. Those probes live behind a `//go:build live` tag, so `go test ./...` cannot reach them and cannot cost anything by accident.

They read the real endpoint from the environment and expect to run **inside the app container**, where the database, the data volume and the injected secrets all are:

```bash
CGO_ENABLED=0 go test -tags live -c -o /tmp/probe.test ./internal/worker
docker compose run --rm --no-deps -T -v /tmp:/probe:ro app /probe/probe.test -test.run TestLive -test.v
```

`PROBE_RUNPOD_API_KEY`, when set, takes precedence over `RUNPOD_API_KEY` — so a throwaway credential can be used for one run without touching Infisical, and without relying on `-e`, which `infisical run` overwrites with its own value.

### Deleting one account from the command line

`cmd/probeclean` deletes a single account through the same `store.DeleteUser` path the admin screen uses, rather than a hand-written SQL script that could drift from it. It is **dry by default**:

```bash
go run ./cmd/probeclean -db /data/mm3.db -user <username>           # what would go
go run ./cmd/probeclean -db /data/mm3.db -user <username> -apply    # delete
```

### Design assets — read-only

Three files at the repository root are **prototype design inputs, not descriptions of the running app**, and are deliberately never edited:

| File | What it is |
|---|---|
| `DESIGN.md` | The OUTBOARD design system — colour tokens, type scale, component and motion rules. Its palette, typography, and accessibility rules are live and honoured by `web/static/input.css`. Its §3 *Layout* and §4 *Components* sketches describe the standalone mock-up, not the shipped pages, and predate both the real backend and multi-user. |
| `index-dark.html`, `index-light.html` | The self-contained static mock-ups that `DESIGN.md` describes. They have no backend, no accounts, and no htmx — they are the visual reference the Tailwind build was derived from. |

Nothing serves them and nothing keeps them in sync with `web/templates/`; treat any disagreement between them and the templates as the mock-up being older, and change the templates. If you want a design change, change `web/static/input.css` and the templates, not these files.

---

## License

Copyright © 2026 sruckh. All rights reserved.
