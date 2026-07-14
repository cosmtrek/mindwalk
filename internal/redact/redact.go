// Package redact removes common credential forms before observability data is
// displayed, exported, or durably quarantined. It intentionally favors false
// positives over exposing a secret.
package redact

import (
	"regexp"

	"github.com/cosmtrek/mindwalk/internal/model"
)

const Marker = "[REDACTED]"

var patterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?s)-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`), Marker},
	{regexp.MustCompile(`\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{16})\b`), Marker},
	{regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`), "Bearer " + Marker},
	{regexp.MustCompile(`(?i)\b(password|passwd|token|secret|api[_-]?key)\s*([:=])\s*[^\s,;]+`), `$1$2` + Marker},
	{regexp.MustCompile(`(?i)(https?://)[^/@\s:]+:[^/@\s]+@`), `$1` + Marker + `@`},
}

// String redacts supported secret patterns and reports how many pattern
// matches were replaced.
func String(value string) (string, int) {
	count := 0
	for _, pattern := range patterns {
		matches := pattern.re.FindAllStringIndex(value, -1)
		count += len(matches)
		if len(matches) > 0 {
			value = pattern.re.ReplaceAllString(value, pattern.replacement)
		}
	}
	return value, count
}

// SessionMeta redacts owner-visible free text from a session listing.
func SessionMeta(meta *model.SessionMeta) int {
	if meta == nil {
		return 0
	}
	var count int
	meta.Title, count = String(meta.Title)
	return count
}

// Trace redacts every free-text field exposed by the normalized trace API.
// Structured repository-relative target paths remain intact so the citymap
// can still identify files.
func Trace(trace *model.Trace) int {
	if trace == nil {
		return 0
	}
	count := 0
	var n int
	trace.Session.Title, n = String(trace.Session.Title)
	count += n
	for i := range trace.Events {
		trace.Events[i].Summary, n = String(trace.Events[i].Summary)
		count += n
		trace.Events[i].Tool, n = String(trace.Events[i].Tool)
		count += n
		for j := range trace.Events[i].Outside {
			trace.Events[i].Outside[j].Path, n = String(trace.Events[i].Outside[j].Path)
			count += n
		}
	}
	for i := range trace.Marks {
		trace.Marks[i].Note, n = String(trace.Marks[i].Note)
		count += n
	}
	return count
}
