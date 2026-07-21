---
name: verify
description: Build, launch, and drive the mindwalk web UI end-to-end for verification.
---

# Verifying mindwalk

## Build & launch

```sh
npm --prefix web run build                  # tsc -b && vite build → web/dist
go build -o bin/mindwalk ./cmd/mindwalk
bin/mindwalk serve --no-open --dev --port <PORT> # --dev serves web/dist from the working tree
```

Gotchas:

- Always pass `--no-open` during automated verification so repeated server
  starts do not create tabs or steal focus from the user's current work.
- Port 8765 is the vite-proxy convention and is often already taken by a dev
  server; pick another port and check the log for `bind: address already in use`.
- Sessions come from `~/.claude/projects` — this machine has real data, no
  fixtures needed. `testdata/claude-session.jsonl` works via `mindwalk open`.
- `bin/mindwalk map <repo>` (or the `/?map=1&repo=<path>` URL) serves the
  static citymap with no session.

## Drive (headless Chrome + CDP, no npm installs)

System Chrome + raw CDP over Node's built-in WebSocket works. WebGL renders
under plain `--headless=new` on the real GPU (Metal, ~120fps); add
`--use-angle=swiftshader --enable-unsafe-swiftshader` only if GPU init fails
(software rendering is ~3x slower):

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  --headless=new --remote-debugging-port=9333 --user-data-dir=<tmp> \
  --window-size=1440,900 --no-first-run about:blank &
```

New-tab endpoint needs PUT: `fetch('http://127.0.0.1:9333/json/new?<url>', {method:'PUT'})`.

## Flows worth driving

- Load via `/?session=<key>` deep link (legacy bare session id resolves only if
  unique); assert `.session-row.active .session-title`.
- Readout `.deck-pos-count` shows `N / N` after load (playhead starts at end).
- `[aria-label="Restart playback"]` → `1 / N`; `[aria-label="Play playback"]`
  → ~3 ticks/second at 1× (the `.speed-btn` / `S` key cycles the multiplier);
  playback draws the ember trail + firefly.
- Scene view lives in the dock's scene section: the top strip icon (tree/
  mountain shape mirrors the current view) opens a compact pop with `Tree` /
  `Terrain` radio rows plus the encoding legend; pressing `V` cycles views
  directly. Switching rebuilds the scene — watch for `Runtime.exceptionThrown`.
  The pop coexists with an open sheet (report stays open while switching).
- `/?map=1&repo=<abs-path>` renders the citymap with no trace and no transport
  (map-only mode); the rail-head folder icon (`[aria-label="Open a repository
  map"]`) opens a popover — primary card for the active session's repo (name +
  path), then an "or open any repository" path input — and opens the map via
  `window.open`, so in headless drive the URL directly instead of clicking.
- The right-edge dock strip is a panel registry with two sections (scene /
  session, hairline divider): View (pop), `Crosshair` = Inspect (sheet; file
  details, teaching empty state when nothing is selected), `Sparkles` =
  Evaluate (sheet; judge report, status dot mirrors running/done/stale/failed).
  Clicking a report finding jumps the playhead to its evidence seq and selects
  the file; the rail rows mirror evaluation state with `.rail-eval` badges.
- `[aria-label="Export video"]` records playback client-side (MediaRecorder →
  webm download): the label flips to `Recording video`, the transport, rail,
  and view toggle lock while recording, and the playhead restores afterwards.
  Capture the download in CDP with `Browser.setDownloadBehavior`.
- Rapid session switch (click uncached row, 150ms later a cached row) must end
  showing the last-clicked session's data.
- Bogus `?session=` must fall back to the newest session with a console.warn.

### Trace Health

- Open a session whose `GET /api/sessions/<key>/health` response has no badge
  and only exact evidence where applicable. The Trace Health shield must exist
  without a dot; opening it must show the four evidence rows and leave every
  `Technical details` disclosure closed.
- Open a session whose response has `badge: "limited"`. The shield gets the
  quiet limited dot. Unavailable and failed rows sort before estimated, exact,
  and not-needed rows; unavailable is neutral rather than red/error styling.
  Expand a row, then open `Technical details` and verify the reason code,
  affected features, and direct/inferred or recognized counts match the JSON.
- To exercise local failure handling, use CDP `Fetch` interception to fail only
  `GET /api/sessions/<key>/health`. The pop shows `Retry trace health` while the
  map canvas and `.deck-pos-count` remain usable. Remove the interception and
  click retry; only the health request repeats and the rows recover.
- Capture the health payload, click `Rescan sessions`, and verify a fresh
  sessions request and refreshed snapshot complete before health is requested
  again. The shield badge and open panel must update to the second payload.
- Keep a Network-domain request log throughout the exact, limited, retry, and
  Rescan checks. There must be zero `POST /api/sessions/<key>/analyze` requests
  and no Evaluate status transition: Trace Health never starts the judge.
