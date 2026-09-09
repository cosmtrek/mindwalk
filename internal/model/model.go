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
	// Truncated marks a map that hit a scan or size budget — the session's
	// tree (or its trace targets) holds more than the citymap shows.
	Truncated bool `json:"truncated,omitempty"`
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
	Provider   string `json:"provider,omitempty"`
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
	// OutcomeKnown distinguishes a recorded success from an unknown non-error
	// result. IsError remains sufficient to identify failures in older traces.
	OutcomeKnown bool   `json:"outcomeKnown,omitempty"`
	Summary      string `json:"summary"`
	// ProviderExecuted is true when the tool was executed server-side
	// by the model provider rather than locally by the harness. Only
	// the crush adapter sets this today.
	ProviderExecuted bool `json:"providerExecuted,omitempty"`
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
	Seq      int    `json:"seq"`
	Type     string `json:"type"`
	Note     string `json:"note,omitempty"`
	Duration int    `json:"duration,omitempty"`
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
	Key     string `json:"key"`
	ID      string `json:"id"`
	Harness string `json:"harness"`
	Title   string `json:"title,omitempty"`
	// Path is the deep-link handle the server uses to recover the
	// session. For filesystem-backed harnesses (Claude Code,
	// Codex) it is the on-disk JSONL file path. For database-
	// backed harnesses (Crush, future Aider/Goose) it is a
	// synthetic URI like "crush://session/<id>" that the adapter
	// resolves to a row in its storage. Callers that need to
	// os.Stat the path must check for the synthetic scheme first.
	Path      string `json:"path"`
	Cwd       string `json:"cwd,omitempty"`
	Model     string `json:"model,omitempty"`
	Provider  string `json:"provider,omitempty"`
	GitBranch string `json:"gitBranch,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	EndedAt   string `json:"endedAt,omitempty"`
	// PromptTokens and CompletionTokens are the session-level token
	// economics from the harness's session table, when available.
	PromptTokens     int64   `json:"promptTokens,omitempty"`
	CompletionTokens int64   `json:"completionTokens,omitempty"`
	Cost             float64 `json:"cost,omitempty"`
	// EventCount and UserTurns together are the cheap staleness signal for
	// report badges: user messages land on marks, not events, so the count
	// alone misses exactly the follow-ups that matter most.
	EventCount int               `json:"eventCount"`
	UserTurns  int               `json:"userTurns"`
	Auxiliary  bool              `json:"-"`
	Agent      *AgentSessionMeta `json:"-"`
}
