package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

func TestRepositoryAPIWorkflowAndPersistence(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config", "repos.json")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Config{RegistryPath: config})
	h := s.Handler()

	empty := apiRequest(t, h, http.MethodGet, "/api/repositories", "", false)
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("empty repositories = %d %s", empty.Code, empty.Body.String())
	}
	if empty.Header().Get("Content-Security-Policy") == "" || empty.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %#v", empty.Header())
	}

	denied := apiRequest(t, h, http.MethodPost, "/api/repositories", `{"path":`+jsonString(repo)+`}`, false)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("mutation without CSRF header = %d", denied.Code)
	}

	created := apiRequest(t, h, http.MethodPost, "/api/repositories", `{"path":`+jsonString(repo)+`,"name":"Fixture","group":"core","tags":["beta","alpha"],"color":"cyan"}`, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var status registry.Status
	if err := json.Unmarshal(created.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Repo.Name != "Fixture" || status.Repo.Group != "core" || status.Repo.Color != "cyan" || strings.Join(status.Repo.Tags, ",") != "alpha,beta" {
		t.Fatalf("created status = %+v", status)
	}
	if info, err := os.Stat(config); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("persisted registry mode: info=%v err=%v", info, err)
	}

	patch := `{"name":"Renamed","group":"owner","tags":["local"],"color":"violet","enabled":false}`
	updated := apiRequest(t, h, http.MethodPatch, "/api/repositories/"+status.Repo.ID, patch, true)
	if updated.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", updated.Code, updated.Body.String())
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Repo.Name != "Renamed" || status.Repo.Enabled || status.Repo.Group != "owner" {
		t.Fatalf("updated status = %+v", status)
	}

	disabledMap := apiRequest(t, h, http.MethodGet, "/api/repositories/"+status.Repo.ID+"/citymap", "", false)
	if disabledMap.Code != http.StatusConflict {
		t.Fatalf("disabled citymap = %d", disabledMap.Code)
	}
	enabled := apiRequest(t, h, http.MethodPatch, "/api/repositories/"+status.Repo.ID, `{"enabled":true}`, true)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable = %d %s", enabled.Code, enabled.Body.String())
	}
	city := apiRequest(t, h, http.MethodGet, "/api/repositories/"+status.Repo.ID+"/citymap", "", false)
	if city.Code != http.StatusOK || !strings.Contains(city.Body.String(), `"main.go"`) {
		t.Fatalf("citymap = %d %s", city.Code, city.Body.String())
	}

	deleted := apiRequest(t, h, http.MethodDelete, "/api/repositories/"+status.Repo.ID, "", true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "main.go")); err != nil {
		t.Fatalf("delete touched repository: %v", err)
	}
	reloaded, err := registry.Load(config)
	if err != nil || len(reloaded.List()) != 0 {
		t.Fatalf("delete not persisted: repos=%v err=%v", reloaded.List(), err)
	}
}

func TestRepositoryAPIRejectsCrossOriginAndOversizedRequests(t *testing.T) {
	s := New(Config{RegistryPath: filepath.Join(t.TempDir(), "repos.json")})
	h := s.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/repositories", strings.NewReader(`{"path":"/tmp"}`))
	req.Host = "127.0.0.1:8765"
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("X-Mindwalk-CSRF", "1")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("cross-origin mutation = %d", resp.Code)
	}

	large := `{"path":"` + strings.Repeat("x", maxRepositoryRequestBytes) + `"}`
	oversized := apiRequest(t, h, http.MethodPost, "/api/repositories", large, true)
	if oversized.Code != http.StatusBadRequest {
		t.Fatalf("oversized request = %d %s", oversized.Code, oversized.Body.String())
	}
}

func TestRepositoryAPIMutationFailsClosedWhileAnotherProcessOwnsState(t *testing.T) {
	config := filepath.Join(t.TempDir(), "repos.json")
	lock, err := registry.AcquireOwnerLock(config)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	h := New(Config{RegistryPath: config}).Handler()
	response := apiRequest(t, h, http.MethodPost, "/api/repositories", `{"path":`+jsonString(t.TempDir())+`}`, true)
	if response.Code != http.StatusConflict {
		t.Fatalf("concurrent API mutation = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("blocked API mutation changed registry: %v", err)
	}
}

func apiRequest(t *testing.T, h http.Handler, method, path, body string, mutate bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Host = "127.0.0.1:8765"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate {
		req.Header.Set("X-Mindwalk-CSRF", "1")
		req.Header.Set("Origin", "http://127.0.0.1:8765")
	}
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	return resp
}

func jsonString(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}
