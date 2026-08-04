package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/adapter/pi"
	"github.com/cosmtrek/mindwalk/internal/citymap"
	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
	"github.com/cosmtrek/mindwalk/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mindwalk:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return serve(args)
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "open":
		return open(args[1:])
	case "map":
		return openMap(args[1:])
	case "build":
		return build(args[1:])
	case "trace":
		return trace(args[1:])
	case "analyze":
		return analyze(args[1:])
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "port to bind on 127.0.0.1")
	claudeDir := fs.String("claude-dir", claudecode.DefaultDir(), "Claude Code projects directory")
	codexDir := fs.String("codex-dir", codex.DefaultDir(), "Codex sessions directory")
	piDir := fs.String("pi-dir", pi.DefaultDir(), "pi sessions directory")
	dev := fs.Bool("dev", false, "prefer web/dist from the working tree")
	noOpen := fs.Bool("no-open", false, "serve without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, PiDir: *piDir, Dev: *dev}).Start(!*noOpen)
}

func open(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	port := fs.Int("port", 0, "port to bind on 127.0.0.1")
	claudeDir := fs.String("claude-dir", claudecode.DefaultDir(), "Claude Code projects directory")
	codexDir := fs.String("codex-dir", codex.DefaultDir(), "Codex sessions directory")
	piDir := fs.String("pi-dir", pi.DefaultDir(), "pi sessions directory")
	noOpen := fs.Bool("no-open", false, "serve without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk open [--no-open] <session.jsonl>")
	}
	session, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	return server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, PiDir: *piDir, OpenSession: session}).Start(!*noOpen)
}

func openMap(args []string) error {
	fs := flag.NewFlagSet("map", flag.ExitOnError)
	port := fs.Int("port", 0, "port to bind on 127.0.0.1")
	dev := fs.Bool("dev", false, "prefer web/dist from the working tree")
	noOpen := fs.Bool("no-open", false, "serve without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: mindwalk map [--no-open] <repo>")
	}
	repo, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return err
	}
	return server.New(server.Config{Port: *port, Dev: *dev, RepoRoot: repo, MapOnly: true}).Start(!*noOpen)
}

func build(args []string) error {
	positional, out, err := parseOutputArgs(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: mindwalk build <repo> [-o out]")
	}
	city, err := citymap.Builder{}.Build(positional[0], nil)
	if err != nil {
		return err
	}
	return writeJSON(out, city)
}

func trace(args []string) error {
	positional, out, err := parseOutputArgs(args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: mindwalk trace <session.jsonl> [-o out]")
	}
	tr, err := parseTrace(positional[0])
	if err != nil {
		return err
	}
	return writeJSON(out, tr)
}

// judgeMatches reports whether a cached report satisfies an explicit judge
// choice; unset flags match anything. A model matches on either the
// canonical name the run recorded (claude-sonnet-5) or the alias it was
// requested with (sonnet) — so repeating an aliased request hits the cache
// instead of paying for a fresh run every time.
func judgeMatches(report *model.Report, cli, modelName string) bool {
	if cli != "" && report.Judge.CLI != cli {
		return false
	}
	if modelName != "" && report.Judge.Model != modelName && report.Judge.RequestedModel != modelName {
		return false
	}
	return true
}

func analyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	out := fs.String("o", "", "write the report to this file instead of stdout")
	judgeCLI := fs.String("judge", "", "judge CLI to use: claude or codex (default: auto-detect)")
	judgeModel := fs.String("model", "", "judge model override, e.g. sonnet or gpt-5.6-sol (default: the CLI's default)")
	noCache := fs.Bool("no-cache", false, "re-run the judge even when a fresh cached report exists")
	noRubric := fs.Bool("no-rubric", false, "skip the task rubric layer: one dimensions-only judge call, bypassing the report cache")
	timeout := fs.Duration("timeout", judge.DefaultTimeout, "judge subprocess timeout")
	// Accept flags after the positional argument, matching trace/build.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: mindwalk analyze <session.jsonl> [-o out] [--judge claude|codex] [--model name] [--no-cache] [--no-rubric]")
	}
	session, err := filepath.Abs(positional[0])
	if err != nil {
		return err
	}
	tr, err := parseTrace(session)
	if err != nil {
		return err
	}

	cache := judge.Cache{Dir: judge.DefaultCacheDir()}
	key := adapter.SessionKey(tr.Session.Harness, session)
	// --no-cache means a fully fresh run: the cached report is neither
	// returned nor mined for a reusable rubric. --no-rubric bypasses the
	// cache in both directions — returning a cached rubric-ful report would
	// contradict the flag, and storing a rubric-less one would downgrade a
	// richer cache entry — so the flag always costs one fresh call.
	var cached *model.Report
	if !*noCache && !*noRubric {
		cached = cache.Load(key)
		// A rubric-enabled request is only answered from cache when the report
		// already settles the rubric question; a rubric-less fresh report gets
		// re-run rather than silently returned without the layer.
		if judge.FreshAgainstTrace(cached, tr) && judgeMatches(cached, *judgeCLI, *judgeModel) &&
			judge.RubricSatisfied(cached) {
			fmt.Fprintln(os.Stderr, "mindwalk: using cached report (pass --no-cache to re-run)")
			return writeJSON(*out, cached)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "mindwalk: judging %d events, this can take a minute or two…\n", tr.Session.EventCount)
	report, err := judge.Analyze(ctx, tr, judge.Options{CLI: *judgeCLI, Model: *judgeModel, NoRubric: *noRubric, CachedReport: cached})
	if err != nil {
		return err
	}
	if !*noRubric {
		if err := cache.Store(key, report); err != nil {
			fmt.Fprintln(os.Stderr, "mindwalk: report cache write failed:", err)
		}
	}
	return writeJSON(*out, report)
}

func parseTrace(path string) (*model.Trace, error) {
	var lastErr error
	for _, source := range []adapter.Source{claudecode.Adapter{}, codex.Adapter{}, pi.Adapter{}} {
		trace, err := source.Parse(path)
		if err == nil {
			return trace, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no session adapters configured")
}

func parseOutputArgs(args []string) ([]string, string, error) {
	var out string
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			i++
			if i >= len(args) {
				return nil, "", fmt.Errorf("%s requires a value", args[i-1])
			}
			out = args[i]
		default:
			positional = append(positional, args[i])
		}
	}
	return positional, out, nil
}

func writeJSON(out string, v any) error {
	var f *os.File
	var err error
	if out == "" {
		f = os.Stdout
	} else {
		f, err = os.Create(out)
		if err != nil {
			return err
		}
		defer f.Close()
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func usage() {
	fmt.Println(`mindwalk

Usage:
  mindwalk                        serve on a random local port and open the UI
  mindwalk serve [--port N] [--no-open] [--claude-dir DIR] [--codex-dir DIR] [--pi-dir DIR]
  mindwalk open [--no-open] <session.jsonl> open a specific Claude Code, Codex, or pi session
  mindwalk map [--no-open] <repo>  open the repository citymap with no session
  mindwalk build <repo> [-o out]  write citymap.json
  mindwalk trace <session> [-o out] write trace.json
  mindwalk analyze <session> [-o out] [--judge claude|codex] [--no-cache] [--no-rubric] evaluate a session with a local agent CLI`)
}
