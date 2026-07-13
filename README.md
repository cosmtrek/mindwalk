# <img src="assets/logo.svg" alt="" width="30" /> mindwalk

A visualization tool that replays coding-agent sessions on a 3D map of your codebase.

https://github.com/user-attachments/assets/20ecdc3b-9bc2-469b-ba99-607f3c1d5e0c

*The 30-second demo — sound on.*

## The problem

A session log records what an agent did, but not how it understood the task:
which parts of the repo it treated as relevant, where it explored before it
acted, whether its footprint matched the scope you had in mind. Reading the
raw JSONL line by line doesn't answer any of that.

## The idea

Draw the repository as a night map, and play the session back as light moving
through it: where the agent searched, read, and edited, the map glows —
everything else stays dark. The agent's understanding of the task becomes a
shape you can see at a glance. One Go binary reads Claude Code and Codex
session logs, fully local; no session data leaves your machine.

## Observatory local setup

```sh
./scripts/setup-local.sh
./scripts/run-local.sh
```

This user-scoped flow runs `npm ci`, builds the embedded frontend and Go
binary, installs `~/.local/bin/mindwalk`, and creates private configuration
and durable data directories. It uses no `sudo`, package-manager mutation, or
external service. Run `./scripts/doctor.sh` to check the installation.

On first launch, choose **Scan my home folder**, **Choose folders**, **Add a
path manually**, or **Skip for now**. Discovery never starts when the page or
server opens: even the home choice must be clicked, reviewed with the locked
exclusions and hard bounds, and confirmed with **Start scan**. Results are
only a preview. Search, sort, or filter them; **Select all visible** affects
only results in the current view. Check the repositories you want,
review their exact paths and editable owner metadata, then confirm once more
to add only that selection. Loading, scanning, cancelling, hiding, or
restarting never registers a repository automatically. Hidden results remain
recoverable through **Show hidden discoveries**.

```sh
# Metadata-only preview; repeat --root to approve more than one root.
mindwalk repos discover --root /absolute/path/to/workspace
mindwalk repos discovered
mindwalk repos add-discovered disc_0123456789abcdef0123456789abcdef

# Manual registration remains available.
mindwalk repos add /absolute/path/to/repository
mindwalk repos list
mindwalk serve
```

`mindwalk repos discover --home` is the explicit CLI equivalent of **Scan my
home folder**. `discover-status` and `discover-cancel` report or cancel a
foreground CLI scan; UI/API scans expose their own real status and Cancel
control. `hide-discovered` and `unhide-discovered` manage the local result
view. Discovery reads directory and Git metadata only, stays inside canonical
owner-approved roots, does not follow directory symlinks or cross filesystem
devices, and skips locked credential/cache/private-data locations. Discovery
parses only bounded, root-confined `.git`, `HEAD`, ref, packed-ref, and
worktree metadata; it does not invoke Git or read Git config, ordinary source,
or `.env` contents. See [Privacy](docs/PRIVACY.md) and the
[threat model](docs/THREAT_MODEL.md) for the complete boundary.

The server binds a random `127.0.0.1` port and opens a browser. Claude Code and
Codex session roots are configurable, while repository association remains
fail-closed (`UNKNOWN`) unless one registered root proves it.

Core commands:

```text
mindwalk serve [--port N] [--no-open] [--claude-dir DIR] [--codex-dir DIR] [--data-dir DIR]
mindwalk open [--no-open] <session.jsonl>   open one specific session
mindwalk map [--no-open] <repo>             local map-only compatibility mode
mindwalk build <repo> [-o out]              write the repository citymap JSON
mindwalk trace <session> [-o out]           write the normalized trace JSON
mindwalk repos list|add|show|edit|enable|disable|remove|validate|refresh
mindwalk repos discover|discover-status|discover-cancel|discovered
mindwalk repos add-discovered|hide-discovered|unhide-discovered
mindwalk memory list|add|search|correct|tombstone
```

`trace` exports are redacted. Memory writes are explicit, append-only
corrections/tombstones; local SQLite FTS retrieval is not model training.
Backup and restore are documented in [docs/BACKUP_RESTORE.md](docs/BACKUP_RESTORE.md).
Restore and uninstall are dry-run by default and require `--apply` to mutate.

The upstream release installer and Windows archives remain available from the
[original mindwalk project](https://github.com/cosmtrek/mindwalk); Observatory
release-candidate testing uses the source-local setup above.

## Reading the picture

- **Tree / Terrain / List views** — the repo as a radial tree, a treemap plain,
  or an accessible no-WebGL file list; start with `?profile=low` for List mode;
  glow ∝ how deeply and how often a file was touched.
- **Touch states** — each file keeps its deepest touch: seen (moss green),
  read (moon white), edited (warm amber), unvisited (dark). The HUD folds
  friction signals — error rate, churned files, edits after the last verify —
  into a review strip.
- **Playback deck** — scrub or play the session over a bucketed histogram of
  the run. Bars sit on a cool/warm spectrum: observation stays cool (search,
  read, exec), mutation glows warm (edit, verify), so editing phases jump out
  at a glance.
- **Timeline marks** — `◇` context compactions, `○` subagent launches,
  `›` user turns; every mark is a click-to-jump target.
- **Inspector** — click a file to pin its visit history; click a visit row to
  jump the playhead to that moment. Observable event provenance is shown when
  available and explicitly labeled `UNAVAILABLE` otherwise.
- **Live, Memory, and Review** — follow durable live events, search explicit
  local memories, inspect display-only agent processes, compare session file
  footprints, and export a redacted Markdown owner-review packet.

![the same session on the terrain view](assets/screenshot-terrain.png)

Keyboard: `Space` play/pause · `←`/`→` step (`⇧` ×10) · `Home`/`End` ends ·
`S` speed · `E` next edit · `X` next error · `M` next mark · `⌘B` session rail.

## Under the hood

Two artifacts, kept deliberately separate:

1. a **trace** — the session log normalized into an ordered stream of
   file-touch events (`internal/adapter`, one adapter per agent format);
2. a **citymap** — a deterministic layout of the repository
   (`internal/citymap`); the same tree always produces the same map, so
   replays are comparable across sessions.

A local Go server (`internal/server`) joins the two and serves the
React/Three.js frontend (`web`). `schema/` mirrors the exported JSON contracts.

## Contributing

Issues and pull requests are welcome. To get a working dev setup:

```sh
make setup   # install frontend dependencies
make serve   # dev server on :8765, serving web/dist from the working tree
make test    # go test + frontend build — run before sending a PR
make build   # regenerate embedded assets and bin/mindwalk
```

Ground rules (see [AGENTS.md](AGENTS.md) for the full architecture notes):

- Keep the boundaries: adapters don't know about rendering, citymap generation
  doesn't depend on playback, the server just connects the two.
- Keep Go code `gofmt`-ed; never hand-edit `internal/server/static` —
  regenerate it with `make build`.
- When trace or citymap JSON shapes change, update `schema/` and the relevant
  tests in the same change.

## License

[MIT](LICENSE) © 2026 Ricko Yu
