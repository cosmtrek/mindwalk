package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalRepoPathResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real-repo")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link-repo")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalRepoPath(link)
	if err != nil {
		t.Fatalf("CanonicalRepoPath: %v", err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got != want {
		t.Fatalf("symlink not resolved: got %s, want %s", got, want)
	}
}

func TestCanonicalRepoPathRejections(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "keys"), 0o700); err != nil {
		t.Fatal(err)
	}
	fileInHome := filepath.Join(home, "notes.txt")
	if err := os.WriteFile(fileInHome, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"empty", "   "},
		{"nonexistent", filepath.Join(home, "missing")},
		{"regular file", fileInHome},
		{"filesystem root", "/"},
		{"home itself", home},
		{"denied .ssh", sshDir},
		{"denied nested under .ssh", filepath.Join(sshDir, "keys")},
		{"denied .claude sessions", claudeDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CanonicalRepoPath(tc.path); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("got %v, want ErrUnsafePath", err)
			}
		})
	}
}

func TestCanonicalRepoPathSymlinkCannotSmuggleDeniedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sneaky := filepath.Join(outside, "innocent-name")
	if err := os.Symlink(filepath.Join(home, ".ssh"), sneaky); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalRepoPath(sneaky); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink into denied dir accepted: %v", err)
	}
}

func TestWithin(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()

	if !Within(root, sub) {
		t.Fatal("nested path reported outside root")
	}
	if !Within(root, root) {
		t.Fatal("root reported outside itself")
	}
	if Within(root, outside) {
		t.Fatal("sibling temp dir reported inside root")
	}
	if Within(root, filepath.Join(root, "..")) {
		t.Fatal("parent traversal reported inside root")
	}

	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	if Within(root, escape) {
		t.Fatal("symlink escape reported inside root")
	}
}
