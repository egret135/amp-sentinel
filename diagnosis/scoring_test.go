package diagnosis

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScoreQuality_HighQuality(t *testing.T) {
	diag := &DiagnosisJSON{
		Summary: "NPE in OrderService.createOrder",
		Conclusion: Conclusion{
			HasIssue:        true,
			Confidence:      0.9,
			ConfidenceLabel: "high",
		},
		RootCauses: []RootCause{
			{
				Rank:       1,
				Hypothesis: "getPrice() returns null when product is discontinued",
				Evidence: []Evidence{
					{Type: "code", Detail: "OrderService.java:42 calls getPrice() without null check", File: "OrderService.java", LineStart: 42, LineEnd: 45},
					{Type: "log", Detail: "NullPointerException at OrderService.createOrder(OrderService.java:42)"},
				},
			},
		},
		CodeLocations: []CodeLocation{
			{File: "OrderService.java", LineStart: 42, LineEnd: 45, Reason: "missing null check"},
		},
		Remediations: []string{"Add null check before getPrice() call and handle discontinued products gracefully"},
	}

	score := ScoreQuality(diag)
	if score.Schema != 20 {
		t.Errorf("Schema = %d, want 20", score.Schema)
	}
	if score.Evidence != 20 {
		t.Errorf("Evidence = %d, want 20", score.Evidence)
	}
	if score.Coherence != 15 {
		t.Errorf("Coherence = %d, want 15", score.Coherence)
	}
	if score.Actionable != 15 {
		t.Errorf("Actionable = %d, want 15", score.Actionable)
	}
	if score.CodeVerify != -1 {
		t.Errorf("CodeVerify = %d, want -1 (N/A before VerifyCodeLocations)", score.CodeVerify)
	}
	if score.NonCodePath != -1 {
		t.Errorf("NonCodePath = %d, want -1 (N/A when code_locations present)", score.NonCodePath)
	}
}

func TestScoreQuality_NoCodeIssue(t *testing.T) {
	diag := &DiagnosisJSON{
		Summary: "No code issue found",
		Conclusion: Conclusion{
			HasIssue:        false,
			Confidence:      0.7,
			ConfidenceLabel: "medium",
		},
		RootCauses: []RootCause{
			{
				Rank:       1,
				Hypothesis: "Infrastructure issue",
				Evidence: []Evidence{
					{Type: "config", Detail: "Database connection pool is exhausted based on monitoring metrics"},
				},
			},
		},
		CodeLocations:  []CodeLocation{},
		Remediations:   []string{"Increase database connection pool size in application.yml"},
		NonCodeFactors: []string{"Database connection pool exhaustion due to traffic spike"},
	}

	score := ScoreQuality(diag)
	if score.NonCodePath == -1 {
		t.Error("NonCodePath should be scored when code_locations is empty")
	}
	if score.NonCodePath != 10 {
		t.Errorf("NonCodePath = %d, want 10", score.NonCodePath)
	}
}

func TestScoreQuality_InsufficientInformation(t *testing.T) {
	diag := &DiagnosisJSON{
		Summary: "Cannot determine root cause",
		Conclusion: Conclusion{
			HasIssue:        true,
			Confidence:      0.3,
			ConfidenceLabel: "low",
		},
		RootCauses: []RootCause{
			{
				Rank:              1,
				Hypothesis:        "insufficient_information",
				Evidence:          []Evidence{},
				VerificationSteps: []string{"Check application logs for the timeframe around the incident"},
			},
		},
		Remediations: []string{"Collect more detailed logs before further analysis"},
	}

	score := ScoreQuality(diag)
	// Should score verification_steps instead of evidence
	if score.Evidence < 10 {
		t.Errorf("Evidence = %d, expected >= 10 (scored via verification_steps)", score.Evidence)
	}
	// Should not flag NO_EVIDENCE for insufficient_information
	for _, f := range score.Flags {
		if f == FlagNoEvidence {
			t.Error("should not flag NO_EVIDENCE for insufficient_information")
		}
	}
}

func TestScoreQuality_HighConfNoSupport(t *testing.T) {
	diag := &DiagnosisJSON{
		Summary: "Memory leak detected",
		Conclusion: Conclusion{
			HasIssue:        true,
			Confidence:      0.95,
			ConfidenceLabel: "high",
		},
		RootCauses: []RootCause{
			{
				Rank:       1,
				Hypothesis: "Memory leak in cache",
				Evidence: []Evidence{
					{Type: "code", Detail: "CacheManager does not evict entries"},
				},
			},
		},
		Remediations: []string{"Add TTL to cache entries"},
	}

	score := ScoreQuality(diag)
	found := false
	for _, f := range score.Flags {
		if f == FlagHighConfNoSupport {
			found = true
		}
	}
	if !found {
		t.Error("expected HIGH_CONF_NO_SUPPORT flag when high confidence with only 1 evidence")
	}
}

func TestNormalizeScore(t *testing.T) {
	s := &QualityScore{
		Schema:      20,
		Evidence:    20,
		CodeVerify:  -1, // N/A
		Coherence:   15,
		Actionable:  15,
		NonCodePath: -1, // N/A
	}
	norm := NormalizeScore(s)
	if norm != 100 {
		t.Errorf("NormalizeScore() = %d, want 100 (all non-N/A dimensions perfect)", norm)
	}
	if s.MaxPossible != 70 {
		t.Errorf("MaxPossible = %d, want 70", s.MaxPossible)
	}
}

func TestNormalizeScore_WithCodeVerify(t *testing.T) {
	s := &QualityScore{
		Schema:      20,
		Evidence:    20,
		CodeVerify:  10, // 50% of files verified
		Coherence:   15,
		Actionable:  15,
		NonCodePath: -1, // N/A
	}
	norm := NormalizeScore(s)
	// total = 80, max = 90, normalized = 80*100/90 = 88
	if norm != 88 {
		t.Errorf("NormalizeScore() = %d, want 88", norm)
	}
}

func TestVerifyCodeLocations_Empty(t *testing.T) {
	score, flags := VerifyCodeLocations("/tmp", nil)
	if score != -1 {
		t.Errorf("score = %d, want -1 (N/A)", score)
	}
	if flags != nil {
		t.Errorf("flags = %v, want nil", flags)
	}
}

func TestVerifyCodeLocations_FileExists(t *testing.T) {
	dir := t.TempDir()
	// Create a test file with some lines
	testFile := filepath.Join(dir, "test.go")
	os.WriteFile(testFile, []byte("line1\nline2\nline3\nline4\nline5\n"), 0644)

	locations := []CodeLocation{
		{File: "test.go", LineStart: 2, LineEnd: 4, Reason: "test"},
	}

	score, flags := VerifyCodeLocations(dir, locations)
	if score != 20 {
		t.Errorf("score = %d, want 20", score)
	}
	if len(flags) != 0 {
		t.Errorf("flags = %v, want empty", flags)
	}
}

func TestVerifyCodeLocations_HallucinatedFile(t *testing.T) {
	dir := t.TempDir()
	locations := []CodeLocation{
		{File: "nonexistent.java", LineStart: 10, LineEnd: 20, Reason: "test"},
	}

	score, flags := VerifyCodeLocations(dir, locations)
	if score != 0 {
		t.Errorf("score = %d, want 0", score)
	}
	found := false
	for _, f := range flags {
		if f == FlagHallucinatedFile {
			found = true
		}
	}
	if !found {
		t.Error("expected HALLUCINATED_FILE flag")
	}
}

func TestVerifyCodeLocations_HallucinatedLine(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "small.go")
	os.WriteFile(testFile, []byte("line1\nline2\nline3\n"), 0644)

	locations := []CodeLocation{
		{File: "small.go", LineStart: 100, LineEnd: 200, Reason: "test"},
	}

	score, flags := VerifyCodeLocations(dir, locations)
	if score != 0 {
		t.Errorf("score = %d, want 0", score)
	}
	found := false
	for _, f := range flags {
		if f == FlagHallucinatedLine {
			found = true
		}
	}
	if !found {
		t.Error("expected HALLUCINATED_LINE flag")
	}
}

func TestVerifyCodeLocations_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	locations := []CodeLocation{
		{File: "../../../etc/passwd", LineStart: 1, LineEnd: 1, Reason: "test"},
		{File: "/etc/hosts", LineStart: 1, LineEnd: 1, Reason: "test"},
	}

	score, flags := VerifyCodeLocations(dir, locations)
	if score != 0 {
		t.Errorf("score = %d, want 0 (path traversal should be rejected)", score)
	}
	hallucinatedCount := 0
	for _, f := range flags {
		if f == FlagHallucinatedFile {
			hallucinatedCount++
		}
	}
	if hallucinatedCount != 2 {
		t.Errorf("expected 2 HALLUCINATED_FILE flags for path traversal, got %d", hallucinatedCount)
	}
}

func TestHasCodeEvidence(t *testing.T) {
	withCode := &DiagnosisJSON{
		RootCauses: []RootCause{
			{Evidence: []Evidence{{Type: "code", Detail: "test"}}},
		},
	}
	if !HasCodeEvidence(withCode) {
		t.Error("expected HasCodeEvidence=true")
	}

	withoutCode := &DiagnosisJSON{
		RootCauses: []RootCause{
			{Evidence: []Evidence{{Type: "log", Detail: "test"}}},
		},
	}
	if HasCodeEvidence(withoutCode) {
		t.Error("expected HasCodeEvidence=false")
	}
}

func TestCollectFlags_EmptyRemediation(t *testing.T) {
	diag := &DiagnosisJSON{
		Summary: "Test",
		Conclusion: Conclusion{
			HasIssue:        true,
			Confidence:      0.5,
			ConfidenceLabel: "medium",
		},
		RootCauses: []RootCause{
			{Hypothesis: "test", Evidence: []Evidence{{Type: "log", Detail: "test"}}},
		},
		Remediations: []string{},
	}

	score := ScoreQuality(diag)
	found := false
	for _, f := range score.Flags {
		if f == FlagEmptyRemediation {
			found = true
		}
	}
	if !found {
		t.Error("expected EMPTY_REMEDIATION flag")
	}
}
