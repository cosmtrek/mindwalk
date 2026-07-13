# PROVENANCE

## Origin

- Derived from https://github.com/cosmtrek/mindwalk (MIT, © 2026 Ricko Yu).
- Local working tree: `~/Mindwalk-Observatory`, git remote `upstream`
  → `https://github.com/cosmtrek/mindwalk.git`. No `origin` remote exists.
- Branch `upgrade/multi-repo-observatory` is currently **identical** to
  `upstream/master` at `97a543c2272b38cb5b8ea9b1b067b21e8ac039cb`
  (`v0.1.0-8-g97a543c`). No Eric-specific code exists yet; only the
  documentation set added in Phase 0 (this file and siblings).

## Component adoption ledger (§II.4 ladder)

Every external component gets a row: ADOPT / ADAPT / REFERENCE-ONLY / REJECT.

| Component | Decision | Reason |
|---|---|---|
| cosmtrek/mindwalk (whole codebase) | **ADOPT + ADAPT** | Working, MIT, matches mission; Observatory extends it in place. Attribution preserved. |
| React / Vite / Three.js / zustand / TypeScript stack | **ADOPT** | Inherited from upstream; permissive licenses; proven by baseline build. |
| OpenTelemetry GenAI semantic conventions | **REFERENCE-ONLY (for now)** | P1–P2 design reference for EventEnvelope field naming; pin the convention version at adoption time. |
| OTel Collector filelog-receiver offset pattern | **REFERENCE-ONLY** | Design reference for P3 persistent offsets; no collector dependency in the default binary. |
| W3C PROV concepts | **REFERENCE-ONLY** | Vocabulary for provenance semantics (§III.1); no RDF dependency. |
| CycloneDX | **PLANNED-ADOPT (P9)** | SBOM format; tooling choice decided at P9 with owner approval for any install. |
| OSV-Scanner / OpenSSF Scorecard | **PLANNED-REFERENCE (P9)** | Dependency/upstream risk checks; requires install approval. |

New rows are appended when any new dependency or borrowed code is considered.
