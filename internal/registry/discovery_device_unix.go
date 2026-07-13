//go:build !windows

package registry

import (
	"os"
	"syscall"
)

func deviceOf(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev)
	}
	return 0
}
