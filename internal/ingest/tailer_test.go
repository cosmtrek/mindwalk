package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailerIncrementalPartialAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("one\npart"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailer := NewTailer(path)
	first, err := tailer.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusGrew || strings.Join(byteStrings(first.Lines), ",") != "one" || tailer.State().Offset != 4 {
		t.Fatalf("first poll = %+v state=%+v", first, tailer.State())
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("ial\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := Resume(tailer.State())
	second, err := restarted.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(byteStrings(second.Lines), ",") != "partial,two" {
		t.Fatalf("resume lines = %q", byteStrings(second.Lines))
	}
	third, err := restarted.Poll()
	if err != nil || third.Status != StatusUnchanged || len(third.Lines) != 0 {
		t.Fatalf("unchanged poll = %+v err=%v", third, err)
	}
}

func TestTailerDetectsTruncationReplacementAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailer := NewTailer(path)
	if _, err := tailer.Poll(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	truncated, err := tailer.Poll()
	if err != nil || truncated.Status != StatusTruncated || tailer.State().Offset != 0 {
		t.Fatalf("truncation = %+v state=%+v err=%v", truncated, tailer.State(), err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := tailer.Poll(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("alt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replaced, err := tailer.Poll()
	if err != nil || replaced.Status != StatusReplaced || strings.Join(byteStrings(replaced.Lines), ",") != "alt" {
		t.Fatalf("replacement = %+v err=%v", replaced, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, err := tailer.Poll()
	if err != nil || missing.Status != StatusMissing {
		t.Fatalf("missing = %+v err=%v", missing, err)
	}
}

func TestTailerBoundsOversizedLinesAcrossPolls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("abcdef\nok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailer := NewTailer(path)
	tailer.MaxLineBytes = 4
	tailer.MaxPollBytes = 64
	poll, err := tailer.Poll()
	if err != nil {
		t.Fatal(err)
	}
	if len(poll.Oversized) != 1 || string(poll.Oversized[0]) != "abcdef" || strings.Join(byteStrings(poll.Lines), ",") != "ok" {
		t.Fatalf("bounded complete lines = %+v", poll)
	}

	longPath := filepath.Join(t.TempDir(), "long.jsonl")
	if err := os.WriteFile(longPath, []byte("abcdefghij\nok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := NewTailer(longPath)
	long.MaxLineBytes = 4
	long.MaxPollBytes = 6
	first, err := long.Poll()
	if err != nil || first.SkippedBytes != 6 || !long.State().Skipping {
		t.Fatalf("first long poll = %+v state=%+v err=%v", first, long.State(), err)
	}
	second, err := long.Poll()
	if err != nil || second.SkippedBytes != 5 || len(second.Lines) != 0 || long.State().Skipping {
		t.Fatalf("second long poll = %+v state=%+v err=%v", second, long.State(), err)
	}
	third, err := long.Poll()
	if err != nil || strings.Join(byteStrings(third.Lines), ",") != "ok" {
		t.Fatalf("third long poll = %+v state=%+v err=%v", third, long.State(), err)
	}
}

func TestTailerRejectsInvalidPersistedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []State{
		{SchemaVersion: 99, Path: path},
		{SchemaVersion: StateVersion, Path: path, Offset: -1},
		{SchemaVersion: StateVersion, Path: path, AnchorLen: 1, AnchorHash: "bad"},
	}
	for _, state := range cases {
		if _, err := Resume(state).Poll(); err == nil {
			t.Fatalf("invalid state accepted: %+v", state)
		}
	}
}

func byteStrings(lines [][]byte) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = string(line)
	}
	return out
}
