package store

import (
	"context"
	"encoding/json"
	"time"
)

// TaskStatus represents the lifecycle state of a diagnosis task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusQueued    TaskStatus = "queued"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusTimeout   TaskStatus = "timeout"
)

// Event represents an event record in the store.
type Event struct {
	ID         string          `json:"id"`
	ProjectKey string          `json:"project_key"`
	Payload    json.RawMessage `json:"payload"`
	Source     string          `json:"source"`
	Severity   string          `json:"severity"`
	Title      string          `json:"title"`
	Status     string          `json:"status"`
	ReceivedAt time.Time       `json:"received_at"`
}

// DiagnosisTask represents a diagnosis task record in the store.
type DiagnosisTask struct {
	ID           string     `json:"id"`
	EventID      string     `json:"event_id"`
	ProjectKey   string     `json:"project_key"`
	Status       TaskStatus `json:"status"`
	Priority     int        `json:"priority"`
	SessionID    string     `json:"session_id"`
	NumTurns     int        `json:"num_turns"`
	DurationMs   int64      `json:"duration_ms"`
	InputTokens  int        `json:"input_tokens"`
	OutputTokens int        `json:"output_tokens"`
	Error        string     `json:"error"`
	RetryCount   int        `json:"retry_count"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at"`
}

// DiagnosisReport represents a diagnosis report record in the store.
type DiagnosisReport struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"task_id"`
	EventID     string    `json:"event_id"`
	ProjectKey  string    `json:"project_key"`
	ProjectName string    `json:"project_name"`
	Summary     string    `json:"summary"`
	RawResult   string    `json:"raw_result"`
	Confidence  string    `json:"confidence"`
	HasIssue    bool      `json:"has_issue"`
	Tainted     bool      `json:"tainted"`
	Notified    bool      `json:"notified"`
	ToolsUsed   []string  `json:"tools_used"`
	SkillsUsed  []string  `json:"skills_used"`
	DiagnosedAt time.Time `json:"diagnosed_at"`

	// P0: Structured output + quality scoring
	StructuredResult   json.RawMessage `json:"structured_result,omitempty"`
	QualityScore       json.RawMessage `json:"quality_score,omitempty"`
	CommitHash         string          `json:"commit_hash,omitempty"`
	PromptVersion      string          `json:"prompt_version,omitempty"`
	OriginalConfidence float64         `json:"original_confidence,omitempty"`
	FinalConfidence    float64         `json:"final_confidence,omitempty"`
	FinalConfLabel     string          `json:"final_confidence_label,omitempty"`

	// P1: Fingerprint reuse
	Fingerprint  string `json:"fingerprint,omitempty"`
	ReusedFromID string `json:"reused_from_id,omitempty"`
}

// EventFilter specifies criteria for listing events.
type EventFilter struct {
	ProjectKey string `json:"project_key"`
	Source     string `json:"source"`
	Status     string `json:"status"`
	Severity   string `json:"severity"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// TaskFilter specifies criteria for listing tasks.
type TaskFilter struct {
	EventID    string     `json:"event_id"`
	ProjectKey string     `json:"project_key"`
	Status     TaskStatus `json:"status"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
}

// UsageSummary holds aggregated usage statistics.
type UsageSummary struct {
	TotalEvents       int                `json:"total_events"`
	TodayEvents       int                `json:"today_events"`
	TasksByStatus     map[TaskStatus]int `json:"tasks_by_status"`
	TotalInputTokens  int64              `json:"total_input_tokens"`
	TotalOutputTokens int64              `json:"total_output_tokens"`
}

// Store defines the persistence interface for amp-sentinel.
type Store interface {
	CreateEvent(ctx context.Context, event *Event) error
	GetEvent(ctx context.Context, id string) (*Event, error)
	UpdateEvent(ctx context.Context, event *Event) error
	ListEvents(ctx context.Context, filter EventFilter) ([]*Event, error)

	CreateTask(ctx context.Context, task *DiagnosisTask) error
	GetTask(ctx context.Context, id string) (*DiagnosisTask, error)
	UpdateTask(ctx context.Context, task *DiagnosisTask) error
	ListTasks(ctx context.Context, filter TaskFilter) ([]*DiagnosisTask, error)
	CountByStatus(ctx context.Context) (map[TaskStatus]int, error)

	SaveReport(ctx context.Context, report *DiagnosisReport) error
	GetReport(ctx context.Context, taskID string) (*DiagnosisReport, error)
	FindRecentReportByFingerprint(ctx context.Context, projectKey, fingerprint string, since time.Time) (*DiagnosisReport, error)

	GetUsageSummary(ctx context.Context) (*UsageSummary, error)

	Close() error
}
