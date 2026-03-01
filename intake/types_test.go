package intake

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSeverityPriority(t *testing.T) {
	tests := []struct {
		severity string
		want     int
	}{
		{"critical", 100},
		{"warning", 50},
		{"info", 10},
		{"unknown", 50},
		{"", 50},
		{"CRITICAL", 50}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			if got := SeverityPriority(tt.severity); got != tt.want {
				t.Errorf("SeverityPriority(%q) = %d, want %d", tt.severity, got, tt.want)
			}
		})
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "title field",
			payload: `{"title":"my alert"}`,
			want:    "my alert",
		},
		{
			name:    "alert_name over lower priority fields",
			payload: `{"alert_name":"alert1","message":"msg1"}`,
			want:    "alert1",
		},
		{
			name:    "alertname field",
			payload: `{"alertname":"prometheus alert"}`,
			want:    "prometheus alert",
		},
		{
			name:    "summary field",
			payload: `{"summary":"a summary"}`,
			want:    "a summary",
		},
		{
			name:    "msg fallback",
			payload: `{"msg":"last resort"}`,
			want:    "last resort",
		},
		{
			name:    "non-string value skipped",
			payload: `{"title":123,"message":"fallback"}`,
			want:    "fallback",
		},
		{
			name:    "empty string skipped",
			payload: `{"title":"","error":"real error"}`,
			want:    "real error",
		},
		{
			name:    "no matching field",
			payload: `{"foo":"bar"}`,
			want:    "",
		},
		{
			name:    "invalid json",
			payload: `not json`,
			want:    "",
		},
		{
			name:    "truncates long title",
			payload: `{"title":"` + strings.Repeat("a", 150) + `"}`,
			want:    strings.Repeat("a", 100) + "...",
		},
		{
			name:    "sanitizes control chars",
			payload: `{"title":"hello\nworld"}`,
			want:    "hello world",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTitle(json.RawMessage(tt.payload))
			if got != tt.want {
				t.Errorf("ExtractTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeDisplayText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"newlines replaced", "hello\nworld", "hello world"},
		{"carriage return replaced", "hello\rworld", "hello world"},
		{"tab replaced", "hello\tworld", "hello world"},
		{"consecutive whitespace collapsed", "hello\n\n\nworld", "hello world"},
		{"control chars removed", "hello\x00\x01world", "helloworld"},
		{"c1 control chars removed", "hello\u0080\u009fworld", "helloworld"},
		{"leading/trailing whitespace trimmed", "  hello  ", "hello"},
		{"mixed whitespace and control", "\n\thello\x00\n\tworld\n", "hello world"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDisplayText(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeDisplayText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveField(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		path string
		want any
	}{
		{
			name: "top-level key",
			m:    map[string]any{"error_msg": "fail"},
			path: "error_msg",
			want: "fail",
		},
		{
			name: "nested object",
			m:    map[string]any{"context": map[string]any{"user": map[string]any{"id": "u1"}}},
			path: "context.user.id",
			want: "u1",
		},
		{
			name: "array index",
			m: map[string]any{
				"exception": map[string]any{
					"values": []any{
						map[string]any{"type": "TypeError"},
						map[string]any{"type": "ValueError"},
					},
				},
			},
			path: "exception.values.0.type",
			want: "TypeError",
		},
		{
			name: "array second index",
			m: map[string]any{
				"items": []any{"a", "b", "c"},
			},
			path: "items.1",
			want: "b",
		},
		{
			name: "missing top-level key",
			m:    map[string]any{"a": 1},
			path: "b",
			want: nil,
		},
		{
			name: "missing nested key",
			m:    map[string]any{"a": map[string]any{"b": 1}},
			path: "a.c",
			want: nil,
		},
		{
			name: "array index out of range",
			m:    map[string]any{"items": []any{"a"}},
			path: "items.5",
			want: nil,
		},
		{
			name: "negative array index",
			m:    map[string]any{"items": []any{"a"}},
			path: "items.-1",
			want: nil,
		},
		{
			name: "path through scalar",
			m:    map[string]any{"a": "scalar"},
			path: "a.b",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveField(tt.m, tt.path)
			if got != tt.want {
				t.Errorf("resolveField(%v, %q) = %v, want %v", tt.m, tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractDisplayFields(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		want DisplayFields
	}{
		{
			name: "nil map",
			m:    nil,
			want: DisplayFields{},
		},
		{
			name: "empty map",
			m:    map[string]any{},
			want: DisplayFields{},
		},
		{
			name: "all fields present",
			m: map[string]any{
				"environment": "production",
				"error_msg":   "something broke",
				"occurred_at": "2025-01-15T10:30:00Z",
				"url":         "https://example.com/api",
			},
			want: DisplayFields{
				Environment: "production",
				ErrorMsg:    "something broke",
				OccurredAt:  "2025-01-15 10:30:00",
				URL:         "https://example.com/api",
			},
		},
		{
			name: "alternate field names",
			m: map[string]any{
				"env":       "staging",
				"message":   "error occurred",
				"timestamp": "2025-06-01T12:00:00Z",
				"endpoint":  "https://example.com",
			},
			want: DisplayFields{
				Environment: "staging",
				ErrorMsg:    "error occurred",
				OccurredAt:  "2025-06-01 12:00:00",
				URL:         "https://example.com",
			},
		},
		{
			name: "nested url via request.url",
			m: map[string]any{
				"request": map[string]any{
					"url": "https://nested.example.com",
				},
			},
			want: DisplayFields{
				URL: "https://nested.example.com",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDisplayFields(tt.m)
			if got != tt.want {
				t.Errorf("ExtractDisplayFields() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestNormalizeTime(t *testing.T) {
	unixWant := time.Unix(1705312200, 0).Format("2006-01-02 15:04:05")
	tests := []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, ""},
		{"empty string", "", ""},
		{"RFC3339", "2025-01-15T10:30:00Z", "2025-01-15 10:30:00"},
		{"RFC3339Nano", "2025-01-15T10:30:00.123456789Z", "2025-01-15 10:30:00"},
		{"datetime with space", "2025-01-15 10:30:00", "2025-01-15 10:30:00"},
		{"datetime with slash", "2025/01/15 10:30:00", "2025-01-15 10:30:00"},
		{"datetime no timezone", "2025-01-15T10:30:00", "2025-01-15 10:30:00"},
		{"unix timestamp seconds", float64(1705312200), unixWant},
		{"unix timestamp milliseconds", float64(1705312200000), unixWant},
		{"out of range too small", float64(1e7), ""},
		{"out of range too large", float64(1e16), ""},
		{"unparseable string", "not a time", ""},
		{"unsupported type", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeTime(tt.v)
			if got != tt.want {
				t.Errorf("normalizeTime(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello..."},
		{"empty string", "", 5, ""},
		{"unicode truncation", "你好世界测试", 4, "你好世界..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.s, tt.maxLen)
			if got != tt.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestEscapeLarkMD(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no special chars", "hello world", "hello world"},
		{"asterisk", "*bold*", "\\*bold\\*"},
		{"underscore", "_italic_", "\\_italic\\_"},
		{"brackets", "[link](url)", "\\[link\\]\\(url\\)"},
		{"tilde", "~strike~", "\\~strike\\~"},
		{"backtick", "`code`", "\\`code\\`"},
		{"mixed", "*_[test]_*", "\\*\\_\\[test\\]\\_\\*"},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EscapeLarkMD(tt.input)
			if got != tt.want {
				t.Errorf("EscapeLarkMD(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsScalar(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want bool
	}{
		{"string", "hello", true},
		{"float64", float64(42), true},
		{"bool", true, true},
		{"json.Number", json.Number("123"), true},
		{"nil", nil, false},
		{"map", map[string]any{}, false},
		{"slice", []any{1, 2}, false},
		{"int", 42, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isScalar(tt.v)
			if got != tt.want {
				t.Errorf("isScalar(%v) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}
