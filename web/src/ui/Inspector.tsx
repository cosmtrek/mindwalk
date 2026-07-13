import { AlertTriangle, X } from "lucide-react";
import { touchWord, type CityFile, type ObservableEvent, type Touch, type TraceEvent } from "../types";

interface InspectorProps {
  file: CityFile;
  touch?: Touch;
  history: TraceEvent[];
	provenance?: ObservableEvent;
  onClose: () => void;
  onJumpTo: (seq: number) => void;
}

export function Inspector({ file, touch, history, provenance, onClose, onJumpTo }: InspectorProps) {
  const slash = file.path.lastIndexOf("/");
  const dir = slash >= 0 ? file.path.slice(0, slash + 1) : "";
  const name = slash >= 0 ? file.path.slice(slash + 1) : file.path;

  return (
    <aside className="inspector" aria-label={`File ${file.path}`}>
      <div className="inspector-head">
        <div>
          <div className="inspector-path">
            <span className="dir">{dir}</span>
            {name}
          </div>
          {file.ghost ? <span className="ghost-badge">ghost — not in this tree</span> : null}
        </div>
        <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close inspector">
          <X size={15} />
        </button>
      </div>
      <dl className="inspector-facts">
        <div>
          <dt>Touch</dt>
          <dd className={touch ? `touch-${touch}` : undefined}>{touchWord(touch)}</dd>
        </div>
        <div>
          <dt>Lang</dt>
          <dd>{file.lang || "text"}</dd>
        </div>
        <div>
          <dt>Lines</dt>
          <dd>{file.lines.toLocaleString()}</dd>
        </div>
        <div>
          <dt>Bytes</dt>
          <dd>{file.bytes.toLocaleString()}</dd>
        </div>
      </dl>
		<section className="provenance-card" data-testid="provenance-inspector">
			<p className="eyebrow">Provenance</p>
			{provenance ? (
				<dl className="provenance-facts">
					<div><dt>Event</dt><dd title={provenance.event.eventId}>{provenance.event.eventType}</dd></div>
					<div><dt>Source</dt><dd>{provenance.event.provenance.sourceName || provenance.event.provenance.sourceType}</dd></div>
					<div><dt>Quality</dt><dd>{provenance.event.provenance.quality}</dd></div>
					<div><dt>Ledger</dt><dd>#{provenance.sequence}</dd></div>
					{provenance.event.provenance.explanation ? (
						<div className="provenance-explanation"><dt>Basis</dt><dd>{provenance.event.provenance.explanation}</dd></div>
					) : null}
					{provenance.event.redactedFields?.length ? (
						<div className="provenance-explanation"><dt>Redacted</dt><dd>{provenance.event.redactedFields.join(", ")}</dd></div>
					) : null}
				</dl>
			) : <p className="muted">UNAVAILABLE — no canonical event is associated with this file.</p>}
		</section>
      <section>
        <p className="eyebrow">Visits · {history.length}</p>
        <div className="history-list">
          {history
            .slice(-14)
            .reverse()
            .map((event) => (
              <button
                key={event.seq}
                className="history-row"
                onClick={() => onJumpTo(event.seq)}
                title={`Jump to step ${event.seq + 1} — ${event.summary}`}
              >
                <span className={`action-dot ${event.action}`} />
                <strong>#{event.seq + 1}</strong>
                <span>{event.tool}</span>
                <span className="history-time">{event.ts ? clock(event.ts) : ""}</span>
                {event.isError ? <AlertTriangle className="history-err" size={13} /> : <span />}
              </button>
            ))}
          {history.length === 0 ? (
            <p className="muted">Not visited yet at this point of the walk. Scrub the timeline forward.</p>
          ) : null}
        </div>
      </section>
    </aside>
  );
}

function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}
