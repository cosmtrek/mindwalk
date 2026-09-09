package adapter

import (
	"path/filepath"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// FallbackTitle sets *target to filepath.Base(path) when the current
// value is empty. Every JSONL adapter records a session title in its
// native log (or computes one from a sidecar), but the fallback when
// nothing was recorded has to be the file path basename so the rail
// and trace views show something useful. Centralising the rule avoids
// adapters drifting on whether to use filepath.Base or just the
// filename stem.
func FallbackTitle(target *string, path string) {
	if *target == "" {
		*target = filepath.Base(path)
	}
}

// FallbackSessionTitle is the SessionMeta-shaped convenience wrapper
// around FallbackTitle.
func FallbackSessionTitle(meta *model.SessionMeta, path string) {
	FallbackTitle(&meta.Title, path)
}

// FallbackTraceSessionTitle is the model.TraceSession-shaped
// convenience wrapper around FallbackTitle.
func FallbackTraceSessionTitle(trace *model.Trace, path string) {
	FallbackTitle(&trace.Session.Title, path)
}
