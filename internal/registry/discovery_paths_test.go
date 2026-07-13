package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalScanRootAllowsExplicitHomeAndCanonicalizes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	child := filepath.Join(home, "Code")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "code-link")
	if err := os.Symlink(child, link); err != nil {
		t.Fatal(err)
	}

	gotHome, err := CanonicalScanRoot(home)
	if err != nil || gotHome != home {
		t.Fatalf("home root = %q, %v", gotHome, err)
	}
	gotChild, err := CanonicalScanRoot(link)
	if err != nil || gotChild != child {
		t.Fatalf("symlinked selected root = %q, %v", gotChild, err)
	}
}

func TestCanonicalScanRootRejectsSystemCredentialAndAppPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	protected := filepath.Join(home, "private-app")
	for _, path := range []string{filepath.Join(home, ".ssh"), protected} {
		if err := os.MkdirAll(filepath.Join(path, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, path string
		extra      []string
	}{
		{"filesystem root", "/", nil},
		{"root home", "/root", nil},
		{"system", "/etc", nil},
		{"system parent", "/var", nil},
		{"credential", filepath.Join(home, ".ssh", "nested"), nil},
		{"application", filepath.Join(protected, "nested"), []string{protected}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CanonicalScanRoot(tc.path, tc.extra...); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("accepted %s: %v", tc.path, err)
			}
		})
	}
}

func TestDefaultDiscoveryExclusionsAreLockedAndIncludePrivatePaths(t *testing.T) {
	home := t.TempDir()
	private := filepath.Join(home, "observatory-private")
	rules := DefaultDiscoveryExclusions(home, private)
	wantNames := map[string]bool{"node_modules": false, ".cache": false, ".ssh": false, "vendor": false}
	foundPrivate := false
	for _, rule := range rules {
		if !rule.Locked {
			t.Fatalf("default exclusion is not locked: %+v", rule)
		}
		if _, ok := wantNames[rule.Basename]; ok {
			wantNames[rule.Basename] = true
		}
		if rule.Path == private {
			foundPrivate = true
		}
	}
	for name, found := range wantNames {
		if !found {
			t.Errorf("missing exclusion %q", name)
		}
	}
	if !foundPrivate {
		t.Fatal("private application path not excluded")
	}
}
