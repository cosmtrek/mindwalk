package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStoreRoundtripDeleteAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	store, err := NewStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state := State{SchemaVersion: StateVersion, Path: "/synthetic/session.jsonl", Offset: 42}
	if err := store.Save("source-1", state); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Load("source-1")
	if err != nil || !ok || got != state {
		t.Fatalf("load = %+v ok=%v err=%v", got, ok, err)
	}
	info, err := os.Stat(filepath.Join(root, "tail-state.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions info=%v err=%v", info, err)
	}
	if err := store.Delete("source-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load("source-1"); err != nil || ok {
		t.Fatalf("deleted state still present ok=%v err=%v", ok, err)
	}
}

func TestStateStoreRejectsInvalidStateAndSymlinkFile(t *testing.T) {
	root := t.TempDir()
	store, err := NewStateStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("bad", State{SchemaVersion: 99, Path: "/tmp/source"}); err == nil {
		t.Fatal("invalid state persisted")
	}

	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"schemaVersion":1,"sources":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tail-state.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load("source"); err == nil {
		t.Fatal("symlink state file followed")
	}
}

func TestStateStoreRejectsCorruptAndUnknownSchema(t *testing.T) {
	for _, contents := range []string{"not-json", `{"schemaVersion":99,"sources":{}}`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "tail-state.json"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		store, err := NewStateStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Load("source"); err == nil {
			t.Fatalf("invalid state file accepted: %s", contents)
		}
	}
}
