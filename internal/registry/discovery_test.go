package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func initGitAt(t *testing.T, path string, bare bool) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	args := []string{"init", "-q", "-b", "main", path}
	if bare {
		args = []string{"init", "-q", "--bare", path}
	}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func commitAt(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, path, "add", "README.md")
	gitRun(t, path, "commit", "-q", "-m", "fixture")
}

func scanFixture(t *testing.T, root string, mutate func(*DiscoveryScanRequest)) (DiscoveryOutcome, error) {
	t.Helper()
	req := DiscoveryScanRequest{Roots: []string{root}, Options: DefaultDiscoveryOptions()}
	if mutate != nil {
		mutate(&req)
	}
	return (DiscoveryScanner{}).Scan(context.Background(), req)
}

func resultByPath(results []DiscoveryResult, path string) (DiscoveryResult, bool) {
	canonical, _ := filepath.EvalSymlinks(path)
	for _, result := range results {
		if result.Path == canonical {
			return result, true
		}
	}
	return DiscoveryResult{}, false
}

func TestDiscoveryFindsOrdinaryBareWorktreeAndBrokenRepositories(t *testing.T) {
	root := t.TempDir()
	ordinary := filepath.Join(root, "ordinary")
	initGitAt(t, ordinary, false)
	commitAt(t, ordinary)
	bare := filepath.Join(root, "archive.git")
	initGitAt(t, bare, true)
	worktree := filepath.Join(root, "worktree")
	gitRun(t, ordinary, "worktree", "add", "-q", "-b", "feature", worktree)
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.Options.FindNested = true })
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{ordinary: DiscoveryTypeRepository, bare: DiscoveryTypeBare, worktree: DiscoveryTypeWorktree, broken: DiscoveryTypeBroken}
	for path, wantType := range checks {
		result, ok := resultByPath(outcome.Results, path)
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		if result.Type != wantType {
			t.Errorf("%s type = %s, want %s (%+v)", path, result.Type, wantType, result)
		}
		if result.State != DiscoveryStateUnknown {
			t.Errorf("%s state = %s", path, result.State)
		}
		if result.Accessible {
			if err := ValidateDiscoveryCandidate(result.Path, result.Type, []string{root}); err != nil {
				t.Errorf("%s failed final metadata validation: %v", path, err)
			}
		}
	}
	ordinaryResult, _ := resultByPath(outcome.Results, ordinary)
	if ordinaryResult.Branch != "main" || ordinaryResult.Head == "" || !ordinaryResult.Accessible {
		t.Fatalf("ordinary metadata = %+v", ordinaryResult)
	}
	worktreeResult, _ := resultByPath(outcome.Results, worktree)
	if worktreeResult.Branch != "feature" || worktreeResult.WorktreeOf == "" {
		t.Fatalf("worktree metadata = %+v", worktreeResult)
	}
}

func TestValidateDiscoveryCandidateRejectsChangedWorktreeMetadata(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "main")
	worktree := filepath.Join(root, "worktree")
	initGitAt(t, main, false)
	commitAt(t, main)
	gitRun(t, main, "worktree", "add", "-q", "-b", "feature-validation", worktree)
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := resultByPath(outcome.Results, main)
	if !ok {
		t.Fatalf("main repository missing: %+v", outcome.Results)
	}
	if err := ValidateDiscoveryCandidate(result.Path, result.Type, []string{root}); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}

	external := filepath.Join(t.TempDir(), "external-git")
	if err := os.Mkdir(external, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(external, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+external+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDiscoveryCandidate(worktree, DiscoveryTypeWorktree, []string{root}); err == nil {
		t.Fatal("worktree whose metadata moved outside approved roots was accepted")
	}
}

func TestDiscoveryNestedToggle(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "nested")
	initGitAt(t, outer, false)
	initGitAt(t, inner, false)

	without, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(without.Results) != 1 || without.Results[0].Path != outer {
		t.Fatalf("nested off = %+v", without.Results)
	}
	with, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.Options.FindNested = true })
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Results) != 2 {
		t.Fatalf("nested on = %+v", with.Results)
	}
}

func TestDiscoveryDeduplicatesOverlappingRootsAndMarksRegisteredHidden(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "projects", "repo")
	initGitAt(t, repo, false)
	id := discoveryID(repo)
	outcome, err := (DiscoveryScanner{}).Scan(context.Background(), DiscoveryScanRequest{
		Roots: []string{root, filepath.Join(root, "projects")}, Options: DefaultDiscoveryOptions(),
		Registered: []Repo{{Path: repo}}, HiddenTokens: []string{id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("duplicate results = %+v", outcome.Results)
	}
	if !outcome.Results[0].AlreadyRegistered || !outcome.Results[0].Hidden || outcome.Results[0].ID != id {
		t.Fatalf("flags = %+v", outcome.Results[0])
	}
	if len(outcome.Results[0].Warnings) == 0 || !strings.Contains(outcome.Results[0].Warnings[0], "duplicate canonical") {
		t.Fatalf("canonical duplicate was not reported: %+v", outcome.Results[0])
	}
	if outcome.Summary.RepositoriesSkipped != 1 {
		t.Fatalf("repository skip counter must count only the duplicate repository: %+v", outcome.Summary)
	}
}

func TestDiscoveryDoesNotFollowSymlinkLoopOrEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideRepo := filepath.Join(outside, "secret-repo")
	initGitAt(t, outsideRepo, false)
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideRepo, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "inside")
	initGitAt(t, inside, false)
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Path != inside {
		t.Fatalf("symlinks escaped scan: %+v", outcome.Results)
	}
	if outcome.Summary.RepositoriesSkipped != 0 {
		t.Fatalf("directory symlinks were mislabeled as skipped repositories: %+v", outcome.Summary)
	}
}

func TestDiscoveryDescriptorAnchorsCannotBeRedirectedByAncestorSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not permit renaming this open-directory fixture")
	}
	rootPath := t.TempDir()
	gitDir := filepath.Join(rootPath, "repo", ".git")
	secretDir := filepath.Join(rootPath, "credentials")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("safe-head"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretDir, "HEAD"), []byte("credential-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := openDiscoveryRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	anchoredGit, err := openDiscoveryRelativeRoot(root, filepath.Join("repo", ".git"))
	if err != nil {
		t.Fatal(err)
	}
	defer anchoredGit.Close()
	moved := filepath.Join(rootPath, "repo", ".git-original")
	if err := os.Rename(gitDir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secretDir, gitDir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openDiscoveryPath(root, filepath.Join("repo", ".git", "HEAD"), false); err == nil {
		t.Fatal("fresh traversal followed the substituted ancestor symlink")
	}
	file, _, err := openDiscoveryPath(anchoredGit, "HEAD", false)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "safe-head" {
		t.Fatalf("anchored descriptor was redirected: %q", contents)
	}
}

func TestDiscoveryGitFileOutsideApprovedRootsNeverRunsGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	externalGitDir := filepath.Join(t.TempDir(), "metadata")
	if err := os.Mkdir(externalGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+externalGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	called := filepath.Join(t.TempDir(), "git-called")
	script := "#!/bin/sh\n: > \"$MINDWALK_GIT_CALLED\"\nexit 99\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MINDWALK_GIT_CALLED", called)
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runGit followed external .git target: %v", err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Type != DiscoveryTypeWorktree || len(outcome.Results[0].Warnings) == 0 {
		t.Fatalf("outside worktree result = %+v", outcome.Results)
	}
	if outcome.Results[0].Accessible {
		t.Fatal("worktree with external metadata was addable")
	}
}

func TestDiscoveryBrokenGitFileIsReported(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "broken-file")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("not git metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Type != DiscoveryTypeBroken || outcome.Results[0].Accessible {
		t.Fatalf("broken result = %+v", outcome.Results)
	}
}

func TestDiscoveryDoesNotRunGitOrFollowInternalMetadataSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	t.Run("config symlink is never opened by Git", func(t *testing.T) {
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		initGitAt(t, repo, false)
		commitAt(t, repo)
		external := filepath.Join(t.TempDir(), "credential-config")
		if err := os.WriteFile(external, []byte("[credential]\nhelper = malicious\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(repo, ".git", "config")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(repo, ".git", "config")); err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		called := filepath.Join(t.TempDir(), "git-called")
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n: > \"$MINDWALK_GIT_CALLED\"\nexit 99\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		t.Setenv("MINDWALK_GIT_CALLED", called)
		outcome, err := scanFixture(t, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discovery executed Git: %v", err)
		}
		if len(outcome.Results) != 1 || !outcome.Results[0].Accessible || outcome.Results[0].Head == "" {
			t.Fatalf("direct metadata result = %+v", outcome.Results)
		}
	})

	t.Run("HEAD symlink is rejected", func(t *testing.T) {
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		external := filepath.Join(t.TempDir(), "HEAD")
		if err := os.WriteFile(external, []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(repo, ".git", "HEAD")); err != nil {
			t.Fatal(err)
		}
		outcome, err := scanFixture(t, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Results) != 1 || outcome.Results[0].Accessible || outcome.Results[0].Type != DiscoveryTypeBroken {
			t.Fatalf("unsafe HEAD metadata = %+v", outcome.Results)
		}
	})

	t.Run("ref path cannot traverse an in-root symlink", func(t *testing.T) {
		root := t.TempDir()
		repo := filepath.Join(root, "repo")
		initGitAt(t, repo, false)
		secretDir := filepath.Join(root, "ordinary-source")
		if err := os.Mkdir(secretDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(secretDir, "main"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		heads := filepath.Join(repo, ".git", "refs", "heads")
		if err := os.RemoveAll(heads); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secretDir, heads); err != nil {
			t.Fatal(err)
		}
		outcome, err := scanFixture(t, root, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Results) != 1 || outcome.Results[0].Accessible || outcome.Results[0].Type != DiscoveryTypeBroken {
			t.Fatalf("metadata read followed an in-root symlink: %+v", outcome.Results)
		}
	})
}

func TestDiscoveryExclusionsAndNormalHiddenRepository(t *testing.T) {
	root := t.TempDir()
	hidden := filepath.Join(root, ".ordinary-hidden", "repo")
	excluded := filepath.Join(root, "node_modules", "repo")
	custom := filepath.Join(root, "generated", "repo")
	initGitAt(t, hidden, false)
	initGitAt(t, excluded, false)
	initGitAt(t, custom, false)
	outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.CustomExclusions = []string{"generated"} })
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Path != hidden {
		t.Fatalf("exclusions/hidden = %+v", outcome.Results)
	}
}

func TestDiscoveryDoesNotReadOrdinarySourceContent(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	initGitAt(t, repo, false)
	secret := filepath.Join(repo, "do-not-read.env")
	if err := os.WriteFile(secret, []byte("SECRET=never-read\n"), 0); err != nil {
		t.Fatal(err)
	}
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].State != DiscoveryStateUnknown {
		t.Fatalf("source-independent result = %+v", outcome.Results)
	}
}

func TestDiscoveryDepthDirectoryAndResultBounds(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "one", "two", "repo")
		initGitAt(t, deep, false)
		outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.Options.MaxDepth = 1 })
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Results) != 0 || outcome.Summary.DirectoriesExamined > 2 {
			t.Fatalf("depth bound = %+v", outcome)
		}
	})
	t.Run("directories", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 20; i++ {
			if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%02d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.Options.MaxDirectories = 4 })
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Summary.DirectoriesExamined > 4 || outcome.Summary.Status != "bounded" || outcome.Summary.LimitReason == "" {
			t.Fatalf("directory bound = %+v", outcome.Summary)
		}
	})
	t.Run("results", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 4; i++ {
			initGitAt(t, filepath.Join(root, fmt.Sprintf("repo%d", i)), false)
		}
		outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) { req.Options.MaxResults = 2 })
		if err != nil {
			t.Fatal(err)
		}
		if len(outcome.Results) != 2 || outcome.Summary.Status != "bounded" {
			t.Fatalf("result bound = %+v", outcome)
		}
	})
}

func TestDiscoveryCancellationAndTimeout(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 200; i++ {
			if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%03d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		outcome, err := (DiscoveryScanner{}).Scan(ctx, DiscoveryScanRequest{Roots: []string{root}, Options: DefaultDiscoveryOptions(), OnProgress: func(progress DiscoveryProgress) {
			if progress.DirectoriesExamined >= 1 {
				cancel()
			}
		}})
		if !errors.Is(err, context.Canceled) || outcome.Summary.Status != "cancelled" {
			t.Fatalf("cancel = status %s err %v", outcome.Summary.Status, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		root := t.TempDir()
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		outcome, err := (DiscoveryScanner{}).Scan(ctx, DiscoveryScanRequest{Roots: []string{root}, Options: DefaultDiscoveryOptions()})
		if !errors.Is(err, context.DeadlineExceeded) || outcome.Summary.Status != "timed_out" {
			t.Fatalf("timeout = status %s err %v", outcome.Summary.Status, err)
		}
	})
	t.Run("configured timeout during active scan", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "child"), 0o755); err != nil {
			t.Fatal(err)
		}
		options := DefaultDiscoveryOptions()
		options.TimeoutSeconds = 1
		delayed := false
		outcome, err := (DiscoveryScanner{}).Scan(context.Background(), DiscoveryScanRequest{
			Roots:   []string{root},
			Options: options,
			OnProgress: func(progress DiscoveryProgress) {
				if progress.DirectoriesExamined == 1 && !delayed {
					delayed = true
					time.Sleep(1100 * time.Millisecond)
				}
			},
		})
		if !errors.Is(err, context.DeadlineExceeded) || outcome.Summary.Status != "timed_out" {
			t.Fatalf("configured timeout = status %s err %v", outcome.Summary.Status, err)
		}
	})
}

func TestDiscoveryPermissionErrorsContinue(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	repo := filepath.Join(root, "visible")
	initGitAt(t, repo, false)
	outcome, err := scanFixture(t, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Summary.PermissionErrors == 0 || len(outcome.Results) != 1 {
		t.Fatalf("permission continuation = %+v", outcome)
	}
}

func TestDiscoveryProgressReportsRealCounters(t *testing.T) {
	root := t.TempDir()
	initGitAt(t, filepath.Join(root, "repo"), false)
	var snapshots []DiscoveryProgress
	outcome, err := scanFixture(t, root, func(req *DiscoveryScanRequest) {
		req.OnProgress = func(progress DiscoveryProgress) { snapshots = append(snapshots, progress) }
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 3 || snapshots[0].Status != "running" || snapshots[len(snapshots)-1].Status != "completed" {
		t.Fatalf("progress = %+v", snapshots)
	}
	if snapshots[len(snapshots)-1].RepositoriesFound != len(outcome.Results) {
		t.Fatal("progress result count is not real")
	}
}

func TestDiscoveryContinuesWhenOneApprovedRootDisappears(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	vanishing := filepath.Join(home, "vanishing")
	remaining := filepath.Join(home, "remaining")
	if err := os.Mkdir(vanishing, 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(remaining, "repo")
	initGitAt(t, repo, false)
	renamed := filepath.Join(home, "moved-after-validation")
	moved := false
	outcome, err := (DiscoveryScanner{}).Scan(context.Background(), DiscoveryScanRequest{
		Roots:   []string{vanishing, remaining},
		Options: DefaultDiscoveryOptions(),
		OnProgress: func(progress DiscoveryProgress) {
			if !moved {
				moved = true
				if err := os.Rename(vanishing, renamed); err != nil {
					t.Fatalf("rename validated root: %v", err)
				}
			}
		},
	})
	if err != nil {
		t.Fatalf("one disappeared root terminated the scan: %v", err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Path != repo {
		t.Fatalf("remaining root was not scanned: %+v", outcome)
	}
	if len(outcome.ScannedRoots) != 1 || outcome.ScannedRoots[0] != remaining {
		t.Fatalf("scanned-root provenance = %#v", outcome.ScannedRoots)
	}
}

func TestDiscoveryStableIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo")
	if discoveryID(path) != discoveryID(path) || !strings.HasPrefix(discoveryID(path), "disc_") {
		t.Fatal("discovery id is unstable")
	}
}
