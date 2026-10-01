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
REMOTE="mm3:${MM3_LS_BUCKET}/mm3/files"

# verify reports what the bucket actually holds, which is the question "a pass
# completed" cannot answer: `rclone copy` exits 0 when it has nothing to do, so
# an empty prefix and a fully synced one look identical. Reading the prefix back
# is what turns the marker file into evidence.
#
# This runs inside the supervised process, which is the only one holding the
# replica credentials, so the listing needs no secret to be handed to anything
# else. The result is published to /tmp/files-sync.verify and the log — the two
# channels scripts/up.sh is allowed to read.
#
# Counts can lag by one pass and that is not a failure: `--min-age 30s` skips a
# song still being written, so a file created moments ago is local-only until
# the next pass. Only an empty prefix alongside local audio is alarming, and
# only that case goes to stderr.
#
# Every step is guarded. Verification is reporting, and a transient listing
# failure must not kill the sync loop that is doing the real work.
verify() {
	if ! listing=$(rclone lsl "${REMOTE}/audio" 2>/dev/null); then
		echo "files-sync: could not list the bucket to verify audio" >&2
		return 0
	fi
	n_remote=$(printf '%s\n' "$listing" | grep -c .) || n_remote=0
	b_remote=$(printf '%s\n' "$listing" | awk '{s += $1} END {printf "%d", s + 0}')
	n_local=$(find /data/audio -type f 2>/dev/null | wc -l) || n_local=0
	n_local=$(printf '%s' "$n_local" | tr -d '[:space:]')

	line="audio verified: bucket ${n_remote} object(s) ${b_remote} byte(s); local ${n_local} file(s)"
	echo "files-sync: ${line}"
	echo "$line" > /tmp/files-sync.verify

	if [ "$n_remote" -eq 0 ] && [ "$n_local" -gt 0 ]; then
		echo "files-sync: WARNING — ${n_local} audio file(s) locally but none in the bucket" >&2
	elif [ "$n_remote" -lt "$n_local" ]; then
		# Expected right after a song is written; picked up by the next pass.
		echo "files-sync: note — bucket is $((n_local - n_remote)) file(s) behind; a file newer than 30s waits for the next pass"
	fi
}

while :; do
	if rclone copy /data "$REMOTE" \
		--exclude 'mm3.db*' --exclude '.mm3.db-litestream/**' \
		--min-age 30s --transfers 4 --log-level NOTICE; then
		date +%s > /tmp/files-sync.ok
		verify
	else
		echo "files-sync: pass failed; retrying in ${INTERVAL}s" >&2
		rm -f /tmp/files-sync.verify
	fi
	sleep "$INTERVAL"
done
