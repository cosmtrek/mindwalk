import { AlertTriangle, X } from "lucide-react";
import { touchWord, type CityFile, type Touch, type TraceEvent } from "../types";

interface InspectorProps {
  /** absent when nothing is selected yet — renders the teaching empty state */
  file?: CityFile;
  touch?: Touch;
  history: TraceEvent[];
  onClose: () => void;
  onJumpTo: (seq: number) => void;
  locked?: boolean;
}

// dock panel content: the selected file's identity, touch state, and visit
// history. The Dock owns positioning; this owns only its own markup.
export function Inspector({
  file,
  touch,
  history,
  onClose,
  onJumpTo,
  locked = false
}: InspectorProps) {
  if (!file) {
    return (
      <div className="dock-body" aria-label="File inspector">
        <div className="inspector-head">
          <div className="inspector-path">Inspect</div>
          <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close inspector">
            <X size={15} />
          </button>
        </div>
        <p className="dock-note">Click a building in the scene to inspect a file — its touch state, size, and every visit the agent paid it.</p>
      </div>
    );
  }
  const slash = file.path.lastIndexOf("/");
  const dir = slash >= 0 ? file.path.slice(0, slash + 1) : "";
  const name = slash >= 0 ? file.path.slice(slash + 1) : file.path;

  return (
    <div className="dock-body" aria-label={`File ${file.path}`}>
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
      <section className="inspector-history">
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
                disabled={locked}
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
    </div>
  );
}

function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}
