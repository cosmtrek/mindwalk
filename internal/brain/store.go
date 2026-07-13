// Package brain implements the local second-brain ledger. Owner mutations are
// explicit, append-only, redacted before persistence, and projected into both
// readable Markdown and a rebuildable SQLite FTS5 index.
package brain

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cosmtrek/mindwalk/internal/event"
	"github.com/cosmtrek/mindwalk/internal/redact"
	_ "modernc.org/sqlite"
)

const (
	SchemaVersion = 1
	maxTitleBytes = 512
	maxBodyBytes  = 64 << 10
	maxLineBytes  = 128 << 10
)

type Record struct {
	SchemaVersion int              `json:"schemaVersion"`
	RecordID      string           `json:"recordId"`
	MemoryID      string           `json:"memoryId"`
	Action        string           `json:"action"`
	Namespace     string           `json:"namespace"`
	Title         string           `json:"title"`
	Body          string           `json:"body,omitempty"`
	OccurredAt    string           `json:"occurredAt"`
	Supersedes    *string          `json:"supersedes,omitempty"`
	Provenance    event.Provenance `json:"provenance"`
}

type Memory struct {
	MemoryID   string           `json:"memoryId"`
	RecordID   string           `json:"recordId"`
	Namespace  string           `json:"namespace"`
	Title      string           `json:"title"`
	Body       string           `json:"body,omitempty"`
	UpdatedAt  string           `json:"updatedAt"`
	Tombstoned bool             `json:"tombstoned"`
	Provenance event.Provenance `json:"provenance"`
}

type SearchResult struct {
	Memory Memory  `json:"memory"`
	Rank   float64 `json:"rank"`
}

type Store struct {
	root       string
	ledgerPath string
	dbPath     string
	markdown   string
	now        func() time.Time
	mu         sync.Mutex
}

func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("brain data root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	markdown := filepath.Join(canonical, "markdown")
	if err := os.MkdirAll(markdown, 0o700); err != nil {
		return nil, err
	}
	s := &Store{
		root: canonical, ledgerPath: filepath.Join(canonical, "memory.jsonl"),
		dbPath: filepath.Join(canonical, "memory.sqlite"), markdown: markdown, now: time.Now,
	}
	if err := s.rebuild(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Create(namespace, title, body string, provenance event.Provenance) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	namespace, title, body, err := cleanInput(namespace, title, body)
	if err != nil {
		return Memory{}, err
	}
	memories, _, err := s.readProjection()
	if err != nil {
		return Memory{}, err
	}
	for _, memory := range memories {
		if !memory.Tombstoned && memory.Namespace == namespace && memory.Title == title && memory.Body == body {
			return memory, nil
		}
	}
	record := Record{SchemaVersion: SchemaVersion, Action: "create", Namespace: namespace, Title: title, Body: body, OccurredAt: s.timestamp(), Provenance: provenance}
	record.MemoryID = memoryID(namespace, title, body)
	return s.appendAndProject(record)
}

func (s *Store) Correct(memoryIDValue, title, body string, provenance event.Provenance) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, _, err := s.readProjection()
	if err != nil {
		return Memory{}, err
	}
	current, ok := memories[memoryIDValue]
	if !ok || current.Tombstoned {
		return Memory{}, errors.New("active memory not found")
	}
	_, title, body, err = cleanInput(current.Namespace, title, body)
	if err != nil {
		return Memory{}, err
	}
	supersedes := current.RecordID
	record := Record{SchemaVersion: SchemaVersion, MemoryID: current.MemoryID, Action: "correct", Namespace: current.Namespace, Title: title, Body: body, OccurredAt: s.timestamp(), Supersedes: &supersedes, Provenance: provenance}
	return s.appendAndProject(record)
}

func (s *Store) Tombstone(memoryIDValue string, provenance event.Provenance) (Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, _, err := s.readProjection()
	if err != nil {
		return Memory{}, err
	}
	current, ok := memories[memoryIDValue]
	if !ok || current.Tombstoned {
		return Memory{}, errors.New("active memory not found")
	}
	supersedes := current.RecordID
	record := Record{SchemaVersion: SchemaVersion, MemoryID: current.MemoryID, Action: "tombstone", Namespace: current.Namespace, Title: current.Title, OccurredAt: s.timestamp(), Supersedes: &supersedes, Provenance: provenance}
	return s.appendAndProject(record)
}

func (s *Store) List(includeTombstones bool) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memories, _, err := s.readProjection()
	if err != nil {
		return nil, err
	}
	out := make([]Memory, 0, len(memories))
	for _, memory := range memories {
		if includeTombstones || !memory.Tombstoned {
			out = append(out, memory)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].MemoryID < out[j].MemoryID
	})
	return out, nil
}

func (s *Store) Search(query, namespace string, limit int) ([]SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	match := ftsQuery(query)
	if match == "" {
		return []SearchResult{}, nil
	}
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	statement := `SELECT memory_id, rank FROM memory_fts WHERE memory_fts MATCH ?`
	args := []any{match}
	if namespace != "" {
		statement += ` AND namespace = ?`
		args = append(args, namespace)
	}
	statement += ` ORDER BY rank, memory_id LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	memories, _, err := s.readProjection()
	if err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for rows.Next() {
		var id string
		var rank float64
		if err := rows.Scan(&id, &rank); err != nil {
			return nil, err
		}
		if memory, ok := memories[id]; ok && !memory.Tombstoned {
			out = append(out, SearchResult{Memory: memory, Rank: rank})
		}
	}
	return out, rows.Err()
}

func (s *Store) appendAndProject(record Record) (Memory, error) {
	if err := validateProvenance(record.Provenance); err != nil {
		return Memory{}, err
	}
	record.RecordID = recordID(record)
	b, err := json.Marshal(record)
	if err != nil {
		return Memory{}, err
	}
	f, err := os.OpenFile(s.ledgerPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Memory{}, err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return Memory{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return Memory{}, err
	}
	if err := f.Close(); err != nil {
		return Memory{}, err
	}
	memories, _, err := s.readProjection()
	if err != nil {
		return Memory{}, err
	}
	if err := s.writeMarkdown(memories[record.MemoryID]); err != nil {
		return Memory{}, err
	}
	if err := s.rebuildIndex(memories); err != nil {
		return Memory{}, err
	}
	return memories[record.MemoryID], nil
}

func (s *Store) rebuild() error {
	memories, _, err := s.readProjection()
	if err != nil {
		return err
	}
	for _, memory := range memories {
		if err := s.writeMarkdown(memory); err != nil {
			return err
		}
	}
	return s.rebuildIndex(memories)
}

func (s *Store) readProjection() (map[string]Memory, []Record, error) {
	memories := map[string]Memory{}
	f, err := os.Open(s.ledgerPath)
	if errors.Is(err, os.ErrNotExist) {
		return memories, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var records []Record
	for {
		line, readErr := r.ReadBytes('\n')
		if len(line) > 0 {
			if readErr == io.EOF || line[len(line)-1] != '\n' {
				return nil, nil, errors.New("memory ledger has a torn final record")
			}
			line = line[:len(line)-1]
			if len(line) > maxLineBytes {
				return nil, nil, errors.New("memory ledger record exceeds limit")
			}
			var record Record
			if err := json.Unmarshal(line, &record); err != nil || verifyRecord(record) != nil {
				return nil, nil, errors.New("memory ledger contains an invalid record")
			}
			current, exists := memories[record.MemoryID]
			if record.Action == "create" && exists {
				return nil, nil, errors.New("memory ledger contains a duplicate create")
			}
			if record.Action != "create" && (!exists || record.Namespace != current.Namespace || record.Supersedes == nil || *record.Supersedes != current.RecordID) {
				return nil, nil, errors.New("memory ledger correction chain is invalid")
			}
			memories[record.MemoryID] = Memory{MemoryID: record.MemoryID, RecordID: record.RecordID, Namespace: record.Namespace, Title: record.Title, Body: record.Body, UpdatedAt: record.OccurredAt, Tombstoned: record.Action == "tombstone", Provenance: record.Provenance}
			records = append(records, record)
		}
		if readErr == io.EOF {
			return memories, records, nil
		}
		if readErr != nil {
			return nil, nil, readErr
		}
	}
}

func (s *Store) rebuildIndex(memories map[string]Memory) error {
	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; DROP TABLE IF EXISTS memory_fts; CREATE VIRTUAL TABLE memory_fts USING fts5(memory_id UNINDEXED, namespace, title, body, tokenize='unicode61');`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	statement, err := tx.Prepare(`INSERT INTO memory_fts(memory_id, namespace, title, body) VALUES (?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, memory := range memories {
		if memory.Tombstoned {
			continue
		}
		if _, err := statement.Exec(memory.MemoryID, memory.Namespace, memory.Title, memory.Body); err != nil {
			statement.Close()
			tx.Rollback()
			return err
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return os.Chmod(s.dbPath, 0o600)
}

func (s *Store) writeMarkdown(memory Memory) error {
	path := filepath.Join(s.markdown, memory.MemoryID+".md")
	status := "active"
	if memory.Tombstoned {
		status = "tombstoned"
	}
	contents := fmt.Sprintf("---\nmemory_id: %s\nnamespace: %s\nstatus: %s\nupdated_at: %s\n---\n\n# %s\n\n%s\n", memory.MemoryID, memory.Namespace, status, memory.UpdatedAt, memory.Title, memory.Body)
	tmp, err := os.CreateTemp(s.markdown, ".memory-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(contents); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func cleanInput(namespace, title, body string) (string, string, string, error) {
	namespace = strings.TrimSpace(namespace)
	title = strings.TrimSpace(title)
	if namespace == "" || title == "" || len(namespace) > 128 || len(title) > maxTitleBytes || len(body) > maxBodyBytes {
		return "", "", "", errors.New("invalid memory namespace, title, or body size")
	}
	if strings.ContainsAny(namespace, "\r\n") || strings.ContainsAny(title, "\r\n") {
		return "", "", "", errors.New("memory namespace and title must be one line")
	}
	namespace, _ = redact.String(namespace)
	title, _ = redact.String(title)
	body, _ = redact.String(body)
	return namespace, title, body, nil
}

func validateProvenance(provenance event.Provenance) error {
	if provenance.SourceType == "" || !event.ValidQuality(provenance.Quality) || (provenance.Quality == event.QualityDerived && provenance.Explanation == "") {
		return errors.New("valid memory provenance is required")
	}
	if provenance.Confidence != nil && (*provenance.Confidence < 0 || *provenance.Confidence > 1) {
		return errors.New("memory provenance confidence must be between zero and one")
	}
	return nil
}

func recordID(record Record) string {
	record.RecordID = ""
	b, _ := json.Marshal(record)
	sum := sha256.Sum256(b)
	return "mrec_" + hex.EncodeToString(sum[:16])
}

func verifyRecord(record Record) error {
	if record.SchemaVersion != SchemaVersion || record.RecordID != recordID(record) || record.MemoryID == "" || record.Namespace == "" || record.Title == "" {
		return errors.New("invalid memory record")
	}
	if record.Action != "create" && record.Action != "correct" && record.Action != "tombstone" {
		return errors.New("invalid memory action")
	}
	parsed, err := time.Parse(time.RFC3339Nano, record.OccurredAt)
	if err != nil || parsed.Location() != time.UTC {
		return errors.New("memory record time must be UTC RFC3339")
	}
	if len(record.Namespace) > 128 || len(record.Title) > maxTitleBytes || len(record.Body) > maxBodyBytes || strings.ContainsAny(record.Namespace+record.Title, "\r\n") {
		return errors.New("memory record fields exceed limits")
	}
	combined := record.Namespace + "\n" + record.Title + "\n" + record.Body
	if redacted, _ := redact.String(combined); redacted != combined {
		return errors.New("memory record contains unredacted content")
	}
	if record.Action == "create" {
		if record.Supersedes != nil || record.MemoryID != memoryID(record.Namespace, record.Title, record.Body) {
			return errors.New("invalid memory create identity")
		}
	} else if record.Supersedes == nil {
		return errors.New("memory correction or tombstone must supersede a record")
	}
	if record.Action == "tombstone" && record.Body != "" {
		return errors.New("memory tombstone must not contain a body")
	}
	if err := validateProvenance(record.Provenance); err != nil {
		return err
	}
	return nil
}

func memoryID(namespace, title, body string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + title + "\x00" + body))
	return "mem_" + hex.EncodeToString(sum[:12])
}

func (s *Store) timestamp() string { return s.now().UTC().Format(time.RFC3339Nano) }

func ftsQuery(query string) string {
	var terms []string
	for _, term := range strings.Fields(query) {
		term = strings.ReplaceAll(term, `"`, `""`)
		if term != "" {
			terms = append(terms, `"`+term+`"`)
		}
	}
	return strings.Join(terms, " AND ")
}
