package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"amp-sentinel/diagnosis"
	"amp-sentinel/intake"
	"amp-sentinel/logger"
	"amp-sentinel/project"
)

func newTestNotifier(dashboardURL string) *FeishuNotifier {
	return NewFeishuNotifier(FeishuConfig{
		SignKey:      "test-sign-key",
		DashboardURL: dashboardURL,
	}, logger.Nop())
}

func baseEvent() *intake.RawEvent {
	return &intake.RawEvent{
		ID:         "evt-1",
		ProjectKey: "proj-1",
		Payload:    json.RawMessage(`{"error_msg":"something broke"}`),
		Source:     "grafana",
		Severity:   "critical",
		Title:      "Test Alert",
		ReceivedAt: time.Now(),
	}
}

func baseProject() *project.Project {
	return &project.Project{
		Key:  "proj-1",
		Name: "TestProject",
	}
}

func cardJSON(card map[string]any) string {
	b, _ := json.Marshal(card)
	return string(b)
}

func TestGenSign(t *testing.T) {
	n := newTestNotifier("")
	ts := "1700000000"
	sign := n.genSign(ts)
	if sign == "" {
		t.Fatal("genSign returned empty string")
	}
	// Must be valid base64
	if strings.ContainsAny(sign, " \t\n") {
		t.Fatalf("genSign result contains whitespace: %q", sign)
	}
	// Deterministic: same inputs produce same output
	if sign2 := n.genSign(ts); sign2 != sign {
		t.Fatalf("genSign not deterministic: %q != %q", sign, sign2)
	}
	// Different timestamp produces different sign
	if sign3 := n.genSign("1700000001"); sign3 == sign {
		t.Fatal("genSign returned same result for different timestamps")
	}
}

func TestBuildCard_HighConfidenceIssue(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   true,
		Confidence: "high",
		Summary:    "NPE in handler",
		DurationMs: 5000,
		NumTurns:   3,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	header := card["header"].(map[string]any)
	if header["template"] != "red" {
		t.Errorf("expected template red, got %v", header["template"])
	}
	title := header["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "🔴") {
		t.Errorf("expected title to contain 🔴, got %q", title)
	}
	if !strings.Contains(s, "发现问题") {
		t.Error("card should contain 发现问题")
	}
}

func TestBuildCard_LowConfidenceIssue(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   true,
		Confidence: "medium",
		Summary:    "Possible memory leak",
		DurationMs: 8000,
		NumTurns:   5,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)

	header := card["header"].(map[string]any)
	if header["template"] != "orange" {
		t.Errorf("expected template orange, got %v", header["template"])
	}
	title := header["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "🟠") {
		t.Errorf("expected title to contain 🟠, got %q", title)
	}
}

func TestBuildCard_NoIssue(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   false,
		Confidence: "high",
		Summary:    "No code issue found",
		DurationMs: 3000,
		NumTurns:   2,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)

	header := card["header"].(map[string]any)
	if header["template"] != "yellow" {
		t.Errorf("expected template yellow, got %v", header["template"])
	}
	title := header["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "🟡") {
		t.Errorf("expected title to contain 🟡, got %q", title)
	}
	if !strings.Contains(cardJSON(card), "未发现代码问题") {
		t.Error("card should contain 未发现代码问题")
	}
}

func TestBuildCard_Tainted(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   true,
		Confidence: "high",
		Summary:    "Found issue but tainted",
		Tainted:    true,
		DurationMs: 4000,
		NumTurns:   3,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	header := card["header"].(map[string]any)
	if header["template"] != "purple" {
		t.Errorf("expected template purple, got %v", header["template"])
	}
	title := header["title"].(map[string]any)["content"].(string)
	if !strings.Contains(title, "🟣") {
		t.Errorf("expected title to contain 🟣, got %q", title)
	}
	if !strings.Contains(s, "安全告警") {
		t.Error("tainted card should contain safety warning (安全告警)")
	}
}

func TestBuildCard_WithDashboardURL(t *testing.T) {
	n := newTestNotifier("https://dashboard.example.com")
	report := &diagnosis.Report{
		HasIssue:   true,
		Confidence: "high",
		Summary:    "NPE",
		DurationMs: 1000,
		NumTurns:   1,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	if !strings.Contains(s, "button") {
		t.Error("card with dashboard URL should contain a button element")
	}
	if !strings.Contains(s, "https://dashboard.example.com#tasks") {
		t.Error("card should contain dashboard URL with #tasks")
	}
}

func TestBuildCard_WithoutDashboardURL(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   false,
		Summary:    "No issue",
		DurationMs: 1000,
		NumTurns:   1,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	if strings.Contains(s, "button") {
		t.Error("card without dashboard URL should not contain a button element")
	}
}

func TestBuildCard_WithOwners(t *testing.T) {
	n := newTestNotifier("")
	proj := baseProject()
	proj.Owners = []string{"alice", "bob"}
	report := &diagnosis.Report{
		HasIssue:   false,
		Summary:    "OK",
		DurationMs: 1000,
		NumTurns:   1,
	}
	card := n.buildCard(proj, baseEvent(), report)
	s := cardJSON(card)

	if !strings.Contains(s, "负责人") {
		t.Error("card with owners should contain 负责人")
	}
	if !strings.Contains(s, "alice") || !strings.Contains(s, "bob") {
		t.Error("card should list owner names")
	}
}

func TestBuildCard_ReusedReport(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:     true,
		Confidence:   "high",
		Summary:      "Known issue",
		ReusedFromID: "rpt-old-123",
		DurationMs:   0,
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	if !strings.Contains(s, "复用历史诊断") {
		t.Error("reused report card should mention 复用历史诊断")
	}
	if !strings.Contains(s, "rpt-old-123") {
		t.Error("reused report card should contain the reused ID")
	}
}

func TestBuildCard_ReusedStaleCommit(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:     true,
		Confidence:   "high",
		Summary:      "Known issue",
		ReusedFromID: "rpt-old-456",
		QualityScore: diagnosis.QualityScore{
			Flags: []string{"REUSED_STALE_COMMIT"},
		},
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	if !strings.Contains(s, "commit 已变更") {
		t.Error("reused stale commit card should show commit 已变更")
	}
}

func TestBuildCard_QualityScore(t *testing.T) {
	n := newTestNotifier("")
	report := &diagnosis.Report{
		HasIssue:   true,
		Confidence: "high",
		Summary:    "Issue found",
		DurationMs: 2000,
		NumTurns:   2,
		QualityScore: diagnosis.QualityScore{
			Normalized: 85,
		},
	}
	card := n.buildCard(baseProject(), baseEvent(), report)
	s := cardJSON(card)

	if !strings.Contains(s, "质量评分") {
		t.Error("card with quality score > 0 should show 质量评分")
	}
	if !strings.Contains(s, "85/100") {
		t.Error("card should display the normalized score as 85/100")
	}
}
