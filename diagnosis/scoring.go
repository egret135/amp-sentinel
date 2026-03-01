package diagnosis

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// QualityScore holds the six-dimension quality assessment of a diagnosis.
// Dimensions marked N/A use -1 and are excluded from the normalized total.
type QualityScore struct {
	Normalized  int      `json:"normalized"`    // 0-100 normalized total
	MaxPossible int      `json:"max_possible"`  // max achievable points (80/90/100)
	Schema      int      `json:"schema"`        // 0-20
	Evidence    int      `json:"evidence"`       // 0-20
	CodeVerify  int      `json:"code_verify"`    // 0-20 or -1 (N/A)
	Coherence   int      `json:"coherence"`      // 0-15
	Actionable  int      `json:"actionable"`     // 0-15
	NonCodePath int      `json:"non_code_path"`  // 0-10 or -1 (N/A)
	Flags       []string `json:"flags,omitempty"`
}

// ScoreQuality computes all quality dimensions except CodeVerify,
// which must be computed separately inside the project lock.
func ScoreQuality(diag *DiagnosisJSON) *QualityScore {
	s := &QualityScore{
		CodeVerify:  -1, // N/A until set by VerifyCodeLocations
		NonCodePath: -1, // default N/A, set below if applicable
	}

	s.Schema = scoreSchema(diag)
	s.Evidence = scoreEvidence(diag)
	s.Coherence = scoreCoherence(diag)
	s.Actionable = scoreActionable(diag)

	// NonCodePath dimension: only scored when code_locations is empty
	if len(diag.CodeLocations) == 0 {
		s.NonCodePath = scoreNonCodePath(diag)
	}

	s.Flags = collectFlags(diag, s)
	return s
}

// NormalizeScore computes the normalized 0-100 score using the dynamic
// max-possible system. N/A dimensions (-1) are excluded from the denominator.
func NormalizeScore(s *QualityScore) int {
	total := 0
	maxPossible := 0

	maxPossible += 20
	total += s.Schema
	maxPossible += 20
	total += s.Evidence

	if s.CodeVerify >= 0 {
		maxPossible += 20
		total += s.CodeVerify
	}

	maxPossible += 15
	total += s.Coherence
	maxPossible += 15
	total += s.Actionable

	if s.NonCodePath >= 0 {
		maxPossible += 10
		total += s.NonCodePath
	}

	s.MaxPossible = maxPossible
	if maxPossible == 0 {
		return 0
	}
	return total * 100 / maxPossible
}

// VerifyCodeLocations checks whether the AI-referenced code locations
// actually exist on disk. MUST be called inside the project lock.
func VerifyCodeLocations(srcDir string, locations []CodeLocation) (int, []string) {
	if len(locations) == 0 {
		return -1, nil // N/A
	}

	var flags []string
	verified := 0
	for _, loc := range locations {
		fullPath, ok := safeJoinUnderRoot(srcDir, loc.File)
		if !ok {
			flags = append(flags, FlagHallucinatedFile)
			continue
		}

		info, err := os.Stat(fullPath)
		if err != nil {
			flags = append(flags, FlagHallucinatedFile)
			continue
		}
		if info.IsDir() {
			flags = append(flags, FlagHallucinatedFile)
			continue
		}

		if loc.LineStart > 0 {
			lineCount, countErr := countLinesSafe(fullPath, 500000)
			if countErr != nil {
				continue // can't read file, skip without flagging
			}
			if loc.LineStart > lineCount || (loc.LineEnd > 0 && loc.LineEnd > lineCount) {
				flags = append(flags, FlagHallucinatedLine)
				continue
			}
		}

		verified++
	}

	score := int(float64(verified) / float64(len(locations)) * 20)
	return score, flags
}

// safeJoinUnderRoot joins root and a relative path, returning the result
// only if it stays within root. Returns ("", false) for path traversal attempts.
func safeJoinUnderRoot(root, rel string) (string, bool) {
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", false
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	full := filepath.Join(root, clean)
	r, err := filepath.Rel(root, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

// HasCodeEvidence returns true if any root cause has evidence of type "code".
func HasCodeEvidence(d *DiagnosisJSON) bool {
	for _, rc := range d.RootCauses {
		for _, ev := range rc.Evidence {
			if ev.Type == "code" {
				return true
			}
		}
	}
	return false
}

// countLinesSafe counts lines in a file, reading at most maxLines.
func countLinesSafe(path string, maxLines int) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		count++
		if count >= maxLines {
			break
		}
	}
	return count, scanner.Err()
}

// scoreSchema checks JSON structural completeness. If we reach this
// function, the JSON has already been parsed and validated, so most
// structural checks pass. We verify individual field quality.
func scoreSchema(diag *DiagnosisJSON) int {
	score := 0

	if diag.Summary != "" {
		score += 5
	}
	// confidence range already validated by parser
	if diag.Conclusion.Confidence >= 0 && diag.Conclusion.Confidence <= 1 {
		score += 5
	}
	if validConfidenceLabels[diag.Conclusion.ConfidenceLabel] {
		score += 3
	}
	if len(diag.RootCauses) > 0 {
		score += 5
	}
	// Check evidence types: deduct if any were auto-fixed from invalid values
	if len(diag.AutoFixedEvidenceTypes) == 0 {
		score += 2
	}
	return score
}

// scoreEvidence evaluates evidence quality across all root causes.
func scoreEvidence(diag *DiagnosisJSON) int {
	// Handle insufficient_information: score based on verification_steps
	if diag.IsInsufficientInformation() {
		return scoreVerificationSteps(diag)
	}

	totalEvidence := 0
	hasDetailedEvidence := false
	for _, rc := range diag.RootCauses {
		totalEvidence += len(rc.Evidence)
		for _, ev := range rc.Evidence {
			if len([]rune(ev.Detail)) > 30 || ev.File != "" {
				hasDetailedEvidence = true
			}
		}
	}

	score := 0
	if totalEvidence >= 1 {
		score += 10
	}
	if hasDetailedEvidence {
		score += 10
	}
	return score
}

// scoreVerificationSteps scores evidence quality when hypothesis is
// "insufficient_information" (evidence may be empty but verification_steps
// must explain what data is needed).
func scoreVerificationSteps(diag *DiagnosisJSON) int {
	totalSteps := 0
	hasDetailedStep := false
	for _, rc := range diag.RootCauses {
		totalSteps += len(rc.VerificationSteps)
		for _, step := range rc.VerificationSteps {
			if len([]rune(step)) > 20 {
				hasDetailedStep = true
			}
		}
	}

	score := 0
	if totalSteps >= 1 {
		score += 10
	}
	if hasDetailedStep {
		score += 10
	}
	return score
}

// scoreCoherence checks that conclusions are internally consistent.
func scoreCoherence(diag *DiagnosisJSON) int {
	score := 0

	// has_issue=true should have non-empty root_causes (already required by schema)
	if diag.Conclusion.HasIssue && len(diag.RootCauses) > 0 {
		score += 8
	} else if !diag.Conclusion.HasIssue {
		score += 8 // no issue claimed, root_causes may explain why
	}

	// high confidence should have >= 2 evidence items
	if diag.Conclusion.ConfidenceLabel == "high" {
		totalEvidence := 0
		for _, rc := range diag.RootCauses {
			totalEvidence += len(rc.Evidence)
		}
		if totalEvidence >= 2 {
			score += 7
		}
	} else {
		score += 7 // non-high confidence, no extra evidence requirement
	}

	return score
}

// scoreActionable checks that remediation suggestions are meaningful.
func scoreActionable(diag *DiagnosisJSON) int {
	score := 0
	if len(diag.Remediations) > 0 {
		score += 8
	}
	for _, r := range diag.Remediations {
		if len([]rune(r)) > 20 {
			score += 7
			break
		}
	}
	return score
}

// scoreNonCodePath checks that non-code factors are explained when
// no code locations are provided.
func scoreNonCodePath(diag *DiagnosisJSON) int {
	score := 0
	if len(diag.NonCodeFactors) > 0 {
		score += 5
	}
	for _, f := range diag.NonCodeFactors {
		if len([]rune(f)) > 20 {
			score += 5
			break
		}
	}
	return score
}

// collectFlags identifies quality issues in the diagnosis.
func collectFlags(diag *DiagnosisJSON, score *QualityScore) []string {
	var flags []string

	// No evidence across all root causes
	totalEvidence := 0
	for _, rc := range diag.RootCauses {
		totalEvidence += len(rc.Evidence)
	}
	if totalEvidence == 0 && !diag.IsInsufficientInformation() {
		flags = append(flags, FlagNoEvidence)
	}

	// High confidence but insufficient evidence support
	if diag.Conclusion.ConfidenceLabel == "high" && totalEvidence < 2 {
		flags = append(flags, FlagHighConfNoSupport)
	}

	// No remediations
	if len(diag.Remediations) == 0 {
		flags = append(flags, FlagEmptyRemediation)
	}

	// Evidence types were auto-fixed from invalid values
	if len(diag.AutoFixedEvidenceTypes) > 0 {
		flags = append(flags, FlagAutoFixedEvType)
	}

	return flags
}
