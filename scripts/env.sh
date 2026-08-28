#!/bin/sh
# Source operator values (non-secret Infisical identity values and the
# deployment's public URL — hosts never enter the repository) and create an
# ephemeral Compose-secret source in /dev/shm:
#
#   . scripts/env.sh
#   docker compose up -d
#   mm3_env_cleanup
#
# Prefer scripts/up.sh, which wraps those three steps with a trap.

INFISICAL_CONFIG="${INFISICAL_CONFIG:-$HOME/.config/mm3-web-infisical/infisical.env}"
INFISICAL_SECRET_GPG="${INFISICAL_SECRET_GPG:-$HOME/.config/mm3-web-infisical/client_secret.gpg}"

if [ ! -f "$INFISICAL_CONFIG" ]; then
	echo "env.sh: $INFISICAL_CONFIG not found" >&2
	return 1 2>/dev/null || exit 1
fi
if [ ! -f "$INFISICAL_SECRET_GPG" ]; then
	echo "env.sh: $INFISICAL_SECRET_GPG not found" >&2
	return 1 2>/dev/null || exit 1
fi

INFISICAL_HOST="$(sed -n 's/^INFISICAL_HOST=//p' "$INFISICAL_CONFIG")"
INFISICAL_CLIENT_ID="$(sed -n 's/^INFISICAL_CLIENT_ID=//p' "$INFISICAL_CONFIG")"
INFISICAL_PROJECT_ID="$(sed -n 's/^INFISICAL_PROJECT_ID=//p' "$INFISICAL_CONFIG")"
INFISICAL_ENV="$(sed -n 's/^INFISICAL_ENV=//p' "$INFISICAL_CONFIG")"
export INFISICAL_HOST INFISICAL_CLIENT_ID INFISICAL_PROJECT_ID
export INFISICAL_ENV="${INFISICAL_ENV:-dev}"

# The canonical public origin, kept in the same operator file so bring-up
# needs no remembered exports. Optional: without it the app trusts the
# request's own Host, which a correctly forwarded proxy already satisfies.
MM3_PUBLIC_URL="$(sed -n 's/^MM3_PUBLIC_URL=//p' "$INFISICAL_CONFIG")"
if [ -n "$MM3_PUBLIC_URL" ]; then
	export MM3_PUBLIC_URL
fi

# Plaintext lives only in RAM (tmpfs). Mode 0644 so the container's
# non-root mm3 user can read the bind-mounted secret.
umask 077
INFISICAL_CLIENT_SECRET_FILE="${INFISICAL_CLIENT_SECRET_FILE:-/dev/shm/mm3-infisical-client-secret.$$}"
gpg --batch --quiet --yes --pinentry-mode loopback \
	--homedir "$(dirname "$INFISICAL_SECRET_GPG")/gnupg" \
	--output "$INFISICAL_CLIENT_SECRET_FILE" \
	--decrypt "$INFISICAL_SECRET_GPG"
chmod 644 "$INFISICAL_CLIENT_SECRET_FILE"
export INFISICAL_CLIENT_SECRET_FILE

mm3_env_cleanup() {
	# Only for manual teardown — NOT called after `up`, because the Docker
	# bind mount resolves by source path and must stay readable.
	rm -f "${INFISICAL_CLIENT_SECRET_FILE:-}"
	unset INFISICAL_CLIENT_SECRET_FILE INFISICAL_CLIENT_SECRET
}

echo "env.sh: Infisical identity ${INFISICAL_CLIENT_ID} -> ${INFISICAL_HOST} (${INFISICAL_ENV}); bootstrap secret in tmpfs" >&2
