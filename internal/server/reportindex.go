package server

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/judge"
	"github.com/cosmtrek/mindwalk/internal/model"
)

// reportIndexTTL bounds how stale the directory listing may get. The reports
// directory is also written by the CLI, so the listing cannot be trusted for
// long; 5s matches the session-list polling cadence.
const reportIndexTTL = 5 * time.Second

// reportIndex keeps the session list's report lookups off the disk hot path.
// Most sessions have no report: one ReadDir per TTL answers "which sessions
// have a report at all" instead of a stat per session per request, and
// per-key loads are memoized against the report file's fingerprint so a
// report is re-read only when it changed. Same-process writers must call
// markPresent after storing a report — the TTL only covers writers this
// server cannot see.
type reportIndex struct {
	mu      sync.Mutex
	scanned time.Time
	present map[string]bool
	loaded  map[string]reportMemo
}

type reportMemo struct {
	fingerprint fileFingerprint
	report      *model.Report
}

// load returns the cached report for key, or nil when none exists. The cache
// is passed per call so a Dir override after construction is observed.
func (ri *reportIndex) load(cache judge.Cache, key string) *model.Report {
	if cache.Dir == "" || key == "" {
		return nil
	}
	ri.mu.Lock()
	defer ri.mu.Unlock()
	if time.Since(ri.scanned) >= reportIndexTTL {
		ri.present = map[string]bool{}
		if entries, err := os.ReadDir(cache.Dir); err == nil {
			for _, entry := range entries {
				if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
					ri.present[name] = true
				}
			}
		}
		for key := range ri.loaded {
			if !ri.present[key] {
				delete(ri.loaded, key)
			}
		}
		ri.scanned = time.Now()
	}
	if !ri.present[key] {
		return nil
	}
	fingerprint, err := fingerprintFile(cache.Path(key))
	if err != nil {
		delete(ri.loaded, key)
		return nil
	}
	if memo, ok := ri.loaded[key]; ok && memo.fingerprint.equal(fingerprint) {
		return memo.report
	}
	report := cache.Load(key)
	if ri.loaded == nil {
		ri.loaded = map[string]reportMemo{}
	}
	ri.loaded[key] = reportMemo{fingerprint: fingerprint, report: report}
	return report
}

// markPresent records that a report for key now exists on disk, so a load
// within the listing TTL finds it. Without this, a poll landing right after
// the server persisted a report — and after the job entry was dropped —
// would read "no report" until the next directory scan.
func (ri *reportIndex) markPresent(key string) {
	ri.mu.Lock()
	defer ri.mu.Unlock()
	if ri.present == nil {
		ri.present = map[string]bool{}
	}
	ri.present[key] = true
}
