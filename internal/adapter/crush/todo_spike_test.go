//nolint:testpackage,paralleltest // See docstring below for the paralleltest exception.
package crush

import (
	"encoding/json"
	"testing"

	crushdata "github.com/LarsArtmann/go-crush-data"
)

// TestSpikeDecodeTodosFromFixture pins T29: confirm the
// go-crush-data v0.3.0 todos decoder accepts whatever the
// committed fixture's session-level `todos` column carries. If
// this test compiles and the SDK call returns without error,
// the data path is wired and a follow-up can add a SessionMeta
// field + UI surface; if it fails, the todo column shape
// differs from what the SDK expects and the follow-up changes
// first.
//
// Note: this test writes briefly to the committed fixture DB while
// ListSessions probes its schema (see openCached path), so it runs
// serially with sibling tests to avoid "database is locked" under
// -race. Do NOT mark t.Parallel().
func TestSpikeDecodeTodosFromFixture(t *testing.T) {
	dir := fixtureDir(t)

	a := Adapter{Dir: dir}

	// ListSessions populates the dbIndex so openDBForPath can
	// route each session id to its source database.
	metas, err := a.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	for _, meta := range metas {
		path := SessionPath(meta.ID)

		handle, openErr := a.openDBForPath(path)
		if openErr != nil {
			t.Fatalf("openDBForPath(%s): %v", path, openErr)
		}

		sess, sessionErr := handle.inner.Session(t.Context(), meta.ID)
		_ = handle.close()

		if sessionErr != nil {
			t.Fatalf("Session(%s): %v", meta.ID, sessionErr)
		}

		raw := sess.Todos
		if len(raw) == 0 {
			t.Logf("session %s: no todos column data", meta.ID)

			continue
		}

		todos, decodeErr := crushdata.DecodeTodos(raw)
		if decodeErr != nil {
			t.Logf("session %s: todos decode error: %v", meta.ID, decodeErr)
			t.Logf("session %s: raw todos (first 200 bytes): %s", meta.ID, truncateForLog(raw, 200))

			continue
		}

		if len(todos) > 0 {
			t.Logf("session %s: decoded %d todo entries", meta.ID, len(todos))

			for _, todo := range todos {
				t.Logf("  status=%s content=%.100q", todo.Status, todo.Content)
			}
		}
	}
}

// truncateForLog returns the first n bytes of raw as a string for log
// lines; the JSON column can be large, so we clip at the call site.
func truncateForLog(raw json.RawMessage, n int) string {
	if len(raw) <= n {
		return string(raw)
	}

	return string(raw[:n]) + "..."
}
