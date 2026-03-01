package diagnosis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amp-sentinel/amp"
	"amp-sentinel/intake"
	"amp-sentinel/logger"
	"amp-sentinel/project"
	"amp-sentinel/skill"
)

// Engine orchestrates the full diagnosis pipeline:
// source preparation → fingerprint reuse check → Amp invocation →
// safety verification → structured parsing → quality scoring → report generation.
type Engine struct {
	ampClient        *amp.Client
	sources          *project.SourceManager
	registry         *project.Registry
	skillMgr         *skill.Manager
	log              logger.Logger
	mode             string
	skillDir         string
	sessionDir       string
	structuredOutput bool
	jsonFixerEnabled bool
	promptVersion    string

	// P1: Fingerprint reuse
	fingerprintLookup FingerprintLookup
	fpConfig          FingerprintConfig
}

// EngineConfig holds configuration for the diagnosis engine.
type EngineConfig struct {
	Mode             string // Amp agent mode (smart/rush/deep)
	SkillDir         string // root directory containing skill definitions
	SessionDir       string // directory to save raw Amp session logs
	StructuredOutput bool   // P0: enable structured JSON output + quality scoring
	JSONFixerEnabled bool   // P0: enable LLM JSON fixer as last-resort fallback
	PromptVersion    string // version tag for A/B testing

	// P1: Fingerprint reuse
	FingerprintLookup FingerprintLookup
	FingerprintConfig FingerprintConfig
}

// NewEngine creates a diagnosis engine.
func NewEngine(
	ampClient *amp.Client,
	sources *project.SourceManager,
	registry *project.Registry,
	skillMgr *skill.Manager,
	log logger.Logger,
	cfg EngineConfig,
) *Engine {
	if cfg.Mode == "" {
		cfg.Mode = "smart"
	}
	if cfg.PromptVersion == "" {
		cfg.PromptVersion = "v1"
	}
	fpCfg := cfg.FingerprintConfig
	if fpCfg.Window == 0 {
		fpCfg.Window = 24 * time.Hour
	}
	if fpCfg.MinScore <= 0 {
		fpCfg.MinScore = 80
	}
	if len(fpCfg.DefaultDedupFields) == 0 {
		fpCfg.DefaultDedupFields = []string{"error_msg", "error", "message", "msg"}
	}

	return &Engine{
		ampClient:         ampClient,
		sources:           sources,
		registry:          registry,
		skillMgr:          skillMgr,
		log:               log,
		mode:              cfg.Mode,
		skillDir:          cfg.SkillDir,
		sessionDir:        cfg.SessionDir,
		structuredOutput:  cfg.StructuredOutput,
		jsonFixerEnabled:  cfg.JSONFixerEnabled,
		promptVersion:     cfg.PromptVersion,
		fingerprintLookup: cfg.FingerprintLookup,
		fpConfig:          fpCfg,
	}
}

// Diagnose runs a full diagnosis for the given incident.
func (e *Engine) Diagnose(ctx context.Context, event *intake.RawEvent) (*Report, error) {
	log := e.log.WithFields(
		logger.String("event_id", event.ID),
		logger.String("project_key", event.ProjectKey),
	)

	// 1. Lookup project
	proj, err := e.registry.Lookup(event.ProjectKey)
	if err != nil {
		return nil, fmt.Errorf("project lookup: %w", err)
	}
	log.Info("diagnosis.started", logger.String("project_name", proj.Name))

	// 2. P1: Fingerprint reuse check — runs BEFORE acquiring the project lock
	//    to avoid holding the lock during store I/O. Only needs the lock briefly
	//    to get the current commit hash for reuse validation.
	fingerprint := ""
	if e.fpConfig.Enabled && e.fingerprintLookup != nil {
		fingerprint = ComputeDiagnosisFingerprint(
			proj.Key, event.Payload, proj.Dedup.Fields, e.fpConfig.DefaultDedupFields,
		)

		since := time.Now().Add(-e.fpConfig.Window)
		cached, lookupErr := e.fingerprintLookup(ctx, proj.Key, fingerprint, since)
		if lookupErr != nil {
			log.Warn("diagnosis.fingerprint_lookup_failed", logger.Err(lookupErr))
		} else if cached != nil {
			// Need commit hash to validate reuse — acquire lock briefly
			reuseUnlock := e.sources.Lock(proj.Key)
			_, prepErr := e.sources.Prepare(ctx, proj)
			var commitHash string
			if prepErr == nil {
				commitHash, _ = e.sources.CommitHash(ctx, proj.Key)
			}
			reuseUnlock()

			if prepErr != nil {
				log.Warn("diagnosis.reuse_prepare_failed", logger.Err(prepErr))
			} else {
				ok, extraFlags := canReuse(cached, commitHash, event.Severity, e.fpConfig.MinScore)
				if ok {
					log.Info("diagnosis.fingerprint_reused",
						logger.String("reused_from", cached.TaskID),
						logger.String("fingerprint", fingerprint),
						logger.Bool("commit_match", commitHash == cached.CommitHash),
					)
					return buildReusedReport(cached, event, proj.Name, commitHash, fingerprint, extraFlags), nil
				}
			}
		}
	}

	// 3. Acquire per-project lock. When structured output is enabled,
	//    we unlock explicitly after code verification to allow scoring
	//    to run without holding the lock.
	unlock := e.sources.Lock(proj.Key)
	unlocked := false
	defer func() {
		if !unlocked {
			unlock()
		}
	}()

	// 4. Prepare source code
	log.Info("diagnosis.preparing_source")
	srcDir, err := e.sources.Prepare(ctx, proj)
	if err != nil {
		return nil, fmt.Errorf("source prepare: %w", err)
	}

	commitHash, _ := e.sources.CommitHash(ctx, proj.Key)
	log.Info("diagnosis.source_ready",
		logger.String("src_dir", srcDir),
		logger.String("commit", commitHash),
	)

	// 5. Build prompt — inject constraints directly instead of writing AGENTS.md
	//    to the source directory (writing files would trigger false tainted detection).
	agentsMD := BuildAgentsMD(proj, event)
	prompt := agentsMD + "\n---\n\n" + BuildPrompt(proj, event)

	var sessionFile *os.File
	if e.sessionDir != "" {
		if err := os.MkdirAll(e.sessionDir, 0755); err != nil {
			log.Warn("diagnosis.session_dir_failed", logger.Err(err))
		} else {
			// Sanitize filename components to prevent path traversal
			safeID := sanitizeFilename(event.ID)
			safeKey := sanitizeFilename(proj.Key)
			fname := fmt.Sprintf("%s_%s_%d.ndjson", safeID, safeKey, time.Now().Unix())
			var createErr error
			sessionFile, createErr = os.Create(filepath.Join(e.sessionDir, fname))
			if createErr != nil {
				log.Warn("diagnosis.session_file_failed", logger.Err(createErr))
			}
		}
	}
	defer func() {
		if sessionFile != nil {
			sessionFile.Close()
		}
	}()

	// Resolve skills and MCP configs for this project
	var mcpServers map[string]amp.MCPServerConfig
	if e.skillMgr != nil && len(proj.Skills) > 0 {
		resolved := e.skillMgr.Resolve(proj.Skills)
		if len(resolved) > 0 {
			skillMCP := e.skillMgr.MCPConfig(resolved)
			if len(skillMCP) > 0 {
				mcpServers = make(map[string]amp.MCPServerConfig, len(skillMCP))
				for name, srv := range skillMCP {
					mcpServers[name] = amp.MCPServerConfig{
						Command: srv.Command,
						Args:    srv.Args,
						Env:     srv.Env,
						URL:     srv.URL,
						Headers: srv.Headers,
					}
				}
			}
			log.Info("diagnosis.skills_resolved", logger.Int("count", len(resolved)), logger.Int("mcp_servers", len(mcpServers)))
		}
	}

	skillsUsed := map[string]struct{}{}

	startTime := time.Now()
	result, err := e.ampClient.Execute(ctx, prompt, amp.ExecuteOption{
		WorkDir:     srcDir,
		Mode:        e.mode,
		Permissions: amp.ReadOnlyPermissions(),
		MCPServers:  mcpServers,
		Labels:      []string{"sentinel", proj.Key, event.Severity},
	}, func(msg amp.StreamMessage) error {
		// Save raw session log
		if sessionFile != nil {
			line, _ := marshalJSON(msg)
			sessionFile.Write(append(line, '\n'))
		}

		// Track skill usage
		if msg.Type == "assistant" && msg.Message != nil {
			for _, block := range msg.Message.Content {
				if block.Type == "tool_use" {
					for _, skill := range proj.Skills {
						if containsSkill(block.Name, skill) {
							skillsUsed[skill] = struct{}{}
						}
					}
				}
			}
		}
		return nil
	})

	if err != nil {
		log.Error("diagnosis.amp_failed", logger.Err(err))
		return nil, fmt.Errorf("amp execution: %w", err)
	}

	// 6. Safety verification — check no source files were modified.
	//    Use an independent context because the diagnosis ctx may be
	//    cancelled due to timeout, but the safety check MUST still run.
	//    Fail-closed: if the check itself fails, treat as tainted.
	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer safetyCancel()

	tainted := false
	hasChanges, checkErr := e.sources.HasChanges(safetyCtx, proj.Key)
	if checkErr != nil {
		tainted = true
		log.Error("diagnosis.safety_check_failed_marking_tainted", logger.Err(checkErr))
	} else if hasChanges {
		tainted = true
		log.Error("security.tainted",
			logger.String("project_key", proj.Key),
			logger.String("event_id", event.ID),
		)
		if resetErr := e.sources.ResetChanges(safetyCtx, proj.Key); resetErr != nil {
			log.Error("security.reset_failed", logger.Err(resetErr))
		}
	}

	// 7. Structured JSON parsing + code verification (inside lock)
	var structuredDiag *DiagnosisJSON
	var codeVerifyScore = -1
	var codeVerifyFlags []string

	if e.structuredOutput && !result.IsError {
		var parseErr error
		structuredDiag, parseErr = ParseDiagnosisJSON(result.Result)
		if parseErr != nil {
			log.Info("diagnosis.structured_parse_failed", logger.Err(parseErr))
		}

		// LLM fixer as last resort — use independent context because
		// the diagnosis ctx may be nearly expired after a long Amp execution.
		if structuredDiag == nil && e.jsonFixerEnabled {
			fixerCtx := context.Background()
			structuredDiag, parseErr = RunJSONFixer(fixerCtx, e.ampClient, result.Result, JSONFixerConfig{})
			if parseErr != nil {
				log.Info("diagnosis.json_fixer_failed", logger.Err(parseErr))
			}
		}

		// Code location verification (must be inside lock — reads srcDir)
		if structuredDiag != nil {
			codeVerifyScore, codeVerifyFlags = VerifyCodeLocations(srcDir, structuredDiag.CodeLocations)

			// Spec: when evidence contains "code" type but code_locations is empty,
			// CodeVerify should be 0 (penalize) not N/A (exempt from scoring).
			if codeVerifyScore == -1 && HasCodeEvidence(structuredDiag) {
				codeVerifyScore = 0
			}
		}
	}

	// 8. Explicit unlock — quality scoring runs outside the lock
	unlock()
	unlocked = true

	// 9. Quality scoring (pure computation, no I/O)
	var qualityScore *QualityScore
	if structuredDiag != nil {
		qualityScore = ScoreQuality(structuredDiag)
		qualityScore.CodeVerify = codeVerifyScore
		qualityScore.Flags = append(qualityScore.Flags, codeVerifyFlags...)
		qualityScore.Normalized = NormalizeScore(qualityScore)
	}

	// 10. Build report
	skills := make([]string, 0, len(skillsUsed))
	for s := range skillsUsed {
		skills = append(skills, s)
	}

	report := &Report{
		IncidentID:    event.ID,
		ProjectKey:    proj.Key,
		ProjectName:   proj.Name,
		RawResult:     result.Result,
		SessionID:     result.SessionID,
		DurationMs:    result.DurationMs,
		NumTurns:      result.NumTurns,
		ToolsUsed:     result.ToolsUsed,
		SkillsUsed:    skills,
		Tainted:       tainted,
		DiagnosedAt:   time.Now(),
		Fingerprint:   fingerprint,
		CommitHash:    commitHash,
		PromptVersion: e.promptVersion,
	}

	if result.IsError {
		report.Summary = "诊断执行异常: " + result.Error
		report.HasIssue = false
		report.Confidence = "low"
		report.FinalConfLabel = "low"
		report.OriginalConfidence = 0.3
		report.FinalConfidence = 0.3
	} else if structuredDiag != nil {
		// Structured output path
		report.Summary = structuredDiag.Summary
		report.HasIssue = structuredDiag.Conclusion.HasIssue
		report.Confidence = structuredDiag.Conclusion.ConfidenceLabel
		report.StructuredResult = structuredDiag
		report.OriginalConfidence = structuredDiag.Conclusion.Confidence
		report.FinalConfidence = structuredDiag.Conclusion.Confidence
		report.FinalConfLabel = structuredDiag.Conclusion.ConfidenceLabel
		if qualityScore != nil {
			report.QualityScore = *qualityScore
		}
	} else {
		// Fallback to heuristic detection (old path)
		report.Summary = extractSummary(result.Result)
		report.HasIssue = detectHasIssue(result.Result)
		report.Confidence = detectConfidence(result.Result)
		report.FinalConfLabel = report.Confidence
		// Map heuristic label to a numeric confidence for consistency
		switch report.Confidence {
		case "high":
			report.OriginalConfidence = 0.8
		case "medium":
			report.OriginalConfidence = 0.5
		case "low":
			report.OriginalConfidence = 0.3
		}
		report.FinalConfidence = report.OriginalConfidence
		if e.structuredOutput {
			// Structured output was enabled but parsing failed — flag it
			report.QualityScore = QualityScore{
				Flags: []string{FlagSchemaInvalid},
			}
		}
	}

	if result.Usage != nil {
		report.Usage = &UsageInfo{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
		}
	}

	elapsed := time.Since(startTime)
	log.Info("diagnosis.completed",
		logger.Bool("has_issue", report.HasIssue),
		logger.String("confidence", report.Confidence),
		logger.Int("quality_score", report.QualityScore.Normalized),
		logger.Int64("duration_ms", elapsed.Milliseconds()),
		logger.Int("turns", result.NumTurns),
		logger.Bool("tainted", tainted),
		logger.Bool("structured", structuredDiag != nil),
	)

	return report, nil
}

// extractSummary takes the first ~200 runes as a rough summary (rune-safe).
func extractSummary(result string) string {
	if len(result) == 0 {
		return ""
	}
	runes := []rune(result)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return result
}

// detectHasIssue performs a heuristic check on whether the report
// indicates a code-level issue was found.
func detectHasIssue(result string) bool {
	noIssueKeywords := []string{
		"未发现明显",
		"代码层面没有问题",
		"代码逻辑无异常",
		"未定位到代码问题",
		"no issue found",
		"no code-level issue",
	}
	lower := strings.ToLower(result)
	for _, kw := range noIssueKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return false
		}
	}
	return true
}

// detectConfidence performs a heuristic check on confidence level.
// NOTE: Keywords must be specific enough to avoid false positives from
// the report structure itself (e.g. "根因分析" appears in every report
// as a section header, so "根本原因" alone would be a false positive).
func detectConfidence(result string) string {
	lower := strings.ToLower(result)

	// Check low confidence first (more conservative default)
	lowKeywords := []string{"不确定", "low confidence", "需要进一步", "建议排查", "无法确认", "可能性较低"}
	for _, kw := range lowKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return "low"
		}
	}

	// High confidence requires strong, specific assertions
	highKeywords := []string{
		"高可能性", "high confidence", "可以确定", "确认根因",
		"根本原因是", "根因为", "问题定位到",
	}
	for _, kw := range highKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return "high"
		}
	}

	return "medium"
}

func containsSkill(toolName, skill string) bool {
	return strings.Contains(strings.ToLower(toolName), strings.ToLower(skill))
}

func marshalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// sanitizeFilename replaces any character not in [A-Za-z0-9._-] with underscore
// to prevent path traversal attacks in session log filenames.
func sanitizeFilename(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
