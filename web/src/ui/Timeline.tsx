import { Loader, Pause, Play, RotateCcw, StepBack, StepForward, Video } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Action, Mark, Trace, TraceEvent } from "../types";

interface TimelineProps {
  trace?: Trace;
  currentSeq: number;
  onChange: (seq: number) => void;
  onExport?: () => void;
  exporting?: boolean;
}

const BUCKETS = 160;
const BASE_TICK_MS = 340;
const SPEEDS = [1, 4, 16] as const;
type Speed = (typeof SPEEDS)[number];

interface Bucket {
  count: number;
  dominant: Action;
}

// marks that land on the same strip pixel collapse into one glyph; the title
// carries the count so dense sessions stay legible
interface MarkGroup {
  type: Mark["type"];
  seq: number;
  pos: number;
  count: number;
  note?: string;
}

const MARK_SLOTS = 220;

const MARK_LABEL: Record<Mark["type"], string> = {
  compaction: "context compaction",
  "user-message": "user message",
  subagent: "subagent"
};

const STRIP_ACTIONS: Action[] = ["search", "read", "edit", "verify", "exec"];

export function Timeline({ trace, currentSeq, onChange, onExport, exporting = false }: TimelineProps) {
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState<Speed>(1);
  const total = trace?.events.length ?? 0;
  const max = Math.max(0, total - 1);
  const seq = Math.min(currentSeq, max);
  const event = trace?.events[seq];

  useEffect(() => {
    setPlaying(false);
  }, [trace]);

  // while a video export is recording, the recorder owns the playhead — force
  // playback off so the Timeline timer can't fight it for currentSeq
  useEffect(() => {
    if (exporting) setPlaying(false);
  }, [exporting]);

  // the timer and shortcuts read position via refs so ticking doesn't tear them down
  const seqRef = useRef(seq);
  const maxRef = useRef(max);
  const playingRef = useRef(playing);
  seqRef.current = seq;
  maxRef.current = max;
  playingRef.current = playing;

  useEffect(() => {
    if (!playing || total === 0 || exporting) return;
    // higher speeds keep the render rate bounded by advancing several events per tick
    const interval = Math.max(85, BASE_TICK_MS / speed);
    const step = Math.max(1, Math.round((speed * interval) / BASE_TICK_MS));
    const timer = window.setInterval(() => {
      if (seqRef.current >= maxRef.current) {
        setPlaying(false);
        return;
      }
      onChange(Math.min(seqRef.current + step, maxRef.current));
    }, interval);
    return () => window.clearInterval(timer);
  }, [playing, speed, total, onChange, exporting]);

  const togglePlay = useCallback(() => {
    if (!playingRef.current && seqRef.current >= maxRef.current) onChange(0);
    setPlaying((v) => !v);
  }, [onChange]);

  const cycleSpeed = useCallback(() => {
    setSpeed((s) => SPEEDS[(SPEEDS.indexOf(s) + 1) % SPEEDS.length]);
  }, []);

  const step = useCallback(
    (delta: number) => {
      onChange(Math.min(maxRef.current, Math.max(0, seqRef.current + delta)));
    },
    [onChange]
  );

  const jumpEvent = useCallback(
    (dir: 1 | -1, pred: (e: TraceEvent) => boolean) => {
      if (!trace) return;
      for (let i = seqRef.current + dir; i >= 0 && i < trace.events.length; i += dir) {
        if (pred(trace.events[i])) {
          onChange(i);
          return;
        }
      }
    },
    [trace, onChange]
  );

  const markSeqs = useMemo(() => {
    const clamped = (trace?.marks ?? []).map((mark) => Math.min(mark.seq, Math.max(0, (trace?.events.length ?? 1) - 1)));
    return [...new Set(clamped)].sort((a, b) => a - b);
  }, [trace]);

  const jumpMark = useCallback(
    (dir: 1 | -1) => {
      const cur = seqRef.current;
      const next = dir === 1 ? markSeqs.find((s) => s > cur) : [...markSeqs].reverse().find((s) => s < cur);
      if (next !== undefined) onChange(next);
    },
    [markSeqs, onChange]
  );

  // playback shortcuts; scene and rail keep their own (⌘B lives in App).
  // suspended while exporting so a keypress can't move the recorded playhead.
  useEffect(() => {
    if (!trace || exporting) return;
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const target = e.target as HTMLElement | null;
      if (target?.closest("input, textarea, select, button, [contenteditable]")) return;
      switch (e.key) {
        case " ":
          e.preventDefault();
          togglePlay();
          return;
        case "ArrowLeft":
          e.preventDefault();
          step(e.shiftKey ? -10 : -1);
          return;
        case "ArrowRight":
          e.preventDefault();
          step(e.shiftKey ? 10 : 1);
          return;
        case "Home":
          e.preventDefault();
          onChange(0);
          return;
        case "End":
          e.preventDefault();
          onChange(maxRef.current);
          return;
      }
      switch (e.key.toLowerCase()) {
        case "s":
          cycleSpeed();
          return;
        case "e":
          jumpEvent(e.shiftKey ? -1 : 1, (ev) => ev.action === "edit");
          return;
        case "x":
          jumpEvent(e.shiftKey ? -1 : 1, (ev) => ev.isError);
          return;
        case "m":
          jumpMark(e.shiftKey ? -1 : 1);
          return;
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [trace, exporting, togglePlay, step, onChange, cycleSpeed, jumpEvent, jumpMark]);

  // transport + scrubber are inert with no trace, or while an export owns the playhead
  const locked = total === 0 || exporting;

  const buckets = useMemo<Bucket[]>(() => {
    if (!trace || total === 0) return [];
    const n = Math.min(BUCKETS, total);
    const out: Bucket[] = [];
    for (let b = 0; b < n; b++) {
      const from = Math.floor((b * total) / n);
      const to = Math.floor(((b + 1) * total) / n);
      const byAction = new Map<Action, number>();
      for (let i = from; i < to; i++) {
        const action = trace.events[i].action;
        byAction.set(action, (byAction.get(action) ?? 0) + 1);
      }
      let dominant: Action = "other";
      let best = -1;
      // edits are rare but load-bearing: let them win the bucket color on ties
      const priority: Action[] = ["edit", "verify", "read", "search", "exec", "other"];
      for (const action of priority) {
        const count = byAction.get(action) ?? 0;
        if (count > best) {
          best = count;
          dominant = action;
        }
      }
      out.push({ count: to - from, dominant });
    }
    return out;
  }, [trace, total]);

  const peak = useMemo(() => buckets.reduce((acc, b) => Math.max(acc, b.count), 1), [buckets]);

  const markGroups = useMemo<MarkGroup[]>(() => {
    if (!trace) return [];
    const groups = new Map<string, MarkGroup>();
    for (const mark of trace.marks) {
      // tail marks can carry seq == events.length; clamp keeps them on the strip
      const seqC = Math.min(mark.seq, max);
      const pos = max > 0 ? seqC / max : 0;
      const key = `${mark.type}:${Math.round(pos * MARK_SLOTS)}`;
      const group = groups.get(key);
      if (group) {
        group.count++;
        group.seq = Math.min(group.seq, seqC);
      } else {
        groups.set(key, { type: mark.type, seq: seqC, pos, count: 1, note: mark.note });
      }
    }
    return [...groups.values()];
  }, [trace, max]);

  return (
    <footer className="deck">
      <div className="deck-main">
        <div className="transport">
          <button
            className="icon-btn"
            onClick={() => onChange(0)}
            disabled={locked}
            title="Restart (Home)"
            aria-label="Restart playback"
          >
            <RotateCcw size={15} />
          </button>
          <button
            className="icon-btn"
            onClick={() => step(-1)}
            disabled={locked}
            title="Step back (←)"
            aria-label="Step back one event"
          >
            <StepBack size={15} />
          </button>
          <button
            className="play-btn"
            onClick={togglePlay}
            disabled={locked}
            title={playing ? "Pause (Space)" : "Play (Space)"}
            aria-label={playing ? "Pause playback" : "Play playback"}
          >
            {playing ? <Pause size={15} /> : <Play size={15} />}
            <span>{playing ? "Pause" : "Play"}</span>
          </button>
          <button
            className="icon-btn"
            onClick={() => step(1)}
            disabled={locked}
            title="Step forward (→)"
            aria-label="Step forward one event"
          >
            <StepForward size={15} />
          </button>
          <button
            className={speed === 1 ? "speed-btn" : "speed-btn engaged"}
            onClick={cycleSpeed}
            disabled={locked}
            title="Cycle playback speed (S)"
            aria-label={`Playback speed ${speed}x`}
          >
            {speed}×
          </button>
          {onExport ? (
            <button
              className={exporting ? "icon-btn recording" : "icon-btn"}
              onClick={onExport}
              disabled={total === 0 || exporting}
              title={exporting ? "Recording video…" : "Export video"}
              aria-label={exporting ? "Recording video" : "Export video"}
            >
              {exporting ? <Loader size={15} className="spin" /> : <Video size={15} />}
            </button>
          ) : null}
        </div>

        <div className="strip">
          <div className="strip-marks" aria-hidden>
            {markGroups.map((group, i) => (
              <span
                key={`${group.type}-${group.seq}-${i}`}
                className={`strip-mark ${group.type}`}
                style={{ left: `${group.pos * 100}%` }}
                title={`${group.note || MARK_LABEL[group.type]}${group.count > 1 ? ` ×${group.count}` : ""}`}
                onClick={() => onChange(group.seq)}
              />
            ))}
          </div>
          <div className="strip-bars" aria-hidden>
            {buckets.map((bucket, i) => (
              <span
                key={i}
                className={`strip-bar ${bucket.dominant}`}
                style={{ height: `${18 + (bucket.count / peak) * 82}%` }}
              />
            ))}
          </div>
          {total > 0 ? (
            <div className="strip-playhead" style={{ left: `${(seq / Math.max(max, 1)) * 100}%` }} aria-hidden />
          ) : null}
          <input
            className="strip-input"
            type="range"
            min={0}
            max={max}
            value={seq}
            disabled={locked}
            onChange={(e) => onChange(Number(e.currentTarget.value))}
            aria-label="Playback position"
            aria-valuetext={event ? `event ${event.seq}: ${event.tool}` : "empty"}
          />
        </div>

        <div className="deck-pos">
          <span className="deck-pos-count">{total > 0 ? `${seq + 1} / ${total}` : "0 / 0"}</span>
          <span className="deck-pos-clock">{event?.ts ? clock(event.ts) : "—"}</span>
        </div>
      </div>

      <div className="deck-foot">
        <div className="readout-now">
          {event ? (
            <>
              <span className={`action-dot ${event.action}`} />
              <span className="readout-tool">{event.tool}</span>
              {event.isError ? <span className="err">error</span> : null}
              <span className="readout-summary" title={event.summary}>
                {event.summary}
              </span>
            </>
          ) : (
            <span className="readout-summary">Select a session to start the walk.</span>
          )}
        </div>
        <div className="deck-legend" aria-hidden>
          <span className="legend-group">
            {STRIP_ACTIONS.map((action) => (
              <span key={action} className="legend-item">
                <span className={`action-dot ${action}`} />
                {action}
              </span>
            ))}
          </span>
          <span className="legend-sep" />
          <span className="legend-group">
            <span className="legend-item">
              <span className="legend-glyph compaction" />
              compaction
            </span>
            <span className="legend-item">
              <span className="legend-glyph subagent" />
              subagent
            </span>
            <span className="legend-item">
              <span className="legend-glyph user-message" />
              user turn
            </span>
          </span>
        </div>
      </div>
    </footer>
  );
}

function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}
