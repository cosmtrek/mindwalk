// Package ingest tails growing JSONL session logs incrementally and
// read-only. It knows nothing about session formats or rendering: it emits
// complete raw lines; callers decide what they mean. Progress is tracked as
// a persistable State so a restart resumes without duplicating or losing
// complete lines. Polling is the deliberate baseline mechanism — no
// filesystem-notification dependency.
package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// StateVersion versions the persisted tail state.
const StateVersion = 1

const (
	// DefaultMaxLineBytes bounds one line; longer lines surface as Oversized.
	DefaultMaxLineBytes = 1 << 20
	// DefaultMaxPollBytes bounds one poll's read window.
	DefaultMaxPollBytes = 8 << 20
	// anchorMax bounds the file-identity anchor prefix.
	anchorMax = 4096
)

// Status classifies what a poll observed.
type Status string

const (
	StatusUnchanged Status = "unchanged"
	StatusGrew      Status = "grew"
	StatusTruncated Status = "truncated"
	StatusReplaced  Status = "replaced"
	StatusMissing   Status = "missing"
)

// State is the persistable resume point for one tailed file.
type State struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
	// Offset is the byte just past the last complete line consumed. An
	// incomplete final line is never consumed — it stays on disk until its
	// newline arrives.
	Offset int64 `json:"offset"`
	// AnchorHash fingerprints the file's first AnchorLen bytes so a replaced
	// or rotated file is detected even at the same path and size.
	AnchorHash string `json:"anchorHash,omitempty"`
	AnchorLen  int    `json:"anchorLen"`
	// Skipping is set while discarding an over-window line's remainder.
	Skipping bool `json:"skipping,omitempty"`
	// FileIdentity is device+inode (or the platform file-index equivalent)
	// when available. AnchorHash remains the portable fallback.
	FileIdentity            string `json:"fileIdentity,omitempty"`
	FileSize                int64  `json:"fileSize,omitempty"`
	ModTime                 string `json:"modTime,omitempty"`
	LastCompleteOffset      int64  `json:"lastCompleteOffset,omitempty"`
	LastAcceptedSourceEvent string `json:"lastAcceptedSourceEvent,omitempty"`
	ProjectedEvents         int    `json:"projectedEvents,omitempty"`
	DurableSequence         int64  `json:"durableSequence,omitempty"`
	// Blocked prevents adapters from reparsing a source that contains a
	// rejected record. Rotation/truncation clears it and permits recovery.
	Blocked bool `json:"blocked,omitempty"`
}

// Poll reports one poll's outcome.
type Poll struct {
	Status Status
	// Lines are the complete lines consumed, without trailing newlines.
	Lines [][]byte
	// Oversized are complete lines longer than MaxLineBytes, surfaced
	// separately so callers can quarantine rather than process them.
	Oversized [][]byte
	// SkippedBytes counts bytes discarded from lines too large to buffer.
	SkippedBytes int64
}

// Tailer incrementally reads one JSONL file. It never writes to the file.
type Tailer struct {
	state        State
	MaxLineBytes int
	MaxPollBytes int
}

// NewTailer starts tailing path from the beginning.
func NewTailer(path string) *Tailer {
	return Resume(State{SchemaVersion: StateVersion, Path: path})
}

// Resume continues from a persisted state.
func Resume(state State) *Tailer {
	return &Tailer{
		state:        state,
		MaxLineBytes: DefaultMaxLineBytes,
		MaxPollBytes: DefaultMaxPollBytes,
	}
}

// State returns the current resume point.
func (t *Tailer) State() State { return t.state }

// Poll reads any new complete lines since the last poll and classifies what
// happened to the file.
func (t *Tailer) Poll() (Poll, error) {
	if err := t.validate(); err != nil {
		return Poll{}, err
	}
	f, err := os.Open(t.state.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Poll{Status: StatusMissing}, nil
	}
	if err != nil {
		return Poll{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Poll{}, err
	}
	size := info.Size()
	identity := fileIdentity(info)

	status := StatusUnchanged
	if t.state.FileIdentity != "" && identity != "" && t.state.FileIdentity != identity {
		t.reset()
		status = StatusReplaced
	}
	if size < t.state.Offset || (t.state.AnchorLen > 0 && size < int64(t.state.AnchorLen)) {
		t.reset()
		status = StatusTruncated
	}
	if t.state.AnchorLen > 0 && size >= int64(t.state.AnchorLen) {
		same, err := t.anchorMatches(f)
		if err != nil {
			return Poll{}, err
		}
		if !same {
			t.reset()
			status = StatusReplaced
		}
	}
	if t.state.AnchorLen == 0 && size > 0 {
		if err := t.setAnchor(f, size); err != nil {
			return Poll{}, err
		}
	}
	if identity != "" {
		t.state.FileIdentity = identity
	}
	if size == t.state.Offset {
		t.recordFileState(info)
		return Poll{Status: status}, nil
	}

	p, err := t.consume(f, size)
	if err != nil {
		return Poll{}, err
	}
	if status == StatusUnchanged && (len(p.Lines) > 0 || len(p.Oversized) > 0 || p.SkippedBytes > 0) {
		status = StatusGrew
	}
	p.Status = status
	t.recordFileState(info)
	return p, nil
}

func (t *Tailer) validate() error {
	if t.state.SchemaVersion != StateVersion {
		return fmt.Errorf("invalid tail state schemaVersion %d", t.state.SchemaVersion)
	}
	if t.state.Path == "" || t.state.Offset < 0 {
		return fmt.Errorf("invalid tail state path or offset")
	}
	if t.MaxLineBytes <= 0 || t.MaxPollBytes <= 0 {
		return fmt.Errorf("tail limits must be positive")
	}
	if t.state.AnchorLen < 0 || t.state.AnchorLen > anchorMax {
		return fmt.Errorf("invalid tail anchor length")
	}
	if (t.state.AnchorLen == 0) != (t.state.AnchorHash == "") || (t.state.AnchorHash != "" && !hashPattern.MatchString(t.state.AnchorHash)) {
		return fmt.Errorf("invalid tail anchor hash")
	}
	if t.state.FileSize < 0 || t.state.LastCompleteOffset < 0 || t.state.ProjectedEvents < 0 || t.state.DurableSequence < 0 {
		return fmt.Errorf("invalid persisted tail metadata")
	}
	return nil
}

func (t *Tailer) reset() {
	t.state.Offset = 0
	t.state.AnchorHash = ""
	t.state.AnchorLen = 0
	t.state.Skipping = false
	t.state.FileIdentity = ""
	t.state.FileSize = 0
	t.state.ModTime = ""
	t.state.LastCompleteOffset = 0
	t.state.LastAcceptedSourceEvent = ""
	t.state.ProjectedEvents = 0
	t.state.DurableSequence = 0
	t.state.Blocked = false
}

func (t *Tailer) recordFileState(info os.FileInfo) {
	t.state.FileSize = info.Size()
	t.state.ModTime = info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	t.state.LastCompleteOffset = t.state.Offset
}

func fileIdentity(info os.FileInfo) string {
	if info == nil || info.Sys() == nil {
		return ""
	}
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return ""
	}
	read := func(name string) (uint64, bool) {
		f := v.FieldByName(name)
		if !f.IsValid() {
			return 0, false
		}
		switch f.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return f.Uint(), true
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return uint64(f.Int()), true
		}
		return 0, false
	}
	if dev, ok := read("Dev"); ok {
		if ino, ok := read("Ino"); ok {
			return fmt.Sprintf("devino:%d:%d", dev, ino)
		}
	}
	if volume, ok := read("VolumeSerialNumber"); ok {
		high, highOK := read("FileIndexHigh")
		low, lowOK := read("FileIndexLow")
		if highOK && lowOK {
			return fmt.Sprintf("fileindex:%d:%d:%d", volume, high, low)
		}
	}
	return ""
}

func (t *Tailer) anchorMatches(f *os.File) (bool, error) {
	buf := make([]byte, t.state.AnchorLen)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return false, err
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]) == t.state.AnchorHash, nil
}

func (t *Tailer) setAnchor(f *os.File, size int64) error {
	n := anchorMax
	if size < int64(n) {
		n = int(size)
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && err != io.EOF {
		return err
	}
	sum := sha256.Sum256(buf)
	t.state.AnchorHash = hex.EncodeToString(sum[:])
	t.state.AnchorLen = n
	return nil
}

// consume reads at most MaxPollBytes from the offset and advances past every
// complete line found. While Skipping, bytes up to the next newline are
// discarded — that is the tail of a line too large to buffer.
func (t *Tailer) consume(f *os.File, size int64) (Poll, error) {
	var p Poll
	window := size - t.state.Offset
	if window > int64(t.MaxPollBytes) {
		window = int64(t.MaxPollBytes)
	}
	buf := make([]byte, window)
	n, err := f.ReadAt(buf, t.state.Offset)
	if err != nil && err != io.EOF {
		return Poll{}, err
	}
	buf = buf[:n]

	if t.state.Skipping {
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			t.state.Offset += int64(len(buf))
			p.SkippedBytes += int64(len(buf))
			return p, nil
		}
		t.state.Offset += int64(nl + 1)
		p.SkippedBytes += int64(nl + 1)
		t.state.Skipping = false
		buf = buf[nl+1:]
	}

	for {
		nl := bytes.IndexByte(buf, '\n')
		if nl < 0 {
			// No newline in what remains. A short partial is an incomplete
			// final line: leave it on disk until its newline arrives. A
			// partial already at MaxLineBytes can never become an emittable
			// line, so discard it and keep discarding until a newline.
			if len(buf) >= t.MaxLineBytes {
				t.state.Offset += int64(len(buf))
				p.SkippedBytes += int64(len(buf))
				t.state.Skipping = true
			}
			return p, nil
		}
		line := buf[:nl]
		if len(line) > t.MaxLineBytes {
			p.Oversized = append(p.Oversized, append([]byte(nil), line...))
		} else {
			p.Lines = append(p.Lines, append([]byte(nil), line...))
		}
		t.state.Offset += int64(nl + 1)
		buf = buf[nl+1:]
	}
}
