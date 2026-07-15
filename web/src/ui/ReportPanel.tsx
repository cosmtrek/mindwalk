import { useCallback, useState, type ReactNode } from "react";
import { AlertTriangle, RefreshCw, Sparkles, X } from "lucide-react";
import type { JudgeChoice, ReportDimension, ReportStatus, Severity, Verdict } from "../types";

interface ReportPanelProps {
  status?: ReportStatus;
  analyzing: boolean;
  locked: boolean;
  onAnalyze: (choice: JudgeChoice) => void;
  onClose: () => void;
  /** jump the playhead to an evidence seq and focus its file in the scene */
  onJumpTo: (seq: number) => void;
}

const DIMENSION_WORDS: Record<string, { title: string; hint: string }> = {
  exploration: { title: "Exploration", hint: "Did the agent build enough understanding before editing?" },
  scope: { title: "Scope", hint: "Does the footprint match what the task needed?" },
  wandering: { title: "Wandering", hint: "Purposeful path, or circles and dead ends?" },
  verification: { title: "Verification", hint: "Were edits verified, and errors followed up?" }
};

/** the mainstream models each judge CLI can be pinned to; "" keeps its default */
const JUDGE_MODELS: Record<string, { value: string; label: string }[]> = {
  claude: [
    { value: "", label: "default model" },
    { value: "sonnet", label: "sonnet" },
    { value: "opus", label: "opus" },
    { value: "fable", label: "fable" }
  ],
  codex: [
    { value: "", label: "default model" },
    { value: "gpt-5.6-sol", label: "gpt-5.6 sol" },
    { value: "gpt-5.6-terra", label: "gpt-5.6 terra" }
  ]
};

const JUDGE_CHOICE_KEY = "mindwalk:judge-choice";

function loadStoredChoice(): JudgeChoice {
  try {
    const raw = localStorage.getItem(JUDGE_CHOICE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<JudgeChoice>;
      return {
        cli: typeof parsed.cli === "string" ? parsed.cli : "",
        model: typeof parsed.model === "string" ? parsed.model : ""
      };
    }
  } catch {
    // corrupt storage reads as "no preference"
  }
  return { cli: "", model: "" };
}

/** clamp a stored choice to what is actually installed and offered */
function resolveChoice(choice: JudgeChoice, clis: string[]): JudgeChoice {
  const cli = clis.includes(choice.cli) ? choice.cli : (clis[0] ?? "");
  const models = JUDGE_MODELS[cli] ?? [];
  const model = models.some((m) => m.value === choice.model) ? choice.model : "";
  return { cli, model };
}

// dock panel content: the session evaluation. The Dock owns positioning;
// this owns only its own markup.
export function ReportPanel({ status, analyzing, locked, onAnalyze, onClose, onJumpTo }: ReportPanelProps) {
  // the judge choice persists across sessions and reloads; the picker shows
  // wherever a run can start (empty, failed, stale)
  const [storedChoice, setStoredChoice] = useState<JudgeChoice>(loadStoredChoice);
  const clis = status?.judgeClis ?? (status?.judgeCli ? [status.judgeCli] : []);
  const choice = resolveChoice(storedChoice, clis);
  const changeChoice = useCallback((next: JudgeChoice) => {
    setStoredChoice(next);
    try {
      localStorage.setItem(JUDGE_CHOICE_KEY, JSON.stringify(next));
    } catch {
      // storage full or unavailable — the selection still applies this session
    }
  }, []);
  const analyze = useCallback(() => onAnalyze(choice), [onAnalyze, choice]);

  return (
    <div className="dock-body" aria-label="Session evaluation">
      <div className="inspector-head">
        <div>
          <div className="inspector-path">Evaluation</div>
          {status?.report ? (
            <div className="report-meta">
              judged by {status.report.judge.cli}
              {status.report.judge.model ? ` · ${status.report.judge.model}` : ""} ·{" "}
              {day(status.report.judge.generatedAt)}
            </div>
          ) : null}
        </div>
        <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close evaluation">
          <X size={15} />
        </button>
      </div>
      <PanelBody
        status={status}
        analyzing={analyzing}
        locked={locked}
        analyze={analyze}
        onJumpTo={onJumpTo}
        picker={clis.length > 0 ? <JudgePicker clis={clis} choice={choice} onChange={changeChoice} /> : null}
      />
    </div>
  );
}

function JudgePicker({
  clis,
  choice,
  onChange
}: {
  clis: string[];
  choice: JudgeChoice;
  onChange: (choice: JudgeChoice) => void;
}) {
  const models = JUDGE_MODELS[choice.cli] ?? [{ value: "", label: "default model" }];
  return (
    <div className="report-picker">
      <select
        value={choice.cli}
        onChange={(e) => onChange({ cli: e.target.value, model: "" })}
        aria-label="Judge agent"
        title="Which agent CLI judges this session"
      >
        {clis.map((cli) => (
          <option key={cli} value={cli}>
            {cli}
          </option>
        ))}
      </select>
      <select
        value={choice.model}
        onChange={(e) => onChange({ ...choice, model: e.target.value })}
        aria-label="Judge model"
        title="Which model the judge runs on"
      >
        {models.map((model) => (
          <option key={model.value} value={model.value}>
            {model.label}
          </option>
        ))}
      </select>
    </div>
  );
}

function PanelBody({
  status,
  analyzing,
  locked,
  analyze,
  onJumpTo,
  picker
}: {
  status?: ReportStatus;
  analyzing: boolean;
  locked: boolean;
  analyze: () => void;
  onJumpTo: (seq: number) => void;
  picker: ReactNode;
}) {
  if (!status) {
    return <p className="report-note">Checking for an existing report…</p>;
  }
  if (status.state === "running" || analyzing) {
    return (
      <div className="report-note">
        <p className="report-running">Judging the trajectory…</p>
        <p>
          The judge reads the whole trace and writes evidence-anchored findings. This usually takes about a
          minute; you can keep exploring meanwhile.
        </p>
      </div>
    );
  }
  if (status.state === "failed") {
    return (
      <div className="report-note">
        <p className="report-error">
          <AlertTriangle size={13} /> Evaluation failed
        </p>
        <p className="report-error-detail">{status.error}</p>
        {picker}
        <button className="report-run" onClick={analyze}>
          <RefreshCw size={13} />
          Retry
        </button>
      </div>
    );
  }
  if (status.state === "none" || !status.report) {
    if (!status.judgeAvailable) {
      return (
        <p className="report-note">
          Evaluation needs a local agent CLI as judge. Install <code>claude</code> or <code>codex</code> and
          make it available on PATH.
        </p>
      );
    }
    return (
      <div className="report-note">
        <p>
          Ask an agent to evaluate this session: how it explored, whether the footprint matched the task,
          where it wandered, and how it verified its work. Every finding links back to the timeline.
        </p>
        {picker}
        <button className="report-run" onClick={analyze}>
          <Sparkles size={13} />
          Evaluate session
        </button>
        <p className="report-cost">
          Runs the selected CLI under your own account and sends it a summary of this session — task wording,
          file paths, event digests — for the model to read. About a minute.
        </p>
      </div>
    );
  }

  const report = status.report;
  return (
    <div className="report-body">
      {status.stale ? (
        <div className="report-stale">
          <span>Based on {report.session.eventCount} events — the session has grown since.</span>
          <div className="report-stale-actions">
            {picker}
            <button className="report-rerun" onClick={analyze} title="Re-evaluate with the current trace">
              <RefreshCw size={12} />
              Re-evaluate
            </button>
          </div>
        </div>
      ) : null}
      <p className="report-task">{report.taskSummary}</p>
      {report.dimensions.map((dimension) => (
        <Dimension key={dimension.name} dimension={dimension} locked={locked} onJumpTo={onJumpTo} />
      ))}
      {report.notableMoments?.length ? (
        <section className="report-section">
          <p className="eyebrow">Moments</p>
          {report.notableMoments.map((moment) => (
            <button
              key={moment.seq}
              className="report-moment"
              onClick={() => onJumpTo(moment.seq)}
              disabled={locked}
              title={`Jump to step ${moment.seq + 1}`}
            >
              <strong>#{moment.seq + 1}</strong>
              <span>{moment.note}</span>
            </button>
          ))}
        </section>
      ) : null}
      <section className="report-section">
        <p className="eyebrow">Narrative</p>
        <p className="report-narrative">{report.narrative}</p>
      </section>
      {!status.stale && picker ? (
        // a fresh report can still be re-judged — with a different agent or
        // model; the stale banner owns this row otherwise
        <section className="report-again">
          <div className="report-stale-actions">
            {picker}
            <button className="report-rerun" onClick={analyze} title="Run a fresh evaluation of this session">
              <RefreshCw size={12} />
              Re-evaluate
            </button>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function Dimension({
  dimension,
  locked,
  onJumpTo
}: {
  dimension: ReportDimension;
  locked: boolean;
  onJumpTo: (seq: number) => void;
}) {
  const words = DIMENSION_WORDS[dimension.name] ?? { title: dimension.name, hint: "" };
  return (
    <section className="report-dimension">
      <div className="report-dimension-head" data-hint={words.hint}>
        <span className="report-dimension-name">{words.title}</span>
        <span className={`verdict verdict-${dimension.verdict}`}>{verdictWord(dimension.verdict)}</span>
      </div>
      {dimension.findings.map((finding) => (
        <button
          key={`${finding.severity}|${finding.evidenceSeqs?.join(",")}|${finding.claim}`}
          className="report-finding"
          onClick={() => {
            const seq = finding.evidenceSeqs?.[0];
            if (seq !== undefined) onJumpTo(seq);
          }}
          disabled={locked || !finding.evidenceSeqs?.length}
          title={
            finding.evidenceSeqs?.length
              ? `Jump to step ${finding.evidenceSeqs[0] + 1} — evidence: ${finding.evidenceSeqs.map((seq) => `#${seq + 1}`).join(" ")}`
              : undefined
          }
        >
          <span className={`severity-dot ${severityClass(finding.severity)}`} />
          <span className="report-claim">{finding.claim}</span>
        </button>
      ))}
    </section>
  );
}

function verdictWord(verdict: Verdict): string {
  return verdict === "insufficient-data" ? "no signal" : verdict;
}

function severityClass(severity: Severity): string {
  return `sev-${severity}`;
}

function day(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString().slice(0, 10);
}
