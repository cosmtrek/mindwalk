import { PanelLeftOpen } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  describeError,
  getAgentTrace,
  getRepoMap,
  getSessionAgents,
  getSessionReport,
  getSessionSnapshot,
  listSessions,
  startSessionAnalyze
} from "./api/client";
import { Crosshair, Sparkles, Mountain, TreePine, Users } from "lucide-react";
import type { AgentGraph, CityMap, JudgeChoice, ReportStatus, Trace } from "./types";
import { Dock, type PanelDescriptor } from "./ui/Dock";
import { AgentsPanel } from "./ui/AgentsPanel";
import { ReportPanel } from "./ui/ReportPanel";
import { ViewPanel } from "./ui/ViewPanel";
import { PlaybackEngine } from "./playback/reducer";
import { downloadBlob, recordingSupported, recordPlayback } from "./playback/recorder";
import { CityScene } from "./scene/CityScene";
import { TreeScene } from "./scene/TreeScene";
import { sessionVisible } from "./state/filters";
import { useAppStore } from "./state/store";
import { Hud } from "./ui/Hud";
import { Inspector } from "./ui/Inspector";
import { SessionRail } from "./ui/SessionRail";
import { toggleRailShortcut } from "./ui/shortcuts";
import { Timeline } from "./ui/Timeline";
import "./styles.css";

function evaluateHint(badge: "running" | "done" | "stale" | "failed" | null): string {
  switch (badge) {
    case "running":
      return "The judge is reading the trace — about a minute";
    case "done":
      return "Evaluation ready";
    case "stale":
      return "Evaluation ready, but the session has grown since";
    case "failed":
      return "The last evaluation failed — open to retry";
    default:
      return "Evaluate this session with your local agent CLI";
  }
}

const MAIN_ACTOR_KEY = "__main__";

export default function App() {
  const {
    sessions,
    activeSessionKey,
    trace,
    city,
    currentSeq,
    selectedPath,
    view,
    loading,
    error,
    hideEmpty,
    harnessFilter,
    railCollapsed,
    mapOnly,
    setView,
    setSessions,
    setActiveSession,
    setData,
    setCityOnly,
    setCurrentSeq,
    setSelectedPath,
    setLoading,
    setError,
    setHideEmpty,
    setHarnessFilter,
    setRailCollapsed
  } = useAppStore();
  const urlSessionConsumed = useRef(false);
  const scanGeneration = useRef(0);
  const loadGeneration = useRef(0);
  const lensGeneration = useRef(0);
  const agentGraphRequest = useRef(0);
  const agentTraceRequest = useRef(0);
  const manualRefreshInFlight = useRef(false);
  const pendingLoads = useRef(0);
  const activeSessionKeyRef = useRef(activeSessionKey);
  activeSessionKeyRef.current = activeSessionKey;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [exporting, setExporting] = useState(false);
  const [openSheet, setOpenSheet] = useState<string | null>(null);
  const [openPop, setOpenPop] = useState<string | null>(null);
  const [reportStatus, setReportStatus] = useState<ReportStatus | undefined>();
  const [agentGraph, setAgentGraph] = useState<AgentGraph | undefined>();
  const [activeAgentID, setActiveAgentID] = useState<string | null>(null);
  const [agentGraphLoading, setAgentGraphLoading] = useState(false);
  const [loadingAgentID, setLoadingAgentID] = useState<string | undefined>();
  const [agentPanelError, setAgentPanelError] = useState<string | undefined>();
  const [agentRetryID, setAgentRetryID] = useState<string | null | undefined>();
  const activeAgentIDRef = useRef<string | null>(null);
  const pendingAgentIDRef = useRef<string | undefined>(undefined);
  const rootTraceRef = useRef<Trace | undefined>(undefined);
  const rootCityRef = useRef<CityMap | undefined>(undefined);
  const actorTraceCache = useRef(new Map<string, Trace>());
  const actorPlayheads = useRef(new Map<string, number>());
  const exportingRef = useRef(false);
  exportingRef.current = exporting;

  // scenes hand up their live <canvas> so the video exporter can capture it;
  // stable identity keeps the scene mount effect from remounting on every render
  const handleCanvasReady = useCallback((canvas: HTMLCanvasElement | null) => {
    canvasRef.current = canvas;
  }, []);

  const beginLoading = useCallback(() => {
    pendingLoads.current++;
    setLoading(true);
  }, [setLoading]);

  const endLoading = useCallback(() => {
    pendingLoads.current = Math.max(0, pendingLoads.current - 1);
    if (pendingLoads.current === 0) setLoading(false);
  }, [setLoading]);

  const resetLens = useCallback(() => {
    const generation = ++lensGeneration.current;
    agentGraphRequest.current++;
    agentTraceRequest.current++;
    activeAgentIDRef.current = null;
    pendingAgentIDRef.current = undefined;
    rootTraceRef.current = undefined;
    rootCityRef.current = undefined;
    actorTraceCache.current.clear();
    actorPlayheads.current.clear();
    setAgentGraph(undefined);
    setActiveAgentID(null);
    setAgentGraphLoading(false);
    setLoadingAgentID(undefined);
    setAgentPanelError(undefined);
    setAgentRetryID(undefined);
    return generation;
  }, []);

  const loadAgentGraph = useCallback(async (key: string, generation = lensGeneration.current) => {
    const request = ++agentGraphRequest.current;
    setAgentGraphLoading(true);
    setAgentPanelError(undefined);
    setAgentRetryID(undefined);
    try {
      const graph = await getSessionAgents(key);
      if (
        generation !== lensGeneration.current ||
        request !== agentGraphRequest.current ||
        activeSessionKeyRef.current !== key
      ) {
        return;
      }
      setAgentGraph(graph);
    } catch (err) {
      if (
        generation === lensGeneration.current &&
        request === agentGraphRequest.current &&
        activeSessionKeyRef.current === key
      ) {
        setAgentPanelError(describeError(err, "loading agents"));
        setAgentRetryID(null);
      }
    } finally {
      if (
        generation === lensGeneration.current &&
        request === agentGraphRequest.current &&
        activeSessionKeyRef.current === key
      ) {
        setAgentGraphLoading(false);
      }
    }
  }, []);

  const loadSession = useCallback(async (key: string) => {
    const generation = ++loadGeneration.current;
    const currentLensGeneration = lensGeneration.current;
    beginLoading();
    setError(undefined);
    try {
      const { trace: nextTrace, city: nextCity } = await getSessionSnapshot(key);
      if (
        generation !== loadGeneration.current ||
        currentLensGeneration !== lensGeneration.current ||
        activeSessionKeyRef.current !== key
      ) {
        return;
      }
      rootTraceRef.current = nextTrace;
      rootCityRef.current = nextCity;
      actorTraceCache.current.set(MAIN_ACTOR_KEY, nextTrace);
      const activeChildID = activeAgentIDRef.current;
      if (activeChildID === null) {
        setData(nextTrace, nextCity);
        const remembered = actorPlayheads.current.get(MAIN_ACTOR_KEY);
        if (remembered !== undefined) {
          setCurrentSeq(Math.min(remembered, Math.max(0, nextTrace.events.length - 1)));
        }
        setSelectedPath(undefined);
      } else {
        const childTrace = actorTraceCache.current.get(activeChildID);
        if (childTrace) {
          const seq = useAppStore.getState().currentSeq;
          setData(childTrace, nextCity);
          setCurrentSeq(Math.min(seq, Math.max(0, childTrace.events.length - 1)));
        }
      }
    } catch (err) {
      if (generation === loadGeneration.current && activeSessionKeyRef.current === key) {
        setError(describeError(err, "loading the session"));
      }
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, setCurrentSeq, setData, setError, setSelectedPath]);

  const invalidateActorTracesForRescan = useCallback(() => {
    const activeActorID = activeAgentIDRef.current;
    actorPlayheads.current.set(
      activeActorID ?? MAIN_ACTOR_KEY,
      useAppStore.getState().currentSeq
    );
    agentTraceRequest.current++;
    pendingAgentIDRef.current = undefined;
    actorTraceCache.current.clear();
    activeAgentIDRef.current = null;
    setActiveAgentID(null);
    setLoadingAgentID(undefined);
    setAgentPanelError(undefined);
    setAgentRetryID(undefined);

    const rootTrace = rootTraceRef.current;
    const rootCity = rootCityRef.current;
    if (rootTrace && rootCity) {
      setData(rootTrace, rootCity);
      const remembered = actorPlayheads.current.get(MAIN_ACTOR_KEY);
      if (remembered !== undefined) {
        setCurrentSeq(Math.min(remembered, Math.max(0, rootTrace.events.length - 1)));
      }
      setSelectedPath(undefined);
    }
  }, [setCurrentSeq, setData, setSelectedPath]);

  const scan = useCallback(async (fresh: boolean) => {
    const generation = ++scanGeneration.current;
    beginLoading();
    setError(undefined);
    try {
      const data = await listSessions(fresh);
      if (generation !== scanGeneration.current) return;
      setSessions(data);
      let preferred: string | undefined;
      if (!urlSessionConsumed.current) {
        urlSessionConsumed.current = true;
        const selector = new URL(window.location.href).searchParams.get("session") ?? undefined;
        const exact = selector ? data.find((session) => session.key === selector) : undefined;
        const legacyMatches = selector && !exact ? data.filter((session) => session.id === selector) : [];
        const fromUrl = exact?.key ?? (legacyMatches.length === 1 ? legacyMatches[0].key : undefined);
        if (fromUrl) {
          preferred = fromUrl;
        } else if (legacyMatches.length > 1) {
          console.warn(`session id "${selector}" is ambiguous; falling back to the latest session`);
        } else if (selector) {
          console.warn(`session "${selector}" not found; falling back to the latest session`);
        }
      }
      // a session can disappear between scans; fall back instead of pinning a dead key
      const currentActiveKey = activeSessionKeyRef.current;
      const stillListed =
        currentActiveKey !== undefined && data.some((session) => session.key === currentActiveKey);
      // prefer a session the rail will actually show; if the filters hide
      // everything, the newest session still beats a blank stage
      const fallback = (
        data.find((session) => sessionVisible(session, { hideEmpty, harness: harnessFilter })) ?? data[0]
      )?.key;
      const next = preferred ?? (stillListed ? currentActiveKey : fallback);
      if (next !== currentActiveKey) {
        const lens = resetLens();
        activeSessionKeyRef.current = next;
        if (!next) loadGeneration.current++;
        setActiveSession(next);
        if (next) void loadAgentGraph(next, lens);
      } else if (fresh && next) {
        invalidateActorTracesForRescan();
        void loadAgentGraph(next);
      }
      if (next) await loadSession(next);
    } catch (err) {
      if (generation === scanGeneration.current) {
        setError(describeError(err, "scanning sessions"));
      }
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, harnessFilter, hideEmpty, invalidateActorTracesForRescan, loadAgentGraph, loadSession, resetLens, setActiveSession, setError, setSessions]);

  const loadRepoMap = useCallback(async (repo?: string) => {
    beginLoading();
    setError(undefined);
    try {
      const city = await getRepoMap(repo);
      setCityOnly(city);
    } catch (err) {
      setError(describeError(err, "loading the repository map"));
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, setCityOnly, setError]);

  // open the static map for a repo in a new tab so the running session stays put
  const openMap = useCallback((repo?: string) => {
    const url = repo ? `/?map=1&repo=${encodeURIComponent(repo)}` : "/?map=1";
    window.open(url, "_blank", "noopener");
  }, []);

  const selectSession = useCallback((key: string) => {
    if (activeSessionKeyRef.current === key) return;
    const lens = resetLens();
    activeSessionKeyRef.current = key;
    setActiveSession(key);
    void loadAgentGraph(key, lens);
    void loadSession(key);
  }, [loadAgentGraph, loadSession, resetLens, setActiveSession]);

  const saveActivePlayhead = useCallback(() => {
    const key = activeAgentIDRef.current ?? MAIN_ACTOR_KEY;
    actorPlayheads.current.set(key, useAppStore.getState().currentSeq);
  }, []);

  const showCachedActor = useCallback((agentID: string | null, nextTrace: Trace, nextCity: CityMap) => {
    saveActivePlayhead();
    activeAgentIDRef.current = agentID;
    setActiveAgentID(agentID);
    setData(nextTrace, nextCity);
    const remembered = actorPlayheads.current.get(agentID ?? MAIN_ACTOR_KEY);
    if (remembered !== undefined) {
      setCurrentSeq(Math.min(remembered, Math.max(0, nextTrace.events.length - 1)));
    }
    setSelectedPath(undefined);
  }, [saveActivePlayhead, setCurrentSeq, setData, setSelectedPath]);

  const selectAgent = useCallback(async (agentID: string | null) => {
    if (exportingRef.current) return;
    const rootKey = activeSessionKeyRef.current;
    const nextCity = rootCityRef.current;
    if (!rootKey || !nextCity) return;

    const request = ++agentTraceRequest.current;
    pendingAgentIDRef.current = undefined;
    setLoadingAgentID(undefined);
    setAgentPanelError(undefined);
    setAgentRetryID(undefined);

    if (activeAgentIDRef.current === agentID) return;

    const cachedTrace =
      agentID === null ? rootTraceRef.current : actorTraceCache.current.get(agentID);
    if (cachedTrace) {
      showCachedActor(agentID, cachedTrace, nextCity);
      return;
    }
    if (agentID === null) return;

    const node = agentGraph?.agents.find((agent) => agent.id === agentID);
    if (!node || node.traceAvailability !== "available") return;

    const generation = lensGeneration.current;
    pendingAgentIDRef.current = agentID;
    setLoadingAgentID(agentID);
    try {
      const nextTrace = await getAgentTrace(rootKey, agentID);
      if (
        generation !== lensGeneration.current ||
        request !== agentTraceRequest.current ||
        pendingAgentIDRef.current !== agentID ||
        activeSessionKeyRef.current !== rootKey ||
        exportingRef.current
      ) {
        return;
      }
      actorTraceCache.current.set(agentID, nextTrace);
      showCachedActor(agentID, nextTrace, rootCityRef.current ?? nextCity);
    } catch (err) {
      if (
        generation === lensGeneration.current &&
        request === agentTraceRequest.current &&
        pendingAgentIDRef.current === agentID &&
        activeSessionKeyRef.current === rootKey
      ) {
        setAgentPanelError(describeError(err, `loading the ${node.label} trace`));
        setAgentRetryID(agentID);
      }
    } finally {
      if (
        generation === lensGeneration.current &&
        request === agentTraceRequest.current &&
        pendingAgentIDRef.current === agentID &&
        activeSessionKeyRef.current === rootKey
      ) {
        pendingAgentIDRef.current = undefined;
        setLoadingAgentID(undefined);
      }
    }
  }, [agentGraph, showCachedActor]);

  const retryAgents = useCallback(() => {
    const key = activeSessionKeyRef.current;
    if (exportingRef.current || !key || agentRetryID === undefined) return;
    if (agentRetryID === null) void loadAgentGraph(key);
    else void selectAgent(agentRetryID);
  }, [agentRetryID, loadAgentGraph, selectAgent]);

  const exportVideo = useCallback(async () => {
    const canvas = canvasRef.current;
    const total = trace?.events.length ?? 0;
    if (!canvas || total === 0 || exportingRef.current) return;
    // the recorder owns the playhead for the duration of the export; setting
    // exporting=true locks the transport, scrubber, session rail, and view
    // toggle (see the `exporting` prop threaded into Timeline/SessionRail/Hud)
    // so nothing else moves the playhead or swaps the canvas mid-recording
    const exportSessionKey = activeSessionKeyRef.current;
    const exportActorID = activeAgentIDRef.current;
    const resumeSeq = useAppStore.getState().currentSeq;
    agentTraceRequest.current++;
    pendingAgentIDRef.current = undefined;
    setLoadingAgentID(undefined);
    exportingRef.current = true;
    setExporting(true);
    setError(undefined);
    try {
      const { blob, extension } = await recordPlayback({
        canvas,
        total,
        setSeq: setCurrentSeq
      });
      const name = trace?.session.id || exportSessionKey || "session";
      downloadBlob(blob, `mindwalk-${name}.${extension}`);
    } catch (err) {
      setError(describeError(err, "exporting the video"));
    } finally {
      // only restore the playhead if we're still on the same session and actor —
      // a guard in case a switch slipped through; normally the UI lock prevents it
      if (
        activeSessionKeyRef.current === exportSessionKey &&
        activeAgentIDRef.current === exportActorID
      ) {
        setCurrentSeq(resumeSeq);
      }
      exportingRef.current = false;
      setExporting(false);
    }
  }, [trace, exporting, setCurrentSeq, setError]);

  // stable callbacks keep SessionRail's memo effective across playback ticks
  const collapseRail = useCallback(() => setRailCollapsed(true), [setRailCollapsed]);
  const expandRail = useCallback(() => setRailCollapsed(false), [setRailCollapsed]);

  // --- session evaluation: fetched on session switch, polled while the judge
  // runs; the judge itself only ever starts from the explicit button press
  const refreshReport = useCallback(async (key: string) => {
    try {
      const status = await getSessionReport(key);
      if (activeSessionKeyRef.current === key) setReportStatus(status);
    } catch {
      // not worth a toast: the status stays undefined, the panel keeps its
      // "checking" state, and the unknown-status retry effect below tries
      // again until the server answers
    }
  }, []);

  useEffect(() => {
    setOpenSheet(null);
    setOpenPop(null);
    setReportStatus(undefined);
    if (activeSessionKey && !mapOnly) void refreshReport(activeSessionKey);
  }, [activeSessionKey, mapOnly, refreshReport]);

  // refreshes the rail's evaluation badges without disturbing selection —
  // scan() owns selection fallback, this only swaps the list data
  const refreshSessionList = useCallback(async () => {
    try {
      setSessions(await listSessions());
    } catch {
      // badge refresh is best-effort; the next scan will catch up
    }
  }, [setSessions]);

  // manual rescan: the active key usually survives it, so the report status
  // must be refetched explicitly — it may have gone stale or finished while
  // we weren't polling
  const refresh = useCallback(() => {
    if (exportingRef.current || manualRefreshInFlight.current) return;
    manualRefreshInFlight.current = true;
    const key = activeSessionKeyRef.current;
    if (key && !mapOnly) void refreshReport(key);
    void scan(true).finally(() => {
      manualRefreshInFlight.current = false;
    });
  }, [scan, refreshReport, mapOnly]);

  useEffect(() => {
    if (reportStatus?.state !== "running" || !activeSessionKey) return;
    const timer = setInterval(() => {
      void refreshReport(activeSessionKey);
      void refreshSessionList();
    }, 2500);
    return () => {
      clearInterval(timer);
      // one more list pass so the rail badge leaves "evaluating" promptly
      void refreshSessionList();
    };
  }, [reportStatus?.state, activeSessionKey, refreshReport, refreshSessionList]);

  // while the report status is unknown (first request failed or still on its
  // way), keep asking — otherwise a single dropped request would pin the
  // panel to "checking" until a session switch or reload
  useEffect(() => {
    if (reportStatus !== undefined || !activeSessionKey || mapOnly) return;
    const timer = setInterval(() => void refreshReport(activeSessionKey), 5000);
    return () => clearInterval(timer);
  }, [reportStatus === undefined, activeSessionKey, mapOnly, refreshReport]);

  // a judge can be running for a session other than the active one; keep the
  // rail badges honest by polling the list until every run finishes (the
  // running-state effect above already polls while the active session runs)
  const anyEvaluating = useMemo(() => sessions.some((s) => s.reportState === "running"), [sessions]);
  useEffect(() => {
    if (!anyEvaluating || reportStatus?.state === "running") return;
    const timer = setInterval(() => void refreshSessionList(), 5000);
    return () => clearInterval(timer);
  }, [anyEvaluating, reportStatus?.state, refreshSessionList]);

  const analyzeSession = useCallback(async (choice: JudgeChoice) => {
    const key = activeSessionKeyRef.current;
    if (!key) return;
    try {
      const status = await startSessionAnalyze(key, choice);
      if (activeSessionKeyRef.current === key) setReportStatus(status);
      void refreshSessionList();
    } catch (err) {
      setError(describeError(err, "starting the evaluation"));
    }
  }, [setError, refreshSessionList]);

  // selecting a file in the scene opens the inspect sheet; deselecting keeps
  // whatever panel the user had open
  const selectFile = useCallback(
    (path: string | undefined) => {
      setSelectedPath(path);
      if (path) setOpenSheet("inspect");
    },
    [setSelectedPath]
  );

  const closeSheet = useCallback(() => setOpenSheet(null), []);
  const closePop = useCallback(() => setOpenPop(null), []);
  const openAgents = useCallback(() => {
    if (!exportingRef.current) setOpenSheet("agents");
  }, []);

  // sheets are exclusive (one full-height paper at a time); pops toggle
  // independently so a view tweak never steals the open report
  const togglePanel = useCallback((panel: PanelDescriptor) => {
    if (panel.presentation === "sheet") {
      setOpenSheet((current) => (current === panel.id ? null : panel.id));
    } else {
      setOpenPop((current) => (current === panel.id ? null : panel.id));
    }
  }, []);

  // a finding jump moves the playhead and focuses the evidence's file so the
  // claim is visible in the scene, not just in the panel — without stealing
  // the dock away from the evaluation tab
  const jumpToEvidence = useCallback(
    (seq: number) => {
      if (exportingRef.current) return;
      const rootTrace = rootTraceRef.current;
      const rootCity = rootCityRef.current;
      if (!rootTrace || !rootCity) return;
      agentTraceRequest.current++;
      pendingAgentIDRef.current = undefined;
      setLoadingAgentID(undefined);
      if (activeAgentIDRef.current !== null) {
        saveActivePlayhead();
        activeAgentIDRef.current = null;
        setActiveAgentID(null);
        setData(rootTrace, rootCity);
      }
      setCurrentSeq(seq);
      actorPlayheads.current.set(MAIN_ACTOR_KEY, seq);
      const event = rootTrace.events[seq];
      const path = event?.targets.find((target) => target.path)?.path;
      setSelectedPath(path);
    },
    [saveActivePlayhead, setCurrentSeq, setData, setSelectedPath]
  );

  const openAgentsAtMark = useCallback((seq: number) => {
    setCurrentSeq(seq);
    setOpenSheet("agents");
  }, [setCurrentSeq]);

  const jumpToHistory = useCallback((seq: number) => {
    if (exportingRef.current) return;
    setCurrentSeq(seq);
  }, [setCurrentSeq]);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "b" || !(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      e.preventDefault();
      const store = useAppStore.getState();
      store.setRailCollapsed(!store.railCollapsed);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  // V cycles the scene view without opening the view pop; locked during
  // export because switching scenes would swap the recorded canvas
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "v" || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return;
      const target = e.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) return;
      if (exportingRef.current) return;
      const store = useAppStore.getState();
      store.setView(store.view === "tree" ? "terrain" : "tree");
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  useEffect(() => {
    const params = new URL(window.location.href).searchParams;
    if (params.get("map") === "1") {
      void loadRepoMap(params.get("repo") ?? undefined);
    } else {
      void scan(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const reportBadge = useMemo(() => {
    if (reportStatus?.state === "running") return "running" as const;
    if (reportStatus?.state === "failed") return "failed" as const;
    if (reportStatus?.state === "done") return reportStatus.stale ? ("stale" as const) : ("done" as const);
    return null;
  }, [reportStatus]);

  const agentLabel = useMemo(() => {
    if (activeAgentID === null) return "Main";
    return agentGraph?.agents.find((agent) => agent.id === activeAgentID)?.label ?? "Subagent";
  }, [activeAgentID, agentGraph]);

  const viewNote =
    view === "tree"
      ? trace
        ? "glow ∝ revisits"
        : "static map"
      : trace
        ? "height ∝ depth × revisits"
        : "height ∝ lines";

  const engine = useMemo(() => new PlaybackEngine(trace, city), [trace, city]);
  const playback = useMemo(() => engine.snapshotAt(currentSeq), [engine, currentSeq]);
  // live tallies for the HUD spectrum; touchByPath mirrors the backend stats scope
  const touchCounts = useMemo(() => {
    let edited = 0;
    let read = 0;
    let seen = 0;
    for (const touch of playback.touchByPath.values()) {
      if (touch === "edit") edited++;
      else if (touch === "read") read++;
      else seen++;
    }
    return { edited, read, seen };
  }, [playback]);
  const selectedFile = useMemo(
    () => (selectedPath ? city?.files.find((file) => file.path === selectedPath) : undefined),
    [city, selectedPath]
  );
  // mirrors the backend churn definition (stats.churnFiles): per path, the
  // number of events that carried an edit touch; churn means three or more
  const churn = useMemo(() => {
    const counts = new Map<string, number>();
    for (const event of trace?.events ?? []) {
      for (const target of event.targets) {
        if (target.touch === "edit" && target.path) {
          counts.set(target.path, (counts.get(target.path) ?? 0) + 1);
        }
      }
    }
    return [...counts.entries()]
      .filter(([, edits]) => edits >= 3)
      .map(([path, edits]) => ({ path, edits }))
      .sort((a, b) => b.edits - a.edits || (a.path < b.path ? -1 : 1));
  }, [trace]);

  return (
    <main className={mapOnly ? "app-frame rail-collapsed" : railCollapsed ? "app-frame rail-collapsed" : "app-frame"}>
      {mapOnly ? null : (
        <SessionRail
          sessions={sessions}
          activeKey={activeSessionKey}
          loading={loading}
          hideEmpty={hideEmpty}
          harnessFilter={harnessFilter}
          collapsed={railCollapsed}
          onSelect={selectSession}
          onRefresh={refresh}
          onHideEmptyChange={setHideEmpty}
          onHarnessFilterChange={setHarnessFilter}
          onCollapse={collapseRail}
          onOpenMap={openMap}
          activeRepo={trace?.session.cwd}
          locked={exporting}
          activeReportState={reportStatus === undefined ? undefined : reportBadge}
        />
      )}
      <section className="stage">
        <div className="viewport">
          {!mapOnly && railCollapsed ? (
            <button
              className="rail-expand"
              onClick={expandRail}
              title={`Show sidebar (${toggleRailShortcut})`}
              aria-label="Show session sidebar"
            >
              <PanelLeftOpen size={15} />
            </button>
          ) : null}
          {view === "tree" ? (
            <TreeScene
              city={city}
              playback={playback}
              selectedPath={selectedPath}
              onSelect={selectFile}
              onCanvasReady={handleCanvasReady}
            />
          ) : (
            <CityScene
              city={city}
              playback={playback}
              selectedPath={selectedPath}
              onSelect={selectFile}
              onCanvasReady={handleCanvasReady}
              locHeights={mapOnly}
            />
          )}
          <Hud
            trace={trace}
            city={city}
            agentLabel={agentLabel}
            editedNow={touchCounts.edited}
            readNow={touchCounts.read}
            seenNow={touchCounts.seen}
            churn={churn}
            onSelectFile={selectFile}
            onOpenAgents={!mapOnly && trace ? openAgents : undefined}
            locked={exporting}
          />
          {city ? (
            <Dock
              panels={[
                {
                  id: "view",
                  icon: view === "tree" ? TreePine : Mountain,
                  hint: `Scene view: ${view} — click to change, or press V`,
                  section: "scene",
                  presentation: "pop",
                  render: () => (
                    <ViewPanel view={view} onViewChange={setView} note={viewNote} locked={exporting} />
                  )
                },
                {
                  id: "inspect",
                  icon: Crosshair,
                  hint: "Inspect the selected file",
                  section: "session",
                  presentation: "sheet",
                  render: () => (
                    <Inspector
                      file={selectedFile}
                      touch={selectedFile ? playback.touchByPath.get(selectedFile.path) : undefined}
                      history={selectedFile ? (playback.historyByPath.get(selectedFile.path) ?? []) : []}
                      onClose={closeSheet}
                      onJumpTo={jumpToHistory}
                      locked={exporting}
                    />
                  )
                },
                ...(!mapOnly && trace
                  ? [
                      {
                        id: "agents",
                        icon: Users,
                        hint: `Agent lenses — current: ${agentLabel}`,
                        section: "session",
                        presentation: "sheet",
                        render: () => (
                          <AgentsPanel
                            graph={agentGraph}
                            current={activeAgentID}
                            loading={agentGraphLoading}
                            loadingAgentID={loadingAgentID}
                            locked={exporting}
                            error={agentPanelError}
                            retryAgentID={agentRetryID}
                            onSelect={(agentID) => void selectAgent(agentID)}
                            onRetry={retryAgents}
                            onClose={closeSheet}
                          />
                        )
                      } satisfies PanelDescriptor
                    ]
                  : []),
                ...(!mapOnly && trace
                  ? [
                      {
                        id: "evaluate",
                        icon: Sparkles,
                        hint: evaluateHint(reportBadge),
                        section: "session",
                        presentation: "sheet",
                        badge: reportBadge,
                        render: () => (
                          <ReportPanel
                            status={reportStatus}
                            analyzing={reportStatus?.state === "running"}
                            locked={exporting}
                            onAnalyze={(choice) => void analyzeSession(choice)}
                            onClose={closeSheet}
                            onJumpTo={jumpToEvidence}
                          />
                        )
                      } satisfies PanelDescriptor
                    ]
                  : [])
              ]}
              openSheet={openSheet}
              openPop={openPop}
              onToggle={togglePanel}
              onClosePop={closePop}
            />
          ) : null}
          {!mapOnly && !loading && sessions.length === 0 ? (
            <div className="empty-stage">
              <div className="card">
                <h2>No sessions found</h2>
                <p>
                  mindwalk scans <code>~/.claude/projects</code> and <code>~/.codex/sessions</code> for agent
                  traces. Run a session there, then refresh.
                </p>
              </div>
            </div>
          ) : null}
          {loading ? (
            <div className="toast">{mapOnly ? "Building the map…" : sessions.length === 0 ? "Scanning sessions…" : "Reading trace…"}</div>
          ) : null}
          {error ? <div className="toast error">{error}</div> : null}
        </div>
        <Timeline
          trace={trace}
          currentSeq={currentSeq}
          onChange={setCurrentSeq}
          onExport={recordingSupported() ? exportVideo : undefined}
          exporting={exporting}
          onSubagentMark={openAgentsAtMark}
        />
      </section>
    </main>
  );
}
