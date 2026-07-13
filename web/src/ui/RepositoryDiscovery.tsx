import {
  AlertTriangle,
  Check,
  ChevronLeft,
  Eye,
  EyeOff,
  FolderSearch,
  LoaderCircle,
  RefreshCw,
  Search,
  X
} from "lucide-react";
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import {
  cancelRepositoryDiscovery,
  forgetRepositoryDiscovery,
  getDiscoveredRepositories,
  getRepositoryDiscoveryConfig,
  getRepositoryDiscoveryStatus,
  registerDiscoveredRepositories,
  resetRepositoryDiscoveryExclusions,
  setDiscoveredRepositoriesHidden,
  startRepositoryDiscovery,
  updateRepositoryDiscoveryConfig
} from "../api/client";
import type {
  DiscoveredRepository,
  DiscoveredRepositoryRegistration,
  DiscoveryRegistrationResult,
  RepositoryDiscoveryConfig,
  RepositoryDiscoveryStatus,
  RepositoryStatus
} from "../types";

type DiscoveryStep = "roots" | "confirm-scan" | "scanning" | "results" | "confirm-add" | "outcome";
type ResultFilter =
  | "all"
  | "unregistered"
  | "registered"
  | "clean"
  | "dirty"
  | "worktrees"
  | "inaccessible"
  | "warnings";
type ResultSort = "name" | "path" | "type" | "modified";

interface RegistrationForm extends DiscoveredRepositoryRegistration {
  originalName: string;
  path: string;
  tagsText: string;
}

interface RepositoryDiscoveryProps {
  open: boolean;
  preferHome?: boolean;
  onClose: () => void;
  onAddManually: () => void;
  onRegistered: (statuses: RepositoryStatus[]) => void;
}

const emptyStatus: RepositoryDiscoveryStatus = {
  status: "idle",
  directoriesExamined: 0,
  repositoriesFound: 0,
  repositoriesSkipped: 0,
  permissionErrors: 0,
  elapsedMillis: 0
};

const resultFilters: { value: ResultFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "unregistered", label: "Unregistered" },
  { value: "registered", label: "Already registered" },
  { value: "clean", label: "Clean" },
  { value: "dirty", label: "Dirty" },
  { value: "worktrees", label: "Worktrees" },
  { value: "inaccessible", label: "Inaccessible" },
  { value: "warnings", label: "Warnings" }
];

export function RepositoryDiscovery({
  open,
  preferHome = false,
  onClose,
  onAddManually,
  onRegistered
}: RepositoryDiscoveryProps) {
  const titleId = useId();
  const descriptionId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const [step, setStep] = useState<DiscoveryStep>("roots");
  const [config, setConfig] = useState<RepositoryDiscoveryConfig>();
  const [status, setStatus] = useState<RepositoryDiscoveryStatus>(emptyStatus);
  const [results, setResults] = useState<DiscoveredRepository[]>([]);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [registrationForms, setRegistrationForms] = useState<RegistrationForm[]>([]);
  const [registrationResults, setRegistrationResults] = useState<DiscoveryRegistrationResult[]>([]);
  const [rootInput, setRootInput] = useState("");
  const [exclusionInput, setExclusionInput] = useState("");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<ResultFilter>("all");
  const [sort, setSort] = useState<ResultSort>("name");
  const [showHidden, setShowHidden] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  const loadResults = useCallback(async (includeHidden: boolean) => {
    const next = await getDiscoveredRepositories(includeHidden);
    setResults(next);
    return next;
  }, []);

  const loadInitialState = useCallback(async () => {
    setBusy(true);
    setError(undefined);
    try {
      const [nextConfig, nextStatus] = await Promise.all([
        getRepositoryDiscoveryConfig(),
        getRepositoryDiscoveryStatus()
      ]);
      const homeRoot = nextConfig.homeRoot ?? nextConfig.suggestedRoots?.[0];
      const preparedConfig = preferHome && homeRoot && !nextConfig.approvedRoots.includes(homeRoot)
        ? { ...nextConfig, approvedRoots: [...nextConfig.approvedRoots, homeRoot] }
        : nextConfig;
      setConfig(preparedConfig);
      setStatus(nextStatus);
      if (nextStatus.status === "running") {
        setStep("scanning");
        return;
      }
      const nextResults = await loadResults(false);
      setStep(preferHome || nextResults.length === 0 ? "roots" : "results");
    } catch (cause) {
      setError(errorMessage(cause, "loading repository discovery"));
      setStep("roots");
    } finally {
      setBusy(false);
    }
  }, [loadResults, preferHome]);

  useEffect(() => {
    if (!open) return;
    openerRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setShowHidden(false);
    setSelectedIds(new Set());
    setRegistrationResults([]);
    void loadInitialState();
    queueMicrotask(() => dialogRef.current?.focus());
    return () => openerRef.current?.focus();
  }, [loadInitialState, open]);

  useEffect(() => {
    if (!open || step !== "scanning") return;
    let disposed = false;
    let timer: number | undefined;
    const poll = async () => {
      try {
        const next = await getRepositoryDiscoveryStatus();
        if (disposed) return;
        setStatus(next);
        if (next.status !== "running") {
          await loadResults(showHidden);
          if (!disposed) setStep("results");
          return;
        }
      } catch (cause) {
        if (!disposed) setError(errorMessage(cause, "reading scan progress"));
      }
      if (!disposed) timer = window.setTimeout(poll, 900);
    };
    timer = window.setTimeout(poll, 250);
    return () => {
      disposed = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [loadResults, open, showHidden, step]);

  useEffect(() => {
    if (!open || (step !== "results" && step !== "outcome")) return;
    void loadResults(showHidden).catch((cause) => setError(errorMessage(cause, "loading scan results")));
  }, [loadResults, open, showHidden, step]);

  const handleDialogKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = focusableElements(dialogRef.current);
    if (focusable.length === 0) {
      event.preventDefault();
      dialogRef.current?.focus();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  const updateOptions = (update: Partial<RepositoryDiscoveryConfig["options"]>) => {
    setConfig((current) => current ? { ...current, options: { ...current.options, ...update } } : current);
  };

  const addRoot = (rawPath: string) => {
    if (!config) return;
    const path = rawPath.trim();
    if (!path.startsWith("/")) {
      setError("Scan roots must be absolute paths. The server will canonicalize and validate them.");
      return;
    }
    if (config.approvedRoots.includes(path)) {
      setError("That scan root is already selected.");
      return;
    }
    setConfig({ ...config, approvedRoots: [...config.approvedRoots, path] });
    setRootInput("");
    setError(undefined);
  };

  const removeRoot = (path: string) => {
    setConfig((current) => current ? {
      ...current,
      approvedRoots: current.approvedRoots.filter((root) => root !== path)
    } : current);
  };

  const addCustomExclusion = () => {
    if (!config) return;
    const value = exclusionInput.trim();
    if (!value) return;
    if (config.customExclusions.includes(value)) {
      setError("That custom exclusion is already configured.");
      return;
    }
    setConfig({ ...config, customExclusions: [...config.customExclusions, value] });
    setExclusionInput("");
    setError(undefined);
  };

  const startScan = async (roots?: string[]) => {
    if (!config || config.approvedRoots.length === 0 || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const saved = await updateRepositoryDiscoveryConfig(config);
      if (!saved) throw new Error("the server did not return the canonical scan plan");
      setConfig(saved);
      const started = await startRepositoryDiscovery(saved, roots ?? saved.approvedRoots);
      setStatus(started ?? { ...emptyStatus, status: "running" });
      setSelectedIds(new Set());
      setResults([]);
      setStep("scanning");
    } catch (cause) {
      setError(errorMessage(cause, "starting the repository scan"));
    } finally {
      setBusy(false);
    }
  };

  const saveSettings = async () => {
    if (!config || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const saved = await updateRepositoryDiscoveryConfig(config);
      if (saved) setConfig(saved);
    } catch (cause) {
      setError(errorMessage(cause, "saving repository discovery settings"));
    } finally {
      setBusy(false);
    }
  };

  const reviewScan = async () => {
    if (!config || config.approvedRoots.length === 0 || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const saved = await updateRepositoryDiscoveryConfig(config);
      if (!saved) throw new Error("the server did not return the canonical scan plan");
      setConfig(saved);
      setStep("confirm-scan");
    } catch (cause) {
      setError(errorMessage(cause, "validating the repository scan plan"));
    } finally {
      setBusy(false);
    }
  };

  const cancelScan = async () => {
    if (busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await cancelRepositoryDiscovery();
      // The scanner owns the terminal state and persists any partial results.
      // Keep polling until it reports cancelled rather than presenting stale
      // results immediately after the cancellation request.
    } catch (cause) {
      setError(errorMessage(cause, "cancelling the repository scan"));
    } finally {
      setBusy(false);
    }
  };

  const filteredResults = useMemo(() => {
    const needle = query.trim().toLowerCase();
    const next = results.filter((result) => {
      if (needle && !`${result.name} ${result.path} ${result.branch ?? ""} ${result.type}`.toLowerCase().includes(needle)) {
        return false;
      }
      switch (filter) {
        case "unregistered": return !result.alreadyRegistered;
        case "registered": return result.alreadyRegistered;
        case "clean": return result.state === "clean";
        case "dirty": return result.state === "dirty";
        case "worktrees": return result.type === "worktree";
        case "inaccessible": return !result.accessible;
        case "warnings": return (result.warnings ?? []).length > 0;
        default: return true;
      }
    });
    next.sort((left, right) => compareResults(left, right, sort));
    return next;
  }, [filter, query, results, sort]);

  const selectedResults = useMemo(
    () => results.filter((result) => selectedIds.has(result.id)),
    [results, selectedIds]
  );
  const addableResults = useMemo(
    () => selectedResults.filter((result) => result.accessible && !result.alreadyRegistered && !result.hidden),
    [selectedResults]
  );
  const selectedVisibleCount = filteredResults.filter((result) => selectedIds.has(result.id)).length;

  const selectAllVisible = () => setSelectedIds(new Set(filteredResults.map((result) => result.id)));
  const toggleResult = (id: string) => {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const setHidden = async (hidden: boolean) => {
    const ids = selectedResults.filter((result) => result.hidden !== hidden).map((result) => result.id);
    if (ids.length === 0 || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await setDiscoveredRepositoriesHidden(ids, hidden);
      await loadResults(showHidden);
      setSelectedIds(new Set());
    } catch (cause) {
      setError(errorMessage(cause, hidden ? "hiding discoveries" : "recovering discoveries"));
    } finally {
      setBusy(false);
    }
  };

  const beginRegistration = () => {
    setRegistrationForms(addableResults.map((result) => ({
      id: result.id,
      originalName: result.name,
      path: result.path,
      name: result.name,
      group: "",
      tags: [],
      tagsText: "",
      color: "",
      enabled: true
    })));
    setStep("confirm-add");
  };

  const updateRegistration = (id: string, update: Partial<RegistrationForm>) => {
    setRegistrationForms((current) => current.map((item) => item.id === id ? { ...item, ...update } : item));
  };

  const submitRegistration = async () => {
    if (registrationForms.length === 0 || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      const response = await registerDiscoveredRepositories(registrationForms.map(({ tagsText, originalName, path, ...item }) => ({
        ...item,
        name: item.name.trim(),
        group: item.group.trim(),
        color: item.color.trim(),
        tags: splitTags(tagsText)
      })));
      setRegistrationResults(response.results);
      const added = response.results.flatMap((result) => result.repository ? [result.repository] : []);
      if (added.length > 0) onRegistered(added);
      await loadResults(showHidden);
      setSelectedIds(new Set());
      setStep("outcome");
    } catch (cause) {
      setError(errorMessage(cause, "registering the approved repositories"));
    } finally {
      setBusy(false);
    }
  };

  const forgetHistory = async () => {
    if (!window.confirm("Forget the latest discovery results and scan summary? Approved roots, exclusions, and hidden preferences remain. Registered repositories are not removed.")) return;
    setBusy(true);
    setError(undefined);
    try {
      await forgetRepositoryDiscovery();
      setShowHidden(false);
      setResults([]);
      setSelectedIds(new Set());
      setStatus(emptyStatus);
      setStep("roots");
      setConfig(await getRepositoryDiscoveryConfig());
    } catch (cause) {
      setError(errorMessage(cause, "forgetting scan history"));
    } finally {
      setBusy(false);
    }
  };

  const resetExclusions = async () => {
    if (!window.confirm("Remove custom exclusions and restore the safe defaults? Locked security exclusions remain enabled.")) return;
    setBusy(true);
    setError(undefined);
    try {
      const next = await resetRepositoryDiscoveryExclusions();
      setConfig(next ?? await getRepositoryDiscoveryConfig());
    } catch (cause) {
      setError(errorMessage(cause, "resetting scan exclusions"));
    } finally {
      setBusy(false);
    }
  };

  if (!open) return null;

  const homeRoot = config?.homeRoot ?? config?.suggestedRoots?.[0];
  const homeSelected = homeRoot !== undefined && config?.approvedRoots.includes(homeRoot) === true;
  const invalidLimits = config ? Object.values(config.options).some((value) => typeof value === "number" && (!Number.isFinite(value) || value <= 0)) : true;

  return (
    <div className="discovery-backdrop">
      <div
        ref={dialogRef}
        className="discovery-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
        tabIndex={-1}
        onKeyDown={handleDialogKeyDown}
      >
        <header className="discovery-header">
          <div>
            <p className="eyebrow">Repository settings</p>
            <h2 id={titleId}>Find repositories</h2>
            <p id={descriptionId}>Local, bounded metadata discovery. Nothing is registered until final approval.</p>
          </div>
          <button className="icon-btn" type="button" onClick={onClose} aria-label="Close repository discovery">
            <X size={16} aria-hidden />
          </button>
        </header>

        {error ? <div className="discovery-alert error" role="alert">{error}</div> : null}
        <div className="discovery-body">
          {step === "roots" ? (
            <RootSetup
              config={config}
              busy={busy}
              homeRoot={homeRoot}
              homeSelected={homeSelected}
              rootInput={rootInput}
              exclusionInput={exclusionInput}
              invalidLimits={invalidLimits}
              onRootInput={setRootInput}
              onExclusionInput={setExclusionInput}
              onAddRoot={addRoot}
              onRemoveRoot={removeRoot}
              onAddExclusion={addCustomExclusion}
              onRemoveExclusion={(value) => setConfig((current) => current ? {
                ...current,
                customExclusions: current.customExclusions.filter((item) => item !== value)
              } : current)}
              onOptions={updateOptions}
              onSave={() => void saveSettings()}
              onReview={() => void reviewScan()}
              onResetExclusions={() => void resetExclusions()}
              onForget={() => void forgetHistory()}
            />
          ) : null}

          {step === "confirm-scan" && config ? (
            <ScanConfirmation
              config={config}
              busy={busy}
              homeSelected={homeSelected}
              onBack={() => setStep("roots")}
              onCancel={onClose}
              onStart={() => void startScan()}
            />
          ) : null}

          {step === "scanning" ? (
            <ScanProgress status={status} maxDirectories={config?.options.maxDirectories ?? 1} busy={busy} onCancel={() => void cancelScan()} />
          ) : null}

          {step === "results" ? (
            <DiscoveryResults
              status={status}
              results={filteredResults}
              allResults={results}
			  approvedRoots={config?.approvedRoots ?? []}
              selectedIds={selectedIds}
              selectedVisibleCount={selectedVisibleCount}
              selectedCount={selectedIds.size}
              addableCount={addableResults.length}
              query={query}
              filter={filter}
              sort={sort}
              showHidden={showHidden}
              busy={busy}
              onQuery={setQuery}
              onFilter={setFilter}
              onSort={setSort}
              onShowHidden={setShowHidden}
              onToggle={toggleResult}
              onSelectVisible={selectAllVisible}
              onClear={() => setSelectedIds(new Set())}
              onHide={() => void setHidden(true)}
              onUnhide={() => void setHidden(false)}
              onAdd={beginRegistration}
              onRescan={() => void startScan()}
              onRescanRoot={(root) => void startScan([root])}
              onConfigure={() => setStep("roots")}
              onAddManually={() => {
                onClose();
                window.setTimeout(onAddManually, 0);
              }}
              onCancel={onClose}
            />
          ) : null}

          {step === "confirm-add" ? (
            <RegistrationConfirmation
              forms={registrationForms}
              busy={busy}
              onChange={updateRegistration}
              onRemove={(id) => setRegistrationForms((current) => current.filter((item) => item.id !== id))}
              onBack={() => setStep("results")}
              onCancel={onClose}
              onSubmit={() => void submitRegistration()}
            />
          ) : null}

          {step === "outcome" ? (
            <RegistrationOutcome
              forms={registrationForms}
              results={registrationResults}
              onBack={() => setStep("results")}
              onDone={onClose}
            />
          ) : null}
        </div>
      </div>
    </div>
  );
}

function RootSetup({
  config,
  busy,
  homeRoot,
  homeSelected,
  rootInput,
  exclusionInput,
  invalidLimits,
  onRootInput,
  onExclusionInput,
  onAddRoot,
  onRemoveRoot,
  onAddExclusion,
  onRemoveExclusion,
  onOptions,
  onSave,
  onReview,
  onResetExclusions,
  onForget
}: {
  config?: RepositoryDiscoveryConfig;
  busy: boolean;
  homeRoot?: string;
  homeSelected: boolean;
  rootInput: string;
  exclusionInput: string;
  invalidLimits: boolean;
  onRootInput: (value: string) => void;
  onExclusionInput: (value: string) => void;
  onAddRoot: (value: string) => void;
  onRemoveRoot: (value: string) => void;
  onAddExclusion: () => void;
  onRemoveExclusion: (value: string) => void;
  onOptions: (update: Partial<RepositoryDiscoveryConfig["options"]>) => void;
  onSave: () => void;
  onReview: () => void;
  onResetExclusions: () => void;
  onForget: () => void;
}) {
  if (!config) return <LoadingState label="Loading safe scan settings…" />;
  const suggested = (config.suggestedRoots ?? []).filter((path) => !config.approvedRoots.includes(path));
  return (
    <section className="discovery-step" aria-labelledby="discovery-roots-title">
      <div className="discovery-step-heading">
        <div><p className="eyebrow">Step 1</p><h3 id="discovery-roots-title">Choose folders to scan</h3></div>
        <span className="discovery-safety-note">Source files are not read</span>
      </div>

      <div className="discovery-choice-grid">
        <button
          type="button"
          className={homeSelected ? "discovery-choice selected" : "discovery-choice"}
          disabled={!homeRoot || homeSelected || busy}
          onClick={() => homeRoot && onAddRoot(homeRoot)}
        >
          <FolderSearch size={18} aria-hidden />
          <span><strong>Scan my home folder</strong><small>{homeRoot ?? "No safe home-folder suggestion is available"}</small></span>
          {homeSelected ? <Check size={16} aria-hidden /> : null}
        </button>
        {suggested.map((path) => (
          <button key={path} type="button" className="discovery-choice" onClick={() => onAddRoot(path)} disabled={busy}>
            <FolderSearch size={18} aria-hidden />
            <span><strong>Add suggested folder</strong><small>{path}</small></span>
          </button>
        ))}
      </div>

      <form className="discovery-inline-form" onSubmit={(event) => { event.preventDefault(); onAddRoot(rootInput); }}>
        <label htmlFor="discovery-root-input">Scan root path</label>
        <div><input id="discovery-root-input" value={rootInput} onChange={(event) => onRootInput(event.currentTarget.value)} placeholder="/absolute/path/to/folder" spellCheck={false} /><button type="submit" disabled={!rootInput.trim() || busy}>Add scan root</button></div>
      </form>

      <div className="discovery-root-list" aria-label="Selected scan roots">
        {config.approvedRoots.length === 0 ? <p className="muted">No folders selected. Scanning remains disabled.</p> : config.approvedRoots.map((root) => (
          <div key={root}><code>{root}</code><button type="button" onClick={() => onRemoveRoot(root)} disabled={busy}>Remove</button></div>
        ))}
      </div>

      <details className="discovery-settings">
        <summary>Limits and exclusions</summary>
        <div className="discovery-limit-grid">
          <NumberSetting label="Maximum depth" value={config.options.maxDepth} onChange={(maxDepth) => onOptions({ maxDepth })} />
          <NumberSetting label="Maximum directories" value={config.options.maxDirectories} onChange={(maxDirectories) => onOptions({ maxDirectories })} />
          <NumberSetting label="Maximum results" value={config.options.maxResults} onChange={(maxResults) => onOptions({ maxResults })} />
          <NumberSetting label="Scan timeout in seconds" value={config.options.timeoutSeconds} onChange={(timeoutSeconds) => onOptions({ timeoutSeconds })} />
        </div>
        <label className="discovery-check"><input type="checkbox" checked={config.options.findNested} onChange={(event) => onOptions({ findNested: event.currentTarget.checked })} /> Find nested repositories</label>
        <label className="discovery-check locked"><input type="checkbox" checked={false} disabled /> Follow symlinks <span>Locked off for safety</span></label>

        <h4>Default security exclusions</h4>
        <ul className="discovery-exclusions">
          {config.exclusions.map((item) => <li key={item.id}><span>{item.label}<small>{item.path ?? item.basename}</small></span>{item.locked ? <strong>Locked</strong> : <span>Default</span>}</li>)}
        </ul>
        <form className="discovery-inline-form compact" onSubmit={(event) => { event.preventDefault(); onAddExclusion(); }}>
          <label htmlFor="discovery-exclusion-input">Custom exclusion</label>
          <div><input id="discovery-exclusion-input" value={exclusionInput} onChange={(event) => onExclusionInput(event.currentTarget.value)} placeholder="folder name or safe path" /><button type="submit" disabled={!exclusionInput.trim()}>Add exclusion</button></div>
        </form>
        {config.customExclusions.length ? <ul className="discovery-custom-exclusions">{config.customExclusions.map((item) => <li key={item}><code>{item}</code><button type="button" onClick={() => onRemoveExclusion(item)}>Remove</button></li>)}</ul> : null}
        <div className="discovery-utility-actions"><button type="button" onClick={onResetExclusions}>Reset exclusions</button><button type="button" onClick={onForget}>Forget scan history</button></div>
      </details>

      <div className="discovery-actions end"><button type="button" onClick={onSave} disabled={busy || invalidLimits}>{busy ? "Saving…" : "Save settings"}</button><button className="primary" type="button" onClick={onReview} disabled={busy || config.approvedRoots.length === 0 || invalidLimits}>Review scan</button></div>
    </section>
  );
}

function ScanConfirmation({ config, busy, homeSelected, onBack, onCancel, onStart }: { config: RepositoryDiscoveryConfig; busy: boolean; homeSelected: boolean; onBack: () => void; onCancel: () => void; onStart: () => void }) {
  return (
    <section className="discovery-step" aria-labelledby="discovery-confirm-title">
      <p className="eyebrow">Explicit approval</p><h3 id="discovery-confirm-title">Confirm repository scan</h3>
      <p className="discovery-lede">The scanner will inspect directory and Git metadata within only these roots. It will not read ordinary source files or register anything.</p>
      {homeSelected ? <div className="discovery-alert warning"><AlertTriangle size={16} aria-hidden /><span>Your home folder can contain many directories. The configured depth, directory, result, and time limits remain enforced.</span></div> : null}
      <ul className="discovery-confirm-roots">{config.approvedRoots.map((root) => <li key={root}><code>{root}</code></li>)}</ul>
      <dl className="discovery-confirm-limits">
        <div><dt>Depth</dt><dd>{config.options.maxDepth}</dd></div><div><dt>Directories</dt><dd>{config.options.maxDirectories.toLocaleString()}</dd></div><div><dt>Results</dt><dd>{config.options.maxResults.toLocaleString()}</dd></div><div><dt>Timeout</dt><dd>{config.options.timeoutSeconds}s</dd></div><div><dt>Nested repositories</dt><dd>{config.options.findNested ? "On" : "Off"}</dd></div><div><dt>Follow symlinks</dt><dd>Off · locked</dd></div>
      </dl>
      <div className="discovery-actions"><button type="button" onClick={onBack}><ChevronLeft size={14} aria-hidden /> Back</button><span><button type="button" onClick={onCancel}>Cancel</button> <button className="primary" type="button" onClick={onStart} disabled={busy}>{busy ? "Starting…" : "Start scan"}</button></span></div>
    </section>
  );
}

function ScanProgress({ status, maxDirectories, busy, onCancel }: { status: RepositoryDiscoveryStatus; maxDirectories: number; busy: boolean; onCancel: () => void }) {
  return (
    <section className="discovery-step discovery-progress" aria-label="Repository scan progress">
      <LoaderCircle className="spin" size={24} aria-hidden /><p className="eyebrow">Bounded local scan</p><h3>Scanning for repositories</h3>
      <p className="discovery-current-root"><span>Current root</span><code>{status.currentRoot ?? "Preparing…"}</code></p>
      <progress max={Math.max(1, maxDirectories)} value={Math.min(status.directoriesExamined, maxDirectories)} />
      <dl className="discovery-progress-grid"><ProgressFact label="Directories examined" value={status.directoriesExamined} /><ProgressFact label="Repositories found" value={status.repositoriesFound} /><ProgressFact label="Repositories skipped" value={status.repositoriesSkipped} /><ProgressFact label="Permission errors" value={status.permissionErrors} /><ProgressFact label="Elapsed" value={formatDuration(status.elapsedMillis)} /></dl>
      <p className="discovery-live-summary" role="status" aria-live="polite">{status.directoriesExamined.toLocaleString()} directories examined; {status.repositoriesFound.toLocaleString()} repositories found.</p>
      <div className="discovery-actions center"><button className="danger" type="button" onClick={onCancel} disabled={busy}>{busy ? "Cancelling…" : "Cancel scan"}</button></div>
    </section>
  );
}

function DiscoveryResults({ status, results, allResults, approvedRoots, selectedIds, selectedVisibleCount, selectedCount, addableCount, query, filter, sort, showHidden, busy, onQuery, onFilter, onSort, onShowHidden, onToggle, onSelectVisible, onClear, onHide, onUnhide, onAdd, onRescan, onRescanRoot, onConfigure, onAddManually, onCancel }: {
  status: RepositoryDiscoveryStatus; results: DiscoveredRepository[]; allResults: DiscoveredRepository[]; approvedRoots: string[]; selectedIds: Set<string>; selectedVisibleCount: number; selectedCount: number; addableCount: number; query: string; filter: ResultFilter; sort: ResultSort; showHidden: boolean; busy: boolean; onQuery: (value: string) => void; onFilter: (value: ResultFilter) => void; onSort: (value: ResultSort) => void; onShowHidden: (value: boolean) => void; onToggle: (id: string) => void; onSelectVisible: () => void; onClear: () => void; onHide: () => void; onUnhide: () => void; onAdd: () => void; onRescan: () => void; onRescanRoot: (root: string) => void; onConfigure: () => void; onAddManually: () => void; onCancel: () => void;
}) {
  const selectedHidden = allResults.some((result) => selectedIds.has(result.id) && result.hidden);
  const selectedShown = allResults.some((result) => selectedIds.has(result.id) && !result.hidden);
  const discoveryRoots = [...new Set(approvedRoots)].sort();
  return (
    <section className="discovery-step" aria-labelledby="discovery-results-title">
      <div className="discovery-step-heading"><div><p className="eyebrow">Review only</p><h3 id="discovery-results-title">Discovered repositories</h3></div><span className={`discovery-scan-state ${status.status}`}>{status.status}</span></div>
      {status.status === "cancelled" ? <div className="discovery-alert warning">Scan cancelled. Any results found before cancellation remain reviewable.</div> : null}
      {status.status === "bounded" ? <div className="discovery-alert warning">The configured safety bound stopped this scan{status.limitReason ? `: ${status.limitReason}` : "."} Partial results remain reviewable.</div> : null}
      {status.status === "timed_out" ? <div className="discovery-alert warning">The configured timeout stopped this scan. Partial results remain reviewable.</div> : null}
      {status.status === "failed" ? <div className="discovery-alert error" role="alert">{status.error ?? "The scan stopped with an error."}</div> : null}
      <div className="discovery-result-tools">
        <label className="discovery-search"><Search size={14} aria-hidden /><span className="sr-only">Search discovered repositories</span><input type="search" value={query} onChange={(event) => onQuery(event.currentTarget.value)} placeholder="Search name, path, branch" aria-label="Search discovered repositories" /></label>
        <label>Sort <select value={sort} onChange={(event) => onSort(event.currentTarget.value as ResultSort)}><option value="name">Name</option><option value="path">Path</option><option value="type">Repository type</option><option value="modified">Last modified</option></select></label>
      </div>
      <div className="discovery-filter-row" role="group" aria-label="Discovery filters">{resultFilters.map((item) => <button key={item.value} type="button" className={filter === item.value ? "active" : ""} aria-pressed={filter === item.value} onClick={() => onFilter(item.value)}>{item.label}</button>)}</div>
      <div className="discovery-selection-bar"><span>{results.length} visible · {selectedVisibleCount} visible selected · {selectedCount} total selected</span><div><button type="button" onClick={onSelectVisible} disabled={results.length === 0}>Select all visible</button><button type="button" onClick={onClear} disabled={selectedCount === 0}>Clear selection</button><button type="button" onClick={() => onShowHidden(!showHidden)} aria-pressed={showHidden}>{showHidden ? <EyeOff size={14} aria-hidden /> : <Eye size={14} aria-hidden />}{showHidden ? "Hide hidden discoveries" : "Show hidden discoveries"}</button></div></div>
      <div className="discovery-results" aria-live="polite">
        {results.length === 0 ? <p className="discovery-empty">No discoveries match the current filters.</p> : results.map((result) => <DiscoveryResultCard key={result.id} result={result} selected={selectedIds.has(result.id)} onToggle={() => onToggle(result.id)} />)}
      </div>
      {discoveryRoots.length ? <details className="discovery-root-rescans"><summary>Rescan one root</summary>{discoveryRoots.map((root) => <button type="button" key={root} disabled={busy} onClick={() => onRescanRoot(root)}>Rescan root: <code>{root}</code></button>)}</details> : null}
      <div className="discovery-result-actions"><div><button type="button" onClick={onConfigure}>Scan settings</button><button type="button" onClick={onRescan} disabled={busy}><RefreshCw size={14} aria-hidden /> Rescan</button><button type="button" onClick={onAddManually}>Add manually</button><button type="button" onClick={onCancel}>Cancel</button></div><div><button type="button" disabled={!selectedShown || busy} onClick={onHide}>Hide selected</button><button type="button" disabled={!selectedHidden || busy} onClick={onUnhide}>Unhide selected</button><button className="primary" type="button" aria-label="Add selected" disabled={addableCount === 0 || busy} onClick={onAdd}>Add selected{addableCount ? ` (${addableCount})` : ""}</button></div></div>
    </section>
  );
}

function DiscoveryResultCard({ result, selected, onToggle }: { result: DiscoveredRepository; selected: boolean; onToggle: () => void }) {
  return (
    <article className={`discovery-result${selected ? " selected" : ""}${(result.warnings ?? []).length ? " warning" : ""}${!result.accessible ? " inaccessible" : ""}`} data-testid={`discovery-result-${result.id}`}>
      <label className="discovery-result-select"><input type="checkbox" checked={selected} onChange={onToggle} aria-label={`Select ${result.name} at ${result.path}`} /><span><strong>{result.name}</strong><code>{result.path}</code></span></label>
      <div className="discovery-badges"><span>{result.type}</span><span className={result.state}>{result.state}</span>{result.alreadyRegistered ? <span className="registered">Already registered</span> : null}{result.hidden ? <span>Hidden</span> : null}{!result.accessible ? <span className="error">Inaccessible</span> : null}</div>
      <dl className="discovery-result-facts"><div><dt>Branch</dt><dd>{result.branch || "detached / unknown"}</dd></div><div><dt>HEAD</dt><dd>{result.head || "unknown"}</dd></div><div><dt>Worktree relationship</dt><dd>{result.worktreeOf || "none"}</dd></div><div><dt>Last modified</dt><dd>{formatDate(result.lastModified)}</dd></div><div><dt>Discovery root</dt><dd><code>{result.discoveryRoot}</code></dd></div><div><dt>Access</dt><dd>{result.accessible ? "accessible" : "inaccessible"}</dd></div></dl>
      {(result.warnings ?? []).length ? <ul className="discovery-result-warnings">{(result.warnings ?? []).map((warning) => <li key={warning}>{warning}</li>)}</ul> : null}
    </article>
  );
}

function RegistrationConfirmation({ forms, busy, onChange, onRemove, onBack, onCancel, onSubmit }: { forms: RegistrationForm[]; busy: boolean; onChange: (id: string, update: Partial<RegistrationForm>) => void; onRemove: (id: string) => void; onBack: () => void; onCancel: () => void; onSubmit: () => void }) {
  return (
    <section className="discovery-step" aria-labelledby="discovery-add-title"><p className="eyebrow">Final approval</p><h3 id="discovery-add-title">Confirm repositories to add</h3><p className="discovery-lede">Only the exact repositories below will be registered. Edit display metadata before confirming.</p>
      <div className="discovery-registration-list">{forms.map((form) => <fieldset key={form.id}><legend>{form.originalName}</legend><code className="discovery-registration-path">{form.path}</code><div className="discovery-registration-grid"><label>{form.originalName} display name<input value={form.name} onChange={(event) => onChange(form.id, { name: event.currentTarget.value })} required /></label><label>{form.originalName} group or project<input value={form.group} onChange={(event) => onChange(form.id, { group: event.currentTarget.value })} /></label><label>{form.originalName} tags<input value={form.tagsText} onChange={(event) => onChange(form.id, { tagsText: event.currentTarget.value })} placeholder="comma separated" /></label><label>{form.originalName} color<input value={form.color} onChange={(event) => onChange(form.id, { color: event.currentTarget.value })} placeholder="optional" /></label></div><label className="discovery-check"><input type="checkbox" checked={form.enabled} onChange={(event) => onChange(form.id, { enabled: event.currentTarget.checked })} /> {form.originalName} enabled</label><button className="discovery-remove-draft" type="button" onClick={() => onRemove(form.id)}>Remove from this batch</button></fieldset>)}</div>
      {forms.length === 0 ? <p className="discovery-empty">No repositories remain in this batch.</p> : null}
      <div className="discovery-actions"><button type="button" onClick={onBack}><ChevronLeft size={14} aria-hidden /> Back</button><span><button type="button" onClick={onCancel}>Cancel</button> <button className="primary" type="button" onClick={onSubmit} disabled={busy || forms.length === 0 || forms.some((form) => !form.name.trim())}>{busy ? "Adding…" : "Confirm and add selected"}</button></span></div>
    </section>
  );
}

function RegistrationOutcome({ forms, results, onBack, onDone }: { forms: RegistrationForm[]; results: DiscoveryRegistrationResult[]; onBack: () => void; onDone: () => void }) {
  const byId = new Map(forms.map((form) => [form.id, form]));
  return (
    <section className="discovery-step" aria-labelledby="discovery-outcome-title"><p className="eyebrow">Registration result</p><h3 id="discovery-outcome-title">Approved repositories processed</h3><div className="discovery-outcomes">{results.map((result) => <article key={result.id} className={result.status === "failed" ? "failed" : "success"}>{result.status === "failed" ? <AlertTriangle size={17} aria-hidden /> : <Check size={17} aria-hidden />}<div><strong>{byId.get(result.id)?.name ?? result.id}</strong><span>{result.status.replace("_", " ")}</span>{result.error ? <p>{result.error}</p> : null}</div></article>)}</div><p className="discovery-lede">A failed repository does not roll back repositories that were added successfully.</p><div className="discovery-actions"><button type="button" onClick={onBack}>Back to results</button><button className="primary" type="button" onClick={onDone}>Done</button></div></section>
  );
}

function NumberSetting({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) {
  return <label>{label}<input type="number" min="1" step="1" value={value} onChange={(event) => onChange(event.currentTarget.valueAsNumber)} /></label>;
}

function ProgressFact({ label, value }: { label: string; value: string | number }) {
  return <div><dt>{label}</dt><dd>{typeof value === "number" ? value.toLocaleString() : value}</dd></div>;
}

function LoadingState({ label }: { label: string }) {
  return <div className="discovery-loading" role="status"><LoaderCircle className="spin" size={20} aria-hidden /> {label}</div>;
}

function compareResults(left: DiscoveredRepository, right: DiscoveredRepository, sort: ResultSort): number {
  let a = "";
  let b = "";
  switch (sort) {
    case "path": a = left.path; b = right.path; break;
    case "type": a = left.type; b = right.type; break;
    case "modified": a = left.lastModified ?? ""; b = right.lastModified ?? ""; break;
    default: a = left.name; b = right.name;
  }
  const direction = sort === "modified" ? -1 : 1;
  return direction * a.localeCompare(b) || left.path.localeCompare(right.path);
}

function splitTags(value: string): string[] {
  return [...new Set(value.split(",").map((tag) => tag.trim()).filter(Boolean))].sort();
}

function formatDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "0s";
  if (milliseconds < 1000) return `${Math.round(milliseconds)}ms`;
  return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 1 : 0)}s`;
}

function formatDate(value?: string): string {
  if (!value) return "unknown";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "unknown" : date.toLocaleString();
}

function errorMessage(cause: unknown, action: string): string {
  const detail = cause instanceof Error ? cause.message : String(cause);
  return detail.trim() ? `Could not finish ${action}: ${detail}` : `Could not finish ${action}.`;
}

function focusableElements(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return [...root.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled]), select:not([disabled]), summary, [href], [tabindex]:not([tabindex='-1'])")].filter((element) => element.getClientRects().length > 0);
}
