package codex

import (
	"fmt"
	"os"

	"github.com/cosmtrek/mindwalk/internal/adapter"
)

func (a Adapter) Diagnostics() []adapter.DiagnosticCheck {
	checks := adapter.FilesystemDiagnostics(a.SessionDir(), ".jsonl")

	idx := a.indexPath()
	if idx != "" {
		if _, err := os.Stat(idx); err != nil {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "session-index",
				Status: "warn",
				Detail: fmt.Sprintf("index file %s not found", idx),
			})
		} else {
			checks = append(checks, adapter.DiagnosticCheck{
				Name:   "session-index",
				Status: "ok",
				Detail: idx,
			})
		}
	}

	return checks
}
