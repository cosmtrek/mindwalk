package event

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxLineBytes bounds a single ledger line; longer lines quarantine.
const MaxLineBytes = 1 << 20

// ErrDuplicate marks an append of an eventId the ledger already holds.
var ErrDuplicate = errors.New("duplicate event")

// ErrUnsafeLogPath marks a ledger path that escapes its configured data root.
var ErrUnsafeLogPath = errors.New("unsafe event log path")

// QuarantineRecord preserves a ledger line that failed verification so bad
// input is never silently dropped.
type QuarantineRecord struct {
	ObservedAt string `json:"observedAt"`
	Reason     string `json:"reason"`
	LineHash   string `json:"lineHash"`
	LineBytes  int    `json:"lineBytes"`
}

// Log is an append-only JSONL ledger of finalized envelopes. Earlier bytes
// are never rewritten; the only mutation besides append is truncating a torn
// final line during Open recovery, after preserving it in the quarantine
// file.
type Log struct {
	path  string
	qpath string
	f     *os.File
	seen  map[string]struct{}
	// now is stubbed in tests; quarantine timestamps are observational
	// metadata, not event identity.
	now func() time.Time
}

// OpenLog opens (creating if needed) the ledger at path, with quarantined
// lines preserved at path+".quarantine". It scans existing content, indexes
// seen eventIds, quarantines undecodable or unverifiable lines, and repairs
// a torn final line left by a crash.
func OpenLog(path string) (*Log, error) {
	l := &Log{path: path, qpath: path + ".quarantine", seen: map[string]struct{}{}, now: time.Now}
	end, err := l.scan()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(end); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	l.f = f
	return l, nil
}

// OpenLogAt opens a ledger beneath dataRoot. name must be a relative path;
// traversal, absolute paths, and symlink escapes fail closed. Application
// code should use this root-bound entry point rather than accepting an
// arbitrary ledger path from an API or configuration value.
func OpenLogAt(dataRoot, name string) (*Log, error) {
	if strings.TrimSpace(dataRoot) == "" || strings.TrimSpace(name) == "" || filepath.IsAbs(name) {
		return nil, fmt.Errorf("%w: root and relative name are required", ErrUnsafeLogPath)
	}
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsafeLogPath, err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(root, filepath.Clean(name))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %s escapes %s", ErrUnsafeLogPath, name, root)
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return nil, err
	}
	if parent != root && !strings.HasPrefix(parent, root+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %s escapes %s", ErrUnsafeLogPath, name, root)
	}
	target = filepath.Join(parent, filepath.Base(target))
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: ledger is a symlink", ErrUnsafeLogPath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	return OpenLog(target)
}

// scan reads the existing ledger, fills the seen-id index, quarantines bad
// complete lines, and returns the offset just past the last complete line. A
// final line without a newline is a torn crash artifact: it is quarantined
// and the returned offset excludes it.
func (l *Log) scan() (int64, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var end int64
	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return 0, err
		}
		complete := err == nil
		if line != "" {
			if !complete {
				if qerr := l.quarantine("torn final line (no newline)", line); qerr != nil {
					return 0, qerr
				}
				break
			}
			if verr := l.index(line[:len(line)-1]); verr != nil {
				if qerr := l.quarantine(verr.Error(), line[:len(line)-1]); qerr != nil {
					return 0, qerr
				}
			}
			end += int64(len(line))
		}
		if err == io.EOF {
			break
		}
	}
	return end, nil
}

// index verifies one complete ledger line and records its eventId.
func (l *Log) index(line string) error {
	if len(line) > MaxLineBytes {
		return fmt.Errorf("line exceeds %d bytes", MaxLineBytes)
	}
	var e Envelope
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return fmt.Errorf("undecodable line: %v", err)
	}
	if err := Verify(e); err != nil {
		return err
	}
	l.seen[e.EventID] = struct{}{}
	return nil
}

func (l *Log) quarantine(reason, line string) error {
	q := QuarantineRecord{
		ObservedAt: l.now().UTC().Format(time.RFC3339Nano),
		Reason:     reason,
		LineBytes:  len(line),
	}
	sum := sha256.Sum256([]byte(line))
	q.LineHash = hex.EncodeToString(sum[:])
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.qpath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Append writes one finalized envelope. It fails closed on anything
// unverifiable and returns ErrDuplicate when the eventId is already stored,
// which makes replayed ingestion idempotent.
func (l *Log) Append(e Envelope) error {
	if err := Verify(e); err != nil {
		return err
	}
	if _, dup := l.seen[e.EventID]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicate, e.EventID)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) > MaxLineBytes {
		return fmt.Errorf("%w: envelope exceeds %d bytes", ErrInvalid, MaxLineBytes)
	}
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.seen[e.EventID] = struct{}{}
	return nil
}

// Len reports how many verified events the ledger holds.
func (l *Log) Len() int { return len(l.seen) }

// Contains reports whether eventId is already stored.
func (l *Log) Contains(eventID string) bool {
	_, ok := l.seen[eventID]
	return ok
}

// Close releases the underlying file.
func (l *Log) Close() error { return l.f.Close() }

// ReadAll returns every verified envelope in append order, skipping (never
// failing on) quarantined content, so a partially corrupted ledger still
// replays its good events.
func ReadAll(path string) ([]Envelope, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Envelope
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == nil || line != "" {
			if err == nil {
				line = line[:len(line)-1]
			}
			var e Envelope
			if line != "" && len(line) <= MaxLineBytes && json.Unmarshal([]byte(line), &e) == nil && Verify(e) == nil {
				out = append(out, e)
			}
		}
		if err == io.EOF {
			return out, nil
		}
	}
}
