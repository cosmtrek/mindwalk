package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/cosmtrek/mindwalk/internal/event"
)

type memoryMutation struct {
	Namespace string `json:"namespace"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrain(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		memories, err := s.brain.List(r.URL.Query().Get("all") == "1")
		if err != nil {
			http.Error(w, "memory ledger unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON(w, memories)
	case http.MethodPost:
		if !allowMutation(w, r) {
			return
		}
		var req memoryMutation
		if !decodeBoundedJSON(w, r, &req) {
			return
		}
		memory, err := s.brain.Create(req.Namespace, req.Title, req.Body, browserMemoryProvenance())
		if err != nil {
			http.Error(w, "invalid memory input", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, memory)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrain(w) {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	results, err := s.brain.Search(r.URL.Query().Get("q"), r.URL.Query().Get("namespace"), limit)
	if err != nil {
		http.Error(w, "memory search unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, results)
}

func (s *Server) handleMemoryResource(w http.ResponseWriter, r *http.Request) {
	if !s.requireBrain(w) {
		return
	}
	id, err := url.PathUnescape(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/memories/"), "/"))
	if err != nil || id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		if !allowMutation(w, r) {
			return
		}
		var req memoryMutation
		if !decodeBoundedJSON(w, r, &req) {
			return
		}
		memory, err := s.brain.Correct(id, req.Title, req.Body, browserMemoryProvenance())
		if err != nil {
			http.Error(w, "active memory not found or input invalid", http.StatusBadRequest)
			return
		}
		writeJSON(w, memory)
	case http.MethodDelete:
		if !allowMutation(w, r) {
			return
		}
		memory, err := s.brain.Tombstone(id, browserMemoryProvenance())
		if err != nil {
			http.Error(w, "active memory not found", http.StatusNotFound)
			return
		}
		writeJSON(w, memory)
	default:
		w.Header().Set("Allow", "PATCH, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) requireBrain(w http.ResponseWriter) bool {
	if s.brainErr != nil || s.brain == nil {
		http.Error(w, "local second brain is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func browserMemoryProvenance() event.Provenance {
	confidence := float64(1)
	return event.Provenance{SourceType: "owner", SourceName: "mindwalk-browser", Quality: event.QualityExact, Confidence: &confidence}
}
