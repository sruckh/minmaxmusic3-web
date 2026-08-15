# Acceptance report — minmaxmusic3-web (stage 08)

Date: 2026-08-15 · image: `mm3-web:latest` · stack: `mm3-app` on `shared_net`.
Every check lists command, exit code/evidence, or a skip with an owner.

## 1. Mechanical checks (offline)

| Check | Command | Result |
|---|---|---|
| Unit + integration tests | `go test ./...` | PASS — 22 tests, incl. stubbed end-to-end generation, ambiguity/restart recovery, in-flight cap, envelope + SSE decoding |
| Vet | `go vet ./...` | PASS (exit 0) |
| Format | `gofmt -l cmd internal` | PASS (empty) |
| ICM audit | `icm audit .` | PASS — "OK, conforms" |
| Design assets untouched | `sha256sum -c .goals/baseline.sha256` | PASS (4 files) |
| Palette exhaustive | `grep -oE '#[0-9A-Fa-f]{6}' web/static/app.css \| sort -u \| wc -l` | PASS — exactly 7 |
| Secret-leak scan | repo-wide regex scan (excluding `.goals`/`.outline`) | PASS — clean |
| ICM coverage greps | 21 required terms under stages/shared/setup | PASS — all present |

## 2. Container & deployment

| Check | Evidence | Result |
|---|---|---|
| Zero published host ports | `docker inspect mm3-app` → `NetworkSettings.Ports = {}` | PASS |
| On shared_net | `docker inspect` → network `shared_net` | PASS |
| Non-root | process user `mm3` (id 100) | PASS |
| Healthcheck responds | `GET /healthz` → `ok` | PASS |
| Tests gate the image | runtime stage `FROM test` → suite runs during build | PASS |
| Pinned base images | golang@digest, infisical-cli@digest, alpine:3.21 | PASS |

## 3. Secrets & Infisical

| Check | Evidence | Result |
|---|---|---|
| No secrets in compose/inspect env | `docker inspect` shows only `INFISICAL_DOMAIN` + `..._FILE` path | PASS |
| Client secret not in image layers | build uses only non-secret COPY; secret is a runtime mount | PASS |
| App process env clean of bootstrap | `/proc/<go pid>/environ` has no TOKEN/CLIENT_SECRET/CLIENT_ID | PASS |
| App secrets injected (5) | LLM_API_KEY, LLM_BASE_URL, LLM_MODEL_ID, RUNPOD_API_KEY, RUNPOD_ENDPOINT present in app env | PASS |
| Client secret mount | read-only mount at `/run/secrets/infisical_client_secret`, source in `/dev/shm` (RAM) | PASS |

## 4. Network ingress

| Check | Evidence | Result |
|---|---|---|
| NPM reaches app | `Host: mm3.gemneye.xyz` → `http://mm3-app:8080/` → 200 | PASS |
| Public HTTPS | `https://mm3.gemneye.xyz/healthz` → `ok`; `/` serves full page | PASS |

## 5. Live end-to-end (authorized)

- **Generation** — `POST /jobs` (acoustic folk, 30 s, seed 7) → worker submitted async → RunPod COMPLETED in ~6 min → player fragment → `GET /audio/{id}` returns a valid **RIFF/WAVE, 32 kHz/16-bit stereo, ~41.4 s**. Song persisted to history with engine badge (`diffusers`). PASS.
- **History** — generated song appears in `/history` with title/length/engine. PASS.
- **Assistant** — `POST /assistant` with a rough idea returned a complete
  parsed draft: tagged lyrics (`[Intro]…`), Global-Metadata-first structured
  caption, `audio_duration: 220`, `seed: 482917`. ~150 s wall clock (one
  120 s timeout + one successful retry — the one-retry policy is doing its
  job). Model id changed mid-acceptance by operator (previous model ran out
  of tokens). PASS.

## 6. Open / follow-ups

- **Assistant latency**: ~2.5 min for a full draft is slow; upstream model
  speed, not the app, is the bottleneck. Options if it matters: trim the
  system prompt, lower max_tokens, or stream. Owner: developer.
- **`docker compose build` interpolation**: `secrets.infisical_client_secret.file` requires `INFISICAL_CLIENT_SECRET_FILE` set; build commands must set the dummy value. Documented in deployment.md. Owner: developer.

## Residual gaps

None blocking. All stages 01–08 pass their contract checks, including all
live checks (real RunPod generation, real LLM assistant, public HTTPS).
