//go:build linux

package registry

import (
	"os"

	"golang.org/x/sys/unix"
)

// mountIDOfFile detects bind mounts even when st_dev is unchanged. A zero
// return means the running kernel/filesystem did not provide STATX_MNT_ID and
// callers retain the conservative device-number fallback.
func mountIDOfFile(file *os.File) uint64 {
	if file == nil {
		return 0
	}
	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0
	}
	return stat.Mnt_id
}
