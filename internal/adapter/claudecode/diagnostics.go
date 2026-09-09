package claudecode

import "github.com/cosmtrek/mindwalk/internal/adapter"

func (a Adapter) Diagnostics() []adapter.DiagnosticCheck {
	return adapter.FilesystemDiagnostics(a.SessionDir(), ".jsonl")
}
