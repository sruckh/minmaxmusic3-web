# Glossary — minmaxmusic3-web

Domain terms used across stages (Layer 3). Define a term once here; other
files link here rather than re-defining it.

- **lyrics / `input`** — words to be sung, with bracketed section tags
  (`[Intro] [Verse] [Pre-Chorus] [Chorus] [Post-Chorus] [Bridge]
  [Instrumental] [Solo] [Outro]`), each tag alone on its own line.
- **style caption / `instructions`** — structured music description with
  three headings: Global Metadata, Vocal Details, Arrangement (~250–450 words).
- **audio_duration** — requested song length in seconds; upper bound, max 300.
- **seed** — integer for reproducible generation.
- **RunPod worker** — the serverless inference backend
  (github.com/sruckh/minmaxmusic3-serverless); called async via `POST /run`,
  polled via `GET /status/{id}` (`POST /runsync` is the blocking smoke-test
  variant — never in the request path).
- **delivery** — how audio returns: `s3` (presigned URL) or inline `base64`.
- **AI assistant** — in-app LLM helper (Infisical `LLM_*` vars) that drafts
  lyrics + caption from a rough idea; see
  `shared/llm-assistant-system-prompt.md`.
- **shared_net** — pre-existing external Docker network; NPM and this
  container are both attached; zero published host ports.
- **NPM** — Nginx Proxy Manager, the sole ingress (`app.example.invalid`).
- **Infisical** — secrets/config store at `https://infisical.example.invalid`,
  project `mini-max-music3-z96r`, env `dev`.
