package adapter

import "os"

// mindwalkHomeEnv is the environment variable that overrides the
// default ~/.mindwalk base directory. Tests set it to a tempdir for
// isolation; CI overrides it for hermetic builds.
const mindwalkHomeEnv = "MINDWALK_HOME"

// MindwalkHome resolves the mindwalk base directory used by every
// disk-backed cache (agent-graph cache, judge report cache). It honours
// the MINDWALK_HOME override and falls back to ~/.mindwalk when no
// override is set. Returns "" when no home directory can be resolved
// so callers can treat the empty result as "do not use the cache".
//
// Both the server (agent graph cache) and the CLI (cache subcommand)
// share this resolution so the MINDWALK_HOME contract — and any future
// extension like XDG path support — is enforced in one place.
func MindwalkHome() string {
	if override := os.Getenv(mindwalkHomeEnv); override != "" {
		return override
	}

	return HomePath(".mindwalk")
}
