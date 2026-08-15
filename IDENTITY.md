# minmaxmusic3-web — ICM Workspace

> Layer 0 · "Where am I?"

This is an **Interpretable Context Methodology (ICM)** workspace. The folder
structure *is* the architecture: numbered folders are stages, markdown files
carry the prompts and context, and local scripts do the mechanical work.

**What this project is:** the web front-end for the **MiniMax Music 3** music
generation model. Go + htmx + Alpine.js + Tailwind v4; inference via a RunPod
serverless worker; an in-app AI assistant helps write lyrics and style
captions. Deployed as a Docker container on the `shared_net` network behind
Nginx Proxy Manager at `mm3.gemneye.xyz` — no published host ports.

**Domain:** minmaxmusic3-web

## Layers
- **Layer 0** — `IDENTITY.md` / `CLAUDE.md` (this file): "Where am I?"
- **Layer 1** — `CONTEXT.md`: "Where do I go?" (routing)
- **Layer 2** — `stages/NN-*/CONTEXT.md`: "What do I do?" (stage contracts)
- **Layer 3** — `_config/`, `shared/`, `references/`: "What rules apply?"
- **Layer 4** — `output/`: "What am I working with?"

## Ground rules
- Source of truth for the build: `.goals/goal-mm3-icm-scaffold.md`
  (verified facts, deliverables, success criteria).
- Design assets (`DESIGN.md`, `index-dark.html`, `index-light.html`,
  `favicon_io.zip`) are read-only inputs — never modify them.
- No secret literal ever appears in this repo; all config lives in Infisical
  (project `mini-max-music3-z96r`, env `dev`).
- Validate after every change: `python3 /root/.claude/skills/icm/scripts/audit <root>`.

## How to use
1. Walk stages with `icm run /opt/docker/minmaxmusic3-web`; edit each `output/` before the next stage.
2. Keep routing fresh with `icm sync /opt/docker/minmaxmusic3-web`.
