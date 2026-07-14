# PRIVACY

Mindwalk Observatory is local-only and metadata-first.

- The production server binds only to `127.0.0.1`.
- Ordinary `serve` lists sessions only for enabled repositories Eric has
  explicitly registered. It never registers repositories automatically.
- Optional repository discovery stays disabled until Eric explicitly selects
  canonical roots, saves that approval, reviews the scan plan, and starts the
  scan. Choosing a suggested home root is not consent to start scanning.
- Discovery opens directory metadata needed to locate `.git`, plus bounded,
  root-confined `.git` indirection, `HEAD`, loose/packed refs, and worktree
  `commondir`. It never invokes Git or reads Git config. It does not open
  ordinary source, `.env`, credential, browser-profile, or key-store contents;
  upload paths/metadata; run repository code; install dependencies; or modify
  repositories.
- Discovery never follows directory symlinks or crosses the selected root's
  filesystem/mount boundary (Linux mount IDs plus device checks; Windows
  volume identity). `/`, protected system/mount roots, credential locations,
  caches, browser profiles, Trash, agent session roots, and Observatory's
  private data/config paths are locked out. Custom exclusions only add skips.
- Discovery results are previews. Registration accepts exact stable result
  IDs, revalidates their canonical paths against owner-approved roots, and
  uses the existing registry only after final confirmation. Partial batch
  failures are reported per repository; unselected results remain unregistered.
- Registered repositories are observed read-only. Registry removal deletes
  only the local registry record.
- Event envelopes contain normalized metadata; raw source records and full
  source-file contents are not part of the event contract.
- Session sources are discovered only below configured canonical Claude/Codex
  roots. Symlink escapes are rejected; unprovable repository association is
  labeled `UNKNOWN` and is not persisted as observable session activity.
- Rejected ledger lines are represented by reason, byte count, and SHA-256 in
  quarantine. Their raw bytes are not copied.
- Common credential patterns are redacted from session titles, event
  summaries, marks, and outside-path details before ledger persistence, API
  display, browser/video use, and `mindwalk trace` export.
- Registry and tail state files use mode `0600`; their parent directories use
  mode `0700`; updates are temp-write, fsync, rename, and directory fsync.
- Private discovery state uses a registry-derived, collision-free sidecar and
  follows the same atomic `0600` contract. A shared owner lock serializes CLI
  and server mutations. The state stores
  approved roots, additive exclusions, scan bounds, hidden canonical tokens,
  the latest time/summary, and one replace-in-place repository-only result
  snapshot needed for exact-ID approval and hidden recovery. It never stores
  a history of ordinary directories examined; Forget scan history removes the
  result snapshot and summary.
- Browser repository maps accept registered IDs. The legacy direct path is
  available only when `mindwalk map <repo>` explicitly configures that root.
- Memory records are written only by explicit CLI/API mutation, redacted
  before append, corrected/tombstoned rather than silently rewritten, and
  indexed locally with parameterized SQLite FTS5. Retrieval is not training.
- Optional integration contracts are disabled by default and expose no live
  send, shell, or external mutation implementation.
- Backup archives contain Observatory config/data only, not repository or
  source-log contents; restore rejects links, special files, traversal paths,
  and excessive archives before explicit `--apply`.

Malformed or oversized source input blocks further adapter parsing of that
file until replacement/truncation recovery. This fail-closed behavior prevents
an adapter from allocating or re-reading rejected content while the service
continues observing other sources.

Repository-discovery acceptance tests must construct synthetic temporary
homes, roots, repositories, worktrees, and inaccessible/broken entries. The
automated proof is forbidden from selecting or scanning the owner's real home.
