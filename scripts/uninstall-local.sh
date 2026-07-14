#!/usr/bin/env bash
set -euo pipefail

apply=0
purge=0
for arg in "$@"; do
  case "$arg" in
    --apply) apply=1 ;;
    --purge-data) purge=1 ;;
    *) echo "unknown option: $arg" >&2; exit 2 ;;
  esac
done

prefix=${MINDWALK_PREFIX:-"$HOME/.local"}
data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}

echo "uninstall plan: remove $prefix/bin/mindwalk"
if (( purge )); then echo "  PURGE local config/data: $config_home $data_home"; else echo "  preserve local config/data"; fi
if (( apply == 0 )); then echo "dry-run only; re-run with --apply"; exit 0; fi
rm -f -- "$prefix/bin/mindwalk"
if (( purge )); then rm -rf -- "$config_home" "$data_home"; fi
echo "uninstall applied"
