package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=test@example.invalid", "-c", "user.name=test"}, args...)
	cmd := exec.Command("git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-q", "-m", "fixture commit")
	return dir
}

func TestReadGitMeta(t *testing.T) {
	dir := newGitRepo(t)
	m := ReadGitMeta(dir)
	if !m.IsGit {
		t.Fatal("git repo not detected")
	}
	if m.Branch != "main" {
		t.Fatalf("branch = %q, want main", m.Branch)
	}
	if m.Commit == "" {
		t.Fatal("commit empty")
	}
	wantRoot, _ := filepath.EvalSymlinks(dir)
	if m.Root != wantRoot {
		t.Fatalf("root = %q, want %q", m.Root, wantRoot)
	}
	if m.Dirty {
		t.Fatal("clean repo reported dirty")
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ReadGitMeta(dir).Dirty {
		t.Fatal("untracked file not reported dirty")
	}
}

func TestReadGitMetaNonGitDir(t *testing.T) {
	if m := ReadGitMeta(t.TempDir()); m.IsGit {
		t.Fatal("plain directory reported as git repo")
	}
}

func TestRemoteCredentialsStripped(t *testing.T) {
	dir := newGitRepo(t)
	gitRun(t, dir, "remote", "add", "origin", "https://user:supersecret@example.invalid/owner/repo.git")
	m := ReadGitMeta(dir)
	if m.Remote != "https://example.invalid/owner/repo.git" {
		t.Fatalf("credentials not stripped: %q", m.Remote)
	}
}

func TestStripCredentialsScpSyntax(t *testing.T) {
	if got := stripCredentials("git@example.invalid:owner/repo.git"); got != "example.invalid:owner/repo.git" {
		t.Fatalf("scp-style remote mangled: %q", got)
	}
}

func TestStripCredentialsRemovesSensitiveURLParts(t *testing.T) {
	got := stripCredentials("https://owner:secret@example.invalid/repo.git?token=secret#private")
	if got != "https://example.invalid/repo.git" {
		t.Fatalf("sensitive URL parts retained: %q", got)
	}
}

func TestStripCredentialsRemovesSensitiveScpSuffixes(t *testing.T) {
	got := stripCredentials("git@example.invalid:owner/repo.git?token=secret#private")
	if got != "example.invalid:owner/repo.git" {
		t.Fatalf("sensitive scp-style URL parts retained: %q", got)
	}
}

func TestParseWorktrees(t *testing.T) {
	out := "worktree /tmp/wt-main\nHEAD abc123\nbranch refs/heads/main\n\nworktree /tmp/wt-fix\nHEAD def456\nbranch refs/heads/fix\n"
	trees := parseWorktrees(out)
	if len(trees) != 2 || trees[0].Branch != "main" || trees[1].Branch != "fix" {
		t.Fatalf("parseWorktrees: %+v", trees)
	}
}

func TestGitWorktreeObserved(t *testing.T) {
	dir := newGitRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	gitRun(t, dir, "worktree", "add", "-q", "-b", "feature", wt)
	m := ReadGitMeta(dir)
	if len(m.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want 2 entries", m.Worktrees)
	}
	found := false
	for _, w := range m.Worktrees {
		if w.Branch == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatal("added worktree branch not observed")
	}
}
