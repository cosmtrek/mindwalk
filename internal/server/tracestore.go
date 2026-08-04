package server

import (
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/adapter"
	"github.com/cosmtrek/mindwalk/internal/model"
)

// traceStore owns every parsed-session cache behind one lock. It keeps two
// layers with the same key space (session key) but different value semantics:
//
//   - snapshots: the trace projected onto its own citymap (file IDs assigned,
//     stats recomputed against the repo file count) plus that citymap — what
//     the session views serve.
//   - raws: the trace exactly as the adapter parsed it, no projection — what
//     subagent views load before projecting onto the *root* session's city.
//
// Keeping the layers apart means a projected snapshot can never be
// re-projected and a raw trace can never be served where file IDs are
// expected. Keys are session keys; the source-file fingerprint is a
// validation field on the entry — never part of the key — so one session
// occupies at most one slot per layer.
type traceStore struct {
	mu        sync.Mutex
	snapshots map[string]*traceEntry
	raws      map[string]*traceEntry
	inflight  map[string]*inflightLoad

	// Injected by the server: parse-and-project for snapshots, parse-only for
	// raws. Called without the store lock held.
	loadSnapshot func(model.SessionMeta) (*model.Trace, *model.CityMap, error)
	loadRaw      func(model.SessionMeta) (*model.Trace, error)
}

type traceEntry struct {
	trace       *model.Trace
	city        *model.CityMap // nil in the raw layer
	fingerprint fileFingerprint
	at          time.Time
	used        time.Time
}

type inflightLoad struct {
	done        chan struct{}
	fingerprint fileFingerprint
	trace       *model.Trace
	city        *model.CityMap
	err         error
}

func newTraceStore(loadSnapshot func(model.SessionMeta) (*model.Trace, *model.CityMap, error), loadRaw func(model.SessionMeta) (*model.Trace, error)) *traceStore {
	return &traceStore{
		snapshots:    map[string]*traceEntry{},
		raws:         map[string]*traceEntry{},
		inflight:     map[string]*inflightLoad{},
		loadSnapshot: loadSnapshot,
		loadRaw:      loadRaw,
	}
}

func (ts *traceStore) LoadSnapshot(meta model.SessionMeta) (*model.Trace, *model.CityMap, error) {
	return ts.load(ts.snapshots, "snapshot", meta, ts.loadSnapshot)
}

func (ts *traceStore) LoadRaw(meta model.SessionMeta) (*model.Trace, error) {
	trace, _, err := ts.load(ts.raws, "raw", meta, func(meta model.SessionMeta) (*model.Trace, *model.CityMap, error) {
		trace, err := ts.loadRaw(meta)
		return trace, nil, err
	})
	return trace, err
}

func (ts *traceStore) load(layer map[string]*traceEntry, kind string, meta model.SessionMeta, loader func(model.SessionMeta) (*model.Trace, *model.CityMap, error)) (*model.Trace, *model.CityMap, error) {
	key := meta.Key
	if key == "" {
		key = adapter.SessionKey(meta.Harness, meta.Path)
	}
	inflightKey := kind + "\x00" + key
	for {
		fingerprint, err := fingerprintFile(meta.Path)
		if err != nil {
			ts.mu.Lock()
			delete(layer, key)
			ts.mu.Unlock()
			return nil, nil, err
		}

		ts.mu.Lock()
		if entry := layer[key]; entry != nil {
			if entry.fingerprint.equal(fingerprint) && time.Since(entry.at) < traceCacheTTL {
				entry.used = time.Now()
				trace, city := entry.trace, entry.city
				ts.mu.Unlock()
				return trace, city, nil
			}
			delete(layer, key)
		}
		if load := ts.inflight[inflightKey]; load != nil {
			done := load.done
			shareSnapshot := fingerprint.equal(load.fingerprint)
			ts.mu.Unlock()
			<-done

			// Requests that observed the same source version must receive the
			// same trace/city snapshot, even if the active file grows while the
			// shared parse is running. A request that already observed a newer
			// version retries after the older load completes.
			if shareSnapshot {
				return load.trace, load.city, load.err
			}
			continue
		}
		load := &inflightLoad{done: make(chan struct{}), fingerprint: fingerprint}
		ts.inflight[inflightKey] = load
		ts.mu.Unlock()

		// Keep the pre-parse fingerprint. If the active session grows during
		// parsing, the next request will see a mismatch and reload it instead
		// of treating the partial snapshot as current.
		ts.run(layer, inflightKey, key, load, meta, loader)
		return load.trace, load.city, load.err
	}
}

// run executes the shared load for key and publishes the result on load. The
// finalize step — cache the result, drop the inflight entry, close load.done
// — runs in a defer so a panicking loader cannot skip it. Without that,
// net/http's per-connection recover would swallow the panic while the
// inflight entry stayed registered, and every later request for the key
// would block forever on a done channel nothing closes.
func (ts *traceStore) run(layer map[string]*traceEntry, inflightKey, key string, load *inflightLoad, meta model.SessionMeta, loader func(model.SessionMeta) (*model.Trace, *model.CityMap, error)) {
	defer func() {
		if r := recover(); r != nil {
			load.trace, load.city = nil, nil
			load.err = fmt.Errorf("load session %s: %v", key, r)
			log.Printf("mindwalk: panic loading session %s: %v\n%s", key, r, debug.Stack())
		}
		ts.mu.Lock()
		if load.err == nil {
			now := time.Now()
			layer[key] = &traceEntry{trace: load.trace, city: load.city, fingerprint: load.fingerprint, at: now, used: now}
			ts.evictLocked(layer)
		}
		delete(ts.inflight, inflightKey)
		close(load.done)
		ts.mu.Unlock()
	}()
	load.trace, load.city, load.err = loader(meta)
}

// evictLocked bounds one layer by dropping the least recently used entries
// once it grows past traceCacheMaxEntries. Caller must hold mu.
func (ts *traceStore) evictLocked(layer map[string]*traceEntry) {
	for len(layer) > traceCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range layer {
			if oldestKey == "" || entry.used.Before(oldest) {
				oldestKey = key
				oldest = entry.used
			}
		}
		if oldestKey == "" {
			return
		}
		delete(layer, oldestKey)
	}
}
