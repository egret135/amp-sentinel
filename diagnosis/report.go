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

	// P0: Structured output
	StructuredResult *DiagnosisJSON `json:"structured_result,omitempty"`

	// P0: Quality scoring (dynamic max-possible system)
	QualityScore QualityScore `json:"quality_score"`

	// P0: Confidence (original from AI, final after Reviewer adjustment)
	OriginalConfidence float64 `json:"original_confidence"`
	FinalConfidence    float64 `json:"final_confidence"`
	FinalConfLabel     string  `json:"final_confidence_label"`

	// P1: Historical fingerprint reuse
	Fingerprint  string `json:"fingerprint,omitempty"`
	CommitHash   string `json:"commit_hash,omitempty"`
	ReusedFromID string `json:"reused_from_id,omitempty"`

	// Version tracking for A/B testing
	PromptVersion string `json:"prompt_version,omitempty"`
}

// UsageInfo tracks token consumption.
type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
