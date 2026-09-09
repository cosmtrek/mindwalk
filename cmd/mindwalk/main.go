package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/adapter/crush"
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
	case "doctor":
		return doctor(args[1:])
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
	crushDir := fs.String("crush-dir", "", "Crush data directory override (containing crush.db); empty = auto-discover")
	noCrush := fs.Bool("no-crush", false, "disable the Crush adapter (skip the per-project .crush scan)")
	dev := fs.Bool("dev", false, "prefer web/dist from the working tree")
	noOpen := fs.Bool("no-open", false, "serve without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, PiDir: *piDir, CrushDir: *crushDir, DisableCrush: *noCrush, Dev: *dev})
	defer srv.Close()
	return runWithSignalShutdown(srv, !*noOpen)
}

func open(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	port := fs.Int("port", 0, "port to bind on 127.0.0.1")
	claudeDir := fs.String("claude-dir", claudecode.DefaultDir(), "Claude Code projects directory")
	codexDir := fs.String("codex-dir", codex.DefaultDir(), "Codex sessions directory")
	piDir := fs.String("pi-dir", pi.DefaultDir(), "pi sessions directory")
	crushDir := fs.String("crush-dir", "", "Crush data directory override (containing crush.db); empty = auto-discover")
	noCrush := fs.Bool("no-crush", false, "disable the Crush adapter (skip the per-project .crush scan)")
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
	srv := server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, PiDir: *piDir, CrushDir: *crushDir, DisableCrush: *noCrush, OpenSession: session})
	defer srv.Close()
	return runWithSignalShutdown(srv, !*noOpen)
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
	srv := server.New(server.Config{Port: *port, Dev: *dev, RepoRoot: repo, MapOnly: true})
	defer srv.Close()
	return runWithSignalShutdown(srv, !*noOpen)
}

const shutdownGracePeriod = 5 * time.Second

// runWithSignalShutdown starts the server with the given browser
// flag and arranges for SIGINT/SIGTERM to trigger a graceful
// shutdown. Previously Start blocked indefinitely on http.Serve and
// ignored signals, leaving in-flight SSE handlers alive across
// shell session restarts. We give in-flight handlers a 5-second
// window to drain before exiting.
func runWithSignalShutdown(srv *server.Server, openBrowser bool) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(openBrowser) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-sigCh:
		fmt.Fprintf(os.Stderr, "mindwalk: received %s, shutting down\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "mindwalk: shutdown: %v\n", err)
		}
		// Wait for Start to return (it does immediately after
		// Shutdown completes) so we don't leak the goroutine.
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case <-time.After(2 * time.Second):
			return nil
		}
	}
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
	crushDir := ""
	for i, arg := range positional {
		if arg == "--crush-dir" && i+1 < len(positional) {
			crushDir = positional[i+1]
			positional = append(positional[:i], positional[i+2:]...)
			break
		}
	}
	if len(positional) != 1 {
		return fmt.Errorf("usage: mindwalk trace [--crush-dir DIR] <session> [-o out]")
	}
	tr, err := parseTrace(positional[0], crushDir)
	if err != nil {
		return fmt.Errorf("parse trace %s: %w", positional[0], err)
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
	crushDir := fs.String("crush-dir", "", "Crush data directory override (containing crush.db); empty = auto-discover")
	noCrush := fs.Bool("no-crush", false, "disable the Crush adapter (skip the per-project .crush scan)")
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
	session := positional[0]
	// crush:// selectors are synthetic handles, not filesystem paths.
	if !strings.HasPrefix(session, "crush://") {
		var err error
		session, err = filepath.Abs(positional[0])
		if err != nil {
			return err
		}
	}
	tr, err := parseTrace(session, crushDirFor(*crushDir, *noCrush))
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

func parseTrace(path string, crushDir string) (*model.Trace, error) {
	var lastErr error
	for _, source := range traceSources(crushDir) {
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

// traceSources returns the adapter sources parseTrace will try, in
// order. An empty crushDir means "auto-discover"; a non-empty value pins
// a specific installation.
func traceSources(crushDir string) []adapter.Source {
	sources := []adapter.Source{claudecode.Adapter{}, codex.Adapter{}, pi.Adapter{}}
	if crushDir == "" {
		return append(sources, crush.Adapter{})
	}
	return append(sources, crush.Adapter{Dir: crushDir})
}

// crushDirFor resolves the crush directory from the --crush-dir and
// --no-crush flag values. --no-crush is signalled by passing a path that
// cannot possibly exist, so the Crush adapter finds nothing to read.
func crushDirFor(override string, noCrush bool) string {
	if noCrush {
		return "/dev/null/mindwalk-no-crush"
	}
	return override
}

// doctorSources builds the adapter sources the doctor command inspects,
// honouring the same --crush-dir/--no-crush flags as serve.
func doctorSources(claudeDir, codexDir, piDir, crushDir string, noCrush bool) []adapter.Source {
	sources := []adapter.Source{
		claudecode.Adapter{Dir: claudeDir},
		codex.Adapter{Dir: codexDir},
		pi.Adapter{Dir: piDir},
	}
	if noCrush {
		return sources
	}
	return append(sources, crush.NewAdapter(crushDir))
}

// closeSources closes any source that implements adapter.Closer. Safe
// to call on mixed source lists where only some adapters hold open
// resources (e.g. crush's database handles).
func closeSources(srcs []adapter.Source) {
	for _, src := range srcs {
		if c, ok := src.(adapter.Closer); ok {
			_ = c.Close()
		}
	}
}

// doctor prints adapter status, data-directory paths, session counts,
// and diagnostic checks so users can verify their configuration and
// troubleshoot issues.
func doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	claudeDir := fs.String("claude-dir", claudecode.DefaultDir(), "Claude Code projects directory")
	codexDir := fs.String("codex-dir", codex.DefaultDir(), "Codex sessions directory")
	piDir := fs.String("pi-dir", pi.DefaultDir(), "pi sessions directory")
	crushDir := fs.String("crush-dir", "", "Crush data directory override (containing crush.db); empty = auto-discover")
	noCrush := fs.Bool("no-crush", false, "disable the Crush adapter (skip the per-project .crush scan)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	srcs := doctorSources(*claudeDir, *codexDir, *piDir, *crushDir, *noCrush)
	defer closeSources(srcs)

	for _, src := range srcs {
		harness := src.Harness()
		metas, err := src.ListSessions()
		status := "ok"
		count := 0
		if err != nil {
			status = "error: " + err.Error()
		} else {
			count = len(metas)
		}
		dirStatus := ""
		if dir := src.SessionDir(); dir != "" {
			if adapter.ReadableDir(dir) {
				dirStatus = " [dir ok]"
			} else {
				dirStatus = " [dir missing]"
			}
		}
		fmt.Printf("%-8s  sessions=%-4d  %s%s\n", harness, count, status, dirStatus)
		if diag, ok := src.(adapter.DiagnosticsSource); ok {
			for _, check := range diag.Diagnostics() {
				fmt.Printf("         %-16s  %-5s  %s\n", check.Name, check.Status, check.Detail)
			}
		}
	}

	fmt.Println()
	fmt.Println("Data directories:")
	fmt.Printf("  claude-dir  %s\n", *claudeDir)
	fmt.Printf("  codex-dir   %s\n", *codexDir)
	fmt.Printf("  pi-dir      %s\n", *piDir)
	if *noCrush {
		fmt.Printf("  crush       disabled\n")
	} else if *crushDir != "" {
		fmt.Printf("  crush-dir   %s\n", *crushDir)
	} else {
		fmt.Printf("  crush       auto-discover\n")
	}
	return nil
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
