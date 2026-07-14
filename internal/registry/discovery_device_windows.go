//go:build windows

package registry

import "os"

// Windows roots do not expose a Unix device number through os.FileInfo.
// Canonical containment and the locked no-symlink policy remain enforced.
func deviceOf(os.FileInfo) uint64 { return 0 }
