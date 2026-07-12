package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/adapter/claudecode"
	"github.com/cosmtrek/mindwalk/internal/adapter/codex"
	"github.com/cosmtrek/mindwalk/internal/citymap"
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
	dev := fs.Bool("dev", false, "prefer web/dist from the working tree")
	noOpen := fs.Bool("no-open", false, "serve without opening a browser")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, Dev: *dev}).Start(!*noOpen)
}

func open(args []string) error {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	port := fs.Int("port", 0, "port to bind on 127.0.0.1")
	claudeDir := fs.String("claude-dir", claudecode.DefaultDir(), "Claude Code projects directory")
	codexDir := fs.String("codex-dir", codex.DefaultDir(), "Codex sessions directory")
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
	return server.New(server.Config{Port: *port, ClaudeDir: *claudeDir, CodexDir: *codexDir, OpenSession: session}).Start(!*noOpen)
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

func parseTrace(path string) (*model.Trace, error) {
	var lastErr error
	for _, source := range []adapter.Source{claudecode.Adapter{}, codex.Adapter{}} {
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
  mindwalk serve [--port N] [--no-open] [--claude-dir DIR] [--codex-dir DIR]
  mindwalk open [--no-open] <session.jsonl> open a specific Claude Code or Codex session
  mindwalk map [--no-open] <repo>  open the repository citymap with no session
  mindwalk build <repo> [-o out]  write citymap.json
  mindwalk trace <session> [-o out] write trace.json`)
}
