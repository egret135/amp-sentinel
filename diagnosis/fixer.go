package diagnosis

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"amp-sentinel/amp"
)

// JSONFixerConfig configures the LLM-based JSON fixer.
type JSONFixerConfig struct {
	Timeout         time.Duration
	MaxOutputTokens int
}

// RunJSONFixer attempts to fix malformed JSON using a lightweight LLM call.
// This is the last-resort fallback when local deterministic fixes fail.
// Uses rush mode with strict resource limits.
func RunJSONFixer(ctx context.Context, client *amp.Client, raw string, cfg JSONFixerConfig) (*DiagnosisJSON, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxOutputTokens == 0 {
		cfg.MaxOutputTokens = 4096
	}

	fixCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	prompt := buildFixerPrompt(raw, cfg.MaxOutputTokens)
	result, err := client.Execute(fixCtx, prompt, amp.ExecuteOption{
		Mode: "rush",
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("json fixer execution: %w", err)
	}

	if result.IsError {
		return nil, fmt.Errorf("json fixer error: %s", result.Error)
	}

	diag, parseErr := ParseDiagnosisJSON(result.Result)
	if parseErr != nil {
		return nil, fmt.Errorf("json fixer output unparseable: %w", parseErr)
	}

	return diag, nil
}

func buildFixerPrompt(malformedJSON string, maxTokens int) string {
	// Truncate input to avoid exceeding context limits
	if len(malformedJSON) > maxTokens*4 {
		truncated := malformedJSON[:maxTokens*4]
		for i := 0; i < 3; i++ {
			if utf8.ValidString(truncated) {
				break
			}
			truncated = truncated[:len(truncated)-1]
		}
		malformedJSON = truncated
	}

	return fmt.Sprintf(`你是一个 JSON 修复工具。以下是一段格式错误的 JSON 诊断报告，请修复它并输出正确的 JSON。

修复规则：
1. 只修复语法错误（缺少引号、多余逗号、未闭合括号等）
2. 不要修改语义内容
3. 输出必须是合法的 JSON，用 `+"`"+`json 代码块包裹
4. 不要添加解释文字

需要修复的 JSON：
%s`, malformedJSON)
}
