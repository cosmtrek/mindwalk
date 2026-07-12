// Client-side video export: capture the live WebGL canvas with MediaRecorder
// while driving the playhead from the first event to the last, then hand back a
// webm Blob. No server, no extra dependency — captureStream + MediaRecorder are
// native browser APIs.

export interface RecordOptions {
  canvas: HTMLCanvasElement;
  total: number; // number of playback events (trace.events.length)
  setSeq: (seq: number) => void; // drives the store's currentSeq
  fps?: number; // capture frame rate
  durationMs?: number; // wall-clock length of the exported run
  onProgress?: (fraction: number) => void; // 0..1
  signal?: AbortSignal; // cancel an in-flight recording
}

export interface RecordResult {
  blob: Blob;
  extension: string; // matches the recorded container (webm / mp4 / …)
}

// Map a MediaRecorder mimeType to a sensible file extension. Falls back to the
// container subtype when it's an unknown-but-plausible type.
function extensionForMime(mimeType: string): string {
  const base = mimeType.split(";")[0].trim().toLowerCase(); // strip codecs
  const subtype = base.split("/")[1] ?? "";
  if (subtype.includes("webm")) return "webm";
  if (subtype.includes("mp4")) return "mp4";
  if (subtype.includes("x-matroska") || subtype.includes("matroska")) return "mkv";
  if (subtype.includes("ogg")) return "ogv";
  return subtype || "webm";
}

const DEFAULT_FPS = 30;
// Long sessions would otherwise produce minutes of video; cap the run and let
// the playhead cover more events per frame. Short sessions play close to
// real-ish time via the per-event floor.
const MAX_DURATION_MS = 30_000;
const PER_EVENT_MS = 120;

function pickMimeType(): string | undefined {
  const candidates = ["video/webm;codecs=vp9", "video/webm;codecs=vp8", "video/webm"];
  for (const type of candidates) {
    if (typeof MediaRecorder !== "undefined" && MediaRecorder.isTypeSupported(type)) return type;
  }
  return undefined;
}

export function recordingSupported(): boolean {
  return (
    typeof MediaRecorder !== "undefined" &&
    typeof HTMLCanvasElement !== "undefined" &&
    typeof HTMLCanvasElement.prototype.captureStream === "function"
  );
}

// Records the playback and resolves with the recorded blob and a file extension
// that matches the actual container. Rejects if the browser can't record or the
// run is aborted.
export function recordPlayback(opts: RecordOptions): Promise<RecordResult> {
  const { canvas, total, setSeq, onProgress, signal } = opts;
  const fps = opts.fps ?? DEFAULT_FPS;

  if (!recordingSupported()) {
    return Promise.reject(new Error("This browser can't record the canvas (MediaRecorder unavailable)."));
  }
  if (total <= 0) {
    return Promise.reject(new Error("Nothing to record — load a session first."));
  }

  const durationMs = opts.durationMs ?? Math.min(MAX_DURATION_MS, Math.max(1000, total * PER_EVENT_MS));
  const mimeType = pickMimeType();
  const stream = canvas.captureStream(fps);
  const recorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
  const chunks: BlobPart[] = [];

  return new Promise<RecordResult>((resolve, reject) => {
    let rafId = 0;
    let startTs = 0;
    let settled = false;

    const cleanup = () => {
      if (rafId) cancelAnimationFrame(rafId);
      stream.getTracks().forEach((track) => track.stop());
      signal?.removeEventListener("abort", onAbort);
    };

    const finish = () => {
      if (settled) return;
      settled = true;
      cleanup();
      // the browser may have chosen a container different from what we
      // requested; trust recorder.mimeType so the blob type and file extension
      // match what was actually recorded
      const actualType = recorder.mimeType || mimeType || "video/webm";
      resolve({ blob: new Blob(chunks, { type: actualType }), extension: extensionForMime(actualType) });
    };

    const fail = (err: Error) => {
      if (settled) return;
      settled = true;
      cleanup();
      try {
        if (recorder.state !== "inactive") recorder.stop();
      } catch {
        // recorder already torn down
      }
      reject(err);
    };

    const onAbort = () => fail(new Error("Recording cancelled."));

    recorder.ondataavailable = (e) => {
      if (e.data && e.data.size > 0) chunks.push(e.data);
    };
    recorder.onerror = () => fail(new Error("Recording failed."));
    recorder.onstop = finish;

    if (signal) {
      if (signal.aborted) {
        fail(new Error("Recording cancelled."));
        return;
      }
      signal.addEventListener("abort", onAbort);
    }

    const tick = (ts: number) => {
      if (settled) return;
      if (!startTs) startTs = ts;
      const fraction = Math.min(1, (ts - startTs) / durationMs);
      const seq = Math.min(total - 1, Math.floor(fraction * (total - 1)));
      setSeq(seq);
      onProgress?.(fraction);
      if (fraction >= 1) {
        // let the final frame paint before closing the recorder
        rafId = requestAnimationFrame(() => {
          if (recorder.state !== "inactive") recorder.stop();
        });
        return;
      }
      rafId = requestAnimationFrame(tick);
    };

    setSeq(0);
    recorder.start();
    rafId = requestAnimationFrame(tick);
  });
}

// Triggers a browser download of the recorded Blob.
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // revoke on the next tick so the download has a chance to start
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
