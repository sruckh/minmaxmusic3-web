<p align="center">
  <img src="assets/readme/hero.svg" alt="MM3 Console Hero Banner" width="100%">
</p>

<h1 align="center">MM3 — MiniMax Music 3 Web Console</h1>

<p align="center">
  <strong>A studio-grade web front-end and AI assistant for the MiniMax Music 3 music generation model.</strong><br>
  Built like classic outboard hardware — type a description of a sound, draft tagged lyrics, and print it to tape.
</p>

<p align="center">
  <a href="#features">Features</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#api-reference">API Reference</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#development">Development</a>
</p>

---

## Features

### 🎛️ Outboard Studio Console UI
Designed after studio hardware and analog channel strips. Built with **Go 1.24**, **htmx**, **Alpine.js**, and **Tailwind CSS v4** following the `OUTBOARD` design system (`DESIGN.md`).
- **5-State Signal Peak Meter**: Visual meter tracking Idle, Audio Signal, AI Layer, Hot Record, and Clipping states.
- **Dual Theme Support**: Dark (`#5E1226` burgundy ground) and Light (`#E5DCDF` mist ground) studio visual themes.

### 🪄 Fast AI Songwriting & Style Assistant
An in-app AI assistant (`POST /assistant`) that turns a rough song idea into correctly formatted MiniMax Music 3 inputs:
- **Tagged Lyrics**: Generates bracketed section tags (`[Intro]`, `[Verse]`, `[Pre-Chorus]`, `[Chorus]`, `[Bridge]`, `[Outro]`) on their own lines.
- **3-Heading Style Caption**: Structure caption formatted into **Global Metadata**, **Vocal Details**, and **Arrangement** timeline.
- **Fast Reasoning-Bypassed Latency**: Sends `thinking: {"type": "disabled"}` and `reasoning_effort: "none"` to bypass 10,000+ token internal thinking delays on reasoning models (`deepseek-v4-flash`, `deepseek-r1`), delivering drafts in under 3 seconds.
- **Robust JSON Parsing**: 3-stage fallback parser handles fenced code blocks, unclosed fences, and raw JSON objects.

### ⚡ RunPod Serverless GPU Inference
Worker pipeline connected to [github.com/sruckh/minmaxmusic3-serverless](https://github.com/sruckh/minmaxmusic3-serverless):
- Generates 32 kHz 16-bit stereo WAV audio.
- Supports S3 presigned URL delivery and inline base64 fallback.
- In-flight concurrency limits and rate-limiting protection per client IP.

### 💾 SQLite Library & Song History
- **SQLite WAL Engine**: Pure-Go SQLite database (`modernc.org/sqlite`) stored at `/data/mm3.db`.
- **Browser Local Timezone Dates**: Song creation timestamps render automatically in the end user's local timezone via Alpine.js.
- **Playback & Navigation**: Song playback screen with audio player, lyrics viewer, style caption, seed replay, and `← Back to history` navigation.

### 🔒 Zero-Trust Security & Infisical Secrets
- **No Hardcoded Secrets**: Secrets (`RUNPOD_API_KEY`, `LLM_API_KEY`) are fetched via **Infisical Universal Auth** machine identities at container startup.
- **RAM-Only Bootstrap Secrets**: Host-side bootstrap secrets are mounted read-only via `tmpfs` (`/dev/shm`).
- **Zero Published Host Ports**: Exposes container port `8080` internally on Docker `shared_net` for Nginx Proxy Manager (`app.example.invalid`).

---

## Architecture

<p align="center">
  <img src="assets/readme/architecture.svg" alt="MM3 System Architecture" width="100%">
</p>

### Execution Flow
1. **User Request**: User inputs a song idea into the AI Assistant or submits the generation form on `app.example.invalid`.
2. **AI Assistant (`POST /assistant`)**: Server calls OmniRoute (`/v1/chat/completions`) with an 8,000 token ceiling and thinking disabled (`thinking: disabled`).
3. **Job Queue (`POST /jobs`)**: Validates input formatting and creates a queued job in `/data/mm3.db`.
4. **Background Worker (`internal/worker`)**: Dequeues queued jobs, submits to RunPod Serverless (`POST /runsync`), downloads output audio WAV to `/data/audio/`, and updates job/song status.
5. **htmx Polling**: Browser polls `GET /jobs/{id}` for state updates and swaps the audio player when complete.

---

## Quick Start

### Prerequisites
- **Docker** and **Docker Compose**
- GPG bootstrap key at `~/.config/mm3-web-infisical/client_secret.gpg`
- Infisical environment config at `~/.config/mm3-web-infisical/infisical.env`

### Bring Up the Stack
Run the secure bring-up script, which decrypts the client secret to RAM (`/dev/shm`) and starts the container stack:

```bash
./scripts/up.sh --build
```

### Check Logs & Health
```bash
docker logs mm3-app --tail 50
```

Verify that secrets were injected and services booted:
```text
time=2026-08-15T19:04:58.009Z level=INFO msg="config loaded" summary="addr=:8080 web=/app/web db=/data/mm3.db audio=/data/audio in_flight=2 runpod_endpoint=set runpod_key=true llm_base=set llm_model=set llm_key=true llm_thinking=disabled llm_reasoning_effort=none"
time=2026-08-15T19:04:58.012Z level=INFO msg=listening addr=:8080
```

---

## API Reference

| Endpoint | Method | Description |
|---|---|---|
| `GET /` | `GET` | Main console homepage (Generate form &amp; AI assistant panel). |
| `POST /assistant` | `POST` | Proxy LLM call to draft tagged lyrics and style caption (`idea` form param). |
| `POST /jobs` | `POST` | Validate input form and queue a music generation job. |
| `GET /jobs/{id}` | `GET` | htmx poll endpoint returning job status or audio player fragment. |
| `GET /history` | `GET` | Paginated song library sorted newest-first. |
| `GET /songs/{id}` | `GET` | Song playback detail page (lyrics, caption, seed, back navigation). |
| `POST /songs/{id}/regenerate` | `POST` | Re-queue generation job using the same seed and parameters. |
| `GET /audio/{id}` | `GET` | Stream or download generated WAV audio file. |
| `GET /healthz` | `GET` | Liveness healthcheck endpoint (`200 OK`). |

---

## Configuration

| Environment Variable | Default Value | Description |
|---|---|---|
| `MM3_ADDR` | `:8080` | Listen address inside container. |
| `MM3_PUBLIC_URL` | `https://app.example.invalid` | Public hostname. |
| `MM3_WEB_DIR` | `/app/web` | Absolute path to web templates and static assets. |
| `MM3_DB_PATH` | `/data/mm3.db` | SQLite database file path. |
| `MM3_AUDIO_DIR` | `/data/audio` | Output directory for audio WAV files. |
| `MM3_MAX_IN_FLIGHT` | `2` | Global concurrent job limit. |
| `LLM_BASE_URL` | *(Infisical)* | OpenAI-compatible LLM gateway URL. |
| `LLM_API_KEY` | *(Infisical)* | LLM authorization key. |
| `LLM_MODEL_ID` | *(Infisical)* | LLM model ID (e.g. `deepseek-v4-flash`). |
| `LLM_THINKING` | `disabled` | Assistant thinking mode (`disabled`, `enabled`, `off`). |
| `LLM_REASONING_EFFORT` | `none` | OpenAI reasoning effort (`none`, `low`, `medium`, `high`). |
| `RUNPOD_ENDPOINT` | *(Infisical)* | RunPod serverless worker URL. |
| `RUNPOD_API_KEY` | *(Infisical)* | RunPod authorization key. |

---

## Development

### Run Unit & Integration Tests
```bash
go test -v ./...
```

### Build Binary
```bash
go build ./...
```

---

## License

Copyright © 2026 sruckh. All rights reserved.
