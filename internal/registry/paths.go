package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafePath marks a repository path rejected by the safety rules. The
// rules fail closed: only explicitly registered, canonicalized directories
// outside the deny list are observable.
var ErrUnsafePath = errors.New("unsafe repository path")

// deniedHomeSubdirs are never registrable: they hold credentials, keys,
// browser profiles, or agent session logs rather than repositories.
var deniedHomeSubdirs = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker",
	".config/gcloud", ".mozilla", ".thunderbird", ".password-store",
	".pki", ".claude", ".codex",
}

// CanonicalRepoPath resolves p to an absolute, symlink-free directory path
// and applies the deny rules. Everything stored or compared in the registry
// goes through here, so a symlink cannot smuggle a denied location in.
func CanonicalRepoPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafePath, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: not a directory: %s", ErrUnsafePath, resolved)
	}
	if err := ValidateCanonicalRepoPath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

// ValidateCanonicalRepoPath applies registry deny rules without resolving or
// opening p. Discovery uses it only after a root-confined, no-symlink metadata
// validation, avoiding a second path resolution race at final approval.
func ValidateCanonicalRepoPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("%w: path is not absolute and canonical", ErrUnsafePath)
	}
	if p == string(filepath.Separator) {
		return fmt.Errorf("%w: filesystem root", ErrUnsafePath)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if canonicalHome, herr := filepath.EvalSymlinks(home); herr == nil {
			home = canonicalHome
		}
		if p == home {
			return fmt.Errorf("%w: home directory itself", ErrUnsafePath)
		}
		for _, d := range deniedHomeSubdirs {
			denied := filepath.Join(home, d)
			if p == denied || strings.HasPrefix(p, denied+string(filepath.Separator)) {
				return fmt.Errorf("%w: inside denied directory %s", ErrUnsafePath, filepath.Join("~", d))
			}
		}
	}
	return nil
}

// WithinCanonical compares paths that have already passed their caller's
// canonical/rooted validation. It deliberately performs no filesystem access.
func WithinCanonical(root, path string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) || filepath.Clean(root) != root || filepath.Clean(path) != path {
		return false
	}
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// Within reports whether path is root or inside it, after both are
// canonicalized. It is the guard later layers use before touching any file a
// log or API request names.
func Within(root, path string) bool {
	croot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	cpath, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	return cpath == croot || strings.HasPrefix(cpath, croot+string(filepath.Separator))
}
