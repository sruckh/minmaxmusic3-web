#!/bin/sh
# Secure bring-up: decrypt the bootstrap client secret into /dev/shm (RAM
# tmpfs — never the persistent disk), let Compose bind-mount it read-only at
# /run/secrets, and start the stack. The /dev/shm source MUST stay in place
# while the container exists (Docker bind-mounts resolve by source path).
# It is gone on every host reboot — simply re-run this script after boot.
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=env.sh
. "$SCRIPT_DIR/env.sh"

docker compose up -d --force-recreate "$@"

i=0
until docker exec mm3-app test -r /run/secrets/infisical_client_secret 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -ge 30 ] && { echo "up.sh: secret still not readable after 30s" >&2; exit 1; }
	sleep 1
done
echo "up.sh: stack running; client secret mounted read-only (source in tmpfs, RAM only)" >&2
