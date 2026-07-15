package model

type Rect struct {
	X float64 `json:"x"`
	Z float64 `json:"z"`
	W float64 `json:"w"`
	D float64 `json:"d"`
}

type RepoMeta struct {
	Root        string `json:"root"`
	Commit      string `json:"commit,omitempty"`
	Dirty       bool   `json:"dirty"`
	GeneratedAt string `json:"generatedAt"`
}

type CityMap struct {
	Version int        `json:"version"`
	Repo    RepoMeta   `json:"repo"`
	Files   []CityFile `json:"files"`
	Dirs    []CityDir  `json:"dirs"`
	Layout  LayoutMeta `json:"layout"`
}

type CityFile struct {
	ID    int    `json:"id"`
	Path  string `json:"path"`
	Dir   string `json:"dir"`
	Lines int    `json:"lines"`
	Bytes int64  `json:"bytes"`
	Lang  string `json:"lang,omitempty"`
	Rect  Rect   `json:"rect"`
	Ghost bool   `json:"ghost"`
}

type CityDir struct {
	Path      string `json:"path"`
	Depth     int    `json:"depth"`
	Rect      Rect   `json:"rect"`
	FileCount int    `json:"fileCount"`
	Lines     int    `json:"lines"`
}

type LayoutMeta struct {
	Algorithm string `json:"algorithm"`
	Weight    string `json:"weight"`
}

type Trace struct {
	Version int          `json:"version"`
	Session TraceSession `json:"session"`
	Events  []Event      `json:"events"`
	Marks   []Mark       `json:"marks"`
	Stats   Stats        `json:"stats"`
}

type TraceSession struct {
	ID         string `json:"id"`
	Harness    string `json:"harness"`
	Model      string `json:"model,omitempty"`
	Title      string `json:"title,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Commit     string `json:"commit,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	EndedAt    string `json:"endedAt,omitempty"`
	EventCount int    `json:"eventCount"`
	Path       string `json:"path,omitempty"`
}

type Event struct {
	Seq         int            `json:"seq"`
	Timestamp   string         `json:"ts,omitempty"`
	Tool        string         `json:"tool"`
	Action      string         `json:"action"`
	Targets     []Target       `json:"targets"`
	Outside     []OutsideTouch `json:"outside,omitempty"`
	ResultBytes int            `json:"resultBytes"`
	IsError     bool           `json:"isError"`
	Summary     string         `json:"summary"`
}

type Target struct {
	Path   string   `json:"path"`
	FileID *int     `json:"fileId,omitempty"`
	Touch  string   `json:"touch"`
	Lines  [][2]int `json:"lines,omitempty"`
	Weak   bool     `json:"weak,omitempty"`
}

type OutsideTouch struct {
	Scope string `json:"scope"`
	Path  string `json:"path"`
}

type Mark struct {
	Seq  int    `json:"seq"`
	Type string `json:"type"`
	Note string `json:"note,omitempty"`
}

type Stats struct {
	FilesInRepo           int          `json:"filesInRepo"`
	Fovea                 int          `json:"fovea"`
	Parafovea             int          `json:"parafovea"`
	Edited                int          `json:"edited"`
	EventsBeforeFirstEdit int          `json:"eventsBeforeFirstEdit"`
	RegressionRate        float64      `json:"regressionRate"`
	ErrorRate             float64      `json:"errorRate"`
	Actions               ActionCounts `json:"actions"`
	Errors                ActionCounts `json:"errors"`
	MaxEditsPerFile       int          `json:"maxEditsPerFile"`
	// ChurnFiles counts files edited in three or more events.
	ChurnFiles  int   `json:"churnFiles"`
	UserTurns   int   `json:"userTurns"`
	Compactions int   `json:"compactions"`
	Subagents   int   `json:"subagents"`
	ResultBytes int64 `json:"resultBytes"`
	// EditsAfterLastVerify counts edit events after the last verify event;
	// when the session never ran a verify it counts every edit event.
	EditsAfterLastVerify int `json:"editsAfterLastVerify"`
	// Observability grades each derived metric's source signal so the UI can
	// tell a true zero from a blind spot in the session log.
	Observability Observability `json:"observability"`
}

// Observability values: "exact" when the harness records the signal
// structurally, "estimated" when it is inferred from command or output text,
// "unavailable" when the log carries no usable signal.
const (
	ObservabilityExact       = "exact"
	ObservabilityEstimated   = "estimated"
	ObservabilityUnavailable = "unavailable"
)

type Observability struct {
	Reads  string `json:"reads"`
	Errors string `json:"errors"`
}

// ActionCounts tallies events per action class; as Stats.Errors it tallies
// only the events that returned an error.
type ActionCounts struct {
	Search int `json:"search"`
	Read   int `json:"read"`
	Edit   int `json:"edit"`
	Exec   int `json:"exec"`
	Verify int `json:"verify"`
	Other  int `json:"other"`
}

type SessionMeta struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	Harness   string `json:"harness"`
	Title     string `json:"title,omitempty"`
	Path      string `json:"path"`
	Cwd       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	GitBranch string `json:"gitBranch,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	// EventCount and UserTurns together are the cheap staleness signal for
	// report badges: user messages land on marks, not events, so the count
	// alone misses exactly the follow-ups that matter most.
	EventCount int               `json:"eventCount"`
	UserTurns  int               `json:"userTurns"`
	Auxiliary  bool              `json:"-"`
	Agent      *AgentSessionMeta `json:"-"`
}
