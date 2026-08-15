# RunPod Worker API — MiniMax Music 3 inference backend

Canonical contract for the serverless worker at
github.com/sruckh/minmaxmusic3-serverless. The Go server is the only caller.

## Endpoint

- Base: `RUNPOD_ENDPOINT` (Infisical, dev) — e.g. `https://api.runpod.ai/v2/<id>`
- Auth: `Authorization: Bearer {RUNPOD_API_KEY}` (Infisical, dev)
- Response size caps: RunPod limits `/runsync`/`/run` responses to 20 MB —
  prefer s3 delivery.

## Async flow (primary — what the app uses)

Cloudflare caps proxied requests at ~100 s; a 300 s generation cannot ride a
synchronous request. Use RunPod's platform-level async API (available on
every serverless endpoint; verify live in stage 08):

1. `POST /run` with the same `{"input": {...}}` body → returns immediately:
   `{"id": "<job-id>", "status": "IN_QUEUE"}`.
2. `GET /status/{id}` → `{"id", "status": "IN_QUEUE" | "IN_PROGRESS" |
   "COMPLETED" | "FAILED" | "CANCELLED", "output": {...} | null,
   "error": ... }`. Poll from the Go background worker (e.g. every 3 s,
   backoff to 10 s), never from the browser.
3. On `COMPLETED`, `output` has the same shape as `/runsync`'s `output`
   (branch on `delivery`). On `FAILED`, surface `error`.
4. `POST /cancel/{id}` aborts an in-flight job (used for admin/timeout).

## Sync flow (smoke tests only)

`POST /runsync` with the same body blocks until the song is ready. **Never
in the browser request path** (proxy timeout). Fine for short offline tests.

## Request

```json
{
  "input": {
    "input": "[Verse]\nMorning light filtering through the pine\n[Chorus]\nSoftly the world begins to breathe",
    "instructions": "Global Metadata: acoustic pop, 96 BPM, C major, warm and intimate building to a wide final chorus. Vocal Details: soft female lead, close and breathy, stacked harmonies in the chorus. Arrangement: fingerpicked guitar and soft piano; brushed drums and upright bass enter at the chorus.",
    "audio_duration": 30.0,
    "seed": 7
  }
}
```

| Field | Type | Rules |
|-------|------|-------|
| `input` | string | Lyrics with section tags (`[Intro] [Verse] [Pre-Chorus] [Chorus] [Post-Chorus] [Bridge] [Instrumental] [Solo] [Outro]`), each tag alone on its line |
| `instructions` | string | Music description; Structured Caption (Global Metadata / Vocal Details / Arrangement) gives most control; tokenized prompt capped at 5,000 tokens |
| `audio_duration` | float | Seconds; upper bound; ≤ 300 (model caps at 9,000 acoustic frames) |
| `seed` | int | Reproducibility |

## Response — success

```json
{
  "status": "success",
  "output": {
    "delivery": "s3",
    "audio_url": "https://…?X-Amz-Signature=…",
    "url_expires_in": 86400,
    "format": "wav", "sampling_rate": 32000, "channels": 2,
    "duration": 30.0, "num_frames": 750, "seed": 7,
    "model": "MiniMaxAI/MiniMax-Music3", "engine": "diffusers"
  }
}
```

Branch on `output.delivery`:

- **`s3`** — presigned `audio_url` (+ `url_expires_in`, `url_expires_at`,
  `bucket`, `key`, `size_bytes`). Fetch/store the audio before expiry.
- **`base64`** — inline audio in the response; only ~105 s of 32 kHz stereo
  fits the 18 MB `MAX_RESPONSE_BYTES` budget. Treat as fallback.

## Response — failure

`status` ≠ `"success"` (e.g. worker error, validation failure): map the
message to the error taxonomy in stage 02's output. Client timeout must
cover 300 s audio generation plus margin.

## Operational notes

- Engines: `diffusers` / `sglang-omni` (response echoes which served).
- Output audio: 32 kHz, 16-bit, stereo WAV.
- The worker is a separate deployable; this app never reconfigures it —
  read-only consumer.
