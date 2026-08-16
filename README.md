<p align="center">
  <img src="assets/readme/hero.svg" alt="MiniMax Music 3 Web Interface Hero Banner" width="100%">
</p>

<h1 align="center">MiniMax Music 3 Web Interface</h1>

<p align="center">
  <strong>A web interface for creating music using text-to-song inference powered by MiniMax Music 3.</strong><br>
  Turn text ideas, tagged lyrics, and style captions into full stereo M4A audio tracks (192 kbps AAC).
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

**minmaxmusic3-web** is a self-hosted web application for generating music from text using the **MiniMax Music 3** model. Users can describe a sound, draft structured lyrics, set style parameters, and generate high-quality audio files.

The application is **multi-user and closed by default**: every route except sign-in, registration, and the health check requires an approved account, and every song is private to the account that generated it until its owner explicitly shares it. See [Authentication & Administration](#authentication--administration) before deploying — an instance with no administrator credentials configured starts and reports healthy, but nobody can ever be approved.

---

## Features

### 🎵 Text-to-Song Music Generation
- Generate full-length music tracks (up to 300 seconds) from text lyrics and style captions.
- Outputs high-quality **stereo M4A (192 kbps AAC)** audio files.

### 🪄 AI Songwriting & Style Assistant
- Integrated AI assistant (`POST /assistant`) that drafts tagged lyrics (`[Verse]`, `[Chorus]`, `[Bridge]`, `[Outro]`) and structured style descriptions from a simple prompt.
- **Fast Response Latency**: Sends `thinking: {"type": "disabled"}` and `reasoning_effort: "none"` to bypass internal thinking delays on reasoning models (`deepseek-v4-flash`), delivering drafts in under 3 seconds.
- **Resilient JSON Parsing**: Multi-stage parser handles closed code fences, unclosed code fences, and raw JSON objects.

### ⚡ RunPod Serverless GPU Inference
- Asynchronous worker queue connected to the RunPod Serverless worker ([sruckh/minmaxmusic3-serverless](https://github.com/sruckh/minmaxmusic3-serverless)).
- Handles job queueing, polling, and audio file downloading with automatic error handling.

### 👥 Multi-User Accounts & Access Control
- **Approval-gated registration**: anyone can request an account at `/register`; the account is created `pending` and cannot sign in until an administrator approves it.
- **Default-deny routing**: the whole HTTP mux is wrapped in the access middleware, so a route is authenticated-only unless its pattern is on an explicit public allowlist.
- **Partitioned library**: `/history` shows *My Songs* (yours alone, even for an administrator) beside *Community Songs* (everything explicitly shared), each paged independently.
- **Explicit sharing**: songs are `is_public = 0` on creation. Sharing grants reading, never writing — only the owner (or an administrator) can rename, un-share, regenerate, or delete.
- **Admin dashboard** at `/admin`: approve, disable, or delete accounts, with a pending-request badge in the nav. Disabling revokes live sessions in the same transaction; deleting removes the account's sessions, jobs, songs, and audio files.
- **Session hardening**: session tokens are stored only as SHA-256; privilege and account status are resolved from the `users` table on every request, so a disable or delete takes effect on the very next request rather than at cookie expiry.

### 💾 Song Library & Playback
- **SQLite Database**: Persists job states and song metadata in `/data/mm3.db` using WAL mode.
- **Local Timezone Display**: Creation timestamps automatically format in the user's local browser timezone.
- **Playback & Management**: Dedicated song playback page with audio player, lyrics display, style caption, seed replay, inline title editing, and deletion.

### 🔒 Secure Deployment
- **Zero Hardcoded Secrets**: Secrets (`RUNPOD_API_KEY`, `LLM_API_KEY`, `ADMIN_USER`, `ADMIN_PASSWORD`) are injected via Infisical Universal Auth machine identities.
- **Administrator credentials never touch the database**: the static administrator is a config-only identity; there is no seeded admin row and no default password.

---

## Architecture

<p align="center">
  <img src="assets/readme/architecture.svg" alt="MiniMax Music 3 System Architecture" width="100%">
</p>

### Execution Flow
1. **User Request**: User enters a song concept into the AI assistant or fills in the generation form.
2. **AI Assistant (`POST /assistant`)**: Proxies to OmniRoute / LLM gateway with thinking disabled (`thinking: disabled`).
3. **Job Queue (`POST /jobs`)**: Validates input and stores a queued job in SQLite (`/data/mm3.db`).
4. **Background Worker**: Dequeues jobs, sends inference requests to RunPod GPU (`POST /runsync`), transcodes audio to stereo 192 kbps M4A (`/data/audio/`), and updates the database.
5. **htmx Polling**: Browser polls `GET /jobs/{id}` and updates the player once generation is complete.

---

## Quick Start

### Prerequisites
- **Docker** &amp; **Docker Compose**
- Infisical environment configuration &amp; client secret
- `ADMIN_USER` and `ADMIN_PASSWORD` present in the Infisical project — **without both, the deployment has no administrator and no account can ever be approved.** See [Authentication & Administration](#authentication--administration).

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

Every route other than `/login`, `/register`, `/logout`, `/healthz`, `/static/`, and `/favicon.ico` requires an approved session. There is no anonymous access to generation, history, audio, or the admin dashboard.

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
| **Delete User** | Permanently removes the account, its sessions, its jobs, its songs, and the songs' audio files. **Not reversible**, and the songs are destroyed rather than reassigned. |

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

## API Reference

Access is enforced by one middleware wrapping the entire mux: anything not listed as **Public** below requires an approved session, and anything under `/admin` additionally requires administrator privilege. State-changing requests are also origin-checked.

| Endpoint | Method | Access | Description |
|---|---|---|---|
| `GET /healthz` | `GET` | Public | Healthcheck endpoint (`200 OK`). Returns healthy even when administrator login is disabled. |
| `GET /login` | `GET` | Public | Sign-in page. |
| `POST /login` | `POST` | Public | Verify credentials and issue a session. Rate limited (10 / 15 min / IP). |
| `GET /register` | `GET` | Public | Account request page. |
| `POST /register` | `POST` | Public | Create a `pending` account. Never issues a session. Rate limited (5 / hour / IP). |
| `POST /logout` | `POST` | Public | Revoke the presented session server-side and clear the cookie. |
| `GET /static/` | `GET` | Public | Static assets, including the favicons under `/static/favicon/`. |
| `GET /favicon.ico` | `GET` | Public | Allowlisted but unregistered, so a browser's automatic probe gets a plain `404` rather than a redirect to `/login`. |
| `GET /` | `GET` | Authenticated | Web console homepage with generation form &amp; assistant panel. |
| `POST /assistant` | `POST` | Authenticated | AI assistant proxy to draft tagged lyrics and style caption. |
| `POST /jobs` | `POST` | Authenticated | Validate form and queue a text-to-song generation job, owned by the caller. |
| `GET /jobs/{id}` | `GET` | Owner / Admin | htmx polling endpoint returning job status or player HTML. |
| `GET /history` | `GET` | Authenticated | Partitioned library: *My Songs* and *Community Songs*, each paged independently (`?mine=`, `?public=`). |
| `GET /history/personal` | `GET` | Authenticated | htmx fragment for the caller's own songs (`?page=`). |
| `GET /history/public` | `GET` | Authenticated | htmx fragment for the community library (`?page=`). |
| `GET /songs/{id}` | `GET` | Owner / Shared / Admin | Playback detail page with lyrics, caption, seed, and history navigation. |
| `POST /songs/{id}/toggle-public` | `POST` | Owner / Admin | Set sharing explicitly — send `public=1` or `public=0`. Not a blind flip. |
| `POST /songs/{id}/regenerate` | `POST` | Owner / Admin | Re-queue generation job using the same inputs and seed. |
| `POST /songs/{id}/title` | `POST` | Owner / Admin | Update song title from the library. |
| `DELETE /songs/{id}` | `DELETE` | Owner / Admin | Delete song, purge database records, and remove audio file. |
| `GET /audio/{id}` | `GET` | Owner / Shared / Admin | Stream or download generated M4A audio file. |
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
| `MM3_PUBLIC_URL` | `https://mm3.gemneye.xyz` | Canonical external origin. Accepted as a same-origin source for state-changing requests behind the reverse proxy. |
| `MM3_WEB_DIR` | `/app/web` | Directory containing web templates and static assets. |
| `MM3_DB_PATH` | `/data/mm3.db` | SQLite database file path. |
| `MM3_AUDIO_DIR` | `/data/audio` | Output directory for audio M4A files. |
| `MM3_MAX_IN_FLIGHT` | `2` | Global concurrent job limit. |
| `LLM_BASE_URL` | *(Infisical)* | OpenAI-compatible LLM gateway URL. |
| `LLM_API_KEY` | *(Infisical)* | LLM authorization key. |
| `LLM_MODEL_ID` | *(Infisical)* | LLM model ID (e.g. `deepseek-v4-flash`). |
| `LLM_THINKING` | `disabled` | LLM thinking mode (`disabled`, `enabled`, `off`). |
| `LLM_REASONING_EFFORT` | `none` | LLM reasoning effort (`none`, `low`, `medium`, `high`). |
| `RUNPOD_ENDPOINT` | *(Infisical)* | RunPod serverless worker URL. |
| `RUNPOD_API_KEY` | *(Infisical)* | RunPod authorization key. |
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
