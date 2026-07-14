#!/usr/bin/env bash
set -euo pipefail

apply=0
archive=
for arg in "$@"; do
  case "$arg" in
    --apply) apply=1 ;;
    -*) echo "unknown option: $arg" >&2; exit 2 ;;
    *) [[ -z "$archive" ]] || { echo "only one backup archive is allowed" >&2; exit 2; }; archive=$arg ;;
  esac
done
[[ -n "$archive" && -f "$archive" && ! -L "$archive" ]] || { echo "usage: restore-local.sh BACKUP.tar.gz [--apply]" >&2; exit 2; }

data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

max_bytes=${MINDWALK_RESTORE_MAX_BYTES:-1073741824}
max_entries=${MINDWALK_RESTORE_MAX_ENTRIES:-100000}
archive_bytes=$(wc -c < "$archive")
(( archive_bytes <= max_bytes )) || { echo "backup exceeds restore size limit" >&2; exit 1; }

entry_count=0
while IFS= read -r entry; do
  (( entry_count += 1 ))
  (( entry_count <= max_entries )) || { echo "backup exceeds restore entry limit" >&2; exit 1; }
  [[ "$entry" != /* && "$entry" != ".." && "$entry" != ../* && "$entry" != */../* && "$entry" != */.. ]] || { echo "unsafe archive path: $entry" >&2; exit 1; }
done < <(tar -tzf "$archive")

while IFS= read -r listing; do
  case "${listing:0:1}" in
    -|d) ;;
    *) echo "unsupported archive entry type" >&2; exit 1 ;;
  esac
done < <(tar -tvzf "$archive")

tar -xzf "$archive" -C "$work" --no-same-owner --no-same-permissions
[[ -f "$work/MANIFEST" && -d "$work/config" && -d "$work/data" ]] || { echo "invalid Mindwalk backup" >&2; exit 1; }
[[ "$(head -n 1 "$work/MANIFEST")" == "schema=1" ]] || { echo "unsupported backup schema" >&2; exit 1; }
if find "$work" -mindepth 1 ! -type f ! -type d -print -quit | grep -q .; then
  echo "backup contains unsupported extracted entries" >&2
  exit 1
fi

echo "restore plan:"
echo "  archive: $archive"
echo "  config:  $config_home"
echo "  data:    $data_home"
if (( apply == 0 )); then
  echo "dry-run only; re-run with --apply"
  exit 0
fi

[[ ! -L "$config_home" && ! -L "$data_home" ]] || { echo "refusing symlink restore target" >&2; exit 1; }
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$(dirname "$config_home")" "$(dirname "$data_home")"
config_stage="$config_home.restore-$stamp"
data_stage="$data_home.restore-$stamp"
config_previous="$config_home.pre-restore-$stamp"
data_previous="$data_home.pre-restore-$stamp"
cp -a "$work/config" "$config_stage"
cp -a "$work/data" "$data_stage"
config_installed=0
data_installed=0

rollback() {
  rm -rf -- "$config_stage" "$data_stage"
  if (( config_installed )); then rm -rf -- "$config_home"; fi
  if (( data_installed )); then rm -rf -- "$data_home"; fi
  if [[ -e "$config_previous" && ! -e "$config_home" ]]; then mv "$config_previous" "$config_home"; fi
  if [[ -e "$data_previous" && ! -e "$data_home" ]]; then mv "$data_previous" "$data_home"; fi
}
trap 'rollback; rm -rf -- "$work"' ERR
if [[ -e "$config_home" ]]; then mv "$config_home" "$config_previous"; fi
if [[ -e "$data_home" ]]; then mv "$data_home" "$data_previous"; fi
mv "$config_stage" "$config_home"
config_installed=1
mv "$data_stage" "$data_home"
data_installed=1
chmod 700 "$config_home" "$data_home"
trap 'rm -rf -- "$work"' EXIT
echo "restore applied; previous directories retained with .pre-restore-$stamp suffix"
