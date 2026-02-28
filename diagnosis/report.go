package diagnosis

import "time"

// Report holds the structured result of a diagnosis.
type Report struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	IncidentID  string    `json:"incident_id"`
	ProjectKey  string    `json:"project_key"`
	ProjectName string    `json:"project_name"`
	Summary     string    `json:"summary"`
	RawResult   string    `json:"raw_result"`
	HasIssue    bool      `json:"has_issue"`
	Confidence  string    `json:"confidence"`
	SessionID   string    `json:"session_id"`
	DurationMs  int64     `json:"duration_ms"`
	NumTurns    int       `json:"num_turns"`
	ToolsUsed   []string  `json:"tools_used"`
	SkillsUsed  []string  `json:"skills_used"`
	Tainted     bool      `json:"tainted"`
	Notified    bool      `json:"notified"`
	DiagnosedAt time.Time `json:"diagnosed_at"`
	Usage       *UsageInfo `json:"usage,omitempty"`
}

// UsageInfo tracks token consumption.
type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
