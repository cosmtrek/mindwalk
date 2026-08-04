package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/model"
)

// The raw and snapshot layers share a key space but must never share entries:
// a session loaded through both APIs is parsed once per layer and each layer
// serves its own cached value afterwards.
func TestTraceStoreLayersAreIndependent(t *testing.T) {
	session := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(session, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := model.SessionMeta{Key: "s", Harness: "test", Path: session}

	snapshotLoads, rawLoads := 0, 0
	snapshotTrace := &model.Trace{Session: model.TraceSession{ID: "projected"}}
	rawTrace := &model.Trace{Session: model.TraceSession{ID: "raw"}}
	city := &model.CityMap{}
	ts := newTraceStore(
		func(model.SessionMeta) (*model.Trace, *model.CityMap, error) {
			snapshotLoads++
			return snapshotTrace, city, nil
		},
		func(model.SessionMeta) (*model.Trace, error) {
			rawLoads++
			return rawTrace, nil
		},
	)

	for i := 0; i < 2; i++ {
		trace, gotCity, err := ts.LoadSnapshot(meta)
		if err != nil || trace != snapshotTrace || gotCity != city {
			t.Fatalf("snapshot load %d: trace=%v city=%v err=%v", i, trace, gotCity, err)
		}
	}
	for i := 0; i < 2; i++ {
		trace, err := ts.LoadRaw(meta)
		if err != nil || trace != rawTrace {
			t.Fatalf("raw load %d: trace=%v err=%v", i, trace, err)
		}
	}
	if snapshotLoads != 1 || rawLoads != 1 {
		t.Fatalf("loads = snapshot:%d raw:%d, want 1 each", snapshotLoads, rawLoads)
	}
}
