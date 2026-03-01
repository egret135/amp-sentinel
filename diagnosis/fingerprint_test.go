package diagnosis

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"amp-sentinel/intake"
)

func TestComputeDiagnosisFingerprint_Basic(t *testing.T) {
	payload := json.RawMessage(`{"error_msg":"connection refused","env":"production"}`)
	defaultFields := []string{"error_msg", "error", "message", "msg"}

	fp1 := ComputeDiagnosisFingerprint("proj-a", payload, nil, defaultFields)
	fp2 := ComputeDiagnosisFingerprint("proj-a", payload, nil, defaultFields)

	if fp1 == "" {
		t.Fatal("fingerprint should not be empty")
	}
	if fp1 != fp2 {
		t.Fatalf("same input should produce same fingerprint: %s vs %s", fp1, fp2)
	}
}

func TestComputeDiagnosisFingerprint_EnvironmentContext(t *testing.T) {
	payloadProd := json.RawMessage(`{"error_msg":"timeout","environment":"production"}`)
	payloadStaging := json.RawMessage(`{"error_msg":"timeout","environment":"staging"}`)
	defaultFields := []string{"error_msg"}

	fpProd := ComputeDiagnosisFingerprint("proj-a", payloadProd, nil, defaultFields)
	fpStaging := ComputeDiagnosisFingerprint("proj-a", payloadStaging, nil, defaultFields)

	if fpProd == fpStaging {
		t.Fatal("different environments should produce different fingerprints")
	}
}

func TestComputeDiagnosisFingerprint_Normalization(t *testing.T) {
	payload1 := json.RawMessage(`{"error_msg":"request 550e8400-e29b-41d4-a716-446655440000 failed at 2026-03-01T10:00:00Z"}`)
	payload2 := json.RawMessage(`{"error_msg":"request 660e8400-e29b-41d4-a716-446655440001 failed at 2026-03-02T15:30:00Z"}`)
	defaultFields := []string{"error_msg"}

	fp1 := ComputeDiagnosisFingerprint("proj-a", payload1, nil, defaultFields)
	fp2 := ComputeDiagnosisFingerprint("proj-a", payload2, nil, defaultFields)

	if fp1 != fp2 {
		t.Fatalf("normalized fingerprints should match: %s vs %s", fp1, fp2)
	}
}

func TestComputeDiagnosisFingerprint_DifferentErrors(t *testing.T) {
	payload1 := json.RawMessage(`{"error_msg":"null pointer exception"}`)
	payload2 := json.RawMessage(`{"error_msg":"connection timeout"}`)
	defaultFields := []string{"error_msg"}

	fp1 := ComputeDiagnosisFingerprint("proj-a", payload1, nil, defaultFields)
	fp2 := ComputeDiagnosisFingerprint("proj-a", payload2, nil, defaultFields)

	if fp1 == fp2 {
		t.Fatal("different errors should produce different fingerprints")
	}
}

func TestComputeDiagnosisFingerprint_ProjectDedupFields(t *testing.T) {
	payload := json.RawMessage(`{"error_msg":"timeout","custom_field":"value123"}`)
	defaultFields := []string{"error_msg"}
	projectFields := []string{"custom_field"}

	fpDefault := ComputeDiagnosisFingerprint("proj-a", payload, nil, defaultFields)
	fpProject := ComputeDiagnosisFingerprint("proj-a", payload, projectFields, defaultFields)

	if fpDefault == fpProject {
		t.Fatal("different dedup fields should produce different fingerprints")
	}
}

func TestCanReuse_BasicMatch(t *testing.T) {
	cached := &Report{
		TaskID:     "task-123",
		HasIssue:   true,
		Confidence: "high",
		CommitHash: "abc123",
		QualityScore: QualityScore{
			Normalized: 85,
		},
	}

	ok, flags := canReuse(cached, "abc123", "warning", 80)
	if !ok {
		t.Fatal("should be reusable")
	}
	if len(flags) != 0 {
		t.Fatalf("expected no flags, got %v", flags)
	}
}

func TestCanReuse_NilCached(t *testing.T) {
	ok, _ := canReuse(nil, "abc123", "warning", 80)
	if ok {
		t.Fatal("nil cached should not be reusable")
	}
}

func TestCanReuse_LowQuality(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{Normalized: 50},
		CommitHash:   "abc123",
	}
	ok, _ := canReuse(cached, "abc123", "warning", 80)
	if ok {
		t.Fatal("low quality should not be reusable")
	}
}

func TestCanReuse_Tainted(t *testing.T) {
	cached := &Report{
		Tainted:      true,
		QualityScore: QualityScore{Normalized: 90},
		CommitHash:   "abc123",
	}
	ok, _ := canReuse(cached, "abc123", "warning", 80)
	if ok {
		t.Fatal("tainted reports should not be reusable")
	}
}

func TestCanReuse_InsufficientInformation(t *testing.T) {
	cached := &Report{
		StructuredResult: &DiagnosisJSON{
			RootCauses: []RootCause{{Hypothesis: "insufficient_information"}},
		},
		QualityScore: QualityScore{Normalized: 90},
		CommitHash:   "abc123",
	}
	ok, _ := canReuse(cached, "abc123", "warning", 80)
	if ok {
		t.Fatal("insufficient_information should not be reusable")
	}
}

func TestCanReuse_HallucinatedFile(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{
			Normalized: 90,
			Flags:      []string{FlagHallucinatedFile},
		},
		CommitHash: "abc123",
	}
	ok, _ := canReuse(cached, "abc123", "warning", 80)
	if ok {
		t.Fatal("hallucinated file should not be reusable")
	}
}

func TestCanReuse_HallucinatedLine(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{
			Normalized: 90,
			Flags:      []string{FlagHallucinatedLine},
		},
		CommitHash: "abc123",
	}
	ok, _ := canReuse(cached, "abc123", "warning", 80)
	if ok {
		t.Fatal("hallucinated line should not be reusable")
	}
}

func TestCanReuse_StaleCommit_Warning(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{Normalized: 90},
		CommitHash:   "old-commit",
	}
	ok, flags := canReuse(cached, "new-commit", "warning", 80)
	if !ok {
		t.Fatal("warning with stale commit should still be reusable")
	}
	if len(flags) != 1 || flags[0] != FlagReusedStaleCommit {
		t.Fatalf("expected REUSED_STALE_COMMIT flag, got %v", flags)
	}
}

func TestCanReuse_StaleCommit_Critical(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{Normalized: 90},
		CommitHash:   "old-commit",
	}
	ok, _ := canReuse(cached, "new-commit", "critical", 80)
	if ok {
		t.Fatal("critical with stale commit should not be reusable")
	}
}

func TestCanReuse_EmptyCommitHash(t *testing.T) {
	cached := &Report{
		QualityScore: QualityScore{Normalized: 90},
		CommitHash:   "",
	}
	ok, flags := canReuse(cached, "abc123", "critical", 80)
	if !ok {
		t.Fatal("empty commit hash on cached should be reusable (unknown commit)")
	}
	if len(flags) != 0 {
		t.Fatalf("expected no flags for empty cached commit, got %v", flags)
	}
}

func TestBuildReusedReport(t *testing.T) {
	cached := &Report{
		TaskID:             "task-orig",
		Summary:            "NPE in OrderService",
		RawResult:          "full raw result",
		HasIssue:           true,
		Confidence:         "high",
		OriginalConfidence: 0.9,
		FinalConfidence:    0.9,
		FinalConfLabel:     "high",
		CommitHash:         "abc123",
		QualityScore:       QualityScore{Normalized: 90, Flags: []string{"FLAG_A"}},
		PromptVersion:      "v1",
	}

	event := &intake.RawEvent{
		ID:         "evt-new",
		ProjectKey: "proj-a",
	}

	report := buildReusedReport(cached, event, "Project A", "abc123", "fp:abc", nil)

	if report.IncidentID != "evt-new" {
		t.Fatalf("expected event id evt-new, got %s", report.IncidentID)
	}
	if report.ReusedFromID != "task-orig" {
		t.Fatalf("expected reused_from task-orig, got %s", report.ReusedFromID)
	}
	if report.Fingerprint != "fp:abc" {
		t.Fatalf("expected fingerprint fp:abc, got %s", report.Fingerprint)
	}
	if report.DurationMs != 0 {
		t.Fatalf("expected 0 duration for reused report, got %d", report.DurationMs)
	}
	if report.Summary != "NPE in OrderService" {
		t.Fatalf("expected cached summary, got %s", report.Summary)
	}
	if report.ProjectName != "Project A" {
		t.Fatalf("expected Project A, got %s", report.ProjectName)
	}
}

func TestBuildReusedReport_WithExtraFlags(t *testing.T) {
	cached := &Report{
		TaskID:       "task-orig",
		QualityScore: QualityScore{Normalized: 85, Flags: []string{"FLAG_A"}},
	}

	event := &intake.RawEvent{ID: "evt-1", ProjectKey: "proj-a"}
	report := buildReusedReport(cached, event, "P", "new-commit", "fp:x", []string{FlagReusedStaleCommit})

	// Should not mutate cached report's flags
	if len(cached.QualityScore.Flags) != 1 {
		t.Fatal("cached report flags should not be mutated")
	}
	if len(report.QualityScore.Flags) != 2 {
		t.Fatalf("expected 2 flags, got %v", report.QualityScore.Flags)
	}
}

func TestNormalizeString(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"error at 2026-03-01T10:00:00Z", "error at <ts>"},
		{"uuid 550e8400-e29b-41d4-a716-446655440000 found", "uuid <uuid> found"},
		{"ptr 0x7fff5fbff8a0 deref", "ptr <addr> deref"},
		{"request 123456 failed", "request 123456 failed"},
		{"order 12345678 processed", "order <n> processed"},
		{"simple error", "simple error"},
	}

	for _, tt := range tests {
		result := normalizeString(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeString(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestExtractEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected string
	}{
		{"top-level environment", `{"environment":"production"}`, "production"},
		{"env field", `{"env":"staging"}`, "staging"},
		{"nested tags.env", `{"tags":{"env":"dev"}}`, "dev"},
		{"no env", `{"error":"oops"}`, ""},
		{"empty payload", `{}`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractEnvironment(json.RawMessage(tt.payload))
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFingerprintLookup_Integration(t *testing.T) {
	lookup := FingerprintLookup(func(_ context.Context, projectKey, fingerprint string, since time.Time) (*Report, error) {
		if projectKey == "proj-a" && fingerprint == "test-fp" {
			return &Report{
				TaskID:       "task-cached",
				Summary:      "cached result",
				HasIssue:     true,
				Confidence:   "high",
				CommitHash:   "abc123",
				QualityScore: QualityScore{Normalized: 90},
			}, nil
		}
		return nil, nil
	})

	ctx := context.Background()
	report, err := lookup(ctx, "proj-a", "test-fp", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report == nil {
		t.Fatal("expected cached report")
	}
	if report.TaskID != "task-cached" {
		t.Fatalf("expected task-cached, got %s", report.TaskID)
	}

	// Miss case
	report, err = lookup(ctx, "proj-b", "test-fp", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if report != nil {
		t.Fatal("expected nil for miss")
	}
}

func TestNormalizePayload(t *testing.T) {
	input := json.RawMessage(`{"msg":"error at 2026-01-01T00:00:00Z for user 550e8400-e29b-41d4-a716-446655440000","count":42}`)
	result := normalizePayload(input)

	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatal(err)
	}
	msg, ok := m["msg"].(string)
	if !ok {
		t.Fatal("msg should be a string")
	}
	if msg == "error at 2026-01-01T00:00:00Z for user 550e8400-e29b-41d4-a716-446655440000" {
		t.Fatal("normalization should have replaced dynamic content")
	}
}
