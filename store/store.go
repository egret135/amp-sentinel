package store

import (
	"context"
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

// Incident represents a fault/error record in the store.
type Incident struct {
	ID          string            `json:"id"`
	ProjectKey  string            `json:"project_key"`
	Title       string            `json:"title"`
	ErrorType   string            `json:"error_type"`
	ErrorMsg    string            `json:"error_msg"`
	Stacktrace  string            `json:"stacktrace"`
	Environment string            `json:"environment"`
	Severity    string            `json:"severity"`
	URL         string            `json:"url"`
	Source      string            `json:"source"`
	Status      string            `json:"status"`
	Metadata    map[string]string `json:"metadata"`
	OccurredAt  time.Time         `json:"occurred_at"`
	ReportedAt  time.Time         `json:"reported_at"`
}

// DiagnosisTask represents a diagnosis task record in the store.
type DiagnosisTask struct {
	ID           string     `json:"id"`
	IncidentID   string     `json:"incident_id"`
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
	IncidentID  string    `json:"incident_id"`
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
}

// IncidentFilter specifies criteria for listing incidents.
type IncidentFilter struct {
	ProjectKey string `json:"project_key"`
	Status     string `json:"status"`
	Severity   string `json:"severity"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// TaskFilter specifies criteria for listing tasks.
type TaskFilter struct {
	IncidentID string     `json:"incident_id"`
	ProjectKey string     `json:"project_key"`
	Status     TaskStatus `json:"status"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
}

// UsageSummary holds aggregated usage statistics.
type UsageSummary struct {
	TotalIncidents    int                `json:"total_incidents"`
	TodayIncidents    int                `json:"today_incidents"`
	TasksByStatus     map[TaskStatus]int `json:"tasks_by_status"`
	TotalInputTokens  int64              `json:"total_input_tokens"`
	TotalOutputTokens int64              `json:"total_output_tokens"`
}

// Store defines the persistence interface for amp-sentinel.
type Store interface {
	CreateIncident(ctx context.Context, inc *Incident) error
	GetIncident(ctx context.Context, id string) (*Incident, error)
	UpdateIncident(ctx context.Context, inc *Incident) error
	ListIncidents(ctx context.Context, filter IncidentFilter) ([]*Incident, error)

	CreateTask(ctx context.Context, task *DiagnosisTask) error
	GetTask(ctx context.Context, id string) (*DiagnosisTask, error)
	UpdateTask(ctx context.Context, task *DiagnosisTask) error
	ListTasks(ctx context.Context, filter TaskFilter) ([]*DiagnosisTask, error)
	CountByStatus(ctx context.Context) (map[TaskStatus]int, error)

	SaveReport(ctx context.Context, report *DiagnosisReport) error
	GetReport(ctx context.Context, taskID string) (*DiagnosisReport, error)

	FindRecentIncident(ctx context.Context, projectKey, errorMsg string, window time.Duration) (*Incident, error)

	GetUsageSummary(ctx context.Context) (*UsageSummary, error)

	Close() error
}
