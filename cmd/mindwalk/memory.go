package main

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/cosmtrek/mindwalk/internal/brain"
	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/server"
)

func memoryCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		return memoryList(args[1:])
	case "add":
		return memoryAdd(args[1:])
	case "search":
		return memorySearch(args[1:])
	case "correct":
		return memoryCorrect(args[1:])
	case "tombstone":
		return memoryTombstone(args[1:])
	default:
		return fmt.Errorf("unknown memory subcommand %q", args[0])
	}
}

func memoryFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("memory "+name, flag.ContinueOnError)
	root, _ := server.DefaultDataRoot()
	data := fs.String("data-dir", filepath.Join(root, "brain"), "local second-brain data directory")
	return fs, data
}

func memoryList(args []string) error {
	fs, data := memoryFlags("list")
	all := fs.Bool("all", false, "include tombstones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := brain.Open(*data)
	if err != nil {
		return err
	}
	memories, err := store.List(*all)
	if err != nil {
		return err
	}
	return writeJSON("", memories)
}

func memoryAdd(args []string) error {
	fs, data := memoryFlags("add")
	namespace := fs.String("namespace", "general", "memory namespace")
	title := fs.String("title", "", "memory title")
	body := fs.String("body", "", "memory body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" || fs.NArg() != 0 {
		return errors.New("usage: mindwalk memory add [-namespace NS] -title TITLE [-body BODY]")
	}
	store, err := brain.Open(*data)
	if err != nil {
		return err
	}
	memory, err := store.Create(*namespace, *title, *body, ownerMemoryProvenance())
	if err != nil {
		return err
	}
	return writeJSON("", memory)
}

func memorySearch(args []string) error {
	fs, data := memoryFlags("search")
	namespace := fs.String("namespace", "", "optional namespace filter")
	limit := fs.Int("limit", 20, "maximum results (1-100)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mindwalk memory search [flags] QUERY")
	}
	store, err := brain.Open(*data)
	if err != nil {
		return err
	}
	results, err := store.Search(fs.Arg(0), *namespace, *limit)
	if err != nil {
		return err
	}
	return writeJSON("", results)
}

func memoryCorrect(args []string) error {
	fs, data := memoryFlags("correct")
	title := fs.String("title", "", "corrected title")
	body := fs.String("body", "", "corrected body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || *title == "" {
		return errors.New("usage: mindwalk memory correct -title TITLE [-body BODY] MEMORY_ID")
	}
	store, err := brain.Open(*data)
	if err != nil {
		return err
	}
	memory, err := store.Correct(fs.Arg(0), *title, *body, ownerMemoryProvenance())
	if err != nil {
		return err
	}
	return writeJSON("", memory)
}

func memoryTombstone(args []string) error {
	fs, data := memoryFlags("tombstone")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: mindwalk memory tombstone MEMORY_ID")
	}
	store, err := brain.Open(*data)
	if err != nil {
		return err
	}
	memory, err := store.Tombstone(fs.Arg(0), ownerMemoryProvenance())
	if err != nil {
		return err
	}
	return writeJSON("", memory)
}

func ownerMemoryProvenance() event.Provenance {
	confidence := float64(1)
	return event.Provenance{SourceType: "owner", SourceName: "mindwalk-cli", Quality: event.QualityExact, Confidence: &confidence}
}
