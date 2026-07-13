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
}

export interface Repository {
  id: string;
  name: string;
  path: string;
  group?: string;
  tags?: string[];
  color?: string;
  enabled: boolean;
  addedAt: string;
}

export interface RepositoryStatus {
  repo: Repository;
  missing: boolean;
  invalidPath?: boolean;
  error?: string;
  git: {
    isGit: boolean;
    root?: string;
    branch?: string;
    commit?: string;
    dirty: boolean;
    remote?: string;
    worktrees?: { path: string; branch?: string }[];
  };
}

export type RepositoryScanState =
  | "idle"
  | "running"
  | "completed"
  | "cancelled"
  | "failed"
  | "bounded"
  | "timed_out";
export type DiscoveredRepositoryType = "repository" | "worktree" | "bare" | "broken";
export type RepositoryCleanState = "clean" | "dirty" | "unknown";
export type RepositoryAccessibility = "accessible" | "inaccessible" | "invalid";

export interface DiscoveryExclusion {
  id: string;
  label: string;
  path?: string;
  basename?: string;
  locked: boolean;
}

export interface DiscoverySummary {
  directoriesExamined: number;
  repositoriesFound: number;
  repositoriesSkipped: number;
  permissionErrors: number;
  elapsedMillis: number;
}

/** Owner-controlled repository scan preferences. The server remains the
 * authority for canonical roots, critical exclusions, and safe bounds. */
export interface RepositoryDiscoveryConfig {
  approvedRoots: string[];
  customExclusions: string[];
  suggestedRoots?: string[];
  homeRoot?: string;
  exclusions: DiscoveryExclusion[];
  options: {
    maxDepth: number;
    maxDirectories: number;
    maxResults: number;
    timeoutSeconds: number;
    findNested: boolean;
  };
  lastScanAt?: string;
  lastScanSummary?: DiscoverySummary;
}

export interface RepositoryDiscoveryStatus {
  scanId?: string;
  status: RepositoryScanState;
  currentRoot?: string;
  directoriesExamined: number;
  repositoriesFound: number;
  repositoriesSkipped: number;
  permissionErrors: number;
  elapsedMillis: number;
  startedAt?: string;
  finishedAt?: string;
  limitReason?: string;
  error?: string;
}

export interface DiscoveredRepository {
  id: string;
  name: string;
  path: string;
  type: DiscoveredRepositoryType;
  branch?: string;
  head?: string;
  state: RepositoryCleanState;
  worktreeOf?: string;
  alreadyRegistered: boolean;
  registeredRepositoryId?: string;
  lastModified?: string;
  accessible: boolean;
  warnings: string[];
  discoveryRoot: string;
  hidden: boolean;
}

export interface DiscoveredRepositoryRegistration {
  id: string;
  name: string;
  group: string;
  tags: string[];
  color: string;
  enabled: boolean;
}

export interface DiscoveryRegistrationResult {
  id: string;
  status: "added" | "already_registered" | "failed";
  repository?: RepositoryStatus;
  error?: string;
}

export interface DiscoveryRegistrationResponse {
  results: DiscoveryRegistrationResult[];
}

export interface EventProvenance {
  sourceType: string;
  sourceName?: string;
  sourceEventId?: string;
  rawEventHash?: string;
  quality: "exact" | "estimated" | "derived" | "unavailable" | "redacted";
  fieldQuality?: Record<string, string>;
  confidence?: number;
  explanation?: string;
}

export interface ObservableEvent {
  sequence: number;
  event: {
    schemaVersion: number;
    eventId: string;
    eventType: string;
    occurredAt: string;
    observedAt: string;
    seq: number;
    repoId?: string;
    sessionId?: string;
    attrs?: Record<string, string>;
    redactedFields?: string[];
    provenance: EventProvenance;
    normalizedEventHash: string;
  };
}

export interface ObservableEventPage {
  events: ObservableEvent[];
  latestSequence: number;
  truncated: boolean;
}

export interface AgentProcess {
  schemaVersion: number;
  id: string;
  sessionId: string;
  kind: string;
  parentAgentId?: string;
  relationshipQuality: string;
  lifecycle: "UNKNOWN";
  lifecycleQuality: string;
  controlCapability: "DISPLAY_ONLY";
  spawnObserved: boolean;
  tools: string[];
  files: string[];
  errors: number;
  verifications: number;
  provenance: EventProvenance;
}

export interface MemoryRecord {
  memoryId: string;
  recordId: string;
  namespace: string;
  title: string;
  body?: string;
  updatedAt: string;
  tombstoned: boolean;
  provenance: EventProvenance;
}

export interface MemorySearchResult {
  memory: MemoryRecord;
  rank: number;
}

export interface SessionReview {
  schemaVersion: number;
  sessionId: string;
  files: string[];
  editedFiles: string[];
  errors: number;
  verifications: number;
  churnFiles: string[];
  scopeDriftTouches: number;
  editsAfterLastVerify: number;
  agentProcesses: number;
  flags: string[];
  provenance: EventProvenance;
}

export interface SessionComparison {
  schemaVersion: number;
  left: SessionReview;
  right: SessionReview;
  sharedFiles: string[];
  onlyLeft: string[];
  onlyRight: string[];
  memoryStatus: "UNAVAILABLE";
  memoryNote: string;
  provenance: EventProvenance;
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

export interface ActionCounts {
  search: number;
  read: number;
  edit: number;
  exec: number;
  verify: number;
  other: number;
}
