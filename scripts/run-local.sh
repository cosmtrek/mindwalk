#!/usr/bin/env bash
set -euo pipefail

prefix=${MINDWALK_PREFIX:-"$HOME/.local"}
data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}
binary="$prefix/bin/mindwalk"

[[ -x "$binary" ]] || { echo "mindwalk is not installed; run scripts/setup-local.sh" >&2; exit 1; }
mkdir -p -m 700 "$data_home" "$config_home"
exec "$binary" serve --config "$config_home/repos.json" --data-dir "$data_home" "$@"
