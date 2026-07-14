#!/usr/bin/env bash
set -euo pipefail

data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}
output=${1:-"mindwalk-backup-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"}
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

[[ ! -L "$output" ]] || { echo "refusing symlink backup target: $output" >&2; exit 1; }
for source in "$config_home" "$data_home"; do
  [[ ! -L "$source" ]] || { echo "refusing symlink source: $source" >&2; exit 1; }
done

mkdir -p "$work/payload/config" "$work/payload/data"
if [[ -d "$config_home" ]]; then cp -a "$config_home/." "$work/payload/config/"; fi
if [[ -d "$data_home" ]]; then cp -a "$data_home/." "$work/payload/data/"; fi
printf 'schema=1\ncreated_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$work/payload/MANIFEST"
tar -C "$work/payload" -czf "$work/backup.tar.gz" MANIFEST config data
mkdir -p "$(dirname "$output")"
install -m 600 "$work/backup.tar.gz" "$output"
echo "backup written: $output"
