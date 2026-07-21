import { ChevronDown, RefreshCw, X } from "lucide-react";
import { useId, useMemo, useState } from "react";
import type {
  ErrorHealth,
  HealthSignalBase,
  ReadHealth,
  SessionHealth,
  SubagentHealth,
  VerificationHealth
} from "../types";

interface HealthPanelProps {
  health?: SessionHealth;
  loading: boolean;
  error?: string;
  onRetry: () => void;
  onClose: () => void;
}

type HealthRow = {
  id: keyof SessionHealth["signals"];
  title: string;
  signal: HealthSignalBase;
  description: string;
  counts: { label: string; value: number }[];
};

const PRIORITY = {
  unavailable: 0,
  estimated: 1,
  exact: 2,
  "not-applicable": 3
} as const;

export function HealthPanel({ health, loading, error, onRetry, onClose }: HealthPanelProps) {
  const [expanded, setExpanded] = useState<HealthRow["id"] | null>(null);
  const disclosureID = useId();
  const rows = useMemo(() => (health ? healthRows(health) : []), [health]);

  return (
    <section className="health-panel" aria-label="Trace health" aria-busy={loading}>
      <div className="health-head">
        <div>
          <div className="inspector-path">Trace health</div>
          <div className="health-head-note">What this trace can support</div>
        </div>
        <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close trace health">
          <X size={15} />
        </button>
      </div>

      {error ? (
        <div className="health-state health-error">
          <p role="alert">{error}</p>
          <p>Retry checks trace health only. Your map, timeline, and agent view stay as they are.</p>
          <button className="health-retry" onClick={onRetry} disabled={loading}>
            <RefreshCw size={13} />
            {loading ? "Retrying…" : "Retry trace health"}
          </button>
        </div>
      ) : loading && !health ? (
        <p className="health-state" role="status">Checking trace evidence…</p>
      ) : health ? (
        <div className="health-list">
          {loading ? <p className="health-refreshing" role="status">Refreshing evidence…</p> : null}
          {rows.map((row) => {
            const open = expanded === row.id;
            const explanationID = `${disclosureID}-${row.id}`;
            const state = healthState(row.signal);
            return (
              <section className="health-row" key={row.id}>
                <button
                  className="health-row-toggle"
                  aria-expanded={open}
                  aria-controls={explanationID}
                  onClick={() => setExpanded(open ? null : row.id)}
                >
                  <span className={`health-signal health-signal-${state}`} aria-hidden />
                  <span className="health-row-title">{row.title}</span>
                  <span className="health-row-state">{stateLabel(state)}</span>
                  <ChevronDown className="health-row-chevron" size={13} aria-hidden />
                </button>
                <p className="health-explanation" id={explanationID} hidden={!open}>
                  {row.description}
                </p>
                <details className="health-technical">
                  <summary>Technical details</summary>
                  <dl>
                    <div>
                      <dt>Reason</dt>
                      <dd><code>{row.signal.reason}</code></dd>
                    </div>
                    {row.counts.map((count) => (
                      <div key={count.label}>
                        <dt>{count.label}</dt>
                        <dd>{count.value}</dd>
                      </div>
                    ))}
                    <div>
                      <dt>Affects</dt>
                      <dd>{row.signal.affects.length ? row.signal.affects.join(", ") : "Nothing"}</dd>
                    </div>
                  </dl>
                </details>
              </section>
            );
          })}
        </div>
      ) : (
        <div className="health-state">
          <p>Trace health has not loaded.</p>
          <button className="health-retry" onClick={onRetry}>
            <RefreshCw size={13} />
            Load trace health
          </button>
        </div>
      )}
    </section>
  );
}

function healthRows(health: SessionHealth): HealthRow[] {
  const { reads, errors, verification, subagents } = health.signals;
  const rows: HealthRow[] = [
    {
      id: "reads",
      title: "File reads",
      signal: reads,
      description: readDescription(reads),
      counts: [
        { label: "Direct", value: reads.directCount },
        { label: "Inferred", value: reads.inferredCount }
      ]
    },
    {
      id: "errors",
      title: "Errors",
      signal: errors,
      description: errorDescription(errors),
      counts: [{ label: "Recognized", value: errors.recognizedCount }]
    },
    {
      id: "verification",
      title: "Verification",
      signal: verification,
      description: verificationDescription(verification),
      counts: [
        { label: "Recognized", value: verification.recognizedCount },
        { label: "Known results", value: verification.knownResultCount },
        { label: "Unknown results", value: verification.unknownResultCount },
        { label: "Edits after last check", value: verification.editsAfterLastVerify }
      ]
    },
    {
      id: "subagents",
      title: "Subagents",
      signal: subagents,
      description: subagentDescription(subagents),
      counts: [
        { label: "Exact links", value: subagents.exactCount },
        { label: "Derived links", value: subagents.derivedCount },
        { label: "Missing traces", value: subagents.missingTraceCount },
        { label: "Unavailable traces", value: subagents.unavailableTraceCount }
      ]
    }
  ];
  return rows.sort((a, b) => healthPriority(a.signal) - healthPriority(b.signal));
}

function healthState(signal: HealthSignalBase): keyof typeof PRIORITY {
  if (signal.availability === "failed" || signal.quality === "unavailable") return "unavailable";
  if (signal.availability === "not-applicable") return "not-applicable";
  if (signal.quality === "estimated") return "estimated";
  if (signal.quality === "exact") return "exact";
  return "unavailable";
}

function healthPriority(signal: HealthSignalBase): number {
  return PRIORITY[healthState(signal)];
}

function stateLabel(state: keyof typeof PRIORITY): string {
  switch (state) {
    case "unavailable":
      return "Limited";
    case "estimated":
      return "Estimated";
    case "exact":
      return "Exact";
    case "not-applicable":
      return "Not needed";
  }
}

function readDescription(signal: ReadHealth): string {
  if (signal.availability === "failed") return "Mindwalk could not inspect file-read evidence. Retry trace health to try again.";
  if (signal.quality === "unavailable") return "This log does not show which files the agent read.";
  if (signal.quality === "estimated") return `The trace records ${count(signal.directCount, "read")} directly; Mindwalk inferred ${count(signal.inferredCount, "read")} from shell activity.`;
  return `Structured trace targets record ${count(signal.directCount, "read")} directly.`;
}

function errorDescription(signal: ErrorHealth): string {
  if (signal.availability === "failed") return "Mindwalk could not inspect error evidence. Retry trace health to try again.";
  if (signal.quality === "unavailable") return "This log does not expose reliable error status.";
  if (signal.quality === "estimated") {
    return signal.recognizedCount === 0
      ? "No errors were recognized, but this log can hide failures inside tool output."
      : `Mindwalk recognized ${count(signal.recognizedCount, "error")} from output text; other failures may not be visible.`;
  }
  return signal.recognizedCount === 0
    ? "The trace records error status directly and reports no errors."
    : `Direct error status reports ${count(signal.recognizedCount, "error")}.`;
}

function verificationDescription(signal: VerificationHealth): string {
  if (signal.availability === "failed") return "Mindwalk could not inspect verification evidence. Retry trace health to try again.";
  if (signal.quality === "unavailable") return "This log does not show whether verification commands passed or failed.";
  if (signal.quality === "estimated") {
    return `${signal.knownResultCount} of ${signal.recognizedCount} recognized verification results are known; ${signal.unknownResultCount} could not be confirmed.`;
  }
  return `The trace recorded ${count(signal.recognizedCount, "verification result")} directly.`;
}

function subagentDescription(signal: SubagentHealth): string {
  if (signal.availability === "not-applicable") return "This session did not launch subagents.";
  if (signal.availability === "failed") return "Mindwalk could not load subagent evidence. Retry trace health to try again.";
  if (signal.quality === "unavailable") return "Subagent activity exists, but this log cannot link it to usable traces.";
  if (signal.quality === "estimated") return `Mindwalk linked ${count(signal.exactCount, "subagent trace")} directly and inferred ${count(signal.derivedCount, "link")} from trace context.`;
  return `Mindwalk linked ${count(signal.exactCount, "subagent trace")} directly.`;
}

function count(value: number, noun: string): string {
  return `${value} ${noun}${value === 1 ? "" : "s"}`;
}
