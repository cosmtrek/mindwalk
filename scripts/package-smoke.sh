#!/usr/bin/env bash
set -euo pipefail

root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
export MINDWALK_PREFIX="$work/prefix"
export MINDWALK_CONFIG_HOME="$work/config"
export MINDWALK_DATA_HOME="$work/data"
mkdir -p "$MINDWALK_PREFIX/bin" "$MINDWALK_CONFIG_HOME" "$MINDWALK_DATA_HOME"
install -m 755 "$root/bin/mindwalk" "$MINDWALK_PREFIX/bin/mindwalk"
printf 'original\n' > "$MINDWALK_CONFIG_HOME/fixture"
printf 'ledger\n' > "$MINDWALK_DATA_HOME/fixture"
"$root/scripts/backup-local.sh" "$work/backup.tar.gz"
printf 'changed\n' > "$MINDWALK_DATA_HOME/fixture"
"$root/scripts/restore-local.sh" "$work/backup.tar.gz"
grep -qx changed "$MINDWALK_DATA_HOME/fixture"
"$root/scripts/restore-local.sh" "$work/backup.tar.gz" --apply
grep -qx ledger "$MINDWALK_DATA_HOME/fixture"

mkdir -p "$work/unsafe/config" "$work/unsafe/data"
printf 'schema=1\n' > "$work/unsafe/MANIFEST"
ln -s /tmp "$work/unsafe/config/escape"
tar -C "$work/unsafe" -czf "$work/unsafe.tar.gz" MANIFEST config data
if "$root/scripts/restore-local.sh" "$work/unsafe.tar.gz" --apply >/dev/null 2>&1; then
  echo "unsafe link archive was accepted" >&2
  exit 1
fi
grep -qx ledger "$MINDWALK_DATA_HOME/fixture"

"$root/scripts/uninstall-local.sh"
test -x "$MINDWALK_PREFIX/bin/mindwalk"
"$root/scripts/uninstall-local.sh" --apply
test ! -e "$MINDWALK_PREFIX/bin/mindwalk"
test -e "$MINDWALK_DATA_HOME/fixture"
echo "package backup/restore/uninstall smoke passed"
