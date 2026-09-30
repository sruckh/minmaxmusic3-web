# Sourced by restore.sh and files-sync.sh. Defines the rclone remote `mm3`
# from the injected MM3_LS_* values, so nothing is written to an rclone.conf.
# no_check_bucket: the application key is scoped to the bucket and cannot
# create one, which rclone would otherwise try before the first upload.
export RCLONE_CONFIG_MM3_TYPE=s3
export RCLONE_CONFIG_MM3_PROVIDER=Other
export RCLONE_CONFIG_MM3_ENDPOINT="$MM3_LS_ENDPOINT"
export RCLONE_CONFIG_MM3_REGION="$MM3_LS_REGION"
export RCLONE_CONFIG_MM3_ACCESS_KEY_ID="$MM3_LS_KEY_ID"
export RCLONE_CONFIG_MM3_SECRET_ACCESS_KEY="$MM3_LS_APP_KEY"
export RCLONE_CONFIG_MM3_NO_CHECK_BUCKET=true
