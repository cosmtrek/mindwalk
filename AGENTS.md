# AGENTS.md

`mindwalk` is a local visualizer for coding-agent sessions. It supports Claude Code and Codex, turning agent session logs plus repository structure into a deterministic 3D "code city" that can be explored in a browser.

## Design

The project has two primary artifacts:

- A normalized trace of what happened during a supported coding-agent session.
- A deterministic citymap of the repository being edited or inspected.

The UI combines those artifacts so users can see how a coding agent moved through a codebase over time. Keep this separation clear: source-specific parsing should not know about rendering, citymap generation should not depend on session playback, and the server should mainly connect data sources to the web client.

## Architecture

- `cmd/mindwalk` provides the CLI commands: serve a local UI, open a session, build a citymap, export a trace, or manage the repository registry (`repos`).
- `internal/adapter` converts supported agent session formats into the shared model. Claude Code and Codex each have an adapter; keep every source, current and future, behind its adapter boundary.
- `internal/model` owns the trace and citymap data contracts.
- `internal/citymap` builds deterministic layouts from repository contents.
- `internal/server` exposes local APIs and serves the web app. `internal/server/static` holds the embedded frontend assets generated from `web/dist`.
- `web` contains the React, Vite, and Three.js frontend.
- `schema` mirrors the exported JSON contracts, including event provenance
  and registered repository status.

Observatory extensions (see `docs/ARCHITECTURE.md` and `docs/EVENT_MODEL.md`):

- `internal/event` owns the canonical EventEnvelope, deterministic event
  identity, the append-only JSONL ledger with quarantine, and the projector
  contract. Nothing may write the ledger except through `event.Log`.
- `internal/registry` owns the explicit, owner-curated registry of observable
  repositories and its optional discovery preview: canonical paths,
  fail-closed deny rules, bounded directory-metadata traversal, read-only Git
  metadata, stable discovery IDs, and private atomic persistence. There is no
  automatic or whole-filesystem discovery scan.
- Repository discovery is a separate preview/approval boundary. It stays off
  until an owner explicitly approves roots and starts it, never follows
  directory symlinks or crosses a scan-root filesystem, never reads ordinary
  file contents, and never registers a result. Only the existing registry may
  register exact, revalidated discovery IDs after final owner confirmation.
- `internal/ingest` owns bounded read-only JSONL tailing and atomic persistent
  resume state. Source parsing stays behind source adapters.
- `internal/redact` is the display/export redaction boundary for normalized
  free text. Rejected raw lines are never copied into durable quarantine.
- `internal/server/repositories.go` exposes only registered repository roots;
  ordinary `serve` filters sessions to enabled registered repositories.
- `internal/product` centralizes product naming so the working title can be
  renamed in one place.

The normal flow is:

```text
Agent session log (Claude Code or Codex) + repository path
  -> Go adapters and citymap builder
  -> local Go server APIs
  -> React/Three.js playback UI
```

## Development

- Use `make setup` to install frontend dependencies.
- Use `make test` for the standard validation pass.
- Use `make serve` for local development.
- Use `make build` when refreshing the distributable binary and embedded frontend assets.

Keep Go code formatted with `gofmt`. Do not hand-edit `internal/server/static`; when bundled assets need to change, regenerate them with `make build` (or `make embed-static`). When trace or citymap JSON shapes change, update `schema` and the relevant tests in the same change.
