package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRepoDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRegistryAddGetListRemove(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "repos.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	dir := newRepoDir(t)
	repo, err := r.Add(dir, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if repo.Name != "proj" {
		t.Fatalf("default name = %q, want proj", repo.Name)
	}
	if !strings.HasPrefix(repo.ID, "repo_") || !repo.Enabled {
		t.Fatalf("unexpected repo record: %+v", repo)
	}
	if _, err := r.Add(dir, "again"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate Add: got %v, want ErrExists", err)
	}
	got, err := r.Get(repo.ID)
	if err != nil || got.Path != repo.Path {
		t.Fatalf("Get: %v %+v", err, got)
	}
	if err := r.SetEnabled(repo.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if got, _ := r.Get(repo.ID); got.Enabled {
		t.Fatal("disable did not stick")
	}
	if err := r.Remove(repo.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := r.Get(repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Remove: got %v, want ErrNotFound", err)
	}
	if err := r.Remove(repo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double Remove: got %v, want ErrNotFound", err)
	}
}

func TestRegistryUpdateOwnerMetadata(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "repos.json"))
	repo, err := r.Add(newRepoDir(t), "before")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Update(repo.ID, "after", "core", "violet", []string{"zeta", "alpha"}); err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get(repo.ID)
	if got.Name != "after" || got.Group != "core" || got.Color != "violet" || strings.Join(got.Tags, ",") != "alpha,zeta" {
		t.Fatalf("metadata update = %+v", got)
	}
	if err := r.Update(repo.ID, "", "", "", nil); err == nil {
		t.Fatal("empty display name accepted")
	}
}

func TestRegistrySaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "repos.json")
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := newRepoDir(t)
	b := newRepoDir(t)
	ra, _ := r.Add(a, "alpha")
	if _, err := r.Add(b, "beta"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.SetEnabled(ra.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if err := r.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("saved file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("registry perms %o, want 600", perm)
	}

	re, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	repos := re.List()
	if len(repos) != 2 || repos[0].Name != "alpha" || repos[1].Name != "beta" {
		t.Fatalf("roundtrip lost repos: %+v", repos)
	}
	if repos[0].Enabled || !repos[1].Enabled {
		t.Fatal("enabled flags lost in roundtrip")
	}
}

func TestRegistryLoadRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repos.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"repos":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown schemaVersion accepted")
	}
}

func TestRegistryLoadRejectsTamperedCanonicalIdentity(t *testing.T) {
	dir := newRepoDir(t)
	path := filepath.Join(t.TempDir(), "repos.json")
	b := []byte(`{"schemaVersion":1,"repos":[{"id":"repo_badbadbadbad","name":"fixture","path":"` + dir + `","enabled":true,"addedAt":"2026-07-13T10:00:00Z"}]}`)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("tampered path/id pair accepted")
	}
}

func TestStatusOfMissingRepo(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "repos.json"))
	dir := newRepoDir(t)
	repo, err := r.Add(dir, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	st, err := r.StatusOf(repo.ID)
	if err != nil {
		t.Fatalf("StatusOf: %v", err)
	}
	if !st.Missing {
		t.Fatal("deleted repo dir not reported missing")
	}
}

func TestStatusRejectsPathReplacedBySymlink(t *testing.T) {
	r, _ := Load(filepath.Join(t.TempDir(), "repos.json"))
	dir := newRepoDir(t)
	repo, err := r.Add(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	st, err := r.StatusOf(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.InvalidPath || st.Git.IsGit {
		t.Fatalf("symlink replacement not blocked: %+v", st)
	}
}

func TestDefaultPathUsesConfigDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	p, err := DefaultPath("mindwalk-observatory")
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(cfg, "mindwalk-observatory", "repos.json")
	if p != want {
		t.Fatalf("DefaultPath = %s, want %s", p, want)
	}
}
