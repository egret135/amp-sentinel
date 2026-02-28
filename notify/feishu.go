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
func (f *FeishuNotifier) Notify(ctx context.Context, proj *project.Project, inc *intake.Incident, report *diagnosis.Report) error {
	webhook := proj.FeishuWebhook
	if webhook == "" {
		webhook = f.defaultWebhook
	}
	if webhook == "" {
		return fmt.Errorf("no feishu webhook configured for project %s", proj.Key)
	}

	card := f.buildCard(proj, inc, report)
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

		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB max
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			// Feishu returns 200 even on logical errors; check response body
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
				logger.String("incident_id", inc.ID),
			)
			return nil
		}

		lastErr = fmt.Errorf("feishu returned status %d: %s", resp.StatusCode, string(respBody))
		f.log.Warn("feishu.retry", logger.Int("attempt", i+1), logger.Err(lastErr))
	}

	return fmt.Errorf("feishu notification failed after %d attempts: %w", f.retryCount, lastErr)
}

func (f *FeishuNotifier) buildCard(proj *project.Project, inc *intake.Incident, report *diagnosis.Report) map[string]any {
	// Determine header color and title
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

	title := fmt.Sprintf("%s — %s", titlePrefix, proj.Name)

	// Incident summary
	elements := []map[string]any{
		{
			"tag": "div",
			"text": map[string]any{
				"tag": "lark_md",
				"content": fmt.Sprintf(
					"**故障标题**: %s\n**严重程度**: %s\n**环境**: %s\n**发生时间**: %s",
					inc.Title,
					strings.ToUpper(inc.Severity),
					inc.Environment,
					inc.OccurredAt.Format("2006-01-02 15:04:05"),
				),
			},
		},
		{"tag": "hr"},
	}

	// Brief diagnosis summary (truncated to 200 chars)
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

	durationStr := fmt.Sprintf("%.1fs", float64(report.DurationMs)/1000)
	elements = append(elements, map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":     "lark_md",
			"content": fmt.Sprintf("**诊断结论**: %s\n**摘要**: %s\n**耗时**: %s | **对话轮次**: %d", resultIcon, summary, durationStr, report.NumTurns),
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
			"title":    map[string]any{"tag": "plain_text", "content": title},
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
