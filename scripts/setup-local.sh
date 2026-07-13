#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
prefix=${MINDWALK_PREFIX:-"$HOME/.local"}
data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}
export PATH="$HOME/.local/go/bin:$PATH"

for command in go node npm make; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done

npm --prefix "$root/web" ci
make -C "$root" build
install -d -m 700 "$prefix/bin" "$data_home" "$config_home"
install -m 755 "$root/bin/mindwalk" "$prefix/bin/mindwalk"

echo "installed $prefix/bin/mindwalk"
echo "run: $root/scripts/run-local.sh"
