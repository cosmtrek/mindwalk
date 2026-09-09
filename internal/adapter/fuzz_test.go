package adapter

import "testing"

// FuzzGitDiffTargets verifies that arbitrary text input never panics
// or duplicates targets in the diff parser.
func FuzzGitDiffTargets(f *testing.F) {
	f.Add("diff --git a/foo.go b/foo.go\n+++ b/foo.go\n")
	f.Add("")
	f.Add("--- a/old\n+++ b/new\n@@ -1,3 +1,4 @@\n")
	f.Add("no diff content at all")
	f.Fuzz(func(t *testing.T, text string) {
		targets := gitDiffTargets(text)

		seen := map[string]bool{}
		for _, target := range targets {
			if seen[target.path] {
				t.Fatalf("duplicate path %q in gitDiffTargets output", target.path)
			}

			seen[target.path] = true
		}
	})
}
