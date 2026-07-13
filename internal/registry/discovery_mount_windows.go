//go:build windows

package registry

import (
	"os"

	"golang.org/x/sys/windows"
)

// Windows volume serials provide the device-boundary fallback used to avoid
// traversing into another mounted/removable/network volume.
func mountIDOfFile(file *os.File) uint64 {
	if file == nil {
		return 0
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0
	}
	return uint64(info.VolumeSerialNumber)
}
