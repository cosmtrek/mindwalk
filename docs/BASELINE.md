# BASELINE — Mindwalk Observatory (Phase 0)

Recorded: 2026-07-13 (updated same day after the approved Go install). All
output below is verbatim from real command runs on Eric's machine (Kali Linux,
`Linux 6.19.14+kali-amd64`). Nothing is simulated. Complete sanitized command
transcript: `docs/evidence/baseline-go-2026-07-13.txt`.

## Upstream pin

| Item | Value |
|---|---|
| Upstream repo | `https://github.com/cosmtrek/mindwalk.git` (remote name: `upstream`) |
| Branch | `upgrade/multi-repo-observatory` |
| HEAD commit | `97a543c2272b38cb5b8ea9b1b067b21e8ac039cb` |
| `git describe --tags --always` | `v0.1.0-8-g97a543c` |
| Tag `v0.1.0` commit | `d7a31c4109ab6e9beb193371a12d9d4c6d39ac03` |
| Relationship to upstream | HEAD == `upstream/master` == merge-base. **Zero local commits**; the tree is pristine upstream tip. |
| Working tree | Clean before Phase 0; after Phase 0 only untracked docs (`THIRD_PARTY_NOTICES.md`, `docs/`). No tracked file modified. |
| License | MIT, `Copyright (c) 2026 Ricko Yu` — verified verbatim in `LICENSE`. Matches expectation. |

There is **no `origin` remote** — local branches exist only on this machine
(acknowledged risk R-008; a GitHub repo will be created after Eric reviews the
Phase 0 documentation diff).

## Toolchain

```text
$ go version
go version go1.25.12 linux/amd64
$ go env GOROOT GOPATH
~/.local/go
~/go
$ node --version
v24.18.0
$ npm --version
11.16.0
```

### Go install record (owner-approved 2026-07-13)

Go was initially **absent** from the machine (searched PATH, /usr/local/go,
/usr/lib/go-*, ~/go, snap, apt — not installed), which blocked the first
baseline run (see history below). Eric approved a user-scoped install:

- Source: `https://go.dev/dl/go1.25.12.linux-amd64.tar.gz` (go.dev only)
- Size: 59,856,753 bytes
- SHA-256 verified **before extraction** against Eric's approved value:
  ```text
  $ echo "234828b7…262ac1  go1.25.12.linux-amd64.tar.gz" | sha256sum -c -
  go1.25.12.linux-amd64.tar.gz: OK
  $ sha256sum go1.25.12.linux-amd64.tar.gz
  234828b7a89e0e303d2556310ee549fbcf253d28de937bac3da13d6294262ac1
  ```
- Extracted with `tar -C ~/.local -xzf …` → `~/.local/go`
  (directory did not previously exist; nothing overwritten).
- No sudo, no apt, no system directory touched.
- PATH: appended exactly one line to `~/.bashrc`:
  `export PATH="$HOME/.local/go/bin:$PATH"` (with an approval-note comment).
  No other line in `.bashrc` was modified.
- Side effects: Go created `~/go` (GOPATH) and `~/.cache/go-build` during the
  test run — standard user-scoped Go behavior.

## Baseline command results — GREEN (§I.5 evidence discipline)

All from `docs/evidence/baseline-go-2026-07-13.txt`; every command exited 0.

```text
$ gofmt -l .
(no output — nothing unformatted)                              exit=0

$ go test ./...
ok  github.com/cosmtrek/mindwalk/cmd/mindwalk                  0.005s
ok  github.com/cosmtrek/mindwalk/internal/adapter              0.010s
ok  github.com/cosmtrek/mindwalk/internal/adapter/claudecode   0.984s
ok  github.com/cosmtrek/mindwalk/internal/adapter/codex        0.011s
ok  github.com/cosmtrek/mindwalk/internal/citymap              0.285s
ok  github.com/cosmtrek/mindwalk/internal/model                0.005s
ok  github.com/cosmtrek/mindwalk/internal/server               0.160s
                                                               exit=0

$ make test      # go test ./... (all ok, cached) + npm --prefix web run build
✓ 1729 modules transformed. … ✓ built in 3.49s                 exit=0

$ make build     # web build + embed-static + go build -o bin/mindwalk
go build -o bin/mindwalk ./cmd/mindwalk                        exit=0

$ git status --short
?? THIRD_PARTY_NOTICES.md
?? docs/                                                       exit=0
$ git diff --stat        (empty — no tracked changes)          exit=0
$ git diff --check       (empty — no whitespace errors)        exit=0
```

`make setup` (earlier the same day) — PASS: `added 88 packages … found 0
vulnerabilities`. Note: the esbuild postinstall was blocked by the local npm
allow-scripts policy; the vite build succeeds regardless (risk R-006).

### History: first run before the Go install (kept as honest record)

- `make test` → FAIL: `make: go: No such file or directory` (Error 127)
- `make build` → web + embed-static steps PASS, `go build` FAIL (Error 127)
- `go test ./...`, `gofmt -l .` → NOT RUN (no toolchain)
- The frontend half passed independently: `npm --prefix web run build`,
  `✓ 1729 modules transformed … ✓ built in 3.76s`, exit 0.

A useful integrity finding from that run: after `embed-static` rewrote
`internal/server/static/`, `git status` stayed empty — the committed embedded
assets are byte-identical to a fresh build (hashes `index-DbaRtpfH.js`,
`index-BWLkBQ0C.css`, `react-CkQa1mN8.js`, `three-DnGjZfD1.js` match exactly).

## Network activity statement (accuracy per owner instruction)

No repository source, prompts, session data, or secrets were intentionally
uploaded anywhere. Outbound network activity during Phase 0 consisted of:
npm downloading pinned dependencies from the configured registry during
`make setup` / `npm ci`, and the owner-approved download of the Go toolchain
tarball from `go.dev`. Nothing else was transmitted.

## Architecture as found (inventory)

Go: **5,847 lines total, zero external Go dependencies** (`go.mod` is module
line + `go 1.25` only).

| Package | Files (lines) | Tests |
|---|---|---|
| `cmd/mindwalk` | main.go (188) | main_test.go (16) |
| `internal/adapter` | adapter.go (903) | adapter_test.go (288) |
| `internal/adapter/claudecode` | adapter.go (337) | adapter_test.go (161) |
| `internal/adapter/codex` | adapter.go (723) | adapter_test.go (676) |
| `internal/citymap` | builder.go (520) | builder_test.go (182) |
| `internal/model` | model.go (164), stats.go (145) | stats_test.go (85) |
| `internal/server` | server.go (768) | server_test.go (691) |

`internal/model` is a **confirmed upstream package** (trace/citymap data
contracts + stats), consistent with upstream `AGENTS.md`.

CLI as found (all preserved, including the newer `map` command):

```text
mindwalk serve [--port N] [--no-open] [--claude-dir DIR] [--codex-dir DIR]
mindwalk open [--no-open] <session.jsonl>
mindwalk map [--no-open] <repo>     open the repository citymap with no session
mindwalk build <repo> [-o out]
mindwalk trace <session> [-o out]
```

Frontend (`web/src`): App.tsx, api/client.ts, playback/{recorder,reducer}.ts,
scene/{CityScene,TreeScene}.tsx + layout/texture/trail utils,
state/{store,filters}.ts, ui/{Hud,Inspector,SessionRail,Timeline,LogoMark}.tsx
+ shortcuts.ts, types.ts, styles.css. Stack: React 19, Vite 7, Three.js 0.182,
zustand 5, TypeScript 5.9, Playwright 1.61 (dev).

Schemas: `schema/trace.schema.json` and `schema/citymap.schema.json`, both
JSON Schema 2020-12 with `"version": { "const": 1 }`.

Security posture as found: server binds `127.0.0.1` only
(`internal/server/server.go:121`); frontend dev/preview also pinned to
`--host 127.0.0.1` (`web/package.json`).

CI: `.github/workflows/release.yml` only — **no test workflow upstream**.
Release automation: `.goreleaser.yaml` (go mod tidy + go test hooks; darwin/
linux/windows, amd64/arm64, CGO disabled).

Test fixtures: `testdata/claude-session.jsonl` (single fixture; codex fixtures
appear to live inline in `internal/adapter/codex/adapter_test.go`).

## Deltas from the master prompt's "VERIFIED UPSTREAM FACTS" (§II.2)

Reality wins; recorded per the reality-divergence rule:

1. **`internal/model` exists and is a confirmed upstream package** (model.go,
   stats.go, stats_test.go). §II.2 listed it as unconfirmed.
2. **The CLI includes `mindwalk map [--no-open] <repo>`** (added by upstream
   commit 9836950 "static full-repo map and client-side video export");
   §II.2's CLI list omitted it. It must be preserved like the others.
3. HEAD is 8 commits **past** `v0.1.0` (`v0.1.0-8-g97a543c`) — pinned above.
   The 8 commits are upstream's own (README/license/docs + the map/video-export
   feature).
4. `web/package.json` has **no `engines` field** — Node version is not pinned
   upstream. Node v24.18.0 + npm 11.16.0 work.
5. Upstream also ships `.claude/launch.json`, `.claude/settings.local.json`,
   and `.claude/skills/verify/SKILL.md` (a UI-verification skill).
6. `make test` = `go test ./...` + `npm --prefix web run build` (exactly as
   documented). `make serve` runs with `--dev` flag on port 8765.

Also verified on owner instruction: **no NeoAI/CultOS-specific staging rules
or files (`.neoai`, `neoai-device-bridge`, etc.) exist anywhere in this
repository** — a full case-insensitive search found zero matches. This is a
standalone project; those rules apply to other repos only.

## Phase 0 verdict

Baseline is **fully proven**: Go and frontend toolchains green, all upstream
tests pass, full build produces `bin/mindwalk`, working tree contains only the
intended untracked documentation. Trustworthy starting point for P1.
