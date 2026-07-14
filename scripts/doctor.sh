#!/usr/bin/env bash
set -euo pipefail

prefix=${MINDWALK_PREFIX:-"$HOME/.local"}
data_home=${MINDWALK_DATA_HOME:-"${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory"}
config_home=${MINDWALK_CONFIG_HOME:-"${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory"}
failures=0

check() {
  if "$@" >/dev/null 2>&1; then echo "PASS  $*"; else echo "FAIL  $*"; failures=$((failures + 1)); fi
}

check test -x "$prefix/bin/mindwalk"
check test -d "$data_home"
check test -d "$config_home"
check "$prefix/bin/mindwalk" repos validate -config "$config_home/repos.json"
if command -v chromium >/dev/null; then echo "PASS  chromium available"; else echo "WARN  chromium unavailable (browser proof only)"; fi
if [[ -d "$data_home" ]]; then
  mode=$(stat -c '%a' "$data_home")
  [[ "$mode" == "700" ]] && echo "PASS  data directory mode 700" || { echo "FAIL  data directory mode $mode"; failures=$((failures + 1)); }
fi

(( failures == 0 )) || exit 1
echo "doctor: healthy"
