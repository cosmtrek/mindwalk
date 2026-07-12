package githistory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

func TestBuildReplaysCommitsOldestToNewest(t *testing.T) {
	repo := initRepo(t)

	// commit 1: add a.go
	writeFile(t, repo, "a.go", "package demo\n")
	gitCommit(t, repo, "add a")

	// commit 2: add b.go, edit a.go
	writeFile(t, repo, "b.go", "package demo\n")
	writeFile(t, repo, "a.go", "package demo\n\nfunc A() {}\n")
	gitCommit(t, repo, "add b, edit a")

	// commit 3: rename a.go -> renamed.go, delete b.go
	gitMove(t, repo, "a.go", "renamed.go")
	gitRemove(t, repo, "b.go")
	gitCommit(t, repo, "rename a, drop b")

	trace, err := Builder{}.Build(repo)
	if err != nil {
		t.Fatal(err)
	}

	// 3 commits + 1 synthetic empty-repo seed event at t=0
	if len(trace.Events) != 4 {
		t.Fatalf("events = %d, want 4 (seed + 3 commits)", len(trace.Events))
	}
	if trace.Session.Harness != Harness {
		t.Fatalf("harness = %q, want %q", trace.Session.Harness, Harness)
	}
	if trace.Session.EventCount != 4 {
		t.Fatalf("eventCount = %d, want 4", trace.Session.EventCount)
	}

	// event 0 is the empty-repo seed: no targets, not a commit
	if len(trace.Events[0].Targets) != 0 {
		t.Fatalf("seed event should have no targets, got %v", paths(trace.Events[0].Targets))
	}
	if trace.Events[0].Tool != "start" {
		t.Fatalf("seed event tool = %q, want start", trace.Events[0].Tool)
	}
	// first real commit (event 1) touches a.go
	if got := paths(trace.Events[1].Targets); !contains(got, "a.go") {
		t.Fatalf("event 1 targets = %v, want a.go", got)
	}
	// seq is dense 0..n-1
	for i, e := range trace.Events {
		if e.Seq != i {
			t.Fatalf("event %d seq = %d", i, e.Seq)
		}
	}
	// every commit event (1..) is an edit-touch commit
	for i, e := range trace.Events[1:] {
		if e.Action != "edit" || e.Tool != "commit" {
			t.Fatalf("commit event %d action/tool = %q/%q", i+1, e.Action, e.Tool)
		}
		for _, tg := range e.Targets {
			if tg.Touch != "edit" {
				t.Fatalf("commit event %d target touch = %q, want edit", i+1, tg.Touch)
			}
		}
	}

	// event 3: rename records the NEW path, delete still records b.go (ghost)
	last := paths(trace.Events[3].Targets)
	if !contains(last, "renamed.go") {
		t.Fatalf("rename target missing new path: %v", last)
	}
	if !contains(last, "b.go") {
		t.Fatalf("delete target missing b.go: %v", last)
	}

	// timestamps present and ordered start<=end
	if trace.Session.StartedAt == "" || trace.Session.EndedAt == "" {
		t.Fatalf("missing session timestamps: %#v", trace.Session)
	}
	if trace.Session.StartedAt > trace.Session.EndedAt {
		t.Fatalf("started %q after ended %q", trace.Session.StartedAt, trace.Session.EndedAt)
	}
	if trace.Session.Commit == "" {
		t.Fatal("missing HEAD commit")
	}
}

func TestBuildErrorsOnNonRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := (Builder{}).Build(dir); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

// helpers

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "config", "user.email", "t@example.com")
	run(t, dir, "git", "config", "user.name", "Tester")
	return dir
}

func writeFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitCommit(t *testing.T, repo, msg string) {
	t.Helper()
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-q", "-m", msg)
}

func gitMove(t *testing.T, repo, from, to string) {
	t.Helper()
	run(t, repo, "git", "mv", from, to)
}

func gitRemove(t *testing.T, repo, name string) {
	t.Helper()
	run(t, repo, "git", "rm", "-q", name)
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func paths(targets []model.Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Path)
	}
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
