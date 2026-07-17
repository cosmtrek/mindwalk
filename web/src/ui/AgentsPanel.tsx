import { AlertTriangle, Bot, Circle, Info, Loader, RefreshCw, X } from "lucide-react";
import { Fragment, useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
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

type AgentDetailState = {
  agentID: string;
  mode: "preview" | "pinned";
  anchor: HTMLElement;
} | null;

interface PopoverPosition {
  left: number;
  top: number;
  width: number;
  maxHeight: number;
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
  const [detail, setDetail] = useState<AgentDetailState>(null);
  const detailAgent = graph?.agents.find((agent) => agent.id === detail?.agentID);

  useEffect(() => {
    if (detail && graph && !detailAgent) setDetail(null);
  }, [detail, detailAgent, graph]);

  const closePanel = () => {
    setDetail(null);
    onClose();
  };

  return (
    <div className="dock-body agents-panel" aria-label="Agent lenses">
      <div className="inspector-head">
        <div>
          <div className="inspector-path">Agents</div>
          <div className="agents-head-note">Choose one trace at a time</div>
        </div>
        <button className="icon-btn" onClick={closePanel} title="Close" aria-label="Close agents">
          <X size={15} />
        </button>
      </div>

      {graphError ? (
        <AgentError error={graphError} locked={locked} onRetry={onRetry} />
      ) : null}

      <div className="agent-list" aria-busy={loading || loadingAgentID !== undefined}>
        <button
          className={
            current === null ? "agent-row agent-row-select active" : "agent-row agent-row-select"
          }
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
                detailMode={detail?.agentID === agent.id ? detail.mode : undefined}
                onPreview={(anchor) =>
                  setDetail((currentDetail) =>
                    currentDetail?.mode === "pinned"
                      ? currentDetail
                      : { agentID: agent.id, mode: "preview", anchor }
                  )
                }
                onPreviewEnd={(agentID) =>
                  setDetail((currentDetail) =>
                    currentDetail?.agentID === agentID && currentDetail.mode === "preview"
                      ? null
                      : currentDetail
                  )
                }
                onToggleDetails={(anchor) =>
                  setDetail((currentDetail) =>
                    currentDetail?.agentID === agent.id && currentDetail.mode === "pinned"
                      ? null
                      : { agentID: agent.id, mode: "pinned", anchor }
                  )
                }
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

      {detail && detailAgent ? (
        <AgentDetailPopover agent={detailAgent} state={detail} onClose={() => setDetail(null)} />
      ) : null}
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
  onSelect,
  detailMode,
  onPreview,
  onPreviewEnd,
  onToggleDetails
}: {
  agent: AgentNode;
  current: boolean;
  loading: boolean;
  locked: boolean;
  onSelect: (agentID: string | null) => void;
  detailMode?: "preview" | "pinned";
  onPreview: (anchor: HTMLElement) => void;
  onPreviewEnd: (agentID: string) => void;
  onToggleDetails: (anchor: HTMLElement) => void;
}) {
  const available = agent.traceAvailability === "available";
  const status = agentStatus(agent, loading);
  const secondary = [agent.role, agent.instructionPreview].filter(Boolean).join(" · ");
  const disabled = !available || locked;

  return (
    <div
      className={["agent-row", current ? "active" : "", disabled ? "disabled" : ""]
        .filter(Boolean)
        .join(" ")}
      onMouseEnter={(event) => onPreview(event.currentTarget)}
      onMouseLeave={() => onPreviewEnd(agent.id)}
      onFocus={(event) => onPreview(event.currentTarget)}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          onPreviewEnd(agent.id);
        }
      }}
    >
      <button
        className="agent-row-select"
        style={{ paddingLeft: `${12 + Math.min(agent.depth, 4) * 14}px` }}
        aria-pressed={current}
        disabled={disabled}
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
            <span className={`agent-row-count agent-status-${agent.traceAvailability}`}>
              {status}
            </span>
          </span>
          <span className="agent-row-secondary">{secondary || "No launch details"}</span>
        </span>
      </button>
      <button
        className="agent-row-detail-trigger"
        aria-label={`${detailMode === "pinned" ? "Unpin" : "Pin"} details for ${agent.label}`}
        aria-pressed={detailMode === "pinned"}
        onClick={(event) => {
          event.stopPropagation();
          onToggleDetails(event.currentTarget.closest(".agent-row") as HTMLElement);
        }}
      >
        <Info size={12} aria-hidden />
      </button>
    </div>
  );
}

function AgentDetailPopover({
  agent,
  state,
  onClose
}: {
  agent: AgentNode;
  state: NonNullable<AgentDetailState>;
  onClose: () => void;
}) {
  const margin = 12;
  const popoverRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<PopoverPosition>({
    left: margin,
    top: margin,
    width: Math.min(520, window.innerWidth - margin * 2),
    maxHeight: window.innerHeight - margin * 2
  });

  useLayoutEffect(() => {
    const updatePosition = () => {
      const gap = 12;
      const width = Math.min(520, window.innerWidth - margin * 2);
      const anchorRect = state.anchor.getBoundingClientRect();
      const leftCandidate = anchorRect.left - width - gap;
      const rightCandidate = anchorRect.right + gap;
      const left =
        leftCandidate >= margin
          ? leftCandidate
          : rightCandidate + width <= window.innerWidth - margin
            ? rightCandidate
            : Math.min(
                Math.max(anchorRect.left, margin),
                window.innerWidth - width - margin
              );
      const maxHeight = window.innerHeight - margin * 2;
      const measuredHeight = Math.min(popoverRef.current?.offsetHeight ?? maxHeight, maxHeight);
      const top = Math.min(
        Math.max(anchorRect.top, margin),
        window.innerHeight - measuredHeight - margin
      );
      setPosition({ left, top, width, maxHeight });
    };

    const animatedPanel = state.anchor.closest(".dock-panel");
    updatePosition();
    window.addEventListener("resize", updatePosition);
    window.addEventListener("scroll", updatePosition, true);
    animatedPanel?.addEventListener("animationend", updatePosition);
    return () => {
      window.removeEventListener("resize", updatePosition);
      window.removeEventListener("scroll", updatePosition, true);
      animatedPanel?.removeEventListener("animationend", updatePosition);
    };
  }, [state.anchor]);

  useEffect(() => {
    if (state.mode !== "pinned") return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (popoverRef.current?.contains(target) || state.anchor.contains(target)) return;
      onClose();
    };

    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("pointerdown", handlePointerDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("pointerdown", handlePointerDown);
    };
  }, [onClose, state.anchor, state.mode]);

  return createPortal(
    <div
      ref={popoverRef}
      className={`agent-detail-popover ${state.mode}`}
      role={state.mode === "preview" ? "tooltip" : "dialog"}
      aria-label={`${agent.label} details`}
      style={position}
    >
      <div className="agent-detail-head">
        <span>
          <strong>{agent.label}</strong>
          {agent.role ? <span>{agent.role}</span> : null}
        </span>
        {state.mode === "pinned" ? (
          <button className="agent-detail-close" onClick={onClose} aria-label="Close details">
            <X size={14} aria-hidden />
          </button>
        ) : null}
      </div>
      {agent.instructionPreview ? (
        <div className="agent-detail-instruction">{agent.instructionPreview}</div>
      ) : null}
      <div className="agent-detail-meta">{agentDetail(agent)}</div>
    </div>,
    document.body
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
