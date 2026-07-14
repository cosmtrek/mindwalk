package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/brain"
)

func TestMemoryAPIExplicitMutationSearchCorrectionAndTombstone(t *testing.T) {
	s := New(Config{DataRoot: t.TempDir(), RegistryPath: t.TempDir() + "/repos.json", ClaudeDir: t.TempDir(), CodexDir: t.TempDir()})
	if s.ingestion != nil {
		t.Cleanup(func() { _ = s.ingestion.Close() })
	}
	request := func(method, target, body string, csrf bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
		if csrf {
			req.Header.Set("X-Mindwalk-CSRF", "1")
		}
		resp := httptest.NewRecorder()
		s.Handler().ServeHTTP(resp, req)
		return resp
	}
	if resp := request(http.MethodPost, "/api/memories", `{"namespace":"project/test","title":"Local FTS","body":"searchable token=synthetic-api-secret"}`, false); resp.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", resp.Code)
	}
	createdResp := request(http.MethodPost, "/api/memories", `{"namespace":"project/test","title":"Local FTS","body":"searchable token=synthetic-api-secret"}`, true)
	if createdResp.Code != http.StatusCreated || bytes.Contains(createdResp.Body.Bytes(), []byte("synthetic-api-secret")) {
		t.Fatalf("create = %d %s", createdResp.Code, createdResp.Body.String())
	}
	if contentType := createdResp.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("create content type = %q", contentType)
	}
	var created brain.Memory
	if err := json.Unmarshal(createdResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	search := request(http.MethodGet, "/api/memories/search?q=searchable", "", false)
	if search.Code != http.StatusOK || !bytes.Contains(search.Body.Bytes(), []byte(created.MemoryID)) {
		t.Fatalf("search = %d %s", search.Code, search.Body.String())
	}
	emptySearch := request(http.MethodGet, "/api/memories/search?q=absent", "", false)
	if emptySearch.Code != http.StatusOK || string(bytes.TrimSpace(emptySearch.Body.Bytes())) != "[]" {
		t.Fatalf("empty search = %d %s", emptySearch.Code, emptySearch.Body.String())
	}
	unsupported := request(http.MethodGet, "/api/memories/"+created.MemoryID, "", false)
	if unsupported.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported resource method = %d %s", unsupported.Code, unsupported.Body.String())
	}
	corrected := request(http.MethodPatch, "/api/memories/"+created.MemoryID, `{"title":"Corrected","body":"durable replacement"}`, true)
	if corrected.Code != http.StatusOK {
		t.Fatalf("correct = %d %s", corrected.Code, corrected.Body.String())
	}
	tombstone := request(http.MethodDelete, "/api/memories/"+created.MemoryID, "", true)
	var deleted brain.Memory
	if err := json.Unmarshal(tombstone.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if tombstone.Code != http.StatusOK || !deleted.Tombstoned {
		t.Fatalf("tombstone = %d %s", tombstone.Code, tombstone.Body.String())
	}
	list := request(http.MethodGet, "/api/memories", "", false)
	if list.Code != http.StatusOK || string(bytes.TrimSpace(list.Body.Bytes())) != "[]" {
		t.Fatalf("active list = %d %s", list.Code, list.Body.String())
	}
}
