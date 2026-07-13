package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/registry"
)

const maxRepositoryRequestBytes = 64 << 10

type repositoryMutation struct {
	Path    string   `json:"path"`
	Name    *string  `json:"name"`
	Group   *string  `json:"group"`
	Tags    []string `json:"tags"`
	Color   *string  `json:"color"`
	Enabled *bool    `json:"enabled"`
}

func (s *Server) handleRepositories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.registryMu.Lock()
		defer s.registryMu.Unlock()
		reg, err := registry.Load(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		statuses := make([]registry.Status, 0, len(reg.List()))
		for _, repo := range reg.List() {
			status, err := reg.StatusOf(repo.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			statuses = append(statuses, status)
		}
		writeJSON(w, statuses)
	case http.MethodPost:
		if !allowMutation(w, r) {
			return
		}
		var req repositoryMutation
		if !decodeBoundedJSON(w, r, &req) {
			return
		}
		ownerLock, err := registry.AcquireOwnerLock(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		defer ownerLock.Close()
		s.registryMu.Lock()
		defer s.registryMu.Unlock()
		reg, err := registry.Load(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		name := ""
		if req.Name != nil {
			name = strings.TrimSpace(*req.Name)
		}
		repo, err := reg.Add(req.Path, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		group, color := "", ""
		if req.Group != nil {
			group = strings.TrimSpace(*req.Group)
		}
		if req.Color != nil {
			color = strings.TrimSpace(*req.Color)
		}
		if err := reg.Update(repo.ID, repo.Name, group, color, cleanTags(req.Tags)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Enabled != nil {
			if err := reg.SetEnabled(repo.ID, *req.Enabled); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := reg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status, _ := reg.StatusOf(repo.ID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, status)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRepositoryResource(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/repositories/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid repository id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 {
		s.handleRepositoryAction(w, r, id, parts[1])
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.writeRepositoryStatus(w, id)
	case http.MethodPatch:
		if !allowMutation(w, r) {
			return
		}
		var req repositoryMutation
		if !decodeBoundedJSON(w, r, &req) {
			return
		}
		ownerLock, err := registry.AcquireOwnerLock(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		defer ownerLock.Close()
		s.registryMu.Lock()
		defer s.registryMu.Unlock()
		reg, err := registry.Load(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		repo, err := reg.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if req.Path != "" {
			http.Error(w, "repository paths are immutable; remove and re-add a moved repository", http.StatusBadRequest)
			return
		}
		if req.Name != nil {
			repo.Name = strings.TrimSpace(*req.Name)
		}
		if req.Group != nil {
			repo.Group = strings.TrimSpace(*req.Group)
		}
		if req.Color != nil {
			repo.Color = strings.TrimSpace(*req.Color)
		}
		if req.Tags != nil {
			repo.Tags = cleanTags(req.Tags)
		}
		if err := reg.Update(repo.ID, repo.Name, repo.Group, repo.Color, repo.Tags); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Enabled != nil {
			if err := reg.SetEnabled(repo.ID, *req.Enabled); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := reg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status, _ := reg.StatusOf(repo.ID)
		writeJSON(w, status)
	case http.MethodDelete:
		if !allowMutation(w, r) {
			return
		}
		ownerLock, err := registry.AcquireOwnerLock(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		defer ownerLock.Close()
		s.registryMu.Lock()
		defer s.registryMu.Unlock()
		reg, err := registry.Load(s.cfg.RegistryPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := reg.Remove(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err := reg.Save(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PATCH, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRepositoryAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if action == "refresh" {
		s.writeRepositoryStatus(w, id)
		return
	}
	if action != "citymap" {
		http.NotFound(w, r)
		return
	}
	s.registryMu.Lock()
	reg, err := registry.Load(s.cfg.RegistryPath)
	if err != nil {
		s.registryMu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := reg.StatusOf(id)
	s.registryMu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !status.Repo.Enabled || status.Missing || status.InvalidPath {
		http.Error(w, "repository is disabled or unavailable", http.StatusConflict)
		return
	}
	city, err := s.repoCityMap(status.Repo.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, city)
}

func (s *Server) writeRepositoryStatus(w http.ResponseWriter, id string) {
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	reg, err := registry.Load(s.cfg.RegistryPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	status, err := reg.StatusOf(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}

func allowMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-Mindwalk-CSRF") != "1" {
		http.Error(w, "missing CSRF protection header", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "cross-origin mutation denied", http.StatusForbidden)
			return false
		}
	}
	return true
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRepositoryRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON: multiple values or trailing content", http.StatusBadRequest)
		return false
	}
	return true
}

func cleanTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
