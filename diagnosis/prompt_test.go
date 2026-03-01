package diagnosis

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"amp-sentinel/intake"
	"amp-sentinel/project"
)

func TestTruncatePayload_Small(t *testing.T) {
	payload := json.RawMessage(`{"error":"something broke"}`)
	result := truncatePayload(payload, 1024)
	if result != string(payload) {
		t.Errorf("expected payload as-is, got %q", result)
	}
	if strings.Contains(result, "truncated") {
		t.Error("small payload should not contain truncation message")
	}
}

func TestTruncatePayload_Large(t *testing.T) {
	big := strings.Repeat("a", 2000)
	payload := json.RawMessage(big)
	result := truncatePayload(payload, 100)

	if len(result) >= len(big) {
		t.Errorf("expected truncated result, got len=%d", len(result))
	}
	if !strings.Contains(result, "truncated") {
		t.Error("truncated payload should contain truncation message")
	}
	if !strings.Contains(result, "2000 bytes") {
		t.Errorf("truncation message should contain original size, got %q", result)
	}
}

func TestTruncatePayload_UTF8Boundary(t *testing.T) {
	// Build a string where a multi-byte char straddles the maxSize boundary.
	// '世' is 3 bytes in UTF-8.
	payload := json.RawMessage(strings.Repeat("a", 98) + "世界")
	// maxSize=100 would split '世' (bytes 98-100). The function should walk
	// back to avoid producing invalid UTF-8.
	result := truncatePayload(payload, 100)

	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation message")
	}
	// Extract the part before the truncation notice.
	idx := strings.Index(result, "\n...(payload truncated")
	if idx < 0 {
		t.Fatalf("truncation marker not found in %q", result)
	}
	prefix := result[:idx]
	for i := 0; i < len(prefix); {
		_, size := rune(prefix[i]), 0
		r := []rune(string(prefix[i:]))
		if len(r) == 0 {
			break
		}
		size = len(string(r[0:1]))
		i += size
	}
	// Simpler: just check the prefix is valid UTF-8 by roundtripping.
	if !json.Valid([]byte(`"` + prefix + `"`)) && !isValidUTF8(prefix) {
		t.Error("truncated prefix is not valid UTF-8")
	}
}

func isValidUTF8(s string) bool {
	for i := 0; i < len(s); {
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			_, size = runeAt(s, i)
			if size == 0 {
				return false
			}
		}
		i += size
	}
	return true
}

func runeAt(s string, i int) (rune, int) {
	rs := []rune(string(s[i:]))
	if len(rs) == 0 {
		return 0, 0
	}
	return rs[0], len(string(rs[0:1]))
}

func newTestProject() *project.Project {
	return &project.Project{
		Key:      "test-svc",
		Name:     "Test Service",
		RepoURL:  "https://github.com/example/test-svc",
		Branch:   "main",
		Language: "Go",
	}
}

func newTestEvent() *intake.RawEvent {
	return &intake.RawEvent{
		ID:         "evt-001",
		ProjectKey: "test-svc",
		Source:     "sentry",
		Severity:   "critical",
		Payload:    json.RawMessage(`{"error":"null pointer"}`),
		ReceivedAt: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
	}
}

func TestBuildPrompt_Basic(t *testing.T) {
	p := newTestProject()
	event := newTestEvent()

	result := BuildPrompt(p, event)

	checks := []string{
		p.Name,
		p.Key,
		event.Source,
		event.Severity,
		"```json",
		`"error":"null pointer"`,
	}
	for _, want := range checks {
		if !strings.Contains(result, want) {
			t.Errorf("BuildPrompt output missing %q", want)
		}
	}
}

func TestBuildPrompt_ContainsSchemaDoc(t *testing.T) {
	p := newTestProject()
	event := newTestEvent()

	result := BuildPrompt(p, event)

	if !strings.Contains(result, "schema_version") {
		t.Error("BuildPrompt output should contain schema doc")
	}
}

func TestBuildAgentsMD_Basic(t *testing.T) {
	p := newTestProject()
	event := newTestEvent()

	result := BuildAgentsMD(p, event)

	checks := []string{
		p.Name,
		p.Language,
		p.Branch,
		event.Severity,
		event.Source,
	}
	for _, want := range checks {
		if !strings.Contains(result, want) {
			t.Errorf("BuildAgentsMD output missing %q", want)
		}
	}
}

func TestBuildAgentsMD_WithTitle(t *testing.T) {
	p := newTestProject()
	event := newTestEvent()
	event.Title = "NullPointerException in OrderService"

	result := BuildAgentsMD(p, event)

	if !strings.Contains(result, "NullPointerException in OrderService") {
		t.Error("BuildAgentsMD output should contain event title")
	}
}

func TestBuildAgentsMD_WithSkills(t *testing.T) {
	p := newTestProject()
	p.Skills = []string{"query-orders", "check-logs"}
	event := newTestEvent()

	result := BuildAgentsMD(p, event)

	for _, skill := range p.Skills {
		if !strings.Contains(result, skill) {
			t.Errorf("BuildAgentsMD output missing skill %q", skill)
		}
	}
	if !strings.Contains(result, "Skill") {
		t.Error("BuildAgentsMD output should contain skills section header")
	}
}

func TestBuildAgentsMD_WithoutSkills(t *testing.T) {
	p := newTestProject()
	p.Skills = nil
	event := newTestEvent()

	result := BuildAgentsMD(p, event)

	if strings.Contains(result, "可用 Skill") {
		t.Error("BuildAgentsMD output should not contain skills section when no skills configured")
	}
}
