#!/usr/bin/env bash
set -euo pipefail

[[ ${1:-} == "--apply" ]] || { echo "dry-run: install a user-scoped desktop launcher; re-run with --apply"; exit 0; }
prefix=${MINDWALK_PREFIX:-"$HOME/.local"}
applications=${XDG_DATA_HOME:-"$HOME/.local/share"}/applications
mkdir -p "$applications"
desktop="$applications/mindwalk-observatory.desktop"
tmp=$(mktemp)
trap 'rm -f -- "$tmp"' EXIT
printf '%s\n' '[Desktop Entry]' 'Type=Application' 'Name=Mindwalk Observatory' "Exec=$prefix/bin/mindwalk serve" 'Terminal=false' 'Categories=Development;' > "$tmp"
install -m 644 "$tmp" "$desktop"
echo "desktop launcher installed: $desktop"
