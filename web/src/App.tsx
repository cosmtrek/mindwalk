import { PanelLeftOpen } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  addRepository,
  describeError,
  getRepoMap,
  getRepositoryCityMap,
	getSessionAgents,
  getSessionEvents,
  getSessionSnapshot,
  listRepositories,
  listSessions,
  updateRepository
} from "./api/client";
import { PlaybackEngine } from "./playback/reducer";
import { downloadBlob, recordingSupported, recordPlayback } from "./playback/recorder";
import { CityScene } from "./scene/CityScene";
import { TreeScene } from "./scene/TreeScene";
import { ListScene } from "./scene/ListScene";
import { sessionVisible } from "./state/filters";
import { useAppStore } from "./state/store";
import { Hud } from "./ui/Hud";
import { Inspector } from "./ui/Inspector";
import { MemorySearch } from "./ui/MemorySearch";
import { RepositoryDiscovery } from "./ui/RepositoryDiscovery";
import { RepositoryOnboarding } from "./ui/RepositoryOnboarding";
import { ReviewCenter } from "./ui/ReviewCenter";
import { SessionRail } from "./ui/SessionRail";
import { toggleRailShortcut } from "./ui/shortcuts";
import { Timeline } from "./ui/Timeline";
import "./styles.css";
import type { AgentProcess, ObservableEvent, RepositoryStatus } from "./types";

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
    setRepositoryMap,
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
  const manualRefreshInFlight = useRef(false);
  const pendingLoads = useRef(0);
  const activeSessionKeyRef = useRef(activeSessionKey);
  activeSessionKeyRef.current = activeSessionKey;
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const [exporting, setExporting] = useState(false);
  const [repositories, setRepositories] = useState<RepositoryStatus[]>([]);
  const [repositoriesLoaded, setRepositoriesLoaded] = useState(false);
  const [activeRepositoryId, setActiveRepositoryId] = useState<string>();
  const [discoveryOpen, setDiscoveryOpen] = useState(false);
  const [discoveryPreferHome, setDiscoveryPreferHome] = useState(false);
  const [onboardingDismissed, setOnboardingDismissed] = useState(false);
  const [liveStatus, setLiveStatus] = useState<"idle" | "connecting" | "live" | "disconnected">("idle");
  const [followLive, setFollowLive] = useState(true);
	const [observableEvents, setObservableEvents] = useState<ObservableEvent[]>([]);
	const [agentProcesses, setAgentProcesses] = useState<AgentProcess[]>([]);
  const followLiveRef = useRef(followLive);
  followLiveRef.current = followLive;

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

  const loadSession = useCallback(async (key: string) => {
    const generation = ++loadGeneration.current;
    beginLoading();
    setError(undefined);
    try {
		const [snapshot, eventPage, processes] = await Promise.all([
			getSessionSnapshot(key),
			getSessionEvents(key).catch(() => ({ events: [], latestSequence: 0, truncated: false })),
			getSessionAgents(key).catch(() => [])
		]);
      if (generation !== loadGeneration.current || activeSessionKeyRef.current !== key) return;
		setData(snapshot.trace, snapshot.city);
		setObservableEvents(eventPage.events);
		setAgentProcesses(processes);
      setSelectedPath(undefined);
    } catch (err) {
      if (generation === loadGeneration.current && activeSessionKeyRef.current === key) {
        setError(describeError(err, "loading the session"));
      }
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, setData, setError, setSelectedPath]);

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
        activeSessionKeyRef.current = next;
        if (!next) loadGeneration.current++;
        setActiveSession(next);
      }
      if (next) await loadSession(next);
    } catch (err) {
      if (generation === scanGeneration.current) {
        setError(describeError(err, "scanning sessions"));
      }
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, harnessFilter, hideEmpty, loadSession, setActiveSession, setError, setSessions]);

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

  const refreshRepositories = useCallback(async () => {
    try {
      const next = await listRepositories();
      setRepositories(next);
      return next;
    } catch (err) {
      setError(describeError(err, "loading registered repositories"));
      return [];
    } finally {
      setRepositoriesLoaded(true);
    }
  }, [setError]);

  const selectRepository = useCallback(async (id?: string) => {
    setActiveRepositoryId(id);
    if (!id) {
      void scan(false);
      return;
    }
    beginLoading();
    setError(undefined);
    try {
      const nextCity = await getRepositoryCityMap(id);
      setRepositoryMap(nextCity);
    } catch (err) {
      setError(describeError(err, "loading the registered repository map"));
    } finally {
      endLoading();
    }
  }, [beginLoading, endLoading, scan, setError, setRepositoryMap]);

  const registerRepository = useCallback(async (path: string, name?: string) => {
    setError(undefined);
    try {
      const created = await addRepository(path, name);
      await refreshRepositories();
      await scan(true);
      await selectRepository(created.repo.id);
    } catch (err) {
      setError(describeError(err, "adding the repository"));
      throw err;
    }
  }, [refreshRepositories, scan, selectRepository, setError]);

  const toggleRepository = useCallback(async (id: string, enabled: boolean) => {
    setError(undefined);
    try {
      await updateRepository(id, { enabled });
      await refreshRepositories();
      if (!enabled && activeRepositoryId === id) {
        setActiveRepositoryId(undefined);
        void scan(false);
      }
    } catch (err) {
      setError(describeError(err, enabled ? "enabling the repository" : "disabling the repository"));
      throw err;
    }
  }, [activeRepositoryId, refreshRepositories, scan, setError]);

  const openDiscovery = useCallback(() => {
    setDiscoveryPreferHome(false);
    setDiscoveryOpen(true);
  }, []);

  const openHomeDiscovery = useCallback(() => {
    setDiscoveryPreferHome(true);
    setDiscoveryOpen(true);
  }, []);

  const focusManualRepository = useCallback(() => {
    setOnboardingDismissed(true);
    setDiscoveryOpen(false);
    setDiscoveryPreferHome(false);
    setRailCollapsed(false);
    window.requestAnimationFrame(() => document.getElementById("rail-open-input")?.focus());
  }, [setRailCollapsed]);

  const repositoriesRegisteredFromDiscovery = useCallback((statuses: RepositoryStatus[]) => {
    if (statuses.length === 0) return;
    void (async () => {
      await refreshRepositories();
      await scan(true);
      await selectRepository(statuses[0].repo.id);
    })();
  }, [refreshRepositories, scan, selectRepository]);

  const refresh = useCallback(() => {
    if (manualRefreshInFlight.current) return;
    manualRefreshInFlight.current = true;
    void scan(true).finally(() => {
      manualRefreshInFlight.current = false;
    });
  }, [scan]);

  const selectSession = useCallback((key: string) => {
    activeSessionKeyRef.current = key;
    setActiveSession(key);
    void loadSession(key);
  }, [loadSession, setActiveSession]);

  const exportVideo = useCallback(async () => {
    const canvas = canvasRef.current;
    const total = trace?.events.length ?? 0;
    if (!canvas || total === 0 || exporting) return;
    // the recorder owns the playhead for the duration of the export; setting
    // exporting=true locks the transport, scrubber, session rail, and view
    // toggle (see the `exporting` prop threaded into Timeline/SessionRail/Hud)
    // so nothing else moves the playhead or swaps the canvas mid-recording
    const exportSessionKey = activeSessionKeyRef.current;
    const resumeSeq = useAppStore.getState().currentSeq;
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
      // only restore the playhead if we're still on the same session — a guard
      // in case a switch slipped through; normally the UI lock prevents it
      if (activeSessionKeyRef.current === exportSessionKey) {
        setCurrentSeq(resumeSeq);
      }
      setExporting(false);
    }
  }, [trace, exporting, setCurrentSeq, setError]);

  // stable callbacks keep SessionRail's memo effective across playback ticks
  const collapseRail = useCallback(() => setRailCollapsed(true), [setRailCollapsed]);
  const expandRail = useCallback(() => setRailCollapsed(false), [setRailCollapsed]);

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

  useEffect(() => {
    if (!activeSessionKey) {
      setLiveStatus("idle");
      return;
    }
    const key = activeSessionKey;
    setLiveStatus("connecting");
    const stream = new EventSource(`/api/sessions/${encodeURIComponent(key)}/stream`);
		stream.addEventListener("observable", () => {
      setLiveStatus("live");
      if (followLiveRef.current && activeSessionKeyRef.current === key) void loadSession(key);
    });
		stream.addEventListener("checkpoint", () => setLiveStatus("live"));
    stream.addEventListener("status", (event) => {
      try {
        const status = JSON.parse((event as MessageEvent<string>).data) as { status?: string };
        setLiveStatus(status.status === "disconnected" ? "disconnected" : "live");
      } catch {
        setLiveStatus("disconnected");
      }
    });
    stream.onerror = () => setLiveStatus("disconnected");
    return () => stream.close();
  }, [activeSessionKey, loadSession]);

  useEffect(() => {
    const params = new URL(window.location.href).searchParams;
    if (params.get("map") === "1") {
      void loadRepoMap(params.get("repo") ?? undefined);
    } else {
			if (params.get("profile") === "low") setView("list");
      void refreshRepositories();
      void scan(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

	// A file deep-link makes provenance review shareable and accessible without
	// requiring a precise 3D raycast. Unknown paths remain fail-closed.
	useEffect(() => {
		const path = new URL(window.location.href).searchParams.get("file");
		if (path && city?.files.some((file) => file.path === path)) setSelectedPath(path);
	}, [city, setSelectedPath]);

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
  const selectedProvenance = useMemo(() => {
		if (!selectedPath) return undefined;
		return [...observableEvents].reverse().find((item) => item.event.attrs?.path === selectedPath);
	}, [observableEvents, selectedPath]);
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
  const showRepositoryOnboarding =
    !mapOnly && repositoriesLoaded && repositories.length === 0 && !loading && !onboardingDismissed && !discoveryOpen;

  return (
    <main
		data-testid="observatory-root"
		data-observable-events={observableEvents.length}
		data-trace-events={trace?.events.length ?? 0}
		className={mapOnly ? "app-frame rail-collapsed" : railCollapsed ? "app-frame rail-collapsed" : "app-frame"}
	>
      {mapOnly ? null : (
        <SessionRail
          sessions={sessions}
          activeKey={activeSessionKey}
          loading={loading}
          hideEmpty={hideEmpty}
          harnessFilter={harnessFilter}
          collapsed={railCollapsed}
          repositories={repositories}
          activeRepositoryId={activeRepositoryId}
          liveStatus={liveStatus}
          followLive={followLive}
          onSelect={selectSession}
          onRefresh={refresh}
          onHideEmptyChange={setHideEmpty}
          onHarnessFilterChange={setHarnessFilter}
          onCollapse={collapseRail}
          onRepositorySelect={(id) => void selectRepository(id)}
          onRepositoryAdd={registerRepository}
          onRepositoryToggle={toggleRepository}
          onOpenDiscovery={openDiscovery}
          onFollowLiveChange={setFollowLive}
          locked={exporting}
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
          {view === "list" ? (
			<ListScene city={city} touchByPath={playback.touchByPath} selectedPath={selectedPath} onSelect={setSelectedPath} />
		  ) : view === "tree" ? (
            <TreeScene
              city={city}
              playback={playback}
              selectedPath={selectedPath}
              onSelect={setSelectedPath}
              onCanvasReady={handleCanvasReady}
            />
          ) : (
            <CityScene
              city={city}
              playback={playback}
              selectedPath={selectedPath}
              onSelect={setSelectedPath}
              onCanvasReady={handleCanvasReady}
              locHeights={mapOnly}
            />
          )}
          <Hud
            trace={trace}
            city={city}
            view={view}
            editedNow={touchCounts.edited}
            readNow={touchCounts.read}
            seenNow={touchCounts.seen}
            churn={churn}
			agents={agentProcesses}
            onViewChange={setView}
            onSelectFile={setSelectedPath}
            locked={exporting}
          />
			<MemorySearch />
			<ReviewCenter sessions={sessions} activeKey={activeSessionKey} />
          {selectedFile ? (
            <Inspector
              file={selectedFile}
              touch={playback.touchByPath.get(selectedFile.path)}
              history={playback.historyByPath.get(selectedFile.path) ?? []}
				provenance={selectedProvenance}
              onClose={() => setSelectedPath(undefined)}
              onJumpTo={setCurrentSeq}
            />
          ) : null}
          {!mapOnly && !loading && sessions.length === 0 && !city && !showRepositoryOnboarding ? (
            <div className="empty-stage">
              <div className="card">
                <h2>No sessions found</h2>
                <p>
                  No Claude Code or Codex sessions are associated with an enabled registered repository. Run a
                  supported session there, then refresh.
                </p>
              </div>
            </div>
          ) : null}
          {showRepositoryOnboarding ? (
            <RepositoryOnboarding
              onScanHome={openHomeDiscovery}
              onChooseFolders={openDiscovery}
              onAddManually={focusManualRepository}
              onSkip={() => setOnboardingDismissed(true)}
            />
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
        />
      </section>
      {mapOnly ? null : (
        <RepositoryDiscovery
          open={discoveryOpen}
          preferHome={discoveryPreferHome}
          onClose={() => {
            setDiscoveryOpen(false);
            setDiscoveryPreferHome(false);
          }}
          onAddManually={focusManualRepository}
          onRegistered={repositoriesRegisteredFromDiscovery}
        />
      )}
    </main>
  );
}
