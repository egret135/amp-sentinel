package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"amp-sentinel/diagnosis"
	"amp-sentinel/intake"
	"amp-sentinel/logger"
	"amp-sentinel/project"
)

// FeishuNotifier sends diagnosis reports to Feishu (Lark) via webhook.
type FeishuNotifier struct {
	defaultWebhook string
	signKey        string
	dashboardURL   string
	httpClient     *http.Client
	log            logger.Logger
	retryCount     int
}

// FeishuConfig holds Feishu webhook configuration.
type FeishuConfig struct {
	DefaultWebhook string
	SignKey         string
	DashboardURL   string
	Timeout        time.Duration
	RetryCount     int
}

// NewFeishuNotifier creates a Feishu notifier.
func NewFeishuNotifier(cfg FeishuConfig, log logger.Logger) *FeishuNotifier {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	retryCount := cfg.RetryCount
	if retryCount == 0 {
		retryCount = 3
	}
	return &FeishuNotifier{
		defaultWebhook: cfg.DefaultWebhook,
		signKey:        cfg.SignKey,
		dashboardURL:   cfg.DashboardURL,
		httpClient:     &http.Client{Timeout: timeout},
		log:            log,
		retryCount:     retryCount,
	}
}

// Notify sends a diagnosis report to the appropriate Feishu webhook.
func (f *FeishuNotifier) Notify(ctx context.Context, proj *project.Project, event *intake.RawEvent, report *diagnosis.Report) error {
	webhook := proj.FeishuWebhook
	if webhook == "" {
		webhook = f.defaultWebhook
	}
	if webhook == "" {
		return fmt.Errorf("no feishu webhook configured for project %s", proj.Key)
	}

	card := f.buildCard(proj, event, report)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}

	if f.signKey != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = f.genSign(ts)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	var lastErr error
	for i := 0; i < f.retryCount; i++ {
		if i > 0 {
			delay := time.NewTimer(time.Duration(i) * time.Second)
			select {
			case <-ctx.Done():
				delay.Stop()
				return ctx.Err()
			case <-delay.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := f.httpClient.Do(req)
		if err != nil {
			lastErr = err
			f.log.Warn("feishu.retry", logger.Int("attempt", i+1), logger.Err(err))
			continue
		}

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var feishuResp struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			}
			if jsonErr := json.Unmarshal(respBody, &feishuResp); jsonErr == nil && feishuResp.Code != 0 {
				lastErr = fmt.Errorf("feishu error code %d: %s", feishuResp.Code, feishuResp.Msg)
				f.log.Warn("feishu.api_error", logger.Int("code", feishuResp.Code), logger.String("msg", feishuResp.Msg))
				continue
			}
			f.log.Info("feishu.sent",
				logger.String("project", proj.Key),
				logger.String("event_id", event.ID),
			)
			return nil
		}

		lastErr = fmt.Errorf("feishu returned status %d: %s", resp.StatusCode, string(respBody))
		f.log.Warn("feishu.retry", logger.Int("attempt", i+1), logger.Err(lastErr))
	}

	return fmt.Errorf("feishu notification failed after %d attempts: %w", f.retryCount, lastErr)
}

func (f *FeishuNotifier) buildCard(proj *project.Project, event *intake.RawEvent, report *diagnosis.Report) map[string]any {
	var template, titlePrefix string
	switch {
	case report.Tainted:
		template = "purple"
		titlePrefix = "🟣 诊断异常（源码被意外修改）"
	case report.HasIssue && report.Confidence == "high":
		template = "red"
		titlePrefix = "🔴 故障诊断报告"
	case report.HasIssue:
		template = "orange"
		titlePrefix = "🟠 故障诊断报告（需进一步确认）"
	default:
		template = "yellow"
		titlePrefix = "🟡 故障诊断报告（未定位到代码问题）"
	}

	cardTitle := fmt.Sprintf("%s — %s", titlePrefix, proj.Name)

	// Extract display fields from payload
	var payloadMap map[string]any
	json.Unmarshal(event.Payload, &payloadMap)
	df := intake.ExtractDisplayFields(payloadMap)

	// Build event summary lines
	var eventLines []string

	displayTitle := event.Title
	if displayTitle == "" && df.ErrorMsg != "" {
		displayTitle = df.ErrorMsg
	}
	fallbackTitle := fmt.Sprintf("来自 %s 的事件", event.Source)
	if displayTitle == "" {
		displayTitle = fallbackTitle
	}
	eventLines = append(eventLines,
		fmt.Sprintf("**标题**: %s", intake.EscapeLarkMD(displayTitle)))

	eventLines = append(eventLines,
		fmt.Sprintf("**严重程度**: %s", strings.ToUpper(event.Severity)))

	eventLines = append(eventLines,
		fmt.Sprintf("**来源**: %s", intake.EscapeLarkMD(event.Source)))

	if df.Environment != "" {
		eventLines = append(eventLines,
			fmt.Sprintf("**环境**: %s", intake.EscapeLarkMD(df.Environment)))
	}

	if df.OccurredAt != "" {
		eventLines = append(eventLines,
			fmt.Sprintf("**发生时间**: %s", df.OccurredAt))
	}

	if df.URL != "" {
		eventLines = append(eventLines,
			fmt.Sprintf("**URL/路径**: %s", intake.EscapeLarkMD(df.URL)))
	}

	if displayTitle == fallbackTitle && df == (intake.DisplayFields{}) && len(event.Payload) > 0 {
		preview := intake.SanitizeDisplayText(intake.TruncateRunes(string(event.Payload), 200))
		if preview != "" {
			eventLines = append(eventLines,
				fmt.Sprintf("**原始数据**: %s", intake.EscapeLarkMD(preview)))
		}
	}

	elements := []map[string]any{
		{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": strings.Join(eventLines, "\n"),
			},
		},
		{"tag": "hr"},
	}

	// Diagnosis summary
	summary := report.Summary
	if summary == "" {
		summary = report.RawResult
	}
	if runes := []rune(summary); len(runes) > 200 {
		summary = string(runes[:200]) + "..."
	}

	var resultIcon string
	if report.HasIssue {
		resultIcon = "🔴 发现问题"
	} else {
		resultIcon = "🟢 未发现代码问题"
	}

	confidenceMap := map[string]string{
		"high": "高", "medium": "中", "low": "低",
	}
	confidenceStr := confidenceMap[report.Confidence]
	if confidenceStr == "" {
		confidenceStr = report.Confidence
	}

	var diagContent string
	if report.ReusedFromID != "" {
		diagContent = fmt.Sprintf(
			"**诊断结论**: %s\n**置信度**: %s\n**摘要**: %s\n**执行方式**: 复用历史诊断（未重新执行 AI 分析）",
			resultIcon, confidenceStr, intake.EscapeLarkMD(summary))
	} else {
		durationStr := fmt.Sprintf("%.1fs", float64(report.DurationMs)/1000)
		diagContent = fmt.Sprintf(
			"**诊断结论**: %s\n**置信度**: %s\n**摘要**: %s\n**耗时**: %s | **对话轮次**: %d",
			resultIcon, confidenceStr, intake.EscapeLarkMD(summary),
			durationStr, report.NumTurns)
	}

	if report.QualityScore.Normalized > 0 {
		diagContent += fmt.Sprintf("\n**质量评分**: %d/100", report.QualityScore.Normalized)
	}
	if len(report.QualityScore.Flags) > 0 {
		diagContent += fmt.Sprintf("\n**质量标记**: %s", strings.Join(report.QualityScore.Flags, ", "))
	}
	if report.ReusedFromID != "" {
		commitNote := "commit 一致"
		for _, flag := range report.QualityScore.Flags {
			if flag == "REUSED_STALE_COMMIT" {
				commitNote = "⚠️ commit 已变更"
				break
			}
		}
		diagContent += fmt.Sprintf("\n**复用自**: %s (%s)", intake.EscapeLarkMD(report.ReusedFromID), commitNote)
	}

	elements = append(elements, map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":     "lark_md",
			"content": diagContent,
		},
	})

	// Tainted warning
	if report.Tainted {
		elements = append(elements,
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "lark_md",
					"content": "⚠️ **安全告警**: 诊断过程中检测到源码被意外修改，已自动回滚。此诊断结果可能不可靠。",
				},
			},
		)
	}

	// Owners
	if len(proj.Owners) > 0 {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**👤 负责人**: %s", strings.Join(proj.Owners, ", ")),
			},
		})
	}

	// Dashboard button
	if f.dashboardURL != "" {
		detailURL := fmt.Sprintf("%s#tasks", strings.TrimRight(f.dashboardURL, "/"))
		elements = append(elements,
			map[string]any{"tag": "hr"},
			map[string]any{
				"tag": "action",
				"actions": []map[string]any{
					{
						"tag": "button",
						"text": map[string]any{
							"tag":     "plain_text",
							"content": "📋 查看完整诊断报告",
						},
						"type": "primary",
						"url":  detailURL,
					},
				},
			},
		)
	}

	return map[string]any{
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": cardTitle},
			"template": template,
		},
		"elements": elements,
	}
}

func (f *FeishuNotifier) genSign(timestamp string) string {
	stringToSign := timestamp + "\n" + f.signKey
	h := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
