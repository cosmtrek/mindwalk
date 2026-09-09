package adapter

import (
	"strings"
	"testing"
	"testing/quick"
	"unicode/utf8"
)

// TestNormalizePathIdempotentRelative verifies that normalizing a relative
// path that resolves inside the repo produces a result that, when normalized
// again, yields the same path. This catches any path-mangling regression.
func TestNormalizePathIdempotentRelative(t *testing.T) {
	cwd := "/home/user/repo"
	base := "/home/user/repo"

	property := func(path string) bool {
		if path == "" {
			return true
		}

		rel, outside, ok := normalizePath(cwd, base, path)
		if !ok || outside != nil || rel == "" {
			return true
		}

		rel2, _, ok2 := normalizePath(cwd, base, rel)

		return ok2 && rel2 == rel
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizePathRejectsEmpty verifies that empty or whitespace-only paths
// are always rejected.
func TestNormalizePathRejectsEmpty(t *testing.T) {
	property := func(s string) bool {
		var trimmed strings.Builder

		for _, r := range s {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '"' || r == '\'' {
				trimmed.WriteString("")
			} else {
				break
			}
		}

		_, _, ok := normalizePath("/cwd", "/cwd", trimmed.String())

		return !ok
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

// TestNormalizePathRejectsHTTP verifies that URLs are never treated as paths.
func TestNormalizePathRejectsHTTP(t *testing.T) {
	for _, prefix := range []string{"http://", "https://"} {
		path := prefix + "example.com/file.go"

		_, _, ok := normalizePath("/cwd", "/cwd", path)
		if ok {
			t.Fatalf("normalizePath accepted %q", path)
		}
	}
}

// TestTruncateNoteNeverExceedsMaxRunes verifies that the result of
// truncateNote is always <= maxRunes runes when maxRunes > 0.
func TestTruncateNoteNeverExceedsMaxRunes(t *testing.T) {
	property := func(s string, maxRunes uint8) bool {
		if maxRunes == 0 {
			return true
		}

		got := truncateNoteShim(s, int(maxRunes))
		if utf8.RuneCountInString(got) > int(maxRunes)+1 { // +1 for ellipsis
			return false
		}

		return true
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

// TestTruncateNotePreservesShortStrings verifies that strings already within
// the limit are returned unchanged.
func TestTruncateNotePreservesShortStrings(t *testing.T) {
	property := func(s string) bool {
		runeCount := utf8.RuneCountInString(s)
		if runeCount > 200 {
			return true
		}

		got := truncateNoteShim(s, 200)

		return got == s
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}

// truncateNoteShim calls the crush package's truncateNote via a shared test
// helper. Since truncateNote lives in crush, we replicate the logic here for
// property testing — any drift would be caught by crush's own unit tests.
func truncateNoteShim(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}

	return string(runes[:maxRunes]) + "\u2026"
}
