package intake

import "time"

// Incident represents a fault/error reported by an external monitoring system.
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
	Metadata    map[string]string `json:"metadata"`
	Source      string            `json:"source"`
	OccurredAt  time.Time         `json:"occurred_at"`
	ReportedAt  time.Time         `json:"reported_at"`
}

// ValidSeverities is the set of accepted severity values.
var ValidSeverities = map[string]bool{
	"critical": true,
	"warning":  true,
	"info":     true,
}

// SeverityPriority maps severity strings to numeric priority for queue ordering.
func SeverityPriority(severity string) int {
	switch severity {
	case "critical":
		return 100
	case "warning":
		return 50
	case "info":
		return 10
	default:
		return 50
	}
}
