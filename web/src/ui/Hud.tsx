import { Map as MapIcon } from "lucide-react";
import { memo, useEffect, useRef, useState } from "react";
import type { ActionCounts, CityMap, MetricObservability, Trace } from "../types";
import type { SceneView } from "../state/store";

export interface ChurnEntry {
  path: string;
  edits: number;
}

interface HudProps {
  trace?: Trace;
  city?: CityMap;
  view: SceneView;
  // live counts at the playhead, passed as primitives so memo stays effective
  editedNow: number;
  readNow: number;
  seenNow: number;
  churn: ChurnEntry[];
  onViewChange: (view: SceneView) => void;
  onSelectFile: (path: string) => void;
  // opens the static full-repo map for a repo path in a new tab; omit the path
  // to use the current session's repo (trace.session.cwd)
  onOpenMap: (repo?: string) => void;
  // while a video export records, the view toggle is locked so switching scenes
  // can't tear down and replace the canvas the recorder is capturing
  locked?: boolean;
}

const CHURN_PANEL_ROWS = 8;

// memo: the app re-renders every playback tick; the HUD only changes when the
// session, the view toggle, or the touch counts under the playhead change
export const Hud = memo(function Hud({
  trace,
  city,
  view,
  editedNow,
  readNow,
  seenNow,
  churn,
  onViewChange,
  onSelectFile,
  onOpenMap,
  locked = false
}: HudProps) {
  const stats = trace?.stats;
  const [mapOpen, setMapOpen] = useState(false);
  const [mapPath, setMapPath] = useState("");
  const sessionRepo = trace?.session.cwd;
  const readFinal = stats ? stats.fovea - stats.edited : 0;
  const unvisitedNow = stats ? Math.max(0, stats.filesInRepo - editedNow - readNow - seenNow) : 0;
  const unvisitedFinal = stats ? Math.max(0, stats.filesInRepo - stats.fovea - stats.parafovea) : 0;
  const ghostCount = city ? city.files.reduce((n, file) => n + (file.ghost ? 1 : 0), 0) : 0;
  const errorCount = stats ? countActions(stats.errors) : 0;
  const showReview = stats ? errorCount > 0 || stats.churnFiles > 0 || stats.actions.edit > 0 : false;

  const [churnOpen, setChurnOpen] = useState(false);
  const churnPanelRef = useRef<HTMLDivElement | null>(null);
  const churnToggleRef = useRef<HTMLButtonElement | null>(null);

  // a new session invalidates the open list; close instead of showing stale rows
  useEffect(() => setChurnOpen(false), [trace]);

  useEffect(() => {
    if (!churnOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (churnPanelRef.current?.contains(target) || churnToggleRef.current?.contains(target)) return;
      setChurnOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setChurnOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [churnOpen]);

  return (
    <div className="hud" aria-hidden={!city}>
      <div className="hud-left">
        <div className="hud-repo">{city ? basename(city.repo.root) : ""}</div>
        {city ? (
          <div className="hud-commit">
            <span>{city.repo.commit || "worktree"}</span>
            {city.repo.dirty ? <span className="dirty">● dirty</span> : null}
            {trace?.session.model ? <span>{trace.session.model}</span> : null}
            {stats ? (
              <span data-hint="Files in the repository map — the denominator of the coverage spectrum below">
                {stats.filesInRepo} files
              </span>
            ) : null}
          </div>
        ) : null}
        {stats ? (
          <>
            {/* the spectrum doubles as scene legend and live tally: each entry is
                a touch state, counted at the playhead → across the whole walk */}
            <div className="spectrum">
              <SpectrumStat
                kind="edit"
                label="edited"
                now={editedNow}
                final={stats.edited}
                hint="Files the agent changed"
              />
              <SpectrumStat
                kind="read"
                label="read"
                now={readNow}
                final={readFinal}
                hint="Files the agent opened and read, but never changed"
              />
              <SpectrumStat
                kind="hit"
                label="seen"
                now={seenNow}
                final={stats.parafovea}
                hint="Files that only appeared in search results, never opened"
              />
              <SpectrumStat
                kind="unvisited"
                label="unvisited"
                now={unvisitedNow}
                final={unvisitedFinal}
                hint="Files in the map the agent never touched"
              />
              {ghostCount > 0 ? (
                <SpectrumStat
                  kind="ghost"
                  label="ghost"
                  now={ghostCount}
                  final={ghostCount}
                  hint="Files the session touched that are gone from the repository — drawn hollow in the scene"
                />
              ) : null}
            </div>
            {/* session scale: quiet background numbers, final totals only —
                unlike the playhead-live spectrum above */}
            <div className="hud-quiet">
              <span data-hint={`Tool calls — ${mixHint(stats.actions)}`}>
                {countActions(stats.actions)} calls
              </span>
              <span data-hint="User messages — each one starts a turn of agent work">
                {stats.userTurns} turns
              </span>
              {stats.subagents > 0 ? (
                <span data-hint="Subagent launches (Task/Agent)">
                  {stats.subagents} subagent{stats.subagents === 1 ? "" : "s"}
                </span>
              ) : null}
              {stats.compactions > 0 ? (
                <span data-hint="Context compactions — the conversation was summarized to free memory">
                  {stats.compactions} compaction{stats.compactions === 1 ? "" : "s"}
                </span>
              ) : null}
              <span data-hint="Tool output the agent consumed over the session">
                {fmtBytes(stats.resultBytes)} output
              </span>
              <span data-hint={rereadHint(stats.observability.reads)}>
                {stats.observability.reads === "unavailable"
                  ? "re-reads n/a"
                  : `re-reads ${approx(stats.observability.reads)}${pct(stats.regressionRate)}`}
              </span>
            </div>
            {/* review: only signals worth a second look — absent when clean */}
            {showReview ? (
              <div className="hud-review">
                <span className="review-label">review</span>
                {errorCount > 0 ? (
                  <span
                    className="warn"
                    data-hint={`${mixHint(stats.errors)} — press X to jump to the next one${errorCaveat(stats.observability.errors)}`}
                  >
                    {approx(stats.observability.errors)}
                    {errorCount} error{errorCount === 1 ? "" : "s"}
                  </span>
                ) : null}
                {stats.churnFiles > 0 ? (
                  <button
                    ref={churnToggleRef}
                    className={churnOpen ? "warn churn-toggle open" : "warn churn-toggle"}
                    aria-expanded={churnOpen}
                    onClick={() => setChurnOpen((open) => !open)}
                    data-hint={`Files edited in three or more separate events — the most-edited file changed ${stats.maxEditsPerFile} times. Click to list them.`}
                  >
                    {stats.churnFiles} file{stats.churnFiles === 1 ? "" : "s"} edited 3+ times
                  </button>
                ) : null}
                {stats.actions.edit > 0 ? (
                  stats.actions.verify === 0 ? (
                    <span
                      className="warn"
                      data-hint="The session edited files but no build or test commands were recognized (go test, make test, npm run build, …)"
                    >
                      never verified
                    </span>
                  ) : stats.editsAfterLastVerify > 0 ? (
                    <span
                      className="warn"
                      data-hint={`Edit events after the session's last build or test run — ${verifyRuns(stats.actions.verify)} total; pass/fail is not tracked`}
                    >
                      {stats.editsAfterLastVerify} edit{stats.editsAfterLastVerify === 1 ? "" : "s"} after
                      last verify
                    </span>
                  ) : (
                    <span
                      className="ok"
                      data-hint={`The last edit was followed by a build or test run — ${verifyRuns(stats.actions.verify)} total; pass/fail is not tracked`}
                    >
                      verified after final edit
                    </span>
                  )
                ) : null}
              </div>
            ) : null}
            {churnOpen ? (
              <div className="churn-panel" ref={churnPanelRef}>
                {churn.slice(0, CHURN_PANEL_ROWS).map((entry) => (
                  <button
                    key={entry.path}
                    className="churn-row"
                    onClick={() => {
                      onSelectFile(entry.path);
                      setChurnOpen(false);
                    }}
                  >
                    <span className="churn-path">{entry.path}</span>
                    <span className="churn-count">
                      {entry.edits} edit{entry.edits === 1 ? "" : "s"}
                    </span>
                  </button>
                ))}
                {churn.length > CHURN_PANEL_ROWS ? (
                  <p className="churn-more">…and {churn.length - CHURN_PANEL_ROWS} more</p>
                ) : null}
              </div>
            ) : null}
          </>
        ) : null}
      </div>
      {city ? (
        <div className="hud-right">
          <div className="view-toggle" role="group" aria-label="Scene view">
            <button
              className={view === "tree" ? "active" : ""}
              onClick={() => onViewChange("tree")}
              disabled={locked}
            >
              Tree
            </button>
            <button
              className={view === "terrain" ? "active" : ""}
              onClick={() => onViewChange("terrain")}
              disabled={locked}
            >
              Terrain
            </button>
          </div>
          <div className="map-controls">
            <button
              className="map-btn"
              onClick={() => setMapOpen((open) => !open)}
              aria-expanded={mapOpen}
              data-hint="Open a static full-repo map — this session's repo, or any repo path"
            >
              <MapIcon size={13} />
              <span>Map</span>
            </button>
            {mapOpen ? (
              <div className="map-menu">
                {sessionRepo ? (
                  <button className="map-menu-row" onClick={() => onOpenMap(sessionRepo)} title={sessionRepo}>
                    This session's repo
                  </button>
                ) : null}
                <form
                  className="map-menu-form"
                  onSubmit={(e) => {
                    e.preventDefault();
                    const path = mapPath.trim();
                    if (path) onOpenMap(path);
                  }}
                >
                  <input
                    type="text"
                    className="map-menu-input"
                    placeholder="/path/to/repo"
                    value={mapPath}
                    onChange={(e) => setMapPath(e.currentTarget.value)}
                    spellCheck={false}
                  />
                  <button type="submit" className="map-menu-go" disabled={mapPath.trim() === ""}>
                    Open
                  </button>
                </form>
              </div>
            ) : null}
          </div>
          <div className="encode-note">
            {view === "tree"
              ? trace
                ? "glow ∝ revisits"
                : "static map"
              : trace
                ? "height ∝ depth × revisits"
                : "height ∝ lines"}
          </div>
        </div>
      ) : null}
    </div>
  );
});

function SpectrumStat({
  kind,
  label,
  now,
  final,
  hint
}: {
  kind: "edit" | "read" | "hit" | "unvisited" | "ghost";
  label: string;
  now: number;
  final: number;
  hint: string;
}) {
  return (
    <div className="spectrum-stat" data-hint={hint}>
      <span className={`legend-dot ${kind}`} />
      <span className="spectrum-label">{label}</span>
      <strong>{now === final ? final : `${now} → ${final}`}</strong>
    </div>
  );
}

function pct(rate: number): string {
  return `${Math.round(rate * 100)}%`;
}

function approx(observability: MetricObservability): string {
  return observability === "estimated" ? "~" : "";
}

function verifyRuns(count: number): string {
  return `${count} verify run${count === 1 ? "" : "s"}`;
}

function rereadHint(observability: MetricObservability): string {
  switch (observability) {
    case "unavailable":
      return "No file reads observed — this session reads files through commands mindwalk could not recognize";
    case "estimated":
      return "Reads that re-read a file unchanged since its last read — inferred from shell commands, so the rate is approximate";
    default:
      return "Reads that re-read a file unchanged since its last read";
  }
}

function errorCaveat(observability: MetricObservability): string {
  return observability === "estimated"
    ? " — inferred from command output; failures inside scripted calls may be missed"
    : "";
}

const ACTION_ORDER = ["search", "read", "edit", "exec", "verify", "other"] as const;

function countActions(counts: ActionCounts): number {
  return ACTION_ORDER.reduce((sum, key) => sum + counts[key], 0);
}

function mixHint(counts: ActionCounts): string {
  const parts = ACTION_ORDER.filter((key) => counts[key] > 0).map((key) => `${counts[key]} ${key}`);
  return parts.length ? parts.join(" · ") : "none";
}

function fmtBytes(bytes: number): string {
  const kb = bytes / 1024;
  if (kb < 1) return `${bytes} B`;
  if (kb < 1000) return `${Math.round(kb)} KB`;
  return `${(kb / 1024).toFixed(1)} MB`;
}

function basename(path: string): string {
  const clean = path.replace(/\/+$/, "");
  return clean.slice(clean.lastIndexOf("/") + 1);
}
