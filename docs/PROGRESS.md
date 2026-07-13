# PROGRESS

Durable checkpoint log (§I.6). Newest first.

## 2026-07-13 — SAFE REPOSITORY DISCOVERY GREEN

**Delivered:** the existing owner-curated repository registry now has an
explicit preview/approval extension: canonical multi-root selection, home
confirmation, locked plus additive exclusions, bounded/cancellable metadata
scanning, ordinary/worktree/`.git`-file/bare/broken/nested detection, stable
IDs, private state, CSRF-protected APIs, preview-only CLI commands, first-run
and Settings entry points, real progress, search/sort/filter/select, hide and
recovery, global or per-root rescan, and exact editable metadata confirmation.
Manual path registration remains available. Discovery never starts, rescans,
or registers anything on browser/server restart.

**Strict-review repairs:** rooted opens now reject every symlink component and
verify file identity across metadata opens; external `gitdir`/`commondir`
targets are rejected before any probe; Linux `STATX_MNT_ID` plus device checks
block bind/mount crossing; the scanner cooperatively yields between bounded
batches; final registration stores only the revalidated canonical discovery
path without resolving it again; per-root/cancelled scans merge only roots
actually visited; inaccessible roots do not erase prior results; an explicit
active flag covers terminal persistence; and one crash-recoverable owner lock
serializes CLI/server registry and discovery mutations. Registry-derived
sidecar names cannot alias a custom `-config` file. The skipped-repository
counter now counts repositories, not excluded directories or symlinks.

**Privacy boundary verified:** traversal does not open ordinary source or
`.env` contents, follow directory symlinks, enter locked credential/cache/
browser/agent/Observatory paths, run repository code, invoke Git, read Git
config, or contact a service. Bounded direct reads cover only `.git`
indirection, `HEAD`, loose/packed refs, and worktree `commondir` within an
approved root. Registered-repository status uses hardened read-only Git
commands separately. One replace-in-place repository-only result snapshot is
operationally required for stable-ID CLI approval and hidden recovery; no
ordinary-directory history is persisted, and Forget scan history removes it.

**Proof:** every required command exited 0 on 2026-07-13: `gofmt -l .`
returned no files; `go test ./... -count=1` passed every Go package;
`npm --prefix web run build`, `make test`, `make build`, and
`git diff --check` passed. `npm --prefix web run test:e2e:discovery` used
system Chromium and temporary synthetic roots only, reporting: “examined 377,
found 2, registered 1 selected + 1 manual, cancellation and restart safe.”
The browser proof also covered an empty approved root, per-root rescan,
filtered Select all visible, exact final confirmation, hide/recover, reduced
motion, LOW/List mode, no automatic registration/rescan, and unchanged
repository snapshots. Extra `go vet ./...`, focused `go test -race`, and a
Windows cross-compile also exited 0. Eric's real home was never scanned.

## 2026-07-13 — LOCAL RELEASE CANDIDATE GREEN: P5-P10 + full acceptance

**Delivered:** deterministic display-only AgentProcess projection; explicit
redacted append-only Markdown/JSONL memory with corrections, tombstones,
namespaces and local SQLite FTS5 CLI/UI search; deterministic review and
session comparison with redacted Markdown export; seven disabled-by-default
receive-only/read-only/dry-run integration contracts; accessible no-WebGL
List/LOW mode, reduced motion, keyboard/named-region coverage, loopback test,
threat model, performance smoke baseline, combined CycloneDX SBOM, and
user-scoped setup/run/doctor/backup/restore/uninstall/desktop scripts.

**Strict-review fixes during acceptance:** comparison arrays now serialize as
`[]` instead of `null` so the Review panel cannot crash; memory no-hit results
also serialize as `[]` and equal FTS ranks tie-break by memory ID; backup and
restore reject symlink/special/traversal/excessive inputs and stage both
destinations with rollback; SBOM generation finds the user-scoped Go toolchain.

**Proof:** `.local-evidence/rc-proof-2026-07-13.txt` (ignored, untracked).
Formatting, vet, focused/full/race Go tests, frontend build, `make test`,
`make build`, system-Chromium synthetic Claude/Codex live/restart flow,
package/malicious-archive smoke, isolated setup/doctor/desktop-dry-run/
uninstall, npm production audit, govulncheck, 163-component CycloneDX
validation, and `git diff --check` all exit 0. Browser proof ended at durable
sequence 24; final LOW fixture measured 59.2 ms DOMContentLoaded and 19,140 KiB
server RSS. The observed repository remained byte-for-byte unchanged.

**Honest deferrals:** the decorative Universe macro view and any autonomous or
controlled execution system are outside this local read-only RC. No fabricated
substitute or runtime control is present. Existing risks R-005/R-006/R-008/R-009
remain documented; no commit, push, public binding, external action, or real
credential/session data was used.

## 2026-07-13 — P3 GREEN: durable source-to-browser live ingestion

**Delivered:** configured-root-only Claude Code/Codex discovery now flows
through the existing adapters and incremental tailer into redacted,
deterministically identified EventEnvelope v1 records, the root-confined
append-only ledger, replay-safe session/file/verification/error/agent
projections, durable global sequence cursors, bounded SSE replay, and React
live-follow. Repository association is exact only when one enabled registered
root proves it; otherwise it is `UNKNOWN`. Source, ingestion-session, health,
quarantine-count, event-page, projection, and provenance APIs expose no raw
source content. Existing snapshot/trace/citymap playback remains intact.

**Recovery and safety:** persisted state includes source token/path, portable
anchor, device/inode where available, offsets and complete-line boundary,
size/modtime, last accepted source hash, projected count, durable sequence,
and blocked-source state. Partial lines wait; incomplete tool calls wait for a
result; restart is exactly-once; replacement/truncation/deletion are detected;
malformed/oversized sources fail closed until rotation; symlink escapes are
metadata-only quarantined and de-duplicated. Rejected bytes and source output
are never copied to the ledger/quarantine, and monitored repositories remain
unchanged. The legacy `mindwalk trace` export now redacts before writing.

**Proof:** `.local-evidence/p3-source-browser-2026-07-13.txt` (ignored,
untracked). All required package/full Go tests, frontend build, `make test`,
`make build`, gofmt, vet/race spot checks, and diff check pass. Playwright with
`/usr/bin/chromium` proves realistic Claude/Codex search/read/edit/command/
verify/failure/edit-after-verify data, live append, pause/resume, partial-line
completion exactly once, automatic SSE reconnect, same-origin server restart
with sequence continuity, Codex live replay, provenance quality, and unchanged
repository bytes; final durable sequence was 25.

**Next:** P5 AgentProcess read models, deriving only relationships the source
actually records and reporting unsupported parentage/state as `UNKNOWN`.

## 2026-07-13 — Recovery + first usable API/UI slice; P3 in progress

**Recovered reality:** branch `upgrade/multi-repo-observatory`; no staged or
intent-to-add entries; all reported P1/P2 files preserved; `internal/ingest`
contained one untested partial tailer. Private-data scan found no credential
material. Go proof uses `PATH="$HOME/.local/go/bin:$PATH"` in non-login shells.

**P1/P2 repairs:** added root-confined `event.OpenLogAt`; quarantine now stores
only reason, byte count, and SHA-256, never rejected raw content; persisted
registry records are validated; symlink replacement fails closed; atomic
saves fsync the directory; Git metadata reports the actual top-level; and
`name`/`group`/`tags`/`color` plus `edit`/`validate`/`refresh` are tested. A
throwaway CLI add/list/show/disable/enable/remove flow exited 0 and left the
fixture repository unchanged.

**Usable partial vertical slice:** bounded CSRF-protected repository APIs,
registered-ID citymaps, security headers, onboarding, repository picker/add/
enable/disable UI, truthful missing/unsafe/disabled/Git states, persistent
0600 configuration, and registry-only session listing in ordinary `serve`.
Arbitrary browser `?repo=` reads now fail 403; explicit `mindwalk map <repo>`
remains compatible. Playwright with `/usr/bin/chromium` verified onboarding ->
add -> real Git state -> deterministic map (exit 0, no console errors).

**P3 foundation, not complete:** `internal/ingest` now tests incremental and
partial lines, restart offsets, replacement/truncation/missing, bounded
oversized lines, invalid/corrupt state, atomic 0600 persistence, and symlink
attacks. SSE has monotonic sequence IDs, `Last-Event-ID`, heartbeat, and
cancellation tests; the UI reconnects and can pause following without closing
the stream. `internal/redact` covers common credential forms before display.
Source-to-event-ledger wiring, durable stream sequence, quarantine service
integration, and end-to-end live-session browser proof remain unproven; the
corresponding rows stay IN_PROGRESS.

**Proof:** `.local-evidence/recovery-vertical-2026-07-13.txt` (ignored local
evidence). `gofmt -l .`, `go test ./... -count=1`, frontend build,
`make test`, `make build`, and `git diff --check` all exit 0. Every Go package
passes; system-Chromium onboarding proof exits 0 with no console errors.

**Exact next action:** run full proof, then finish P3 adapter-to-event wiring
and synthetic live Claude/Codex browser proof before starting P5/P6/P7.

## 2026-07-13 — P2 GREEN: multi-repository registry + `repos` CLI

**Added:** `internal/product` (centralized naming, §I.0);
`internal/registry` — `paths.go` (CanonicalRepoPath with fail-closed deny
rules: fs root, home itself, ~/.ssh ~/.gnupg ~/.aws ~/.azure ~/.kube
~/.docker ~/.config/gcloud ~/.mozilla ~/.thunderbird ~/.password-store ~/.pki
~/.claude ~/.codex; symlink smuggling blocked; `Within` traversal guard),
`registry.go` (stable path-hash IDs, atomic 0600 temp+fsync+rename saves,
schemaVersion 1, live StatusOf with missing detection), `git.go` (read-only
branch/HEAD/dirty/worktrees/remote via `git -C`, **credentials stripped from
remote URLs**); `cmd/mindwalk/repos.go` (`repos list|add|show|remove|enable|
disable`, `-config` override) + tests incl. CLI-level unsafe-path rejection.
**Modified:** `cmd/mindwalk/main.go` (new `repos` case + usage line),
`AGENTS.md` (Observatory-extension notes appended; upstream text preserved).
Existing commands untouched; zero new deps.

**Proof:** docs/evidence/p2-registry-2026-07-13.txt — 8/8 exit 0 (gofmt, vet,
registry+cmd verbose tests, full go test, make test, make build, diff-check,
status). Ledger: REPO-001..004 → TESTED (groups/tags editing + doctor
deferred to a later checkpoint, noted in REPO-004).

**Next:** P3 — live ingestion: incremental JSONL tailing with persistent
offsets, rotation/truncation recovery, ledger wiring behind content-capture-
off defaults.

## 2026-07-13 — P1.2 GREEN: append-only ledger + projector contract

**Added:** `internal/event/log.go` (JSONL ledger: Verify-gated Append, dedupe
by eventId across reopen, fsync per append, torn-tail crash recovery that
preserves the torn line in a `.quarantine` dead-letter file, malformed/
oversized-line quarantine, never rewrites existing bytes),
`internal/event/projector.go` (Projector interface + Replay), tests for
roundtrip/duplicates/tamper-rejection/torn-tail/quarantine/append-only/
deterministic-idempotent-replay/damaged-ledger replay. Still zero wiring,
zero new deps. SQLite/FTS indexes deliberately deferred — first external dep
needs its own owner-visible decision (P6).

**Proof:** docs/evidence/p1.2-ledger-2026-07-13.txt — 8/8 commands exit 0
(gofmt, go vet ./..., event tests, full go test, make test, make build,
git diff --check, git status). Ledger: EVT-003, EVT-004 → TESTED.

**Next:** P2 — multi-repository registry (`internal/registry`): safe roots,
canonical path + symlink-escape validation, git metadata, then
backward-compatible CLI wiring.

## 2026-07-13 — P1.1 GREEN: trusted event foundation (`internal/event`)

**Added:** `internal/event/{event,id,validate}.go` + three test files;
`schema/event.schema.json`; `schema/provenance.schema.json`;
`docs/EVENT_MODEL.md`. **No existing file touched; no wiring anywhere** —
adapters/server/UI unchanged by design (invariants 6–7). Zero new
dependencies (invariant 8).

**Delivered:** EventEnvelope v1 (camelCase wire form per repo convention);
51 canonical event types; quality states exact/estimated/derived/unavailable/
redacted; provenance record with rawEventHash-not-content; deterministic
SHA-256 identity (`ev1_…`) via canonical serialization (UTC-normalized times,
sorted maps); Finalize/Verify; fail-closed validation; pointer optionals so
missing ≠ recorded-empty; golden identity pin.

**Proof (docs/evidence/p1.1-event-2026-07-13.txt — 8/8 commands exit 0):**
`gofmt -l .` clean · `go test ./internal/event/... -count=1 -v` all pass ·
`go test ./... -count=1` all 8 packages ok · `npm --prefix web run build` ok ·
`make test` ok · `make build` ok · `git diff --check` clean. Ledger: EVT-001,
EVT-002 → TESTED; EVT-005 → IN_PROGRESS (design holds, ingestion enforcement
at P3). All fixtures synthetic (invariant 10).

**Next:** P1.2 — append-only event ledger + projector contract (EVT-003/004),
continuing without owner stop per continuation authorization.

## 2026-07-13 (later still) — Untracked documentation diff VALIDATED (owner process)

Ran Eric's intent-to-add validation: `git add -N` over the 11 intended files
(confirmed exhaustive via `git status --porcelain -uall`), then
`git diff --stat` (697 insertions), `--check`, `--name-only`, full content
diff, and targeted secret/private-data detectors (11 categories).

**Findings and dispositions:**
1. `docs/PROVENANCE.md:6` contained an absolute owner-home path → FIXED to
   `~/Mindwalk-Observatory`.
2. Evidence file had a blank line at EOF (caught by `git diff --check`,
   which the pre-intent-to-add run could not see) → FIXED (single trailing
   newline; transcript content untouched).
3. Secret detector "sk-…" hits at evidence lines 44/46/71/73 → FALSE
   POSITIVE: Schibsted Grotesk font asset filenames in vite output
   (`…grotesk-latin-…`), public upstream assets.
4. NeoAI/CultOS grep hits (3 lines) are intentional absence-statements
   documenting the owner-ordered verification — not copied rules.
5. Markdown fences balanced; no session data; no secrets; no contradictions
   or weakened localhost/read-only defaults found; evidence sections match
   the exact commands reported.

After fixes: `git diff --check` exit 0. Intent-to-add entries removed with
`git reset -- THIRD_PARTY_NOTICES.md docs/`; all files confirmed present and
untracked. Owner decisions this round: origin remains deferred (R-008);
continuation into P1.1+ authorized without per-checkpoint stops.

## 2026-07-13 (later) — Approved checkpoint: Go install + green baseline + doc corrections; STOPPED for review

**Branch:** unchanged — `upgrade/multi-repo-observatory` at `97a543c…`
(== `upstream/master`). No tracked file modified; nothing staged or committed.

**Completed (all owner-approved this checkpoint):**
- Go 1.25.12 installed user-scoped to `~/.local/go` from go.dev; SHA-256
  verified **before** extraction (`go1.25.12.linux-amd64.tar.gz: OK`, matches
  Eric's approved value exactly); no sudo/apt; `~/.local/go` did not
  previously exist; one PATH line appended to `~/.bashrc`.
- Full baseline run — **all green, every command exit 0**: `go version`,
  `go env GOROOT GOPATH`, `gofmt -l .` (clean), `go test ./...` (7/7 packages
  ok), `make test`, `make build` (produces `bin/mindwalk`),
  `git status --short` (docs only), `git diff --stat` / `--check` (empty).
  Complete sanitized transcript: `docs/evidence/baseline-go-2026-07-13.txt`.
- Ledger: BASE-003 → TESTED with evidence ref.
- Doc corrections per owner instruction: `mindwalk map [--no-open] <repo>`
  added to preserved CLI (BASELINE, ARCHITECTURE); `internal/model` stated as
  confirmed upstream package; accurate network-activity statement added to
  BASELINE; verified **zero** NeoAI/CultOS references exist in this repo
  (nothing to remove).
- Design decision D-001 (deliberate hybrid) recorded in
  `docs/DESIGN_DECISIONS.md`; R-002 closed. P4 UI work NOT started.
- R-001 closed (Go installed); R-008 remains open-acknowledged: origin remote
  deferred until Eric reviews the Phase 0 doc diff.

**Unresolved risks:** R-008 (no origin remote — acknowledged, deferred);
R-005 (no upstream CI test workflow); R-006 (esbuild allow-scripts).

**Exact next action:** Eric reviews the documentation set + evidence file.
After his approval: create GitHub origin (his call), then checkpoint P1.1
(`internal/event` skeleton per docs/UPGRADE_PLAN.md). P1.1 is NOT started.

## 2026-07-13 — Phase 0 executed (baseline + docs); STOPPED for owner input

**Branch:** `upgrade/multi-repo-observatory` at
`97a543c2272b38cb5b8ea9b1b067b21e8ac039cb` (== `upstream/master`,
`v0.1.0-8-g97a543c`). Working tree was clean before Phase 0; after Phase 0 the
only changes are new untracked docs (no source file touched, nothing staged,
nothing committed).

**Completed:**
- Baseline captured and verified — see `docs/BASELINE.md` (real command
  output; §I.5 discipline).
- Upstream pin + license (MIT © 2026 Ricko Yu) verified; attribution set
  written: `THIRD_PARTY_NOTICES.md`, `docs/PROVENANCE.md`,
  `docs/OPEN_SOURCE_INVENTORY.md`.
- `docs/ARCHITECTURE.md` (as-found + planned target), `docs/UPGRADE_PLAN.md`
  (P0–P10 + proposed P1.1 checkpoint), `docs/REQUIREMENTS_LEDGER.md`
  (37 rows), `docs/RISK_REGISTER.md` (10 risks).

**Tests run (verbatim results in docs/BASELINE.md):**
- `make setup` → PASS (88 packages, 0 vulnerabilities)
- `npm --prefix web run build` → PASS (tsc + vite, exit 0)
- `make test` → FAIL at `go test ./...`: `make: go: No such file or directory`
- `make build` → web/embed-static steps PASS (tree stayed byte-identical);
  `go build` FAIL (Go missing)
- `go test ./...`, `gofmt -l .` → NOT RUN (no Go toolchain)

**Unresolved risks:** R-001 Go toolchain missing (HIGH, blocks P0 completion
and all of P1+); R-002 design-language conflict (needs Eric before P4);
R-008 no origin remote (work exists only on this laptop).

**Exact next action:** Eric decides R-001 (Go install method). Then: complete
baseline (`go test ./...`, `gofmt -l .`, `make build`), update BASE-003 →
TESTED with pasted output, then start checkpoint P1.1
(`internal/event` skeleton — see docs/UPGRADE_PLAN.md).
