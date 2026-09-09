package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestParseJSONInputEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\t  "} {
		got := ParseJSONInput(in)
		if got == nil {
			t.Fatalf("ParseJSONInput(%q) returned nil", in)
		}

		if len(got) != 0 {
			t.Fatalf("ParseJSONInput(%q) = %v, want empty map", in, got)
		}
	}
}

func TestParseJSONInputValidObject(t *testing.T) {
	got := ParseJSONInput(`{"command":"ls","path":"foo"}`)
	if got["command"] != "ls" || got["path"] != "foo" {
		t.Fatalf("ParseJSONInput(object) = %v", got)
	}
}

func TestParseJSONInputNestedString(t *testing.T) {
	got := ParseJSONInput(`"{\"command\":\"ls\"}"`)
	if got["command"] != "ls" {
		t.Fatalf("ParseJSONInput(nested string) = %v", got)
	}
}

func TestParseJSONInputMalformedFallsBackToRaw(t *testing.T) {
	got := ParseJSONInput("not json at all")
	if got["_raw"] != "not json at all" {
		t.Fatalf("ParseJSONInput(malformed) = %v, want _raw fallback", got)
	}
}

func TestParseJSONInputNonObjectValueEncodedAsRaw(t *testing.T) {
	got := ParseJSONInput("[1,2,3]")
	if got["_raw"] != "[1,2,3]" {
		t.Fatalf("ParseJSONInput(array) = %v, want _raw fallback to encoded value", got)
	}
}

func TestFallbackTitleSetsOnlyWhenEmpty(t *testing.T) {
	t.Run("empty gets basename", func(t *testing.T) {
		title := ""
		FallbackTitle(&title, "/some/where/session.jsonl")

		if title != "session.jsonl" {
			t.Fatalf("FallbackTitle empty = %q", title)
		}
	})
	t.Run("non-empty preserved", func(t *testing.T) {
		title := "Already Set"
		FallbackTitle(&title, "/some/where/session.jsonl")

		if title != "Already Set" {
			t.Fatalf("FallbackTitle non-empty = %q", title)
		}
	})
}

func TestFallbackSessionTitleDelegates(t *testing.T) {
	meta := model.SessionMeta{Title: ""}
	FallbackSessionTitle(&meta, "/a/b/foo.jsonl")

	if meta.Title != "foo.jsonl" {
		t.Fatalf("FallbackSessionTitle = %q", meta.Title)
	}
}

func TestFallbackTraceSessionTitleDelegates(t *testing.T) {
	trace := &model.Trace{Session: model.TraceSession{}}
	FallbackTraceSessionTitle(trace, "/a/b/bar.jsonl")

	if trace.Session.Title != "bar.jsonl" {
		t.Fatalf("FallbackTraceSessionTitle = %q", trace.Session.Title)
	}
}

func TestApplySubagentLabelOnlyWhenEmpty(t *testing.T) {
	t.Run("empty gets Subagent", func(t *testing.T) {
		node := &model.AgentNode{}
		ApplySubagentLabel(node)

		if node.Label != SubagentLabel {
			t.Fatalf("label = %q", node.Label)
		}
	})
	t.Run("non-empty preserved", func(t *testing.T) {
		node := &model.AgentNode{Label: "preset"}
		ApplySubagentLabel(node)

		if node.Label != "preset" {
			t.Fatalf("label = %q", node.Label)
		}
	})
}

func TestApplyLaunchNicknamePreferenceOrder(t *testing.T) {
	t.Run("nickname wins when label empty", func(t *testing.T) {
		node := &model.AgentNode{}
		ApplyLaunchNickname(node, AgentLaunchOutput{Nickname: "runner"})

		if node.Label != "runner" {
			t.Fatalf("label = %q", node.Label)
		}
	})
	t.Run("existing label beats nickname", func(t *testing.T) {
		node := &model.AgentNode{Label: "child"}
		ApplyLaunchNickname(node, AgentLaunchOutput{Nickname: "runner"})

		if node.Label != "child" {
			t.Fatalf("label = %q", node.Label)
		}
	})
	t.Run("falls back to Subagent when both empty", func(t *testing.T) {
		node := &model.AgentNode{}
		ApplyLaunchNickname(node, AgentLaunchOutput{})

		if node.Label != SubagentLabel {
			t.Fatalf("label = %q, want Subagent fallback", node.Label)
		}
	})
}

func TestUnlinkedLaunchStatus(t *testing.T) {
	cases := []struct {
		name   string
		launch AgentLaunch
		want   string
	}{
		{"empty output, observed", AgentLaunch{OutputObserved: true}, model.AgentStatusUnknown},
		{"empty output, not observed", AgentLaunch{}, model.AgentStatusUnknown},
		{
			"valid JSON output, observed",
			AgentLaunch{OutputObserved: true, Output: `{"id":1}`},
			model.AgentStatusUnknown,
		},
		{"garbage output, observed", AgentLaunch{OutputObserved: true, Output: "not json"}, model.AgentStatusFailed},
		{"garbage output, not observed", AgentLaunch{Output: "not json"}, model.AgentStatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UnlinkedLaunchStatus(c.launch); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestNormalizePathCollapsesEquivalents(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	abs := filepath.Join(cwd, "session.jsonl")

	cases := []struct {
		in, want string
	}{
		{abs, abs},
		{"./session.jsonl", abs},
		{"session.jsonl", abs},
	}
	for _, c := range cases {
		got := NormalizePath(c.in)
		if got != c.want {
			t.Fatalf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSessionKeyUsesNormalizedPath(t *testing.T) {
	base, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	a := SessionKey("test", filepath.Join(base, "session.jsonl"))

	b := SessionKey("test", "./session.jsonl")
	if a != b {
		t.Fatalf("SessionKey should normalise path: %q != %q", a, b)
	}
}

func TestMindwalkHomeHonoursOverride(t *testing.T) {
	t.Setenv("MINDWALK_HOME", "/tmp/custom-mindwalk")

	if got := MindwalkHome(); got != "/tmp/custom-mindwalk" {
		t.Fatalf("MindwalkHome = %q, want override", got)
	}
}

func TestMindwalkHomeFallsBackToHomePath(t *testing.T) {
	t.Setenv("MINDWALK_HOME", "")
	t.Setenv("HOME", "/tmp/fake-home")

	if got := MindwalkHome(); got != filepath.Join("/tmp/fake-home", ".mindwalk") {
		t.Fatalf("MindwalkHome = %q, want fallback under $HOME", got)
	}
}

func TestMindwalkHomeEmptyWhenHomeDirUnset(t *testing.T) {
	t.Setenv("MINDWALK_HOME", "")
	t.Setenv("HOME", "")
	// UserHomeDir returns "" on platforms without HOME; the function
	// should propagate that to its caller.
	home := MindwalkHome()
	if home != "" {
		t.Fatalf("MindwalkHome = %q, want empty when no home resolvable", home)
	}
}

func TestAgentLaunchOutputJSONRoundTrip(t *testing.T) {
	in := AgentLaunchOutput{AgentID: "a1", Nickname: "n1", TaskName: "t1"}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"agent_id":"a1","nickname":"n1","task_name":"t1"}`
	if string(data) != want {
		t.Fatalf("marshal = %s, want %s", data, want)
	}
}
