package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxGitMetadataBytes = 1 << 20

var errGitOutputLimit = errors.New("git metadata output exceeds limit")

// GitMeta is read-only observed git state; the registry never mutates a
// repository.
type GitMeta struct {
	IsGit     bool       `json:"isGit"`
	Root      string     `json:"root,omitempty"`
	Branch    string     `json:"branch,omitempty"`
	Commit    string     `json:"commit,omitempty"`
	Dirty     bool       `json:"dirty"`
	Remote    string     `json:"remote,omitempty"` // credentials stripped
	Worktrees []Worktree `json:"worktrees,omitempty"`
}

type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

// ReadGitMeta observes the repository's git state. A non-git directory
// yields IsGit=false rather than an error.
func ReadGitMeta(root string) GitMeta {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := runGit(ctx, root, "rev-parse", "--git-dir"); err != nil {
		return GitMeta{}
	}
	m := GitMeta{IsGit: true}
	if out, err := runGit(ctx, root, "rev-parse", "--show-toplevel"); err == nil {
		m.Root = strings.TrimSpace(string(out))
	}
	if out, err := runGit(ctx, root, "branch", "--show-current"); err == nil {
		m.Branch = strings.TrimSpace(string(out))
	}
	if out, err := runGit(ctx, root, "rev-parse", "--short", "HEAD"); err == nil {
		m.Commit = strings.TrimSpace(string(out))
	}
	if out, err := runGit(ctx, root, "status", "--porcelain", "--untracked-files=normal"); err == nil {
		m.Dirty = len(strings.TrimSpace(string(out))) > 0
	}
	if out, err := runGit(ctx, root, "remote", "get-url", "origin"); err == nil {
		m.Remote = stripCredentials(strings.TrimSpace(string(out)))
	}
	if out, err := runGit(ctx, root, "worktree", "list", "--porcelain"); err == nil {
		m.Worktrees = parseWorktrees(string(out))
	}
	return m
}

// runGit executes only caller-selected read commands with hooks, automatic
// maintenance, prompts, pagers, and optional locks disabled. A discovered
// repository can never make metadata inspection execute repository code.
func runGit(ctx context.Context, root string, args ...string) ([]byte, error) {
	base := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=false",
		"-c", "core.fsmonitor=false",
		"-c", "credential.helper=",
		"-c", "submodule.recurse=false",
		"-C", root,
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_PAGER=cat",
		"PAGER=cat",
	)
	var output boundedGitOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if output.truncated {
		return nil, errGitOutputLimit
	}
	return output.Bytes(), nil
}

type boundedGitOutput struct {
	bytes.Buffer
	truncated bool
}

func (w *boundedGitOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := maxGitMetadataBytes - w.Len()
	if remaining <= 0 {
		w.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		w.truncated = true
	}
	_, _ = w.Buffer.Write(p)
	return original, nil
}

// stripCredentials removes userinfo from remote URLs so an embedded token
// can never reach the registry, an API response, or a display.
func stripCredentials(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme == "" {
		// Scheme-less/scp-style remotes are not parsed as URLs. Drop query and
		// fragment suffixes anyway: they are unnecessary for display and can
		// carry tokens just like their URL counterparts.
		if sensitive := strings.IndexAny(remote, "?#"); sensitive >= 0 {
			remote = remote[:sensitive]
		}
		// Remove the user portion from scp-like syntax as well. It is not needed
		// for repository identification and may expose an owner account name.
		if at, colon := strings.LastIndex(remote, "@"), strings.Index(remote, ":"); at >= 0 && colon > at {
			return remote[at+1:]
		}
		return remote
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func parseWorktrees(out string) []Worktree {
	var trees []Worktree
	var cur Worktree
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			if cur.Path != "" {
				trees = append(trees, cur)
			}
			cur = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	if cur.Path != "" {
		trees = append(trees, cur)
	}
	return trees
}
