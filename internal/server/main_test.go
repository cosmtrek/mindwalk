package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain redirects every data-directory env var the Crush and pi
// adapters consult at call time into a throwaway temp directory. This
// prevents any test that forgets to set CrushDir/PiDir from scanning
// the host's real ~/.local/share/crush/projects.json or ~/.pi tree,
// which turns a 2-second suite into a multi-minute one on a machine
// with many Crush project databases.
func TestMain(m *testing.M) {
	isolated, _ := os.MkdirTemp("", "mindwalk-test-*")
	_ = os.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, "crush"))
	_ = os.Setenv("XDG_DATA_HOME", filepath.Join(isolated, "xdg"))
	_ = os.Setenv("MINDWALK_HOME", filepath.Join(isolated, "mindwalk"))
	code := m.Run()
	_ = os.RemoveAll(isolated)

	os.Exit(code)
}
