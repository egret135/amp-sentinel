package diagnosis

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"amp-sentinel/intake"
)

// FingerprintLookup is a function that finds a recent high-quality report
// matching the given fingerprint. Returns nil if no match is found.
// The since parameter specifies the earliest diagnosis time to consider.
type FingerprintLookup func(ctx context.Context, projectKey, fingerprint string, since time.Time) (*Report, error)

// FingerprintConfig holds settings for diagnosis fingerprint reuse.
type FingerprintConfig struct {
	Enabled          bool          // whether fingerprint reuse is enabled
	Window           time.Duration // how far back to look for reusable reports (default 24h)
	MinScore         int           // minimum quality score for reuse (default 80)
	DefaultDedupFields []string    // fallback dedup fields when project doesn't configure them
}

// FlagReusedStaleCommit indicates the reused report's commit differs from current.
const FlagReusedStaleCommit = "REUSED_STALE_COMMIT"

// ComputeDiagnosisFingerprint computes a fingerprint for diagnosis reuse.
// It extends the intake dedup fingerprint with environment context and
// value normalization to match semantically identical errors.
func ComputeDiagnosisFingerprint(projectKey string, payload json.RawMessage, dedupFields []string, defaultDedupFields []string) string {
	// Normalize the payload values before fingerprinting to collapse
	// dynamic content (timestamps, UUIDs, etc.) into placeholders.
	normalizedPayload := normalizePayload(payload)

	// Build DedupConfig from project fields
	var cfg *intake.DedupConfig
	if len(dedupFields) > 0 {
		cfg = &intake.DedupConfig{Fields: dedupFields}
	}

	baseFP := intake.ComputeFingerprint(projectKey, normalizedPayload, cfg, defaultDedupFields)

	// Append environment context to prevent cross-environment reuse
	env := extractEnvironment(payload)
	if env != "" {
		return baseFP + ":" + env
	}
	return baseFP
}

// canReuse checks whether a cached report is eligible for reuse.
func canReuse(cached *Report, currentCommitHash, severity string, minScore int) (ok bool, flags []string) {
	if cached == nil {
		return false, nil
	}

	// Never reuse tainted reports
	if cached.Tainted {
		return false, nil
	}

	// Never reuse insufficient_information conclusions
	if cached.StructuredResult != nil && cached.StructuredResult.IsInsufficientInformation() {
		return false, nil
	}

	// Check quality score
	if cached.QualityScore.Normalized < minScore {
		return false, nil
	}

	// Check for hallucination flags
	for _, f := range cached.QualityScore.Flags {
		if f == FlagHallucinatedFile || f == FlagHallucinatedLine {
			return false, nil
		}
	}

	// Commit hash consistency check
	commitMatch := currentCommitHash == "" || cached.CommitHash == "" || currentCommitHash == cached.CommitHash
	if !commitMatch {
		// Critical events must not reuse stale-commit reports
		if severity == "critical" {
			return false, nil
		}
		// Non-critical: allow reuse but flag stale commit
		flags = append(flags, FlagReusedStaleCommit)
	}

	return true, flags
}

// buildReusedReport creates a Report that references the original cached report.
func buildReusedReport(cached *Report, event *intake.RawEvent, projName, currentCommitHash, fingerprint string, extraFlags []string) *Report {
	report := &Report{
		IncidentID:         event.ID,
		ProjectKey:         event.ProjectKey,
		ProjectName:        projName,
		Summary:            cached.Summary,
		RawResult:          cached.RawResult,
		HasIssue:           cached.HasIssue,
		Confidence:         cached.Confidence,
		SessionID:          cached.SessionID,
		DurationMs:         0, // reused, no execution time
		NumTurns:           0,
		ToolsUsed:          cached.ToolsUsed,
		SkillsUsed:         cached.SkillsUsed,
		Tainted:            false,
		DiagnosedAt:        time.Now(),
		StructuredResult:   cached.StructuredResult,
		QualityScore:       cached.QualityScore,
		OriginalConfidence: cached.OriginalConfidence,
		FinalConfidence:    cached.FinalConfidence,
		FinalConfLabel:     cached.FinalConfLabel,
		CommitHash:         currentCommitHash,
		Fingerprint:        fingerprint,
		ReusedFromID:       cached.TaskID,
		PromptVersion:      cached.PromptVersion,
	}

	// Append extra flags (e.g., REUSED_STALE_COMMIT)
	if len(extraFlags) > 0 {
		qs := report.QualityScore
		qs.Flags = append(append([]string(nil), qs.Flags...), extraFlags...)
		report.QualityScore = qs
	}

	return report
}

// --- Normalization helpers ---

var (
	reTimestamp = regexp.MustCompile(`\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}[.\d]*[Z]?([+-]\d{2}:?\d{2})?`)
	reUUID     = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reMemAddr  = regexp.MustCompile(`0x[0-9a-fA-F]{6,16}`)
	reNumbers  = regexp.MustCompile(`\b\d{8,}\b`)
)

// normalizePayload applies normalization to JSON string values,
// replacing dynamic content with placeholders.
func normalizePayload(payload json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return payload
	}
	normalizeMap(m)
	out, err := json.Marshal(m)
	if err != nil {
		return payload
	}
	return out
}

func normalizeMap(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case string:
			m[k] = normalizeString(val)
		case map[string]any:
			normalizeMap(val)
		case []any:
			normalizeSlice(val)
		}
	}
}

func normalizeSlice(s []any) {
	for i, v := range s {
		switch val := v.(type) {
		case string:
			s[i] = normalizeString(val)
		case map[string]any:
			normalizeMap(val)
		case []any:
			normalizeSlice(val)
		}
	}
}

func normalizeString(s string) string {
	s = reTimestamp.ReplaceAllString(s, "<TS>")
	s = reUUID.ReplaceAllString(s, "<UUID>")
	s = reMemAddr.ReplaceAllString(s, "<ADDR>")
	s = reNumbers.ReplaceAllString(s, "<N>")
	return strings.ToLower(strings.TrimSpace(s))
}

// extractEnvironment attempts to extract environment info from the payload.
var envCandidateFields = []string{
	"environment", "env", "deploy_env", "stage",
	"labels.env", "tags.env", "tags.environment",
}

func extractEnvironment(payload json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return ""
	}
	for _, field := range envCandidateFields {
		v := resolvePayloadField(m, field)
		if s, ok := v.(string); ok && s != "" {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	return ""
}

// resolvePayloadField extracts a value from a map supporting dotted paths.
func resolvePayloadField(m map[string]any, path string) any {
	if !strings.Contains(path, ".") {
		return m[path]
	}
	segments := strings.Split(path, ".")
	var current any = m
	for _, seg := range segments {
		cm, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = cm[seg]
	}
	return current
}
