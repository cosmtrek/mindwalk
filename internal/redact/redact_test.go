package redact

import (
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestStringRedactsCredentialPatterns(t *testing.T) {
	cases := []string{
		"sk-proj-abcdefghijklmnopqrstuvwxyz012345",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"AKIAABCDEFGHIJKLMNOP",
		"Authorization: Bearer abc.def.ghi",
		"password=hunter2",
		"api_key:supersecret",
		"https://user:secret@example.invalid/repo.git",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nsecret material\n-----END OPENSSH PRIVATE KEY-----",
	}
	for _, input := range cases {
		got, count := String(input)
		if count == 0 || !strings.Contains(got, Marker) || strings.Contains(got, "hunter2") || strings.Contains(got, "secret material") {
			t.Fatalf("redaction failed for %q: %q count=%d", input, got, count)
		}
	}
}

func TestTraceRedactsFreeTextButPreservesTargets(t *testing.T) {
	trace := &model.Trace{
		Session: model.TraceSession{Title: "token=topsecret"},
		Events: []model.Event{{
			Tool:    "exec",
			Summary: "curl -H 'Authorization: Bearer abcdef'",
			Targets: []model.Target{{Path: "src/token.go"}},
			Outside: []model.OutsideTouch{{Scope: "home", Path: "https://user:pass@example.invalid"}},
		}},
		Marks: []model.Mark{{Note: "password=hunter2"}},
	}
	if count := Trace(trace); count != 4 {
		t.Fatalf("redaction count = %d, want 4", count)
	}
	if trace.Events[0].Targets[0].Path != "src/token.go" {
		t.Fatal("structured target path was modified")
	}
	encoded := trace.Session.Title + trace.Events[0].Summary + trace.Events[0].Outside[0].Path + trace.Marks[0].Note
	for _, secret := range []string{"topsecret", "abcdef", "user:pass", "hunter2"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("secret %q remains in trace: %s", secret, encoded)
		}
	}
}
