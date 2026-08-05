# <img src="assets/logo.svg" alt="" width="30" /> mindwalk

A visualization tool that replays coding-agent sessions on a 3D map of your codebase.

https://github.com/user-attachments/assets/5153481b-3805-45e6-a61f-372250a969eb

## The problem

A session log records what an agent did, but not how it understood the task:
which parts of the repo it treated as relevant, where it explored before it
acted, whether its footprint matched the scope you had in mind. Reading the
raw JSONL line by line doesn't answer any of that.

## The idea

Draw the repository as a night map, and play the session back as light moving
through it: where the agent searched, read, and edited, the map glows —
everything else stays dark. The agent's understanding of the task becomes a
shape you can see at a glance. One Go binary reads Claude Code, Codex, and pi
session logs, fully local; viewing sends nothing anywhere. The one exception
is the optional session evaluation: when you explicitly run it, a summary of
that session (task wording, file paths, event digests) is sent to the model
behind your own `claude` or `codex` CLI — see
[Session evaluation](#session-evaluation).

## Quick start

```sh
curl -fsSL https://raw.githubusercontent.com/cosmtrek/mindwalk/master/scripts/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
mindwalk
```

The installer verifies the binary against `checksums.txt` and installs to
`~/.local/bin` (override with `INSTALL_DIR`; pin a release with `VERSION`).
Windows archives are on [GitHub Releases](https://github.com/cosmtrek/mindwalk/releases)
To build from source: `make setup && make build` → `bin/mindwalk`.

>[!TIP]
>**Nix** users can add mindwalk via [numtide/llm-agents](https://github.com/numtide/llm-agents.nix) flake.

With no arguments, mindwalk scans `~/.claude/projects`, `~/.codex/sessions`,
and `~/.pi/agent/sessions`, serves the UI on a random local port, and opens a
browser:

```text
mindwalk serve [--port N] [--no-open] [--claude-dir DIR] [--codex-dir DIR] [--pi-dir DIR]
mindwalk open [--no-open] <session.jsonl>   open one specific session
mindwalk map [--no-open] <repo>             open a repository map, no session needed
mindwalk build <repo> [-o out]              write the repository citymap JSON
mindwalk trace <session> [-o out]           write the normalized trace JSON
mindwalk analyze <session> [--judge claude|codex] [--model name] [--no-rubric]
                                            evaluate one session (see below)
```

## Reading the picture

- **Tree / Terrain views** — the repo as a radial tree or a treemap plain;
  glow ∝ how deeply and how often a file was touched.
- **Touch states** — each file keeps its deepest touch: seen (moss green),
  read (moonlight blue), edited (warm amber), unvisited (dark). Files the
  session touched that are no longer in the repo linger as wireframe ghosts.
  The HUD folds friction signals — error rate, churned files, edits after the
  last verify — into a review strip.
- **Playback deck** — scrub or play the session over a bucketed histogram of
  the run. Bars sit on a cool/warm spectrum: observation stays cool (search,
  read, exec), mutation glows warm (edit, verify), so editing phases jump out
  at a glance. Restart, speed, and video export fold into the deck's `⋯` menu;
  export records the playback to a `.webm` entirely client-side.
- **Timeline marks** — `◇` context compactions, `○` subagent launches,
  `›` user turns; every mark is a click-to-jump target.
- **Agent lenses** — when a session launched subagents, the HUD carries a
  subagent count and an agents panel: pick a lens to replay any subagent's
  trace on the same map, then step back out to the main trace.
- **Inspector** — click a file to pin its visit history; click a visit row to
  jump the playhead to that moment.
- **Evaluate** — ask a local agent CLI to judge the session's trajectory,
  scored against criteria drafted from your own request; session rows carry
  the evaluation state as a quiet badge. See
  [Session evaluation](#session-evaluation).
- **Repo map** — `mindwalk map <repo>` (or the folder icon in the session
  rail) renders any repository's citymap with no session attached; height
  encodes lines of code instead of attention.

![the same session on the terrain view](assets/screenshot-terrain.png)

![agent lenses over the same session](assets/screenshot-agents.png)

Keyboard: `Space` play/pause · `←`/`→` step (`⇧` ×10) · `Home`/`End` ends ·
`S` speed · `V` view · `E` next edit · `X` next error · `M` next mark ·
`⌘B` session rail.

## Session evaluation

The evaluate panel (and `mindwalk analyze`) asks a local agent CLI to judge
how the session went. A report has two layers:

- **Process dimensions** — exploration, scope, wandering, verification: four
  fixed lenses, the same for every session, so reports stay comparable.
- **Task scorecard** — before scoring, the judge drafts criteria from your
  own request wording: what would count as done for *this* task, grouped per
  task when the session carried several. Each criterion is then scored
  against the session, alongside the dimensions, in one pass.

Every finding in either layer must cite timeline events you can click
through to, and no verdict is the model's to decide: dimension and criterion
verdicts are rolled up mechanically from finding severities. When the log
simply can't show whether a criterion was met, its coverage drops and the
verdict reads "no signal" — an unverifiable criterion is a blind spot, not a
failure. Pick the judge (any installed CLI) and its model in the panel; the
report records who actually judged.

The scorecard steps aside rather than getting in the way: sessions with no
tool events or too little task text skip it, and a failed criteria draft
degrades to a dimensions-only report. `--no-rubric` (or `"rubric": false` on
the analyze API) skips it explicitly, in a single judge call. How the
scorecard is built — and why it is shaped the way it is — is covered in
[docs/dynamic-rubric-evaluation.md](docs/dynamic-rubric-evaluation.md).

**What leaves your machine, and only when you ask:** evaluation runs your own
`claude` or `codex` CLI — up to two sealed calls, one drafting criteria and
one scoring. Both send only that session's summary — the user messages'
wording, file paths, and one-line event digests — to the model behind your
account. Nothing is sent while viewing sessions, and no other session is
included. The judge subprocess runs sealed: no tools, no MCP servers, no user
or project settings, and no session persistence.

Reports are cached in `~/.mindwalk/reports`, one per session; a report goes
stale (never auto-reruns) when the session's content changes. Re-evaluating
a session whose task wording hasn't changed reuses the drafted criteria —
scores can move, the yardstick doesn't.

## Under the hood

Three artifacts, kept deliberately separate:

1. a **trace** — the session log normalized into an ordered stream of
   file-touch events (`internal/adapter`, one adapter per agent format);
   adapters also correlate subagent sessions into an agent graph, so each
   subagent's trace can be replayed on its own;
2. a **citymap** — a deterministic layout of the repository
   (`internal/citymap`); the same tree always produces the same map, so
   replays are comparable across sessions;
3. a **report** — an LLM judge's evidence-anchored findings about one
   session (`internal/judge`): four fixed process dimensions plus a
   task-specific scorecard; the judge only contributes findings, verdicts
   are always rolled up mechanically, so reports stay comparable too.

A local Go server (`internal/server`) joins them and serves the
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
  doesn't depend on playback, the judge reads only the normalized trace, and
  the server just connects the pieces.
- Keep Go code `gofmt`-ed; never hand-edit `internal/server/static` —
  regenerate it with `make build`.
- When trace, citymap, or report JSON shapes change, update `schema/` and the
  relevant tests in the same change.

## Star History

<a href="https://www.star-history.com/?repos=cosmtrek%2Fmindwalk&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=cosmtrek/mindwalk&type=date&theme=dark&legend=top-left&sealed_token=6ylPq85HVVSbxQtqpYdSNx2EFZXMTk4AhnMG197AQm7TDwfenvf415jqPnPRxRiXz4l_f7NRUM2OlNDptSLXC18Q7cX8CQpUBkJtepMUJg6gYhdNM9fTBqBN08fY19HNfmoCFjN2SThT9w81tO_WWCThVBZtf8tMRUC7Bmi3jJ3HFs-4734aDGFw-LOe" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=cosmtrek/mindwalk&type=date&legend=top-left&sealed_token=6ylPq85HVVSbxQtqpYdSNx2EFZXMTk4AhnMG197AQm7TDwfenvf415jqPnPRxRiXz4l_f7NRUM2OlNDptSLXC18Q7cX8CQpUBkJtepMUJg6gYhdNM9fTBqBN08fY19HNfmoCFjN2SThT9w81tO_WWCThVBZtf8tMRUC7Bmi3jJ3HFs-4734aDGFw-LOe" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=cosmtrek/mindwalk&type=date&legend=top-left&sealed_token=6ylPq85HVVSbxQtqpYdSNx2EFZXMTk4AhnMG197AQm7TDwfenvf415jqPnPRxRiXz4l_f7NRUM2OlNDptSLXC18Q7cX8CQpUBkJtepMUJg6gYhdNM9fTBqBN08fY19HNfmoCFjN2SThT9w81tO_WWCThVBZtf8tMRUC7Bmi3jJ3HFs-4734aDGFw-LOe" />
 </picture>
</a>

## License

[MIT](LICENSE) © 2026 Ricko Yu
