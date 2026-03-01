package diagnosis

import (
	"testing"
)

func TestParseDiagnosisJSON_ValidJSON(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "NPE in OrderService",
		"conclusion": {
			"has_issue": true,
			"confidence": 0.85,
			"confidence_label": "high"
		},
		"root_causes": [
			{
				"rank": 1,
				"hypothesis": "Null check missing",
				"evidence": [
					{"type": "code", "detail": "getPrice() returns null", "file": "OrderService.java", "line_start": 42, "line_end": 45}
				]
			}
		],
		"code_locations": [
			{"file": "OrderService.java", "line_start": 42, "line_end": 45, "reason": "missing null check"}
		],
		"remediations": ["Add null check before calling getPrice()"],
		"next_actions": ["Check related callers"],
		"non_code_factors": []
	}`

	diag, err := ParseDiagnosisJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diag.Summary != "NPE in OrderService" {
		t.Errorf("summary = %q, want %q", diag.Summary, "NPE in OrderService")
	}
	if !diag.Conclusion.HasIssue {
		t.Error("expected has_issue=true")
	}
	if diag.Conclusion.Confidence != 0.85 {
		t.Errorf("confidence = %f, want 0.85", diag.Conclusion.Confidence)
	}
	if len(diag.RootCauses) != 1 {
		t.Errorf("root_causes count = %d, want 1", len(diag.RootCauses))
	}
}

func TestParseDiagnosisJSON_CodeBlockWrapped(t *testing.T) {
	input := "Here is the diagnosis:\n```json\n" + `{
		"schema_version": "v1",
		"summary": "Config error",
		"conclusion": {"has_issue": false, "confidence": 0.6, "confidence_label": "medium"},
		"root_causes": [{"rank": 1, "hypothesis": "No code issue found"}],
		"code_locations": [],
		"remediations": ["Check database config"],
		"non_code_factors": ["Database connection pool exhausted"]
	}` + "\n```\nEnd of report."

	diag, err := ParseDiagnosisJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diag.Summary != "Config error" {
		t.Errorf("summary = %q, want %q", diag.Summary, "Config error")
	}
	if diag.Conclusion.HasIssue {
		t.Error("expected has_issue=false")
	}
}

func TestParseDiagnosisJSON_TrailingComma(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "Test",
		"conclusion": {"has_issue": true, "confidence": 0.5, "confidence_label": "medium"},
		"root_causes": [{"rank": 1, "hypothesis": "Test cause", "evidence": [],},],
		"code_locations": [],
		"remediations": ["Fix it",],
		"non_code_factors": [],
	}`

	diag, err := ParseDiagnosisJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diag.Summary != "Test" {
		t.Errorf("summary = %q, want %q", diag.Summary, "Test")
	}
}

func TestParseDiagnosisJSON_MissingSummary(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "",
		"conclusion": {"has_issue": true, "confidence": 0.5, "confidence_label": "medium"},
		"root_causes": [{"rank": 1, "hypothesis": "Test"}]
	}`

	_, err := ParseDiagnosisJSON(input)
	if err == nil {
		t.Fatal("expected error for empty summary")
	}
}

func TestParseDiagnosisJSON_MissingRootCauses(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "Test summary",
		"conclusion": {"has_issue": true, "confidence": 0.5, "confidence_label": "medium"},
		"root_causes": []
	}`

	_, err := ParseDiagnosisJSON(input)
	if err == nil {
		t.Fatal("expected error for empty root_causes")
	}
}

func TestParseDiagnosisJSON_InvalidConfidence(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "Test",
		"conclusion": {"has_issue": true, "confidence": 1.5, "confidence_label": "high"},
		"root_causes": [{"rank": 1, "hypothesis": "Test"}]
	}`

	_, err := ParseDiagnosisJSON(input)
	if err == nil {
		t.Fatal("expected error for confidence > 1.0")
	}
}

func TestParseDiagnosisJSON_AutoFixConfidenceLabel(t *testing.T) {
	input := `{
		"schema_version": "v1",
		"summary": "Test",
		"conclusion": {"has_issue": true, "confidence": 0.9, "confidence_label": "INVALID"},
		"root_causes": [{"rank": 1, "hypothesis": "Test"}]
	}`

	diag, err := ParseDiagnosisJSON(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diag.Conclusion.ConfidenceLabel != "high" {
		t.Errorf("confidence_label = %q, want %q (auto-fixed from invalid)", diag.Conclusion.ConfidenceLabel, "high")
	}
}

func TestParseDiagnosisJSON_UnterminatedString(t *testing.T) {
	// Simulates truncated AI output
	input := `{
		"schema_version": "v1",
		"summary": "Test summary",
		"conclusion": {"has_issue": true, "confidence": 0.7, "confidence_label": "medium"},
		"root_causes": [{"rank": 1, "hypothesis": "Truncated output`

	// Should attempt fix but likely fail due to missing fields
	_, err := ParseDiagnosisJSON(input)
	if err == nil {
		t.Log("surprisingly parsed truncated JSON — acceptable if balanced")
	}
}

func TestExtractJSONBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"json block", "text\n```json\n{\"a\":1}\n```\nmore", `{"a":1}`},
		{"code block", "```\n{\"b\":2}\n```", `{"b":2}`},
		{"no block", `{"c":3}`, `{"c":3}`},
		{"non-json block", "```\nhello\n```", "```\nhello\n```"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONBlock(tt.input)
			if got != tt.want {
				t.Errorf("extractJSONBlock() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFixTrailingComma(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"object", `{"a": 1,}`, `{"a": 1}`},
		{"array", `[1, 2,]`, `[1, 2]`},
		{"nested", `{"a": [1,],}`, `{"a": [1]}`},
		{"no trailing", `{"a": 1}`, `{"a": 1}`},
		{"comma in string preserved", `{"text": "a,}"}`, `{"text": "a,}"}`},
		{"comma-bracket in string preserved", `{"msg": "err,]end", "x": 1}`, `{"msg": "err,]end", "x": 1}`},
		{"escaped quote in string", `{"a": "he said \"hi,\"",}`, `{"a": "he said \"hi,\""}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fixTrailingComma(tt.input)
			if got != tt.want {
				t.Errorf("fixTrailingComma(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsInsufficientInformation(t *testing.T) {
	diag := &DiagnosisJSON{
		RootCauses: []RootCause{
			{Hypothesis: "insufficient_information"},
		},
	}
	if !diag.IsInsufficientInformation() {
		t.Error("expected IsInsufficientInformation()=true")
	}

	diag2 := &DiagnosisJSON{
		RootCauses: []RootCause{
			{Hypothesis: "memory leak in connection pool"},
		},
	}
	if diag2.IsInsufficientInformation() {
		t.Error("expected IsInsufficientInformation()=false")
	}
}
