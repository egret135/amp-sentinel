package diagnosis

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Quality flag constants identify specific quality issues in a diagnosis.
const (
	FlagSchemaInvalid     = "SCHEMA_INVALID"
	FlagNoEvidence        = "NO_EVIDENCE"
	FlagHallucinatedFile  = "HALLUCINATED_FILE"
	FlagHallucinatedLine  = "HALLUCINATED_LINE"
	FlagHighConfNoSupport = "HIGH_CONF_NO_SUPPORT"
	FlagNoConclusion      = "NO_CONCLUSION"
	FlagEmptyRemediation  = "EMPTY_REMEDIATION"
	FlagAutoFixedEvType   = "AUTO_FIXED_EVIDENCE_TYPE"
)

var validEvidenceTypes = map[string]bool{
	"code": true, "log": true, "stack": true, "config": true,
}

var validConfidenceLabels = map[string]bool{
	"high": true, "medium": true, "low": true,
}

// DiagnosisJSON represents the structured output from AI diagnosis.
type DiagnosisJSON struct {
	SchemaVersion  string         `json:"schema_version"`
	Summary        string         `json:"summary"`
	Conclusion     Conclusion     `json:"conclusion"`
	RootCauses     []RootCause    `json:"root_causes"`
	CodeLocations  []CodeLocation `json:"code_locations"`
	Remediations   []string       `json:"remediations"`
	NextActions    []string       `json:"next_actions"`
	NonCodeFactors []string       `json:"non_code_factors"`

	// AutoFixedEvidenceTypes records original invalid evidence types
	// that were auto-corrected during validation. Not serialized.
	AutoFixedEvidenceTypes []string `json:"-"`
}

// Conclusion holds the diagnosis verdict.
type Conclusion struct {
	HasIssue        bool    `json:"has_issue"`
	Confidence      float64 `json:"confidence"`
	ConfidenceLabel string  `json:"confidence_label"`
}

// RootCause describes a potential root cause.
type RootCause struct {
	Rank              int        `json:"rank"`
	Hypothesis        string     `json:"hypothesis"`
	Evidence          []Evidence `json:"evidence"`
	CounterEvidence   []string   `json:"counter_evidence"`
	VerificationSteps []string   `json:"verification_steps"`
}

// Evidence supports a root cause hypothesis.
type Evidence struct {
	Type      string `json:"type"`
	Detail    string `json:"detail"`
	File      string `json:"file,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
}

// CodeLocation identifies a specific code location relevant to the diagnosis.
type CodeLocation struct {
	File      string `json:"file"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Reason    string `json:"reason"`
}

// IsInsufficientInformation returns true if any root cause indicates
// insufficient information to make a diagnosis.
func (d *DiagnosisJSON) IsInsufficientInformation() bool {
	for _, rc := range d.RootCauses {
		if rc.Hypothesis == "insufficient_information" {
			return true
		}
	}
	return false
}

// DiagnosisOutputSchemaDoc documents the expected JSON output format
// for inclusion in the diagnosis prompt.
const DiagnosisOutputSchemaDoc = `{
  "schema_version": "v1",
  "summary": "一句话故障摘要（≤200字符）",
  "conclusion": {
    "has_issue": true,
    "confidence": 0.85,
    "confidence_label": "high|medium|low"
  },
  "root_causes": [
    {
      "rank": 1,
      "hypothesis": "可能的根本原因描述（如信息不足可填 insufficient_information）",
      "evidence": [
        {
          "type": "code|log|stack|config",
          "detail": "具体证据描述",
          "file": "src/service/OrderService.java",
          "line_start": 123,
          "line_end": 140
        }
      ],
      "counter_evidence": ["反面证据或不支持此假设的因素"],
      "verification_steps": ["验证此根因的具体步骤"]
    }
  ],
  "code_locations": [
    {
      "file": "src/service/OrderService.java",
      "line_start": 123,
      "line_end": 140,
      "reason": "此处未做 null 检查导致 NPE"
    }
  ],
  "remediations": ["具体的修复建议"],
  "next_actions": ["进一步排查建议"],
  "non_code_factors": ["可能的非代码因素（基础设施/配置/外部依赖等）"]
}

字段约束：
- summary: 必填，≤200字符
- conclusion.confidence: 必填，0.0~1.0
- conclusion.confidence_label: 必填，枚举 high|medium|low
- root_causes: 必填，≥1项。允许 hypothesis="insufficient_information" 表示信息不足
- evidence.type: 枚举 code|log|stack|config
- code_locations: 可为空数组，但 has_issue=true 时应尽量提供
- non_code_factors: 当 has_issue=false 时必填
- 不要编造根因或证据来满足格式要求`

// ParseDiagnosisJSON attempts to parse the raw AI output as structured JSON.
// Applies local deterministic fixes (extract code block, fix trailing commas, etc.).
// Returns nil and an error if parsing fails after all local fix attempts.
func ParseDiagnosisJSON(raw string) (*DiagnosisJSON, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty input")
	}

	// Attempt 1: extract ```json block and parse
	jsonStr := extractJSONBlock(raw)
	if diag, err := parseAndValidate(jsonStr); err == nil {
		return diag, nil
	}

	// Attempt 2: apply local deterministic fixes
	fixed := fixTrailingComma(jsonStr)
	fixed = fixUnterminatedString(fixed)
	if diag, err := parseAndValidate(fixed); err == nil {
		return diag, nil
	}

	// Attempt 3: extract raw JSON object from text (no code block wrapper)
	if obj := extractJSONObject(raw); obj != "" && obj != jsonStr {
		obj = fixTrailingComma(obj)
		obj = fixUnterminatedString(obj)
		if diag, err := parseAndValidate(obj); err == nil {
			return diag, nil
		}
	}

	return nil, fmt.Errorf("failed to parse diagnosis JSON after local fixes")
}

var reJSONBlock = regexp.MustCompile("(?s)```json\\s*\\n?(.*?)```")
var reCodeBlock = regexp.MustCompile("(?s)```\\s*\\n?(.*?)```")

// extractJSONBlock extracts JSON from ```json ... ``` code blocks.
// If no code block is found, returns the input trimmed.
func extractJSONBlock(raw string) string {
	if matches := reJSONBlock.FindStringSubmatch(raw); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	if matches := reCodeBlock.FindStringSubmatch(raw); len(matches) > 1 {
		candidate := strings.TrimSpace(matches[1])
		if len(candidate) > 0 && candidate[0] == '{' {
			return candidate
		}
	}
	return strings.TrimSpace(raw)
}

// extractJSONObject finds the first balanced { ... } object in the text.
func extractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		if escaped {
			escaped = false
			continue
		}
		c := raw[i]
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	// Unbalanced — return from start to end (truncated output)
	return raw[start:]
}

// fixTrailingComma removes trailing commas before } and ],
// respecting string boundaries so commas inside strings are untouched.
func fixTrailingComma(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			b.WriteByte(c)
			continue
		}
		if c == '\\' && inString {
			escaped = true
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			inString = !inString
			b.WriteByte(c)
			continue
		}
		if inString {
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			// Look ahead past whitespace for } or ]
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				// Skip the trailing comma (and whitespace will be written naturally)
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// fixUnterminatedString closes unterminated strings and unbalanced brackets
// at the end of truncated JSON output.
func fixUnterminatedString(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	// Check for unterminated string
	inString := false
	escaped := false
	for _, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
		}
	}
	if inString {
		s += `"`
	}
	return balanceBrackets(s)
}

// balanceBrackets appends missing } and ] to close unbalanced JSON.
func balanceBrackets(s string) string {
	var stack []byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' && inString {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		s += string(stack[i])
	}
	return s
}

// parseAndValidate unmarshals JSON and validates the diagnosis structure.
func parseAndValidate(jsonStr string) (*DiagnosisJSON, error) {
	var diag DiagnosisJSON
	if err := json.Unmarshal([]byte(jsonStr), &diag); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	if err := validateDiagnosisJSON(&diag); err != nil {
		return nil, fmt.Errorf("validation: %w", err)
	}
	return &diag, nil
}

// validateDiagnosisJSON checks that required fields are present and valid.
// Auto-fixes minor issues (label mismatch, summary truncation) rather than rejecting.
func validateDiagnosisJSON(d *DiagnosisJSON) error {
	if d.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if runes := []rune(d.Summary); len(runes) > 200 {
		d.Summary = string(runes[:200])
	}

	if d.Conclusion.Confidence < 0 || d.Conclusion.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1, got %f", d.Conclusion.Confidence)
	}

	if !validConfidenceLabels[d.Conclusion.ConfidenceLabel] {
		d.Conclusion.ConfidenceLabel = confidenceLabelFromValue(d.Conclusion.Confidence)
	}

	if len(d.RootCauses) == 0 {
		return fmt.Errorf("root_causes must have at least 1 entry")
	}

	for i, rc := range d.RootCauses {
		for j, ev := range rc.Evidence {
			if ev.Type != "" && !validEvidenceTypes[ev.Type] {
				d.AutoFixedEvidenceTypes = append(d.AutoFixedEvidenceTypes, ev.Type)
				d.RootCauses[i].Evidence[j].Type = "log"
			}
		}
	}

	return nil
}

func confidenceLabelFromValue(conf float64) string {
	if conf >= 0.8 {
		return "high"
	}
	if conf >= 0.5 {
		return "medium"
	}
	return "low"
}
