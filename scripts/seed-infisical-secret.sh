#!/usr/bin/env bash
# seed-infisical-secret.sh — one-time seed of the Infisical client secret
# for minmaxmusic3-web into a project-owned gpg store.
#
# Pattern follows the host convention (shlink/dub precedents): the secret
# lives ONLY as gpg ciphertext at ~/.config/mm3-web-infisical/client_secret.gpg,
# encrypted to a project-owned no-passphrase key (uid mm3-web-infisical).
# Nothing here contacts the network.
#
# Usage:
#   scripts/seed-infisical-secret.sh <secret>        # from argument
#   scripts/seed-infisical-secret.sh --file <path>   # first line of file
#   echo <secret> | scripts/seed-infisical-secret.sh # from stdin
#   scripts/seed-infisical-secret.sh --verify        # round-trip check
#                                                  (prints sha256 prefix only)
set -euo pipefail

PROFILE_DIR="${HOME}/.config/mm3-web-infisical"
KEYRING="${PROFILE_DIR}/gnupg"
CIPHER="${PROFILE_DIR}/client_secret.gpg"
ENV_FILE="${PROFILE_DIR}/infisical.env"
UID_NAME="mm3-web-infisical"

# Non-secret identity parameters (client id / project id are not secrets).
# The Infisical host identifies this deployment, so it comes from the
# operator's environment rather than being committed here.
: "${INFISICAL_HOST:?"export INFISICAL_HOST=<infisical-base-url> before seeding"}"
INFISICAL_CLIENT_ID="e71fea5c-499d-4b9d-a91c-086ca9abcfc0"
INFISICAL_PROJECT_ID="bd1ff4df-ddf9-4037-a183-4d7a92b232f4"
INFISICAL_ENV="dev"

die() { echo "seed-infisical-secret: $*" >&2; exit 1; }

gpg() { command gpg --homedir "$KEYRING" --batch --yes --pinentry-mode loopback "$@"; }

ensure_keyring() {
  [ -d "$KEYRING" ] || mkdir -p "$KEYRING"
  chmod 700 "$KEYRING"
  if ! gpg --list-keys 2>/dev/null | grep -q "$UID_NAME"; then
    echo "seed-infisical-secret: creating project key (uid ${UID_NAME}, rsa3072, no passphrase)" >&2
    PARAMS="$(mktemp)"
    cat > "$PARAMS" <<EOF
%no-protection
Key-Type: RSA
Key-Length: 3072
Name-Real: ${UID_NAME}
Key-Usage: encrypt
Expire-Date: 0
%commit
EOF
    gpg --generate-key --batch "$PARAMS" 2>/dev/null
    rm -f "$PARAMS"
    gpg --list-keys 2>/dev/null | grep -q "$UID_NAME" || die "key generation failed"
  fi
}

write_env_file() {
  [ -d "$PROFILE_DIR" ] || mkdir -p "$PROFILE_DIR"
  chmod 700 "$PROFILE_DIR"
  # The file is the operator-values store (see scripts/env.sh). Preserve a
  # previously recorded public URL across re-seeding unless overridden now.
  OLD_URL="$(sed -n 's/^MM3_PUBLIC_URL=//p' "$ENV_FILE" 2>/dev/null || true)"
  MM3_PUBLIC_URL="${MM3_PUBLIC_URL:-$OLD_URL}"
  cat > "$ENV_FILE" <<EOF
INFISICAL_HOST=${INFISICAL_HOST}
INFISICAL_CLIENT_ID=${INFISICAL_CLIENT_ID}
INFISICAL_PROJECT_ID=${INFISICAL_PROJECT_ID}
INFISICAL_ENV=${INFISICAL_ENV}
EOF
  if [ -n "$MM3_PUBLIC_URL" ]; then
    printf 'MM3_PUBLIC_URL=%s\n' "$MM3_PUBLIC_URL" >> "$ENV_FILE"
  fi
  chmod 600 "$ENV_FILE"
}

read_secret() {
  if [ "${1:-}" = "--file" ]; then
    [ -r "${2:-}" ] || die "--file: unreadable path"
    IFS= read -r SECRET < "$2"
  elif [ -n "${1:-}" ]; then
    SECRET="$1"
  elif [ ! -t 0 ]; then
    IFS= read -r SECRET
  else
    die "no secret given: pass an argument, --file <path>, or stdin"
  fi
  [ -n "$SECRET" ] || die "secret is empty"
}

main() {
  if [ "${1:-}" = "--verify" ]; then
    [ -f "$CIPHER" ] || die "no ciphertext at ${CIPHER} — seed first"
    PLAIN="$(gpg --decrypt "$CIPHER" 2>/dev/null || die "decrypt failed")"
  else
    ensure_keyring
    write_env_file
    read_secret "$@"
    PLAIN="$SECRET"
    printf '%s' "$PLAIN" | gpg --encrypt --recipient "$UID_NAME" --output "$CIPHER" 2>/dev/null
    chmod 600 "$CIPHER"
    # prove the round-trip before declaring success
    CHECK="$(gpg --decrypt "$CIPHER" 2>/dev/null || die "round-trip decrypt failed")"
    [ "$CHECK" = "$PLAIN" ] || die "round-trip mismatch"
  fi
  # print only a sha256 prefix — never the secret
  printf 'seed-infisical-secret: OK (secret sha256 %s…)\n' \
    "$(printf '%s' "$PLAIN" | sha256sum | cut -c1-12)"
  printf 'seed-infisical-secret: ciphertext %s\n' "$CIPHER"
}

main "$@"
