# Stage 07 — containerization

> Layer 2 · "What do I do?"

**Purpose:** Containerize the app for the host's deployment shape and wire
runtime secrets from Infisical — no ports on the host, NPM-only ingress.

## Inputs
| Source | File/Location | Section/Scope | Why |
|--------|---------------|---------------|-----|
| Storage notes | ../06-song-history/output/history.md | persistence | volume mounts |
| Goal spec | ../../.goals/goal-mm3-icm-scaffold.md | Infisical + deploy | the contract |
| Conventions | ../../_config/conventions.md | secrets rule | no literals |

## Process
1. Multi-stage `Dockerfile`: tests → CGO-free Go build + Tailwind v4 CSS →
   alpine runtime; `EXPOSE` only (no `ports:` mapping ever).
2. `docker-compose.yml`: service on pre-existing external `shared_net`;
   named `/data` volume; never a `.env` file.
3. **Runtime secret delivery — hardened timbre entrypoint pattern**
   (`references/infisical-entrypoint-pattern.md`): client secret decrypted
   to `/dev/shm`, mounted read-only at `/run/secrets`, then unlinked;
   entrypoint mints a token via `infisical login --plain`, unsets bootstrap
   identity, and `exec infisical run`; inner shell removes the token before
   Go execs. CLI copied from an immutable vendor-image digest (no curl|sh).
   Project `mini-max-music3-z96r` (id
   `bd1ff4df-ddf9-4037-a183-4d7a92b232f4`), env **dev**, host
   `https://secrets.gemneye.xyz`.
4. Bring-up script `scripts/up.sh` wraps tmpfs secret creation, compose up,
   and cleanup; `scripts/env.sh` remains sourceable for operators.
5. NPM proxy host `mm3.gemneye.xyz` → `http://<service>:<port>` (document
   the manual NPM step; Cloudflare→NPM TLS per host convention).

## Outputs
| Artifact | Location | Format |
|----------|----------|--------|
| Dockerfile + compose + up script | ../../Dockerfile, ../../docker-compose.yml, ../../scripts/ | docker/shell |
| Deployment notes | output/deployment.md | markdown |

## Checkpoints
- [ ] Human approves the secret-delivery decision before the image is built.

## Audits
- [ ] `docker inspect` → shared_net, zero published host ports.
- [ ] No secret in any image layer, compose file, or script.
- [ ] NPM reaches the service by name on shared_net.
