package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestReposAddRemoveViaCLI(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "repos.json")
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"repos", "add", "-config", cfg, "-name", "fixture", dir}); err != nil {
		t.Fatalf("repos add: %v", err)
	}
	r, err := registry.Load(cfg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	repos := r.List()
	if len(repos) != 1 || repos[0].Name != "fixture" {
		t.Fatalf("registry after add: %+v", repos)
	}
	if err := run([]string{"repos", "edit", "-config", cfg, "-name", "renamed", "-group", "core", "-tags", "beta,alpha", "-color", "cyan", repos[0].ID}); err != nil {
		t.Fatalf("repos edit: %v", err)
	}
	r, _ = registry.Load(cfg)
	edited, _ := r.Get(repos[0].ID)
	if edited.Name != "renamed" || edited.Group != "core" || strings.Join(edited.Tags, ",") != "alpha,beta" || edited.Color != "cyan" {
		t.Fatalf("edited metadata = %+v", edited)
	}
	if err := run([]string{"repos", "validate", "-config", cfg, repos[0].ID}); err != nil {
		t.Fatalf("repos validate: %v", err)
	}
	if err := run([]string{"repos", "refresh", "-config", cfg}); err != nil {
		t.Fatalf("repos refresh: %v", err)
	}

	if err := run([]string{"repos", "disable", "-config", cfg, repos[0].ID}); err != nil {
		t.Fatalf("repos disable: %v", err)
	}
	r, _ = registry.Load(cfg)
	if got, _ := r.Get(repos[0].ID); got.Enabled {
		t.Fatal("disable not persisted")
	}

	if err := run([]string{"repos", "remove", "-config", cfg, repos[0].ID}); err != nil {
		t.Fatalf("repos remove: %v", err)
	}
	r, _ = registry.Load(cfg)
	if len(r.List()) != 0 {
		t.Fatal("remove not persisted")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("repository contents were touched by remove")
	}
}

func TestReposRejectsUnsafePathViaCLI(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "repos.json")
	home := t.TempDir()
	t.Setenv("HOME", home)
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o700); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"repos", "add", "-config", cfg, ssh})
	if err == nil || !strings.Contains(err.Error(), "unsafe repository path") {
		t.Fatalf("denied dir accepted by CLI: %v", err)
	}
	if _, statErr := os.Stat(cfg); !os.IsNotExist(statErr) {
		t.Fatal("registry file created despite rejected add")
	}
}

func TestReposUnknownSubcommand(t *testing.T) {
	if err := run([]string{"repos", "explode"}); err == nil {
		t.Fatal("unknown subcommand accepted")
	}
}

func TestReposMutationFailsClosedWhileAnotherProcessOwnsState(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "repos.json")
	repo := t.TempDir()
	lock, err := registry.AcquireOwnerLock(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := run([]string{"repos", "add", "-config", cfg, repo}); !errors.Is(err, registry.ErrOwnerStateBusy) {
		t.Fatalf("concurrent CLI mutation was not blocked: %v", err)
	}
	if _, err := os.Stat(cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("blocked mutation changed registry: %v", err)
	}
}
