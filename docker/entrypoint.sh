#!/bin/sh
# Inject app secrets from Infisical, then hand off to the app.
#
# Bootstrap secret: a read-only Compose secret at
# /run/secrets/infisical_client_secret — never an environment variable, so
# it is absent from docker inspect and image layers. The host helper creates
# its source in /dev/shm and unlinks it after `docker compose up`.
#
# App secrets (LLM_*, RUNPOD_*) are injected by `infisical run`. A final
# shell unsets the minted token before exec'ing the Go process.
set -eu

if [ -z "${INFISICAL_CLIENT_ID:-}" ]; then
	echo "entrypoint: no Infisical identity configured; starting without secret injection" >&2
	exec "$@"
fi

: "${INFISICAL_DOMAIN:?INFISICAL_DOMAIN is required when an identity is set}"
: "${INFISICAL_PROJECT_ID:?INFISICAL_PROJECT_ID is required when an identity is set}"
: "${INFISICAL_ENV:?INFISICAL_ENV is required when an identity is set}"
: "${INFISICAL_CLIENT_SECRET_FILE:?INFISICAL_CLIENT_SECRET_FILE is required}"
[ -r "$INFISICAL_CLIENT_SECRET_FILE" ] || {
	echo "entrypoint: client-secret file is not readable" >&2
	exit 1
}

BOOTSTRAP_VALUE="$(cat "$INFISICAL_CLIENT_SECRET_FILE")"
[ -n "$BOOTSTRAP_VALUE" ] || {
	echo "entrypoint: client-secret file is empty" >&2
	exit 1
}

echo "entrypoint: authenticating to Infisical at ${INFISICAL_DOMAIN} (${INFISICAL_ENV})" >&2
INFISICAL_TOKEN="$(infisical login \
	--method=universal-auth \
	--client-id="${INFISICAL_CLIENT_ID}" \
	--client-secret="${BOOTSTRAP_VALUE}" \
	--domain="${INFISICAL_DOMAIN}" \
	--plain --silent)"
export INFISICAL_TOKEN

# Bootstrap credentials have done their job. They must not reach the app.
unset BOOTSTRAP_VALUE INFISICAL_CLIENT_ID INFISICAL_CLIENT_SECRET INFISICAL_CLIENT_SECRET_FILE

echo "entrypoint: injecting ${INFISICAL_ENV} app secrets and starting" >&2
# Write a launcher that unsets the minted token inside the SAME shell that
# execs the app. `infisical run` forwards its own env (including
# INFISICAL_TOKEN) to the child — only an unset in the child's own shell,
# immediately before exec, removes it from the app's process environment.
LAUNCHER="$(mktemp /tmp/mm3-launch.XXXXXX)"
printf '%s\n' '#!/bin/sh' 'unset INFISICAL_TOKEN' 'exec "$@"' > "$LAUNCHER"
chmod 700 "$LAUNCHER"
exec infisical run \
	--projectId="${INFISICAL_PROJECT_ID}" \
	--env="${INFISICAL_ENV}" \
	--domain="${INFISICAL_DOMAIN}" \
	--silent \
	-- "$LAUNCHER" "$@"
