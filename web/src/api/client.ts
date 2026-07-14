import type {
  AgentProcess,
  CityMap,
  DiscoveredRepository,
  DiscoveredRepositoryRegistration,
  DiscoveryRegistrationResponse,
  DiscoveryRegistrationResult,
  MemorySearchResult,
  ObservableEventPage,
  RepositoryDiscoveryConfig,
  RepositoryDiscoveryStatus,
  RepositoryStatus,
  SessionComparison,
  SessionMeta,
  SessionReview,
  Trace
} from "../types";

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    const detail = (await res.text()).trim();
    throw new Error(detail || `${res.status} ${res.statusText}`);
  }
  return res.json() as Promise<T>;
}

async function mutateJSON<T>(url: string, method: string, body?: unknown): Promise<T> {
  const res = await fetch(url, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Mindwalk-CSRF": "1"
    },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!res.ok) {
    const detail = (await res.text()).trim();
    throw new Error(detail || `${res.status} ${res.statusText}`);
  }
  return res.json() as Promise<T>;
}

async function mutateOptionalJSON<T>(url: string, method: string, body?: unknown): Promise<T | undefined> {
  const res = await fetch(url, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Mindwalk-CSRF": "1"
    },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  const text = await res.text();
  if (!res.ok) throw new Error(text.trim() || `${res.status} ${res.statusText}`);
  return text.trim() ? (JSON.parse(text) as T) : undefined;
}

// raw fetch failures read like stack noise in the toast; translate the two
// failure shapes (server gone vs. server said no) into something actionable
export function describeError(err: unknown, doing: string): string {
  if (err instanceof TypeError) {
    return `Can't reach the mindwalk server while ${doing} — is it still running?`;
  }
  const detail = (err instanceof Error ? err.message : String(err)).trim();
  return detail ? `Couldn't finish ${doing}: ${detail}` : `Couldn't finish ${doing}`;
}

export function listSessions(fresh = false): Promise<SessionMeta[]> {
  return getJSON<SessionMeta[]>(fresh ? "/api/sessions?fresh=1" : "/api/sessions");
}

export function getTrace(key: string): Promise<Trace> {
  return getJSON<Trace>(`/api/sessions/${encodeURIComponent(key)}/trace`);
}

export function getCityMap(key: string): Promise<CityMap> {
  return getJSON<CityMap>(`/api/sessions/${encodeURIComponent(key)}/citymap`);
}

export function getSessionSnapshot(key: string): Promise<{ trace: Trace; city: CityMap }> {
  return getJSON<{ trace: Trace; city: CityMap }>(`/api/sessions/${encodeURIComponent(key)}/snapshot`);
}

export function getSessionEvents(key: string, after = 0, limit = 1000): Promise<ObservableEventPage> {
  return getJSON<ObservableEventPage>(
    `/api/sessions/${encodeURIComponent(key)}/events?after=${after}&limit=${limit}`
  );
}

export function getSessionAgents(key: string): Promise<AgentProcess[]> {
  return getJSON<AgentProcess[]>(`/api/sessions/${encodeURIComponent(key)}/agents`);
}

export function searchMemories(query: string, namespace = ""): Promise<MemorySearchResult[]> {
  const params = new URLSearchParams({ q: query });
  if (namespace) params.set("namespace", namespace);
  return getJSON<MemorySearchResult[]>(`/api/memories/search?${params}`);
}

export function getSessionReview(key: string): Promise<SessionReview> {
  return getJSON<SessionReview>(`/api/sessions/${encodeURIComponent(key)}/review`);
}

export function compareSessions(left: string, right: string): Promise<SessionComparison> {
  const params = new URLSearchParams({ left, right });
  return getJSON<SessionComparison>(`/api/compare?${params}`);
}

// backs the static full-repo map view: the citymap for a repo, with no session
// or trace attached. Without a repo path the server falls back to its
// configured RepoRoot (the `mindwalk map <repo>` case).
export function getRepoMap(repo?: string): Promise<CityMap> {
  const url = repo ? `/api/repomap?repo=${encodeURIComponent(repo)}` : "/api/repomap";
  return getJSON<CityMap>(url);
}

export function listRepositories(): Promise<RepositoryStatus[]> {
  return getJSON<RepositoryStatus[]>("/api/repositories");
}

export function addRepository(path: string, name?: string): Promise<RepositoryStatus> {
  return mutateJSON<RepositoryStatus>("/api/repositories", "POST", { path, name: name || undefined });
}

export function updateRepository(id: string, update: Partial<{ name: string; group: string; tags: string[]; color: string; enabled: boolean }>): Promise<RepositoryStatus> {
  return mutateJSON<RepositoryStatus>(`/api/repositories/${encodeURIComponent(id)}`, "PATCH", update);
}

export function getRepositoryCityMap(id: string): Promise<CityMap> {
  return getJSON<CityMap>(`/api/repositories/${encodeURIComponent(id)}/citymap`);
}

export function getRepositoryDiscoveryConfig(): Promise<RepositoryDiscoveryConfig> {
  return getJSON<RepositoryDiscoveryConfig>("/api/repository-discovery/config");
}

export function updateRepositoryDiscoveryConfig(
  config: RepositoryDiscoveryConfig
): Promise<RepositoryDiscoveryConfig | undefined> {
  return mutateOptionalJSON<RepositoryDiscoveryConfig>("/api/repository-discovery/config", "PUT", {
    approvedRoots: config.approvedRoots,
    customExclusions: config.customExclusions,
    options: config.options
  });
}

export function startRepositoryDiscovery(
  config: RepositoryDiscoveryConfig,
  roots: string[] = config.approvedRoots
): Promise<RepositoryDiscoveryStatus | undefined> {
  return mutateOptionalJSON<RepositoryDiscoveryStatus>("/api/repository-discovery/start", "POST", {
    roots,
    options: config.options
  });
}

export function getRepositoryDiscoveryStatus(): Promise<RepositoryDiscoveryStatus> {
  return getJSON<RepositoryDiscoveryStatus>("/api/repository-discovery/status");
}

export function cancelRepositoryDiscovery(): Promise<RepositoryDiscoveryStatus | undefined> {
  return mutateOptionalJSON<RepositoryDiscoveryStatus>("/api/repository-discovery/cancel", "POST");
}

export function getDiscoveredRepositories(showHidden = false): Promise<DiscoveredRepository[]> {
  return getJSON<DiscoveredRepository[]>(
    `/api/repository-discovery/results${showHidden ? "?showHidden=1" : ""}`
  );
}

export function setDiscoveredRepositoriesHidden(ids: string[], hidden: boolean): Promise<void> {
  return mutateOptionalJSON<void>("/api/repository-discovery/hide", "POST", { ids, hidden }).then(() => undefined);
}

export async function registerDiscoveredRepositories(
  repositories: DiscoveredRepositoryRegistration[]
): Promise<DiscoveryRegistrationResponse> {
  const response = await mutateJSON<DiscoveryRegistrationResponse | DiscoveryRegistrationResult[]>(
    "/api/repository-discovery/register",
    "POST",
    { repositories }
  );
  return Array.isArray(response) ? { results: response } : response;
}

export function forgetRepositoryDiscovery(): Promise<void> {
  return mutateOptionalJSON<void>("/api/repository-discovery/forget", "POST").then(() => undefined);
}

export function resetRepositoryDiscoveryExclusions(): Promise<RepositoryDiscoveryConfig | undefined> {
  return mutateOptionalJSON<RepositoryDiscoveryConfig>(
    "/api/repository-discovery/reset-exclusions",
    "POST"
  );
}
