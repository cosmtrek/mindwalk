# UPGRADE PLAN — Mindwalk → Mindwalk Observatory

Phased per the master prompt §V.3. A phase does not advance while the current
one has unexplained failures. Blocked optional integrations are deferred
honestly, never faked.

## Phase gates

| Phase | Scope | Proof gate |
|---|---|---|
| **P0** | Baseline, attribution, ledger, architecture, risk | **COMPLETE + TESTED** 2026-07-13 |
| **P1** | Canonical `EventEnvelope`, provenance fields, append-only log, projector contract | **COMPLETE + TESTED**; privacy hardening added during recovery |
| **P2** | Repo registry, safe roots, Git/worktree metadata, config migrations | **COMPLETE + TESTED**; API/UI metadata extensions added during recovery |
| **P3** | Incremental live ingestion, persistent offsets, stream resume | **COMPLETE + TESTED** 2026-07-13; synthetic Claude/Codex restart browser proof |
| **P4** | Owner Command Center + accessible views | **RC UI TESTED**; decorative Universe macro view deferred without fabricated data |
| **P5** | AgentProcess registry + parent/sub-agent visualization | **COMPLETE + TESTED**; supported relationships only, display-only controls |
| **P6** | Second-brain ledger (Markdown/JSONL/FTS) + search UI | **COMPLETE + TESTED**; explicit writes, provenance, correction/tombstone |
| **P7** | Session comparison, review center, provenance explorer, exports | **COMPLETE + TESTED**; deterministic fixtures, redacted Markdown/JSON |
| **P8** | Optional integration capability contracts | **COMPLETE + TESTED** as disabled receive/read/dry-run contracts; no external action |
| **P9** | Security, SBOM, vuln checks, accessibility, low-power performance | **COMPLETE + TESTED**; scans clean, SBOM valid, measured browser flow |
| **P10** | Packaging, backup/restore, docs, end-to-end RC | **COMPLETE + TESTED**; isolated user setup and restore acceptance green |

## P0 completion record

Eric approved the Go install on 2026-07-13 (Go 1.25.12, user-scoped,
checksum-verified — see docs/BASELINE.md). The previously blocked baseline is
now fully green: `gofmt -l .` clean, `go test ./...` all 7 packages ok,
`make test` and `make build` exit 0, `bin/mindwalk` builds. Evidence:
`docs/evidence/baseline-go-2026-07-13.txt`. BASE-003 → TESTED.

## Completed Phase 1 checkpoint (historical plan)

**Checkpoint P1.1 — `internal/event` package skeleton, zero behavior change.**

- Files: `internal/event/envelope.go` (EventEnvelope v1 struct: §III.1
  provenance fields, §III.2 canonical event types as constants),
  `internal/event/id.go` (deterministic event IDs + raw/normalized SHA-256
  hashes), `internal/event/id_test.go`, `internal/event/envelope_test.go`,
  `schema/event.schema.json` (v1, `const` version like the existing schemas),
  `docs/EVENT_MODEL.md`.
- Explicitly NOT in this checkpoint: no wiring into adapters/server/UI, no
  ingestion, no persistence. Existing commands and tests untouched.
- Acceptance: `go test ./internal/event/` green; `go test ./...` still green;
  `gofmt -l .` empty; `npm --prefix web run build` still green; schema file
  validates against the test fixtures; ledger rows EVT-001/EVT-002 →
  IMPLEMENTED/TESTED with pasted output.

## Owner decisions — status

1. Go install (R-001) — **APPROVED and done** (2026-07-13, evidence in
   docs/BASELINE.md).
2. Design language (R-002) — **DECIDED**: deliberate hybrid, recorded in
   docs/DESIGN_DECISIONS.md D-001. P4 UI work not started.
3. Origin remote (R-008) — **DEFERRED by Eric**: GitHub repo will be created
   after the Phase 0 documentation diff is reviewed. Nothing pushed until then.
4. P1.1 start — **AUTHORIZED and completed**. Continuation through local
   release-candidate checkpoints is authorized; external/risky boundaries
   remain unchanged.

## Completed P3 checkpoint

Claude Code/Codex discovery is wired through the existing adapters and tested
tail/state primitives to redacted deterministic envelopes, the append-only
ledger, replay-safe projectors, durable bounded SSE, and live/provenance UI.
Synthetic browser proof covers pause/resume, partial completion, reconnect,
same-origin process restart, both harnesses, and unchanged repository bytes.
Evidence: `.local-evidence/p3-source-browser-2026-07-13.txt`.

## Local release-candidate checkpoint

P5-P10 and the release-definition workflow are implemented and proven in the
ignored `.local-evidence/rc-proof-2026-07-13.txt`. The RC remains local and
uncommitted by instruction. Decorative Universe expansion and any future
CONTROLLED/AUTONOMOUS adapter require a separate owner decision and are not
represented by fake data or controls.
