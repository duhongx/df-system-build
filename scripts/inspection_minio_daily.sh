#!/bin/sh
# Guide cloudhis-39: keep dated zero-hour inode samples for later reports.
# Install/schedule explicitly on the customer's MinIO server; this script
# neither changes cron nor installs any software.
set -eu
umask 077

if [ "$#" -ne 1 ] || [ "${1#/}" = "$1" ]; then
  echo "usage: inspection_minio_daily.sh /absolute/path/to/daily-inodes.log" >&2
  exit 2
fi
record_file=$1
if [ "$(date +%H:%M)" != "00:00" ]; then
  echo "This sample must run at 00:00 in the server's timezone." >&2
  exit 1
fi
sample_date=$(date +%Y-%m-%d)
inode_count=$(df -i | awk '$1 == "/dev/mapper/openeuler-root" {print $3}')
case "$inode_count" in
  ''|*[!0-9]*) echo "Could not read exactly one openeuler-root inode count." >&2; exit 1 ;;
esac
mkdir -p -- "$(dirname -- "$record_file")"
lock_dir="${record_file}.lock"
if ! mkdir -- "$lock_dir"; then
  echo "Another sample is being recorded; no data was overwritten." >&2
  exit 1
fi
trap 'rmdir -- "$lock_dir"' EXIT HUP INT TERM
if [ -f "$record_file" ] && awk -v day="$sample_date" '$1 == day {found=1} END {exit !found}' "$record_file"; then
  echo "A sample for $sample_date already exists."
  exit 0
fi
printf '%s %s\n' "$sample_date" "$inode_count" >> "$record_file"
