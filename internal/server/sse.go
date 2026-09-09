package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// ssePollInterval is how often the SSE handler checks the progress log for
// new events. The judge run takes ~30-60s per phase, so 200ms is instant from
// the user's perspective while costing negligible CPU.
const ssePollInterval = 200 * time.Millisecond

// sseHeartbeat is the maximum gap between writes before the handler sends a
// keep-alive comment. Long judge runs (~2 min) can sit idle between phases;
// without periodic traffic, reverse proxies (nginx, Cloudflare) may drop the
// connection. SSE comment lines (": ...") are ignored by EventSource clients
// but count as traffic to intermediary proxies. A var (not const) so tests
// can shorten it.
var sseHeartbeat = 15 * time.Second

// handleSessionAnalyzeStream streams judge progress events to the browser
// via Server-Sent Events. The frontend opens an EventSource on this endpoint
// when an evaluation is running and receives "progress" events (one per
// judge milestone) followed by a terminal "status" event carrying the same
// reportStatus the polling endpoint returns. If no job is running, the
// handler sends just the "status" event and closes.
//
// Each progress event carries an SSE "id:" line so the browser's EventSource
// tracks the last received event and sends it back as Last-Event-ID on
// reconnect. This lets a dropped connection resume from where it left off
// instead of replaying from the beginning.
func (s *Server) handleSessionAnalyzeStream(w http.ResponseWriter, r *http.Request, selector string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	meta, err := s.findSession(selector)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)

		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()

	// If no job is running, send the current status and close — the
	// frontend opens an EventSource when it sees state "running", but the
	// job may have finished between the last poll and this connection.
	job, ok := s.analyze.snapshot(meta.Key)
	if !ok || job.done {
		s.sseSendStatus(w, flusher, meta)

		return
	}

	// Tail the progress log until the job finishes. If the client sent a
	// Last-Event-ID header (reconnect after a dropped connection), resume
	// AFTER that event: the browser already has it, so re-sending it would
	// duplicate a milestone in the progress panel.
	offset := resumeOffset(r.Header.Get("Last-Event-ID"))
	lastWrite := time.Now()

	for {
		wrote := false
		// Drain any new progress events since the last poll.
		if job.progress != nil {
			events, next := job.progress.since(offset)
			for i, evt := range events {
				writeSSEWithID(w, flusher, "progress", evt, offset+i)

				wrote = true
			}

			offset = next
		}

		// Check whether the job finished since last poll.
		current, ok := s.analyze.snapshot(meta.Key)
		if !ok || current.done {
			s.sseSendStatus(w, flusher, meta)

			return
		}
		// Refresh the progress handle from the latest snapshot so
		// events queued after the initial connection are not missed.
		job = current

		// Send a keep-alive comment when no data has flowed for the
		// heartbeat interval, keeping intermediary proxies from
		// timing out the connection during long judge phases.
		if !wrote && time.Since(lastWrite) >= sseHeartbeat {
			if _, err := fmt.Fprintf(w, ": keep-alive\n\n"); err != nil {
				return
			}

			flusher.Flush()

			lastWrite = time.Now()
		} else if wrote {
			lastWrite = time.Now()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(ssePollInterval):
		}
	}
}

// sseSendStatus builds the report status for the session and sends it as a
// terminal "status" SSE event, then flushes. Called when the job is not
// running (no job, already done, or just finished while tailing).
func (s *Server) sseSendStatus(w http.ResponseWriter, flusher http.Flusher, meta model.SessionMeta) {
	trace, _, err := s.traceAndMapMeta(meta)
	if err != nil {
		writeSSE(w, flusher, "status", reportStatus{State: "failed", Error: err.Error()})

		return
	}

	writeSSE(w, flusher, "status", s.buildReportStatus(meta, trace))
}

// writeSSE serialises one SSE event: an "event:" line naming the type, a
// "data:" line with the JSON payload, and a blank line to delimit. The
// caller must flush afterwards.
func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any) {
	writeSSEWithID(w, flusher, eventType, payload, -1)
}

// writeSSEWithID is like writeSSE but also emits an "id:" line when id >= 0.
// The id lets the browser's EventSource track the last received event and
// send it back as Last-Event-ID on reconnect.
func writeSSEWithID(w http.ResponseWriter, flusher http.Flusher, eventType string, payload any, id int) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	if id >= 0 {
		if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, eventType, data); err != nil {
			return
		}
	} else {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data); err != nil {
			return
		}
	}

	flusher.Flush()
}

// resumeOffset converts a Last-Event-ID header value into the index to start
// reading from in the progress log. A missing or unparseable header means a
// fresh connection (start at 0). A valid id N means "the browser already has
// event N", so we resume at N+1 — re-sending N would duplicate a milestone.
func resumeOffset(header string) int {
	if header == "" {
		return 0
	}

	id, err := strconv.Atoi(header)
	if err != nil || id < 0 {
		return 0
	}

	return id + 1
}
