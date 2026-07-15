export type Action = "search" | "read" | "edit" | "exec" | "verify" | "other";
export type Touch = "hit" | "read" | "edit";

/** the words the HUD legend uses for each touch state — every surface that
 * names a touch must speak this vocabulary, not the wire values */
export function touchWord(touch?: Touch): string {
  switch (touch) {
    case "hit":
      return "seen";
    case "read":
      return "read";
    case "edit":
      return "edited";
    default:
      return "unvisited";
  }
}

export interface SessionMeta {
  key: string;
  id: string;
  harness: string;
  title?: string;
  path: string;
  cwd?: string;
  model?: string;
  gitBranch?: string;
  startedAt?: string;
  endedAt?: string;
  eventCount: number;
  /** user-turn count; with eventCount, the badge's cheap staleness signal */
  userTurns?: number;
  /** evaluation state for the rail badge; absent when never evaluated */
  reportState?: "running" | "done" | "stale" | "failed";
}

export type AgentKind = "main" | "subagent";
export type AgentStatus = "main" | "launched" | "failed" | "unknown";
export type TraceAvailability = "available" | "missing" | "unavailable";
export type AgentLinkQuality = "exact" | "derived" | "unavailable";
export type AgentLinkMethod =
  | "root"
  | "codex-agent-id"
  | "codex-parent-thread-id"
  | "claude-tool-use-id"
  | "claude-subagents-directory"
  | "unavailable";

export interface AgentGraph {
  version: number;
  rootSessionKey: string;
  agents: AgentNode[];
}

export interface AgentNode {
  id: string;
  parentId?: string;
  depth: number;
  kind: AgentKind;
  label: string;
  role?: string;
  instructionPreview?: string;
  launchSeq?: number;
  launchCallId?: string;
  status: AgentStatus;
  traceAvailability: TraceAvailability;
  traceSessionKey?: string;
  traceEventCount: number;
  linkQuality: AgentLinkQuality;
  linkMethod: AgentLinkMethod;
}

export interface Rect {
  x: number;
  z: number;
  w: number;
  d: number;
}

export interface CityMap {
  version: number;
  repo: {
    root: string;
    commit?: string;
    dirty: boolean;
    generatedAt: string;
  };
  files: CityFile[];
  dirs: CityDir[];
  layout: {
    algorithm: string;
    weight: string;
  };
}

export interface CityFile {
  id: number;
  path: string;
  dir: string;
  lines: number;
  bytes: number;
  lang?: string;
  rect: Rect;
  ghost: boolean;
}

export interface CityDir {
  path: string;
  depth: number;
  rect: Rect;
  fileCount: number;
  lines: number;
}

export interface Trace {
  version: number;
  session: {
    id: string;
    harness: string;
    model?: string;
    title?: string;
    cwd?: string;
    commit?: string;
    startedAt?: string;
    endedAt?: string;
    eventCount: number;
    path?: string;
  };
  events: TraceEvent[];
  marks: Mark[];
  stats: Stats;
}

export interface TraceEvent {
  seq: number;
  ts?: string;
  tool: string;
  action: Action;
  targets: Target[];
  outside?: OutsideTouch[];
  resultBytes: number;
  isError: boolean;
  summary: string;
}

export interface Target {
  path: string;
  fileId?: number;
  touch: Touch;
  lines?: [number, number][];
  weak?: boolean;
}

export interface OutsideTouch {
  scope: "home" | "tmp" | "other";
  path: string;
}

export interface Mark {
  seq: number;
  type: "compaction" | "user-message" | "subagent";
  note?: string;
}

export interface Stats {
  filesInRepo: number;
  fovea: number;
  parafovea: number;
  edited: number;
  eventsBeforeFirstEdit: number;
  regressionRate: number;
  errorRate: number;
  actions: ActionCounts;
  errors: ActionCounts;
  maxEditsPerFile: number;
  /** files edited in three or more events */
  churnFiles: number;
  userTurns: number;
  compactions: number;
  subagents: number;
  resultBytes: number;
  /** edit events after the last verify event; every edit event when the session never verified */
  editsAfterLastVerify: number;
  observability: Observability;
}

/**
 * Grades each derived metric's source signal: "exact" when the harness
 * records it structurally, "estimated" when inferred from command or output
 * text, "unavailable" when the log carries no usable signal.
 */
export interface Observability {
  reads: MetricObservability;
  errors: MetricObservability;
}

export type MetricObservability = "exact" | "estimated" | "unavailable";

/** LLM-assisted evaluation of one session; verdicts are server-derived
 * from finding severities, never decided by the judge itself */
export interface Report {
  version: number;
  session: {
    id: string;
    harness: string;
    model?: string;
    /** event count the report was generated from — the staleness signal */
    eventCount: number;
  };
  judge: {
    cli: string;
    /** the LLM that actually judged, as reported by the CLI itself */
    model?: string;
    promptVersion: number;
    generatedAt: string;
  };
  taskSummary: string;
  dimensions: ReportDimension[];
  notableMoments?: ReportMoment[];
  narrative: string;
}

export type Verdict = "good" | "warning" | "problem" | "insufficient-data";
export type Severity = "info" | "warning" | "problem";
export type DimensionName = "exploration" | "scope" | "wandering" | "verification";

export interface ReportDimension {
  name: DimensionName;
  verdict: Verdict;
  findings: ReportFinding[];
}

export interface ReportFinding {
  claim: string;
  severity: Severity;
  evidenceSeqs?: number[];
}

export interface ReportMoment {
  seq: number;
  note: string;
}

export interface ReportStatus {
  state: "none" | "running" | "done" | "failed";
  /** done, but generated from fewer events than the trace now has */
  stale: boolean;
  report?: Report;
  error?: string;
  judgeAvailable: boolean;
  /** default judge (first available) */
  judgeCli?: string;
  /** every installed judge CLI, preference order first */
  judgeClis?: string[];
}

/** the panel's judge selection, sent with the analyze request */
export interface JudgeChoice {
  cli: string;
  /** empty string keeps the CLI's default model */
  model: string;
}

export interface ActionCounts {
  search: number;
  read: number;
  edit: number;
  exec: number;
  verify: number;
  other: number;
}
