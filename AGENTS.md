# AGENTS.md

`mindwalk` is a local visualizer for coding-agent sessions. It supports Claude Code, Codex, pi, and Copilot, turning agent session logs plus repository structure into a deterministic 3D "code city" that can be explored in a browser.

## Design

The project has three primary artifacts:

- A normalized trace of what happened during a supported coding-agent session.
- A deterministic citymap of the repository being edited or inspected.
- An evaluation report: an LLM judge's findings about one session — four fixed process dimensions plus a task-specific rubric layer — generated on explicit request only.

The UI combines those artifacts so users can see how a coding agent moved through a codebase over time — and, when asked, how well. Keep the separation clear: source-specific parsing should not know about rendering, citymap generation should not depend on session playback, the judge reads only the normalized trace (never raw session logs), and the server should mainly connect data sources to the web client.

## Architecture

- `cmd/mindwalk` provides the CLI commands: serve a local UI, open a session, build a citymap, export a trace, or evaluate a session.
- `internal/adapter` converts supported agent session formats into the shared model. Claude Code, Codex, pi, and Copilot each have an adapter; keep every source, current and future, behind its adapter boundary.
- `internal/model` owns the trace, citymap, and report data contracts.
- `internal/citymap` builds deterministic layouts from repository contents.
- `internal/judge` renders a trace into an evidence document and runs a sealed local agent CLI (claude or codex) over it in up to two calls: the first drafts a task rubric — task-grouped criteria derived from the session's user messages — and the second is one unified scoring pass over the four fixed dimensions plus any rubric criteria. The rubric phase can skip (no events, no or too-little task text), reuse the cached report's rubric when the task wording is unchanged, or degrade to a dimensions-only report when generation fails; it never blocks the fixed layer. The judge subprocess gets no tools; verdicts — per dimension and per criterion — are always derived mechanically from finding severities and coverage, never decided by the LLM. Reports are cached in `~/.mindwalk/reports`; `docs/dynamic-rubric-evaluation.md` explains the rubric layer.
- `internal/server` exposes local APIs and serves the web app. `internal/server/static` holds the embedded frontend assets generated from `web/dist`.
- `web` contains the React, Vite, and Three.js frontend.
- `schema` mirrors the exported JSON contracts.

The normal flow is:

```text
Agent session log (Claude Code, Codex, pi, or Copilot) + repository path
  -> Go adapters and citymap builder
  -> local Go server APIs
  -> React/Three.js playback UI
       └─ evaluate (explicit request) -> internal/judge -> report panel
```

## Development

- Use `make setup` to install frontend dependencies.
- Use `make test` for the standard validation pass.
- Use `make serve` for local development.
- Use `make build` when refreshing the distributable binary and embedded frontend assets.

Keep Go code formatted with `gofmt`. Do not hand-edit `internal/server/static`; when bundled assets need to change, regenerate them with `make build` (or `make embed-static`). When trace, citymap, or report JSON shapes change, update `schema` and the relevant tests in the same change.

Evaluation invariants worth protecting: a judge run starts only from an explicit user action (never from scanning), the judge subprocess stays sealed (no tools, no user config, no session persistence — see `internal/judge/cli.go`), every finding must cite real trace events, and the trace content handed to the judge is untrusted input. The rubric layer inherits that stance: a rubric is derived from untrusted input and stays untrusted — hard shape and size caps, passed to the scoring call as data, never as instructions — the four fixed dimensions are enforced by Go regardless of what the rubric contains, and a criterion the log cannot verify loses coverage instead of gaining a warning.
