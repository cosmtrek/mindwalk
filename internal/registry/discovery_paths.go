package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoveryExclusion describes one directory rule shown to the owner. Locked
// rules protect credentials, caches, system data, and Observatory's own state.
type DiscoveryExclusion struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Path     string `json:"path,omitempty"`
	Basename string `json:"basename,omitempty"`
	Locked   bool   `json:"locked"`
}

var protectedScanRoots = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/media", "/mnt",
	"/proc", "/root", "/run", "/sbin", "/snap", "/sys", "/usr", "/var",
}

var lockedDiscoveryBasenames = []string{
	".aws", ".azure", ".cache", ".claude", ".codex", ".docker", ".gnupg",
	".kube", ".mozilla", ".password-store", ".pki", ".ssh", ".thunderbird",
	".gradle", ".m2", ".npm", ".venv", "__pycache__", "build", "dist",
	"node_modules", "target", "vendor", "venv",
}

// CanonicalScanRoot validates an explicitly owner-approved discovery root.
// Unlike CanonicalRepoPath it permits the owner's home directory. System,
// credential, and application-private roots remain fail-closed.
func CanonicalScanRoot(path string, extraProtectedPaths ...string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: empty scan root", ErrUnsafePath)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: scan root is not an accessible directory", ErrUnsafePath)
	}
	resolved = filepath.Clean(resolved)
	if resolved == string(filepath.Separator) {
		return "", fmt.Errorf("%w: filesystem root", ErrUnsafePath)
	}
	denied := append([]string(nil), protectedScanRoots...)
	home, _ := os.UserHomeDir()
	if home != "" {
		if canonical, evalErr := filepath.EvalSymlinks(home); evalErr == nil {
			home = canonical
		}
		for _, rel := range []string{
			".config/mindwalk-observatory", ".local/share/Trash",
			".local/share/mindwalk-observatory", ".local/share/keyrings",
			".config/gcloud", ".config/gh", ".config/1Password",
			".config/Bitwarden", ".config/keepassxc", ".mozilla", ".thunderbird",
			".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker",
			".password-store", ".pki", ".claude", ".codex",
		} {
			denied = append(denied, filepath.Join(home, filepath.FromSlash(rel)))
		}
	}
	denied = append(denied, extraProtectedPaths...)
	for _, candidate := range denied {
		canonical, ok := canonicalExistingOrClean(candidate)
		if ok && pathWithin(canonical, resolved) {
			return "", fmt.Errorf("%w: protected scan root %s", ErrUnsafePath, resolved)
		}
	}
	return resolved, nil
}

// DefaultDiscoveryExclusions returns the fixed, owner-visible exclusion set.
// Callers can add custom rules, but locked rules cannot be weakened.
func DefaultDiscoveryExclusions(home string, protectedPaths ...string) []DiscoveryExclusion {
	var out []DiscoveryExclusion
	for _, name := range lockedDiscoveryBasenames {
		out = append(out, DiscoveryExclusion{
			ID:       exclusionID("name:" + name),
			Label:    name,
			Basename: name,
			Locked:   true,
		})
	}
	paths := []string{}
	if home != "" {
		paths = append(paths,
			filepath.Join(home, ".local", "share", "Trash"),
			filepath.Join(home, ".local", "share", "keyrings"),
			filepath.Join(home, ".config", "chromium"),
			filepath.Join(home, ".config", "google-chrome"),
			filepath.Join(home, ".config", "microsoft-edge"),
			filepath.Join(home, ".config", "BraveSoftware"),
			filepath.Join(home, ".config", "gcloud"),
			filepath.Join(home, ".config", "gh"),
			filepath.Join(home, ".config", "1Password"),
			filepath.Join(home, ".config", "Bitwarden"),
			filepath.Join(home, ".config", "keepassxc"),
			filepath.Join(home, ".local", "share", "mindwalk-observatory"),
			filepath.Join(home, ".config", "mindwalk-observatory"),
		)
	}
	paths = append(paths, protectedPaths...)
	seen := map[string]bool{}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, DiscoveryExclusion{
			ID:     exclusionID("path:" + path),
			Label:  path,
			Path:   path,
			Locked: true,
		})
	}
	return out
}

func exclusionID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "excl_" + hex.EncodeToString(sum[:8])
}

func canonicalExistingOrClean(path string) (string, bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), true
	}
	return filepath.Clean(abs), true
}

func pathWithin(root, path string) bool {
	root, path = filepath.Clean(root), filepath.Clean(path)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
