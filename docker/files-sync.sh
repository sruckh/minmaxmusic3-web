#!/bin/sh
# Copy everything under /data that litestream does not cover — generated
# audio and staged uploads — to the bucket, on a timer.
#
# `copy`, never `sync`: sync would delete bucket objects that are absent
# locally, so one mistakenly empty volume would erase the backup. The price is
# that audio for deleted songs stays in the bucket; a bucket lifecycle rule is
# the place to age it out.
#
# --min-age skips a file the app is still writing. Songs are written once and
# never modified, so a partial upload is never re-read.
set -eu

: "${MM3_LS_BUCKET:?MM3_LS_BUCKET is not set — secrets were not injected}"
: "${MM3_LS_ENDPOINT:?MM3_LS_ENDPOINT is not set}"
: "${MM3_LS_REGION:?MM3_LS_REGION is not set}"
: "${MM3_LS_KEY_ID:?MM3_LS_KEY_ID is not set}"
: "${MM3_LS_APP_KEY:?MM3_LS_APP_KEY is not set}"

. /usr/local/bin/rclone-env.sh

INTERVAL="${MM3_FILES_SYNC_INTERVAL:-900}"

while :; do
	if rclone copy /data "mm3:${MM3_LS_BUCKET}/mm3/files" \
		--exclude 'mm3.db*' --exclude '.mm3.db-litestream/**' \
		--min-age 30s --transfers 4 --log-level NOTICE; then
		date +%s > /tmp/files-sync.ok
	else
		echo "files-sync: pass failed; retrying in ${INTERVAL}s" >&2
	fi
	sleep "$INTERVAL"
done
