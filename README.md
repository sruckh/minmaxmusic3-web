<p align="center">
  <img src="assets/readme/hero.svg" alt="MiniMax Music 3 Web Interface Hero Banner" width="100%">
</p>

<h1 align="center">MiniMax Music 3 Web Interface</h1>

<p align="center">
  <strong>A web interface for creating music using text-to-song inference powered by MiniMax Music 3.</strong><br>
  Turn text ideas, tagged lyrics, and style captions into full 32 kHz stereo WAV audio tracks.
</p>

<p align="center">
  <a href="#overview">Overview</a> •
  <a href="#features">Features</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#api-reference">API Reference</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#development">Development</a>
</p>

---

## Overview

**minmaxmusic3-web** is a self-hosted web application for generating music from text using the **MiniMax Music 3** model. Users can describe a sound, draft structured lyrics, set style parameters, and generate high-quality audio files.

---

## Features

### 🎵 Text-to-Song Music Generation
- Generate full-length music tracks (up to 300 seconds) from text lyrics and style captions.
- Outputs high-quality **32 kHz 16-bit stereo WAV** audio files.

### 🪄 AI Songwriting & Style Assistant
- Integrated AI assistant (`POST /assistant`) that drafts tagged lyrics (`[Verse]`, `[Chorus]`, `[Bridge]`, `[Outro]`) and structured style descriptions from a simple prompt.
- **Fast Response Latency**: Sends `thinking: {"type": "disabled"}` and `reasoning_effort: "none"` to bypass internal thinking delays on reasoning models (`deepseek-v4-flash`), delivering drafts in under 3 seconds.
- **Resilient JSON Parsing**: Multi-stage parser handles closed code fences, unclosed code fences, and raw JSON objects.

### ⚡ RunPod Serverless GPU Inference
- Asynchronous worker queue connected to the RunPod Serverless worker ([sruckh/minmaxmusic3-serverless](https://github.com/sruckh/minmaxmusic3-serverless)).
- Handles job queueing, polling, and audio file downloading with automatic error handling.

### 💾 Song Library & Playback
- **SQLite Database**: Persists job states and song metadata in `/data/mm3.db` using WAL mode.
- **Local Timezone Display**: Creation timestamps automatically format in the user's local browser timezone.
- **Playback & Download**: Dedicated song playback page with audio player, lyrics display, style caption, seed replay, and a `← Back to history` button.

### 🔒 Secure Deployment
- **Zero Hardcoded Secrets**: Secrets (`RUNPOD_API_KEY`, `LLM_API_KEY`) are injected via Infisical Universal Auth machine identities.
- **Isolated Network Shape**: Container runs with zero published host ports behind Nginx Proxy Manager on Docker `shared_net`.

---

## Architecture

<p align="center">
  <img src="assets/readme/architecture.svg" alt="MiniMax Music 3 System Architecture" width="100%">
</p>

### Execution Flow
1. **User Request**: User enters a song concept into the AI assistant or fills in the generation form.
2. **AI Assistant (`POST /assistant`)**: Proxies to OmniRoute / LLM gateway with thinking disabled (`thinking: disabled`).
3. **Job Queue (`POST /jobs`)**: Validates input and stores a queued job in SQLite (`/data/mm3.db`).
4. **Background Worker**: Dequeues jobs, sends inference requests to RunPod GPU (`POST /runsync`), downloads WAV audio to `/data/audio/`, and updates the database.
5. **htmx Polling**: Browser polls `GET /jobs/{id}` and updates the player once generation is complete.

---

## Quick Start

### Prerequisites
- **Docker** &amp; **Docker Compose**
- Infisical environment configuration &amp; client secret

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
time=2026-08-15T19:04:58.009Z level=INFO msg="config loaded" summary="addr=:8080 web=/app/web db=/data/mm3.db audio=/data/audio in_flight=2 runpod_endpoint=set runpod_key=true llm_base=set llm_model=set llm_key=true llm_thinking=disabled llm_reasoning_effort=none"
time=2026-08-15T19:04:58.012Z level=INFO msg=listening addr=:8080
```

---

## API Reference

| Endpoint | Method | Description |
|---|---|---|
| `GET /` | `GET` | Web console homepage with generation form &amp; assistant panel. |
| `POST /assistant` | `POST` | AI assistant proxy to draft tagged lyrics and style caption. |
| `POST /jobs` | `POST` | Validate form and queue a text-to-song generation job. |
| `GET /jobs/{id}` | `GET` | htmx polling endpoint returning job status or player HTML. |
| `GET /history` | `GET` | Paginated song library sorted newest-first with local timestamps. |
| `GET /songs/{id}` | `GET` | Playback detail page with lyrics, caption, seed, and history navigation. |
| `POST /songs/{id}/regenerate` | `POST` | Re-queue generation job using the same inputs and seed. |
| `GET /audio/{id}` | `GET` | Stream or download generated WAV audio file. |
| `GET /healthz` | `GET` | Healthcheck endpoint (`200 OK`). |

---

## Configuration

| Environment Variable | Default Value | Description |
|---|---|---|
| `MM3_ADDR` | `:8080` | Server listen address. |
| `MM3_PUBLIC_URL` | `https://mm3.gemneye.xyz` | Public application URL. |
| `MM3_WEB_DIR` | `/app/web` | Directory containing web templates and static assets. |
| `MM3_DB_PATH` | `/data/mm3.db` | SQLite database file path. |
| `MM3_AUDIO_DIR` | `/data/audio` | Output directory for audio WAV files. |
| `MM3_MAX_IN_FLIGHT` | `2` | Global concurrent job limit. |
| `LLM_BASE_URL` | *(Infisical)* | OpenAI-compatible LLM gateway URL. |
| `LLM_API_KEY` | *(Infisical)* | LLM authorization key. |
| `LLM_MODEL_ID` | *(Infisical)* | LLM model ID (e.g. `deepseek-v4-flash`). |
| `LLM_THINKING` | `disabled` | LLM thinking mode (`disabled`, `enabled`, `off`). |
| `LLM_REASONING_EFFORT` | `none` | LLM reasoning effort (`none`, `low`, `medium`, `high`). |
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
