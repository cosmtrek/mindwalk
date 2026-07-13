import { ClipboardCheck, X } from "lucide-react";
import { useEffect, useState } from "react";
import { compareSessions, getSessionReview } from "../api/client";
import type { SessionComparison, SessionMeta, SessionReview } from "../types";

export function ReviewCenter({ sessions, activeKey }: { sessions: SessionMeta[]; activeKey?: string }) {
  const [open, setOpen] = useState(false);
  const [review, setReview] = useState<SessionReview>();
  const [comparison, setComparison] = useState<SessionComparison>();
  const [right, setRight] = useState("");

  useEffect(() => {
    if (!open || !activeKey) return;
    void getSessionReview(activeKey).then(setReview).catch(() => setReview(undefined));
  }, [activeKey, open]);

  return (
    <div className="review-center">
      <button className="memory-toggle" onClick={() => setOpen((value) => !value)} aria-expanded={open}>
        <ClipboardCheck size={14} aria-hidden /> Review
      </button>
      {open ? (
        <section className="review-panel" aria-label="Review center">
          <header>
            <div><p className="eyebrow">Review center</p><strong>{review?.sessionId || "No session"}</strong></div>
            <button className="icon-btn" onClick={() => setOpen(false)} aria-label="Close review center"><X size={14} /></button>
          </header>
          {review ? (
            <>
              <div className="review-metrics">
                <span>{review.errors} errors</span><span>{review.verifications} verifies</span>
                <span>{review.editsAfterLastVerify} unverified edits</span><span>{review.scopeDriftTouches} scope drift</span>
              </div>
              <ul>{review.flags.map((flag) => <li key={flag}>{flag}</li>)}</ul>
              {activeKey ? <a href={`/api/sessions/${encodeURIComponent(activeKey)}/review?format=markdown`}>Export redacted Markdown</a> : null}
            </>
          ) : <p className="muted">Review data unavailable.</p>}
          {activeKey && sessions.length > 1 ? (
            <div className="compare-control">
              <label htmlFor="compare-session">Compare with</label>
              <select id="compare-session" value={right} onChange={(event) => {
                const value = event.currentTarget.value;
                setRight(value);
                setComparison(undefined);
                if (value) void compareSessions(activeKey, value).then(setComparison);
              }}>
                <option value="">Choose a session</option>
                {sessions.filter((session) => session.key !== activeKey).map((session) => <option key={session.key} value={session.key}>{session.title || session.id}</option>)}
              </select>
            </div>
          ) : null}
          {comparison ? (
            <div className="comparison-result" data-testid="comparison-result">
              <span>{comparison.sharedFiles.length} shared</span>
              <span>{comparison.onlyLeft.length} only left</span>
              <span>{comparison.onlyRight.length} only right</span>
              <p>{comparison.memoryStatus} — {comparison.memoryNote}</p>
            </div>
          ) : null}
        </section>
      ) : null}
    </div>
  );
}
