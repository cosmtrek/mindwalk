//go:build !linux && !windows

package registry

import "os"

func mountIDOfFile(_ *os.File) uint64 { return 0 }
