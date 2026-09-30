#!/bin/sh
# Secure bring-up: decrypt the bootstrap client secret into /dev/shm (RAM
# tmpfs — never the persistent disk), let Compose bind-mount it read-only at
# /run/secrets, and start the stack. The /dev/shm source MUST stay in place
# while the container exists (Docker bind-mounts resolve by source path).
# It is gone on every host reboot — simply re-run this script after boot.
#
# --build is not optional. compose.yml sets both `build:` and
# `image: mm3-web:latest`, so Compose reuses the tagged image whenever it
# already exists and --force-recreate then rebuilds only the *container*:
# without --build this script restarts the old binary and the old templates
# and still reports healthy. With an unchanged tree the layer cache makes it
# nearly free; when something changed, `FROM test AS build` reruns the suite
# and refuses to produce a runtime image if it fails. Set MM3_SKIP_BUILD=1 for
# a plain restart — passing --no-build cannot work, Compose rejects it
# alongside --build.
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=env.sh
. "$SCRIPT_DIR/env.sh"

if [ "${MM3_SKIP_BUILD:-}" = "1" ]; then
	docker compose up -d --force-recreate "$@"
else
	docker compose up -d --build --force-recreate "$@"
fi

i=0
until docker exec mm3-app test -r /run/secrets/infisical_client_secret 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -ge 30 ] && { echo "up.sh: secret still not readable after 30s" >&2; exit 1; }
	sleep 1
done
echo "up.sh: stack running; client secret mounted read-only (source in tmpfs, RAM only)" >&2

# Administrator credentials are the one omission the health check cannot
# catch: without ADMIN_USER and ADMIN_PASSWORD the app starts, serves, and
# reports healthy with no administrator, so no registration can ever be
# approved. Say so here rather than leaving it to be discovered later. Only
# the presence flag is ever read — no value is printed.
i=0
until docker logs mm3-app 2>&1 | grep -q 'msg="config loaded"'; do
	i=$((i + 1))
	[ "$i" -ge 30 ] && { echo "up.sh: app has not logged its config after 30s" >&2; break; }
	sleep 1
done
if docker logs mm3-app 2>&1 | grep -q 'admin_login=true'; then
	echo "up.sh: administrator sign-in enabled (admin_login=true)" >&2
else
	echo "up.sh: WARNING — administrator sign-in is DISABLED." >&2
	echo "up.sh:   ADMIN_USER and ADMIN_PASSWORD must both be set in Infisical" >&2
	echo "up.sh:   (project mini-max-music3-z96r, env ${INFISICAL_ENV:-dev})." >&2
	echo "up.sh:   Until they are, nobody can approve a registration." >&2
	echo "up.sh:   See README.md § Authentication & Administration." >&2
fi

# Off-host replication is the only copy of the data that survives a lost
# volume, and nothing else says so when it is not working: the app is healthy
# either way. The first pass must reach the bucket within a minute of start.
# Only log lines and a marker file are read — the replica credentials live in
# the entrypoint's environment and are not visible to `docker exec`.
i=0
until docker logs mm3-litestream 2>&1 | grep -q 'compaction complete'; do
	i=$((i + 1))
	[ "$i" -ge 60 ] && break
	sleep 1
done
if docker logs mm3-litestream 2>&1 | grep -q 'compaction complete' \
	&& ! docker logs mm3-litestream 2>&1 | grep -q 'level=ERROR'; then
	echo "up.sh: database replication to the bucket is running" >&2
else
	echo "up.sh: WARNING — database replication is NOT confirmed." >&2
	echo "up.sh:   Check MM3_LS_* in Infisical (env ${INFISICAL_ENV:-dev}), then:" >&2
	echo "up.sh:   docker logs mm3-litestream --tail 30" >&2
fi
i=0
until docker exec mm3-files-sync test -f /tmp/files-sync.ok 2>/dev/null; do
	i=$((i + 1))
	[ "$i" -ge 60 ] && break
	sleep 1
done
if docker exec mm3-files-sync test -f /tmp/files-sync.ok 2>/dev/null; then
	echo "up.sh: audio/uploads sync to the bucket completed a pass" >&2
else
	echo "up.sh: WARNING — audio/uploads sync has not completed a pass." >&2
	echo "up.sh:   docker logs mm3-files-sync --tail 30" >&2
fi
