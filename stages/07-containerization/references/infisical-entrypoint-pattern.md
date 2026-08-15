# Infisical runtime injection — hardened timbre pattern

Based on `/opt/docker/moss-tts-v1.5-web` (Go + NPM + shared_net), then
hardened by this gauntlet: app secrets arrive through `infisical run`, while
the Universal Auth client secret is a mounted Compose secret — never an
environment variable visible to `docker inspect`.

## The four pieces

1. **Encrypted host store (durable)**
   - `~/.config/mm3-web-infisical/infisical.env`: non-secret host, client id,
     project id, env `dev` (0600).
   - `client_secret.gpg`: ciphertext encrypted to the project-owned keyring
     (seeded by `scripts/seed-infisical-secret.sh`). No plaintext on disk.

2. **Secure bring-up (`scripts/up.sh`)**
   - Sources `scripts/env.sh`, which decrypts into a 0600 file under
     `/dev/shm` (tmpfs/RAM) and exports only the file path.
   - Compose mounts it read-only at
     `/run/secrets/infisical_client_secret`.
   - After `docker compose up -d`, a trap unlinks the host tmpfs path.
     Re-run `scripts/up.sh` after host reboot/container recreation.

3. **Image (`secretbase` + entrypoint)**
   - Alpine shell + CA certificates.
   - Infisical CLI copied from immutable vendor-image digest
     `infisical/cli@sha256:4fd22fff…`, not a curl|sh installer.
   - Entrypoint reads `/run/secrets/...`, logs in with
     `--method=universal-auth --plain --silent`, then unsets client
     id/secret-file variables.

4. **Process handoff**
   ```sh
   exec infisical run --projectId="$INFISICAL_PROJECT_ID" \
     --env="$INFISICAL_ENV" --domain="$INFISICAL_DOMAIN" --silent -- \
     sh -c 'unset INFISICAL_TOKEN; exec "$@"' sh "$@"
   ```
   App secrets (LLM_API_KEY, LLM_BASE_URL, LLM_MODEL_ID,
   RUNPOD_API_KEY, RUNPOD_ENDPOINT) enter the intermediate shell via
   `infisical run`; the minted access token is removed before Go execs.

## Compose invariants

- `expose: ["8080"]` only — no `ports:`; external `shared_net`; NPM fronts
  `mm3-app:8080`; Cloudflare terminates TLS.
- Environment carries only non-secret identity metadata + app config.
- Client secret uses top-level `secrets:` file source; app secrets never
  appear in compose, image layers, or `docker inspect`.
- Named `/data` volume persists SQLite WAL + audio.

## Project parameters

Host `https://secrets.gemneye.xyz` · project
`bd1ff4df-ddf9-4037-a183-4d7a92b232f4` / `mini-max-music3-z96r` · env
**dev** · client id `e71fea5c-499d-4b9d-a91c-086ca9abcfc0`.
