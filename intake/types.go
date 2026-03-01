package intake

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// RawEvent is a thin envelope wrapping an arbitrary JSON payload.
// The system does not interpret the payload — it is passed directly
// to Amp AI for analysis.
type RawEvent struct {
	ID         string          `json:"id"`
	ProjectKey string          `json:"project_key"`
	Payload    json.RawMessage `json:"payload"`
	Source     string          `json:"source"`
	Severity   string          `json:"severity"`
	Title      string          `json:"title"`
	ReceivedAt time.Time       `json:"received_at"`
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

// titleCandidateFields lists candidate field names for title extraction,
// ordered by priority (strongest "title semantics" first).
var titleCandidateFields = []string{
	"title",
	"alert_name",
	"alertname",
	"summary",
	"subject",
	"description",
	"error_msg",
	"error",
	"message",
	"msg",
}

// ExtractTitle attempts to extract a human-readable title from the payload.
// Only string values are accepted — objects, arrays, numbers are skipped.
// The result is sanitized and truncated to 100 runes.
func ExtractTitle(payload json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return ""
	}
	for _, field := range titleCandidateFields {
		if v, ok := m[field]; ok {
			s, isStr := v.(string)
			if !isStr || s == "" {
				continue
			}
			s = SanitizeDisplayText(s)
			if s == "" {
				continue
			}
			runes := []rune(s)
			if len(runes) > 100 {
				return string(runes[:100]) + "..."
			}
			return s
		}
	}
	return ""
}

// SanitizeDisplayText removes control characters and collapses whitespace,
// producing a single-line string safe for Feishu card display.
func SanitizeDisplayText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		if r < 0x20 || (r >= 0x7F && r <= 0x9F) {
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// resolveField extracts a value from the map, supporting:
//   - Top-level keys: "error_msg"
//   - Nested objects: "context.user.id"
//   - Array indices: "exception.values.0.type"
func resolveField(m map[string]any, path string) any {
	if !strings.Contains(path, ".") {
		return m[path]
	}

	segments := strings.Split(path, ".")
	var current any = m
	for _, seg := range segments {
		switch c := current.(type) {
		case map[string]any:
			current = c[seg]
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(c) {
				return nil
			}
			current = c[idx]
		default:
			return nil
		}
	}
	return current
}

// DisplayFields holds fields extracted from payload for display purposes.
type DisplayFields struct {
	Environment string
	ErrorMsg    string
	OccurredAt  string
	URL         string
}

var displayFieldCandidates = map[string][]string{
	"environment": {"environment", "env", "deploy_env", "stage", "labels.env", "tags.env"},
	"error_msg":   {"error_msg", "error", "message", "msg", "error_message"},
	"occurred_at": {"occurred_at", "timestamp", "@timestamp", "time", "event_time", "startsAt"},
	"url":         {"url", "request_url", "request.url", "http.url", "endpoint", "uri"},
}

// ExtractDisplayFields extracts display-worthy fields from the payload.
func ExtractDisplayFields(m map[string]any) DisplayFields {
	if m == nil {
		return DisplayFields{}
	}

	df := DisplayFields{}
	df.Environment = SanitizeDisplayText(findScalarField(m, displayFieldCandidates["environment"]))
	df.ErrorMsg = SanitizeDisplayText(
		TruncateRunes(findScalarField(m, displayFieldCandidates["error_msg"]), 200))
	df.URL = SanitizeDisplayText(
		TruncateRunes(findScalarField(m, displayFieldCandidates["url"]), 200))

	rawTime := findField(m, displayFieldCandidates["occurred_at"])
	df.OccurredAt = normalizeTime(rawTime)

	return df
}

// findScalarField resolves the first matching scalar field from candidates.
func findScalarField(m map[string]any, candidates []string) string {
	for _, path := range candidates {
		v := resolveField(m, path)
		if v == nil {
			continue
		}
		switch val := v.(type) {
		case string:
			if val != "" {
				return val
			}
		case float64:
			return strconv.FormatFloat(val, 'f', -1, 64)
		case bool:
			return strconv.FormatBool(val)
		}
	}
	return ""
}

// findField resolves the first matching field of any type from candidates.
func findField(m map[string]any, candidates []string) any {
	for _, path := range candidates {
		v := resolveField(m, path)
		if v != nil {
			return v
		}
	}
	return nil
}

// normalizeTime attempts to parse the value as a time and format it for display.
// Returns "" if the value cannot be parsed.
func normalizeTime(v any) string {
	if v == nil {
		return ""
	}

	switch t := v.(type) {
	case string:
		if t == "" {
			return ""
		}
		for _, layout := range timeLayouts {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed.Format("2006-01-02 15:04:05")
			}
		}
		return ""

	case float64:
		if t > 1e15 || t < 1e8 {
			return ""
		}
		if t > 1e12 {
			t /= 1000
		}
		return time.Unix(int64(t), 0).Format("2006-01-02 15:04:05")
	}

	return ""
}

var timeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05.000Z",
	"Jan 2, 2006 3:04:05 PM",
}

// TruncateRunes truncates s to maxLen runes, appending "..." if truncated.
func TruncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// EscapeLarkMD escapes Markdown special characters for Feishu card display.
var larkMDReplacer = strings.NewReplacer(
	"*", "\\*",
	"_", "\\_",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"~", "\\~",
	"`", "\\`",
)

func EscapeLarkMD(s string) string {
	return larkMDReplacer.Replace(s)
}

// isScalar returns true if v is a string, number, or bool.
func isScalar(v any) bool {
	switch v.(type) {
	case string, float64, bool, json.Number:
		return true
	}
	return false
}
