package diagnosis

import (
	"strings"
	"testing"
)

func TestExtractSummary(t *testing.T) {
	longASCII := strings.Repeat("a", 250)
	// CJK string: 210 runes, each 3 bytes in UTF-8
	longCJK := strings.Repeat("你", 210)

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"short string no truncation", "hello world", "hello world"},
		{"exactly 200 runes", strings.Repeat("x", 200), strings.Repeat("x", 200)},
		{"long ASCII truncated", longASCII, strings.Repeat("a", 200) + "..."},
		{"long CJK truncated at rune boundary", longCJK, strings.Repeat("你", 200) + "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSummary(tt.input)
			if got != tt.want {
				t.Errorf("extractSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectHasIssue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"normal text has issue", "found a nil pointer dereference in handler.go", true},
		{"contains 未发现明显", "经过分析，未发现明显的代码缺陷", false},
		{"contains 代码层面没有问题", "总结：代码层面没有问题", false},
		{"contains 代码逻辑无异常", "检查后代码逻辑无异常", false},
		{"contains 未定位到代码问题", "未定位到代码问题，建议排查配置", false},
		{"contains no issue found", "After review, no issue found in codebase.", false},
		{"contains No Issue Found case insensitive", "No Issue Found in the logs.", false},
		{"contains no code-level issue", "There is no code-level issue here.", false},
		{"empty string", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectHasIssue(tt.input)
			if got != tt.want {
				t.Errorf("detectHasIssue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDetectConfidence(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"low keyword 不确定", "目前不确定根因是什么", "low"},
		{"low keyword low confidence", "This is a low confidence diagnosis.", "low"},
		{"low keyword 需要进一步", "需要进一步排查日志", "low"},
		{"low keyword 建议排查", "建议排查网络配置", "low"},
		{"low keyword 无法确认", "无法确认问题原因", "low"},
		{"low keyword 可能性较低", "代码导致的可能性较低", "low"},
		{"high keyword 高可能性", "该问题高可能性由内存泄漏导致", "high"},
		{"high keyword high confidence", "high confidence: root cause identified", "high"},
		{"high keyword 可以确定", "可以确定是空指针异常", "high"},
		{"high keyword 确认根因", "已确认根因为连接池耗尽", "high"},
		{"high keyword 根本原因是", "根本原因是缓存失效", "high"},
		{"high keyword 根因为", "根因为数据库超时", "high"},
		{"high keyword 问题定位到", "问题定位到第42行的并发bug", "high"},
		{"neutral text returns medium", "The service restarted and logs show timeout errors.", "medium"},
		{"empty returns medium", "", "medium"},
		{"both low and high keywords low wins", "不确定但高可能性是内存泄漏", "low"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectConfidence(tt.input)
			if got != tt.want {
				t.Errorf("detectConfidence() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainsSkill(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		skill    string
		want     bool
	}{
		{"exact match", "log-analysis", "log-analysis", true},
		{"case insensitive match", "LogAnalysis", "loganalysis", true},
		{"substring match", "mcp_log-analysis_query", "log-analysis", true},
		{"no match", "code-review", "log-analysis", false},
		{"empty skill", "code-review", "", true},
		{"empty tool name", "", "log-analysis", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsSkill(tt.toolName, tt.skill)
			if got != tt.want {
				t.Errorf("containsSkill(%q, %q) = %v, want %v", tt.toolName, tt.skill, got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean string", "hello-world_v1.2", "hello-world_v1.2"},
		{"string with spaces", "hello world", "hello_world"},
		{"path separators", "../../etc/passwd", ".._.._etc_passwd"},
		{"special characters", "event@2024#01!log", "event_2024_01_log"},
		{"CJK characters", "事件报告", "____"},
		{"mixed", "proj-1/event:2024", "proj-1_event_2024"},
		{"empty string", "", ""},
		{"all safe characters", "AZaz09._-", "AZaz09._-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
