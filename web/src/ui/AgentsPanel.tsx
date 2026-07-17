import { AlertTriangle, Bot, Circle, Loader, RefreshCw, X } from "lucide-react";
import { Fragment } from "react";
import type { AgentGraph, AgentNode } from "../types";

interface AgentsPanelProps {
  graph?: AgentGraph;
  current: string | null;
  loading: boolean;
  loadingAgentID?: string;
  locked?: boolean;
  error?: string;
  retryAgentID?: string | null;
  onSelect: (agentID: string | null) => void;
  onRetry: () => void;
  onClose: () => void;
}

export function AgentsPanel({
  graph,
  current,
  loading,
  loadingAgentID,
  locked = false,
  error,
  retryAgentID,
  onSelect,
  onRetry,
  onClose
}: AgentsPanelProps) {
  const children = graph?.agents.filter((agent) => agent.kind !== "main") ?? [];
  const main = graph?.agents.find((agent) => agent.kind === "main");
  const graphError = error && retryAgentID === null ? error : undefined;

  return (
    <div className="dock-body agents-panel" aria-label="Agent lenses">
      <div className="inspector-head">
        <div>
          <div className="inspector-path">Agents</div>
          <div className="agents-head-note">Choose one trace at a time</div>
        </div>
        <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close agents">
          <X size={15} />
        </button>
      </div>

      {graphError ? (
        <AgentError error={graphError} locked={locked} onRetry={onRetry} />
      ) : null}

      <div className="agent-list" aria-busy={loading || loadingAgentID !== undefined}>
        <button
          className={current === null ? "agent-row active" : "agent-row"}
          aria-pressed={current === null}
          disabled={locked}
          onClick={() => onSelect(null)}
        >
          <span className="agent-row-icon" aria-hidden>
            <Circle size={12} />
          </span>
          <span className="agent-row-copy">
            <span className="agent-row-primary">
              <span className="agent-row-title">
                Main
                {current === null ? <span className="agent-current">current</span> : null}
              </span>
              <span className="agent-row-count">{eventCount(main?.traceEventCount ?? 0)}</span>
            </span>
            <span className="agent-row-secondary">Root trace</span>
          </span>
        </button>

        {children.map((agent) => {
          const rowError = error && retryAgentID === agent.id ? error : undefined;
          return (
            <Fragment key={agent.id}>
              <AgentRow
                agent={agent}
                current={current === agent.id}
                loading={loadingAgentID === agent.id}
                locked={locked}
                onSelect={onSelect}
              />
              {rowError ? (
                <AgentError error={rowError} locked={locked} onRetry={onRetry} rowLocal />
              ) : null}
            </Fragment>
          );
        })}

        {graph && children.length === 0 ? <p className="agents-empty">No child agents found.</p> : null}
        {!graph && loading ? (
          <p className="agents-state" aria-live="polite">
            <Loader size={13} className="spin" aria-hidden />
            Loading agents…
          </p>
        ) : null}
      </div>

    </div>
  );
}

function AgentError({
  error,
  locked,
  onRetry,
  rowLocal = false
}: {
  error: string;
  locked: boolean;
  onRetry: () => void;
  rowLocal?: boolean;
}) {
  return (
    <div className={rowLocal ? "agents-error row-local" : "agents-error"} role="alert">
      <span>
        <AlertTriangle size={14} aria-hidden />
        {error}
      </span>
      <button className="agents-retry" onClick={onRetry} disabled={locked}>
        <RefreshCw size={13} aria-hidden />
        Retry
      </button>
    </div>
  );
}

function AgentRow({
  agent,
  current,
  loading,
  locked,
  onSelect
}: {
  agent: AgentNode;
  current: boolean;
  loading: boolean;
  locked: boolean;
  onSelect: (agentID: string | null) => void;
}) {
  const available = agent.traceAvailability === "available";
  const status = agentStatus(agent, loading);
  const secondary = [agent.role, agent.instructionPreview].filter(Boolean).join(" · ");

  return (
    <button
      className={current ? "agent-row active" : "agent-row"}
      style={{ paddingLeft: `${12 + Math.min(agent.depth, 4) * 14}px` }}
      aria-pressed={current}
      disabled={!available || locked}
      onClick={() => onSelect(agent.id)}
      aria-label={`${agent.label}, ${status}`}
    >
      <span className="agent-row-icon" aria-hidden>
        {loading ? <Loader size={13} className="spin" /> : <Bot size={13} />}
      </span>
      <span className="agent-row-copy">
        <span className="agent-row-primary">
          <span className="agent-row-title">
            {agent.label}
            {current ? <span className="agent-current">current</span> : null}
          </span>
          <span className={`agent-row-count agent-status-${agent.traceAvailability}`}>{status}</span>
        </span>
        <span className="agent-row-secondary">{secondary || "No launch details"}</span>
        <span className="agent-row-detail" role="tooltip">
          {agent.instructionPreview ? <span>{agent.instructionPreview}</span> : null}
          <span>{agentDetail(agent)}</span>
        </span>
      </span>
    </button>
  );
}

function agentStatus(agent: AgentNode, loading: boolean): string {
  if (loading) return "Loading trace…";
  if (agent.traceAvailability === "missing") return "Trace missing";
  if (agent.status === "failed") return "Launch failed · no trace";
  if (agent.traceAvailability === "unavailable") return "Trace unavailable";
  return eventCount(agent.traceEventCount);
}

function eventCount(count: number): string {
  return `${count} event${count === 1 ? "" : "s"}`;
}

function agentDetail(agent: AgentNode): string {
  return `Launch: ${agent.status} · Trace: ${agent.traceAvailability} · Correlation: ${agent.linkQuality} via ${agent.linkMethod}`;
}
