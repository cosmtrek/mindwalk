import { Eye, EyeOff, FolderPlus, FolderSearch, PanelLeftClose, RefreshCw, Search } from "lucide-react";
import { memo, useMemo, useState } from "react";
import { sessionVisible } from "../state/filters";
import { LogoMark } from "./LogoMark";
import { toggleRailShortcut } from "./shortcuts";
import type { RepositoryStatus, SessionMeta } from "../types";

interface SessionRailProps {
  sessions: SessionMeta[];
  activeKey?: string;
  loading: boolean;
  hideEmpty: boolean;
  harnessFilter?: string;
  collapsed: boolean;
  repositories: RepositoryStatus[];
  activeRepositoryId?: string;
  liveStatus: "idle" | "connecting" | "live" | "disconnected";
  followLive: boolean;
  onSelect: (key: string) => void;
  onRefresh: () => void;
  onHideEmptyChange: (hide: boolean) => void;
  onHarnessFilterChange: (harness?: string) => void;
  onCollapse: () => void;
  onRepositorySelect: (id?: string) => void;
  onRepositoryAdd: (path: string, name?: string) => Promise<void>;
  onRepositoryToggle: (id: string, enabled: boolean) => Promise<void>;
  onOpenDiscovery: () => void;
  onFollowLiveChange: (follow: boolean) => void;
  // while a video export records, session switching is locked so it can't swap
  // the canvas or playhead out from under the recorder
  locked?: boolean;
}

// memo: the app re-renders every playback tick; the rail's props only change
// on scans, session switches, and filter changes
export const SessionRail = memo(function SessionRail({
  sessions,
  activeKey,
  loading,
  hideEmpty,
  harnessFilter,
  collapsed,
  repositories,
  activeRepositoryId,
  liveStatus,
  followLive,
  onSelect,
  onRefresh,
  onHideEmptyChange,
  onHarnessFilterChange,
  onCollapse,
  onRepositorySelect,
  onRepositoryAdd,
  onRepositoryToggle,
  onOpenDiscovery,
  onFollowLiveChange,
  locked = false
}: SessionRailProps) {
  const [query, setQuery] = useState("");
  const [repoPath, setRepoPath] = useState("");
  const [repoName, setRepoName] = useState("");
  const [addingRepo, setAddingRepo] = useState(false);
  const activeRepository = repositories.find((item) => item.repo.id === activeRepositoryId);
  const harnesses = useMemo(() => [...new Set(sessions.map((s) => s.harness))].sort(), [sessions]);
  const emptyCount = useMemo(() => sessions.filter((s) => s.eventCount === 0).length, [sessions]);
  // a persisted filter can name a harness with no sessions this scan; treating
  // it as "all" avoids an empty list with no visible chip to clear it
  const effectiveHarness = harnessFilter && harnesses.includes(harnessFilter) ? harnessFilter : undefined;
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return sessions.filter((session) => {
      if (activeRepository && session.cwd !== activeRepository.repo.path) return false;
      if (!sessionVisible(session, { hideEmpty, harness: effectiveHarness }, activeKey)) return false;
      if (!q) return true;
      return `${session.title ?? ""} ${session.id} ${session.gitBranch ?? ""} ${session.harness}`
        .toLowerCase()
        .includes(q);
    });
  }, [sessions, query, hideEmpty, effectiveHarness, activeKey, activeRepository]);

  return (
    <aside className={collapsed ? "session-rail collapsed" : "session-rail"}>
      <div className="rail-head">
        <h1 className="wordmark">
          <LogoMark />
          <span>
            mindwalk<span className="spark">.</span>
          </span>
        </h1>
        <div className="rail-head-actions">
          <button className="icon-btn" onClick={onRefresh} title="Rescan sessions" aria-label="Rescan sessions">
            <RefreshCw size={15} />
          </button>
          <button
            className="icon-btn"
            onClick={onCollapse}
            title={`Hide sidebar (${toggleRailShortcut})`}
            aria-label="Hide session sidebar"
          >
            <PanelLeftClose size={15} />
          </button>
        </div>
      </div>
      <section className="repo-picker" aria-label="Repository picker">
        <label className="rail-open-label" htmlFor="repository-picker">
          Repository
        </label>
        <select
          id="repository-picker"
          className="repo-select"
          value={activeRepositoryId ?? ""}
          onChange={(event) => onRepositorySelect(event.currentTarget.value || undefined)}
        >
          <option value="">All observed sessions</option>
          {repositories.map(({ repo, missing, invalidPath }) => (
            <option key={repo.id} value={repo.id} disabled={!repo.enabled || missing || invalidPath}>
              {repo.name}{missing ? " — missing" : invalidPath ? " — unsafe" : repo.enabled ? "" : " — disabled"}
            </option>
          ))}
        </select>
        {activeRepository ? (
          <div className="repo-facts">
            <span>{repositoryState(activeRepository)}</span>
            <button
              type="button"
              onClick={() => void onRepositoryToggle(activeRepository.repo.id, !activeRepository.repo.enabled)}
            >
              {activeRepository.repo.enabled ? "Disable" : "Enable"}
            </button>
          </div>
        ) : null}
        <button type="button" className="repo-discovery-open" onClick={onOpenDiscovery}>
          <FolderSearch size={13} aria-hidden /> Find repositories
        </button>
      </section>
      <div className="rail-controls">
        <label className="rail-filter">
          <Search size={14} aria-hidden />
          <input
            type="search"
            placeholder="Filter sessions"
            value={query}
            onChange={(e) => setQuery(e.currentTarget.value)}
            aria-label="Filter sessions"
          />
        </label>
        {harnesses.length > 1 || emptyCount > 0 ? (
          <div className="rail-chips" role="group" aria-label="Session filters">
            {harnesses.length > 1 ? (
              <>
                <button
                  className={effectiveHarness === undefined ? "chip active" : "chip"}
                  onClick={() => onHarnessFilterChange(undefined)}
                >
                  all
                </button>
                {harnesses.map((harness) => (
                  <button
                    key={harness}
                    className={effectiveHarness === harness ? "chip active" : "chip"}
                    onClick={() => onHarnessFilterChange(harness)}
                  >
                    {harnessLabel(harness)}
                  </button>
                ))}
              </>
            ) : null}
            {emptyCount > 0 ? (
              <button
                className={hideEmpty ? "eye-toggle" : "eye-toggle showing"}
                onClick={() => onHideEmptyChange(!hideEmpty)}
                aria-pressed={!hideEmpty}
                title={
                  hideEmpty ? `Show ${emptyCount} empty sessions` : `Hide ${emptyCount} empty sessions`
                }
                aria-label={
                  hideEmpty ? `Show ${emptyCount} empty sessions` : `Hide ${emptyCount} empty sessions`
                }
              >
                {hideEmpty ? <EyeOff size={13} aria-hidden /> : <Eye size={13} aria-hidden />}
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      <div className="session-list" aria-busy={loading}>
        {shown.map((session) => (
          <button
            key={session.key}
            className={session.key === activeKey ? "session-row active" : "session-row"}
            onClick={() => onSelect(session.key)}
            disabled={locked}
          >
            <span className="session-title">{session.title || session.id}</span>
            <span className="session-meta">
              <span className={`harness-dot ${harnessClass(session.harness)}`} aria-hidden />
              <span className="session-meta-text">
                {harnessLabel(session.harness)} · {session.eventCount}{" "}
                {session.eventCount === 1 ? "call" : "calls"}
                {session.gitBranch ? ` · ${session.gitBranch}` : ""}
                {session.endedAt ? ` · ${shortDate(session.endedAt)}` : ""}
              </span>
            </span>
          </button>
        ))}
        {shown.length === 0 ? (
          <p className="muted" style={{ padding: "10px 8px" }}>
            {loading && sessions.length === 0 ? "Scanning sessions…" : "No matching sessions."}
          </p>
        ) : null}
      </div>
      <form
        className="rail-open"
        onSubmit={(e) => {
          e.preventDefault();
          const path = repoPath.trim();
          if (!path || addingRepo) return;
          setAddingRepo(true);
          void onRepositoryAdd(path, repoName.trim() || undefined)
            .then(() => {
              setRepoPath("");
              setRepoName("");
            })
            .catch(() => undefined)
            .finally(() => setAddingRepo(false));
        }}
      >
        <label className="rail-open-label" htmlFor="rail-open-input">
          Add a repository
        </label>
        <div className="rail-open-row">
          <input
            id="rail-open-input"
            type="text"
            className="rail-open-input"
            placeholder="/absolute/path/to/repo"
            value={repoPath}
            onChange={(e) => setRepoPath(e.currentTarget.value)}
            spellCheck={false}
          />
          <button type="submit" className="rail-open-btn" disabled={repoPath.trim() === "" || addingRepo} title="Register repository">
            <FolderPlus size={13} aria-hidden />
            <span>{addingRepo ? "Adding…" : "Add"}</span>
          </button>
        </div>
        <input
          type="text"
          className="rail-open-input"
          placeholder="Display name (optional)"
          value={repoName}
          onChange={(e) => setRepoName(e.currentTarget.value)}
        />
      </form>
      <div className="rail-foot">
        <span>
          {shown.length === sessions.length
            ? `${sessions.length} session${sessions.length === 1 ? "" : "s"}`
            : `${shown.length} of ${sessions.length} sessions`}
        </span>
        {liveStatus !== "idle" ? (
          <button
            type="button"
            className={`live-state ${liveStatus}`}
            onClick={() => onFollowLiveChange(!followLive)}
            aria-pressed={followLive}
            title="Pause or resume live following; ingestion continues"
          >
            {liveStatus} · {followLive ? "following" : "paused"}
          </button>
        ) : null}
      </div>
    </aside>
  );
});

function repositoryState(status: RepositoryStatus): string {
  if (status.missing) return "missing";
  if (status.invalidPath) return "unsafe path";
  if (!status.repo.enabled) return "disabled";
  if (!status.git.isGit) return "not git";
  return `${status.git.branch || "detached"}${status.git.commit ? ` @ ${status.git.commit}` : ""}${status.git.dirty ? " · dirty" : ""}`;
}

function harnessLabel(harness: string): string {
  switch (harness) {
    case "claude-code":
      return "claude";
    default:
      return harness;
  }
}

function harnessClass(harness: string): string {
  switch (harness) {
    case "claude-code":
      return "claude";
    case "codex":
      return "codex";
    default:
      return "other";
  }
}

function shortDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const now = new Date();
  const sameYear = d.getFullYear() === now.getFullYear();
  const md = `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
  const hm = `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  return sameYear ? `${md} ${hm}` : `${d.getFullYear()}-${md}`;
}
