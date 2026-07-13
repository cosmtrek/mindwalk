# ARCHITECTURE — Mindwalk Observatory

Status labels: IMPLEMENTED (working today) · IN_PROGRESS (partially proven) ·
PLANNED (not built). Nothing partial is claimed complete.

## As found (IMPLEMENTED — upstream v0.1.0-8-g97a543c)

```text
Agent session log (Claude Code JSONL / Codex) + repository path
  -> internal/adapter          one adapter per source (claudecode/, codex/)
       normalizes into the shared trace model
  -> internal/model            trace + citymap data contracts, stats
  -> internal/citymap          deterministic repo layout (same tree -> same map)
  -> internal/server           localhost-only API (127.0.0.1), joins trace +
                               citymap, serves embedded frontend
       internal/server/static  GENERATED from web/dist — never hand-edit
  -> web/                      React 19 + Vite 7 + Three.js playback UI
schema/                        exported JSON contracts (trace v1, citymap v1)
```

Boundaries that MUST be preserved (upstream rule + operating contract §II.3):

- Adapters do not know about rendering.
- Citymap generation does not depend on session playback.
- The renderer never parses raw provider logs.
- The server only connects bounded modules and enforces access policy.
- Trace/citymap JSON shape changes update `schema/` + tests in the same change.

CLI (IMPLEMENTED — all five commands preserved): `mindwalk serve [--port N]
[--no-open] [--claude-dir DIR] [--codex-dir DIR]` ·
`mindwalk open [--no-open] <session.jsonl>` ·
`mindwalk map [--no-open] <repo>` (repository citymap with no session) ·
`mindwalk build <repo> [-o out]` · `mindwalk trace <session> [-o out]`.
No args: scans `~/.claude/projects` + `~/.codex/sessions`, random local port,
opens browser.

## Observatory extension (P1-P10 release-candidate implementation)

The extension inserts an event spine *underneath* the existing flow without
breaking it:

```text
source logs / OTLP / local exports
  -> source-specific adapters and collectors        (extends internal/adapter)
  -> canonical EventEnvelope                        (new internal/event)
  -> append-only local event log (JSONL)            (new, durable truth)
  -> projectors + rebuildable SQLite FTS5 index     (new, replay-safe)
  -> trace, provenance, memory, citymap read models
  -> local API + SSE/WebSocket stream               (extends internal/server)
  -> React/Three.js owner UI                        (extends web/)
```

Extension modules, all behind the same boundaries:

- `internal/event` — IMPLEMENTED + TESTED EventEnvelope v1, deterministic
  identity, append-only root-confined ledger, quarantine metadata, replay.
- `internal/registry` — IMPLEMENTED + TESTED explicit multi-repo registry,
  owner metadata, atomic persistence, Git/worktree observation. Its bounded
  discovery extension is IMPLEMENTED + TESTED by synthetic Go and Chromium
  acceptance proof.
- `internal/server/repositories.go` + web picker/onboarding — IMPLEMENTED
  registered-repository API/UI slice. The optional discovery/selection
  extension is IMPLEMENTED + TESTED.
- `internal/ingest`, SSE, and `internal/redact` — IMPLEMENTED + TESTED
  configured-root discovery, resume state, source quarantine, adapter-to-ledger
  normalization, replay projectors, durable bounded SSE, live-follow, and
  provenance inspection.
- `internal/agents` — deterministic display-only AgentProcess projection;
  unsupported lifecycle/parentage remains `UNKNOWN`.
- `internal/brain` — explicit append-only redacted JSONL/Markdown memory,
  corrections/tombstones/namespaces, and rebuildable SQLite FTS5 search.
- `internal/review` — deterministic review/comparison read models and redacted
  Markdown packets; unsupported memory comparison is `UNAVAILABLE`.
- `internal/integration` — disabled capability contracts only: receive-only,
  read-only, or dry-run; no external action implementation.
- `web/` — real API-backed repository/session/live/provenance/agent/memory/
  review surfaces plus accessible no-WebGL List mode and reduced motion.
- `scripts/` — user-scoped setup/run/doctor/backup/restore/uninstall/desktop
  flows; restore/uninstall are dry-run until explicit `--apply`.

Design constraints carried everywhere: metadata-only capture by default;
redaction before persistence AND display; loopback-only; SAFE mode default;
every derived Observatory fact carries provenance or shows
UNAVAILABLE/REDACTED/DISCONNECTED. No demo/fabricated data path is enabled.

## Owner-started repository discovery (IMPLEMENTED + TESTED)

Discovery extends the existing registry; it is not a second repository
system and it is not part of session-log discovery:

```text
existing owner-selected directory roots
  -> PUT /api/repository-discovery/config       canonicalize + persist approval
  -> POST /api/repository-discovery/start       explicit second action
  -> internal/registry.DiscoveryScanner         bounded metadata-only walk
  -> polling status + replace-in-place result snapshot
  -> owner filters/selects exact stable discovery IDs
  -> final metadata confirmation
  -> POST /api/repository-discovery/register    revalidate ID/path/root
  -> existing internal/registry.Registry.Add    selected repositories only
```

The scanner never runs on server/browser startup. It resolves each explicitly
selected root, rejects `/`, protected system/credential/private-data roots,
stays on the root filesystem, and skips directory symlinks. Locked exclusions
cover caches, dependency/build trees, browser profiles, credential stores,
configured deny paths, Trash, and Observatory state. Custom exclusions can
only add restrictions. Discovery reads directory entries and timestamps plus
bounded, rooted `.git` indirection, `HEAD`, loose/packed refs, and worktree
`commondir` metadata. It does not invoke Git, read Git config, or open ordinary
source and `.env` files. Hardened read-only Git commands remain a separate
post-registration status path.

`DiscoveryOptions` defaults to depth 10, 25,000 directories, 2,000 results,
and 300 seconds. Nested-repository descent defaults off; following directory
symlinks is fixed off. Cancellation and timeout use contexts. Progress is
updated in memory at bounded checkpoints and polled over HTTP rather than
streaming every examined directory. Permission failures increment counters
and do not abort other roots. Repository cleanliness may truthfully remain
`unknown` when discovery cannot establish it within the metadata-only
boundary.

State is stored beside the owner registry in a collision-free private, atomic
`<registry filename>.discovery.json` sidecar.
It contains approved roots, custom exclusions, bounds, hidden discovery
tokens, last scan time/summary, and one replace-in-place repository-only
result snapshot required by `discovered`, `add-discovered`, and hidden-result
recovery. It never records ordinary directories examined and is not an
append-only scan history. **Forget scan history** deletes that snapshot and
summary while retaining owner preferences; **Reset exclusions** clears only
custom exclusions and cannot weaken locked rules.

Loopback HTTP contract:

| method | route | purpose |
|---|---|---|
| `GET`, `PUT` | `/api/repository-discovery/config` | inspect/update owner-approved roots, custom exclusions, and hard bounds |
| `POST` | `/api/repository-discovery/start` | start an asynchronous scan of an already-approved root set |
| `GET` | `/api/repository-discovery/status` | poll real bounded progress |
| `POST` | `/api/repository-discovery/cancel` | cancel the active scan |
| `GET` | `/api/repository-discovery/results?showHidden=1` | read the latest repository-only preview |
| `POST` | `/api/repository-discovery/hide` | hide or recover stable discovery IDs |
| `POST` | `/api/repository-discovery/register` | add exact confirmed IDs through the existing registry |
| `POST` | `/api/repository-discovery/forget` | forget latest results and summary |
| `POST` | `/api/repository-discovery/reset-exclusions` | remove custom exclusions only |

Every mutating route requires the existing same-origin/CSRF guard and bounded
JSON decoding. Start rejects roots not already in the owner-approved set.
Registration never accepts a replacement path: it resolves the submitted ID
from the latest snapshot, revalidates the canonical repository and approved
root, then reports per-repository success or failure without rolling back
other successful registrations.

CLI parity is under `mindwalk repos`: `discover --root <path>` (repeatable) or
`discover --home` creates a preview; `discover-status`, `discover-cancel`, and
`discovered` inspect/control it; only explicit `add-discovered <id>...`
registers results; `hide-discovered` and `unhide-discovered` manage visibility.
CLI discovery is preview-only by default.

## Design-language note

DECIDED by Eric on 2026-07-13: a deliberate hybrid — upstream's nocturnal
foundation, touch states, and warm-amber edit semantics, plus Eric's cosmic
background, neon-blue plasma, cyan/violet activity, and sacred-geometry layout
systems. Not a generic cyberpunk dashboard; every glow backed by a real event.
Full decision record: `docs/DESIGN_DECISIONS.md` D-001. The functional P4
slice has started; broad decorative expansion remains deferred.
