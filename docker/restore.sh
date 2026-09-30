#!/bin/sh
# One-shot, runs before the app starts: bring back whatever /data is missing
# from the off-host replica. A no-op when /data already holds a database.
#
# Fails CLOSED. If the bucket cannot be read, this exits non-zero and the app
# never starts: an app that came up on an empty database would give litestream
# a fresh, unrelated database to replicate over the real one.
#
# An empty bucket is not an error (first ever start): litestream's
# -if-replica-exists makes that a clean no-op.
set -eu

: "${MM3_LS_BUCKET:?MM3_LS_BUCKET is not set — secrets were not injected}"
: "${MM3_LS_ENDPOINT:?MM3_LS_ENDPOINT is not set}"
: "${MM3_LS_REGION:?MM3_LS_REGION is not set}"
: "${MM3_LS_KEY_ID:?MM3_LS_KEY_ID is not set}"
: "${MM3_LS_APP_KEY:?MM3_LS_APP_KEY is not set}"

. /usr/local/bin/rclone-env.sh

if [ -f /data/mm3.db ]; then
	echo "restore: /data/mm3.db exists; database left as is" >&2
else
	echo "restore: no database on the volume; looking for a replica" >&2
	litestream restore -config /etc/litestream.yml \
		-if-db-not-exists -if-replica-exists /data/mm3.db
	[ -f /data/mm3.db ] && echo "restore: database restored from the replica" >&2
fi

# Audio and uploaded recordings. Only when the audio directory is missing;
# `copy` never deletes, so it cannot take a file away from a live volume.
if [ -d /data/audio ]; then
	echo "restore: /data/audio exists; files left as is" >&2
else
	echo "restore: no audio directory; copying files from the replica" >&2
	rclone copy "mm3:${MM3_LS_BUCKET}/mm3/files" /data --log-level NOTICE
fi
