# Deployment — stage 07 output

## Decision (recorded per contract step 3)

**Runtime secret delivery: the timbre entrypoint method** (Docker Entrypoint
per the Global Agent Rules), already human-endorsed by adoption when the
timbre precedent was introduced. Rationale and trade-offs in
`../../references/infisical-entrypoint-pattern.md`. The dub ADR's `docker
inspect` concern is resolved: the client secret is a Compose secret sourced
from a 0600 `/dev/shm` file and mounted read-only at `/run/secrets`; Compose
carries only the non-secret client id/project/env. The five app secrets
(LLM_API_KEY, LLM_BASE_URL, LLM_MODEL_ID, RUNPOD_API_KEY,
RUNPOD_ENDPOINT) never appear in compose, `docker inspect`, or any image
layer — they arrive via `infisical run` at process start. The entrypoint
unsets bootstrap identity and the minted token before exec'ing Go.

## Artifacts

- `Dockerfile` — five stages: `test` (runs `go test ./...` in the build),
  `build` (CGO-free binary + Tailwind v4 CSS, unminified on purpose so the
  palette check holds), `secretbase` (alpine 3.21 + Infisical CLI +
  entrypoint), `runtime` (non-root `mm3` user, `/data` volume, `EXPOSE
  8080` only, healthcheck on `/healthz`).
- `docker/entrypoint.sh` — verbatim timbre pattern; bare start when no
  identity is configured (dev convenience).
- `docker-compose.yml` — `expose`, **no `ports:`**, `shared_net`
  (external), named `data` volume, machine identity via shell env, `tools`
  profile for the test image.
- `scripts/env.sh` — sources the non-secret identity and decrypts the client
  secret into a 0600 file in `/dev/shm` (RAM only), exporting only its path.
- `scripts/up.sh` — recommended bring-up: sources env.sh, lets Compose mount
  the tmpfs file read-only at `/run/secrets/infisical_client_secret`, starts
  the stack, then unlinks the host path via a trap. The entrypoint removes
  bootstrap identity/token variables before exec'ing the Go process.
- `scripts/seed-infisical-secret.sh` — one-time seed of the gpg store
  (already executed with the real secret, verified end-to-end against
  `https://secrets.gemneye.xyz` on 2026-08-14).

## NPM step (manual, once)

Proxy host `mm3.gemneye.xyz` → `http://mm3-app:8080` (scheme http on the
forward side; Cloudflare terminates TLS in front and the app sets
`MM3_PUBLIC_URL=https://…` for any absolute URLs). Block common exploits on;
websocket support unnecessary.

## Boot lessons (live, 2026-08-15)

- Docker bind-mounts resolve by **source path**: the `/dev/shm` secret source
  must persist for the container's lifetime (it is gone on reboot; re-run
  `scripts/up.sh` after every boot). `up.sh` waits for the mount to be
  readable inside the container before returning.
- The app runs as non-root (`mm3`), so the secret source must be mode
  **0644** (tmpfs + RAM-only bounds the exposure; the plaintext never touches
  the persistent disk).
- `infisical run` forwards `INFISICAL_TOKEN` into the child environment.
  The launcher written by the entrypoint (`mktemp` + `unset` + `exec`) runs
  inside `infisical run`'s child shell, so the final app process carries
  **no** token — verified in `/proc/<pid>/environ`: the Go process holds
  only the five app secrets; the wrapper holds only the injection token.
- NPM reaches `mm3-app:8080` on shared_net; no published host ports.

## Verification (stage 08 executes these)

`docker inspect mm3-app` → `shared_net`, zero published host ports; NPM
container reaches `http://mm3-app:8080/healthz`; app secrets absent from
`docker inspect` output except the machine identity; `docker compose run
--rm test` exits 0.
