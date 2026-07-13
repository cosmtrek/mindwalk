# Backup, restore, and uninstall

Mindwalk Observatory stores its owner-curated registry under
`${XDG_CONFIG_HOME:-$HOME/.config}/mindwalk-observatory` and durable ledgers,
indexes, and tail state under
`${XDG_DATA_HOME:-$HOME/.local/share}/mindwalk-observatory`. Override these
locations with `MINDWALK_CONFIG_HOME` and `MINDWALK_DATA_HOME`.

## Backup

Stop the local server, then create a mode-0600 archive:

```sh
./scripts/backup-local.sh ~/mindwalk-backup.tar.gz
```

The script refuses symlink data/config sources and a symlink output target.
The archive contains schema marker `1`, configuration, and durable data. It
does not copy registered repository contents or source session logs.

## Restore

Inspect the plan first; the default is non-mutating:

```sh
./scripts/restore-local.sh ~/mindwalk-backup.tar.gz
./scripts/restore-local.sh ~/mindwalk-backup.tar.gz --apply
```

Restore rejects oversized archives, excessive entries, traversal paths,
symlinks, hard links, and special files. It stages both destinations before
replacement and attempts rollback on an apply error. Existing directories are
retained with a `.pre-restore-TIMESTAMP-PID` suffix for manual rollback. After
restore, run:

```sh
./scripts/doctor.sh
```

To roll back manually, stop the server, move the restored directory aside,
then rename the matching `.pre-restore-*` directory to its original name.

## Uninstall

Uninstall preserves data unless purge is explicitly requested. Both forms are
dry-run until `--apply` is present:

```sh
./scripts/uninstall-local.sh
./scripts/uninstall-local.sh --apply
./scripts/uninstall-local.sh --purge-data --apply
```

Create and verify a backup before using `--purge-data`.
