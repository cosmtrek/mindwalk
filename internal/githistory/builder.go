// Package githistory turns a repository's commit history into a mindwalk trace:
// one commit becomes one event, and the files changed in that commit become its
// targets (all with an "edit" touch). The result flows through the exact same
// playback pipeline as a coding-agent session — the citymap builder, ghost-file
// handling, stats, and the frontend all treat it as an ordinary trace.
package githistory

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

// Harness is the trace source identifier for git-history traces.
const Harness = "git"

// Builder produces a trace from a repository's git history.
type Builder struct{}

// record separator between commits in the git log output; a byte sequence that
// will not appear in commit metadata or file paths.
const commitSep = "\x1e" // ASCII record separator

// maxCommits bounds how much history is walked so a very large monorepo can't
// buffer hundreds of MB of git output or produce a multi-thousand-event trace
// that the playback UI can't sensibly scrub. The most recent commits are kept
// (git log is newest-first before --reverse). Raise if deep history matters.
const maxCommits = 5000

// Build walks the repository's git history oldest-to-newest and returns a trace
// with one event per commit (capped at maxCommits, most-recent kept). It returns
// an error if the path is not a git repository or has no commits.
func (Builder) Build(repoRoot string) (*model.Trace, error) {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}

	// %H commit, %cI committer date (RFC3339), %s subject, %an author.
	// --numstat -z gives per-file "added\tdeleted\tpath" entries (NUL-separated),
	// so each commit carries per-file churn (lines added+deleted). Commits are
	// separated by our record separator so we can split reliably even when a
	// subject contains newlines. --max-count bounds memory on large histories;
	// git applies the limit before --reverse, so we keep the newest commits.
	format := commitSep + "%H%x00%cI%x00%an%x00%s%x00"
	cmd := exec.Command("git", "-C", abs,
		"log", "--reverse", "--no-color", fmt.Sprintf("--max-count=%d", maxCommits),
		"--format="+format, "--numstat", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git log failed in %q: %s", repoRoot, msg)
		}
		return nil, fmt.Errorf("git log failed (is %q a git repository?): %w", repoRoot, err)
	}

	commits := parseLog(out)
	if len(commits) == 0 {
		return nil, fmt.Errorf("no commits found in %q", repoRoot)
	}

	trace := &model.Trace{
		Version: 1,
		Session: model.TraceSession{
			ID:      "git-history",
			Harness: Harness,
			Cwd:     abs,
			Path:    abs,
		},
		Events: make([]model.Event, 0, len(commits)+1),
		Marks:  []model.Mark{},
	}

	// seed event: the empty repository at t=0. Playback opens here (dark, nothing
	// lit) so the first commit reads as the first change — the repo growing from
	// nothing — rather than the starting frame already being populated.
	trace.Events = append(trace.Events, model.Event{
		Tool:      "start",
		Action:    "other",
		Targets:   []model.Target{},
		Timestamp: commits[0].date,
		Summary:   "empty repository",
	})

	for _, c := range commits {
		// merge and empty commits touch no files; they still get an event (with
		// empty targets) so the timeline count matches `git log`
		targets := make([]model.Target, 0, len(c.files))
		commitLOC := 0
		for _, f := range c.files {
			// carry the file's churn in this commit (added+deleted) in Lines as a
			// single [added, deleted] pair, so the terrain view can color each
			// file by the size of the commit that touched it
			targets = append(targets, model.Target{
				Path:  f.path,
				Touch: "edit",
				Lines: [][2]int{{f.added, f.deleted}},
			})
			commitLOC += f.added + f.deleted
		}
		summary := c.subject
		if c.author != "" {
			summary = fmt.Sprintf("%s — %s", c.subject, c.author)
		}
		trace.Events = append(trace.Events, model.Event{
			Timestamp:   c.date,
			Tool:        "commit",
			Action:      "edit",
			Targets:     targets,
			ResultBytes: commitLOC, // total lines changed in the commit
			Summary:     summary,
		})
	}

	// dense 0..n-1 seq, matching what every adapter guarantees downstream
	for i := range trace.Events {
		trace.Events[i].Seq = i
	}

	// committer dates aren't guaranteed monotonic under rebase/cherry-pick, so
	// derive the session span from the min/max rather than the first/last commit
	startedAt, endedAt := commits[0].date, commits[0].date
	for _, c := range commits {
		if c.date < startedAt {
			startedAt = c.date
		}
		if c.date > endedAt {
			endedAt = c.date
		}
	}
	trace.Session.EventCount = len(trace.Events)
	trace.Session.StartedAt = startedAt
	trace.Session.EndedAt = endedAt
	trace.Session.Commit = shortHash(commits[len(commits)-1].hash)
	trace.Session.Title = fmt.Sprintf("git history — %d commit%s", len(commits), plural(len(commits)))

	// a git trace has no error signal and no read/search distinction; commits
	// are exact facts, so grade the observability accordingly. filesInRepo=0
	// here — the server recomputes stats once the citymap file count is known.
	trace.Stats = model.ComputeStats(trace, 0, model.ObservabilityExact)

	return trace, nil
}

// SessionKey returns the cache identity for a repo's history trace, matching the
// scheme used for real sessions.
func SessionKey(repoRoot string) string {
	return adapter.SessionKey(Harness, repoRoot)
}

type changedFile struct {
	path    string
	added   int
	deleted int
}

type commit struct {
	hash    string
	date    string
	author  string
	subject string
	files   []changedFile
}

// parseLog parses `git log --format=<sep>%H\0%cI\0%an\0%s\0 --numstat -z`.
// Records are separated by commitSep; within a record the header has four
// NUL-terminated fields followed by zero or more NUL-terminated numstat entries.
func parseLog(out []byte) []commit {
	var commits []commit
	// split on the record separator; the first chunk before the first separator
	// is empty and skipped
	for _, chunk := range bytes.Split(out, []byte(commitSep)) {
		if len(chunk) == 0 {
			continue
		}
		fields := splitNUL(chunk)
		if len(fields) < 4 {
			continue
		}
		c := commit{
			hash:    strings.TrimSpace(fields[0]),
			date:    strings.TrimSpace(fields[1]),
			author:  strings.TrimSpace(fields[2]),
			subject: fields[3],
		}
		c.files = parseNumstat(fields[4:])
		commits = append(commits, c)
	}
	return commits
}

// parseNumstat consumes the NUL-separated numstat entries that follow the commit
// header. Each entry is "added\tdeleted\tpath" (path may be empty for a rename,
// in which case the next two NUL fields are the old and new paths). Binary files
// report "-\t-\t..." which we treat as 0 churn.
func parseNumstat(fields []string) []changedFile {
	var files []changedFile
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if strings.TrimSpace(entry) == "" {
			continue
		}
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		added := parseCount(parts[0])
		deleted := parseCount(parts[1])
		path := parts[2]
		if path == "" {
			// rename/copy: the path is empty here; the following two NUL fields
			// are old and new paths — record the new one
			if i+2 < len(fields) {
				path = fields[i+2]
				i += 2
			} else if i+1 < len(fields) {
				path = fields[i+1]
				i++
			}
		}
		if path == "" {
			continue
		}
		files = append(files, changedFile{path: filepath.ToSlash(path), added: added, deleted: deleted})
	}
	return files
}

// parseCount reads a numstat count; "-" (binary files) and junk become 0.
func parseCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func splitNUL(b []byte) []string {
	parts := bytes.Split(b, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	// a trailing NUL leaves an empty final element; drop it so field counts are
	// predictable
	if n := len(out); n > 0 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
