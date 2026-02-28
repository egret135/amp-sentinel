package amp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"amp-sentinel/logger"
)

// ExecuteOption configures an Amp CLI invocation.
type ExecuteOption struct {
	WorkDir     string                    // working directory (--cwd equivalent via cmd.Dir)
	Mode        string                    // agent mode: smart / rush / deep
	Permissions []string                  // permission rules (read-only enforcement)
	MCPServers  map[string]MCPServerConfig // MCP server configurations
	Labels      []string                  // thread labels
	Thinking    bool                      // include thinking blocks in output
}

// MCPServerConfig describes an MCP server for the settings file.
type MCPServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// ExecuteResult holds the outcome of a single Amp execution.
type ExecuteResult struct {
	SessionID  string
	Result     string
	Error      string
	IsError    bool
	DurationMs int64
	NumTurns   int
	Usage      *Usage
	ToolsUsed  []string
}

// MessageHandler is called for each streaming message during execution.
// Return a non-nil error to abort the execution.
type MessageHandler func(msg StreamMessage) error

// Client wraps the Amp CLI for programmatic invocation.
type Client struct {
	binary string // path to amp binary
	apiKey string // AMP_API_KEY
	log    logger.Logger
}

// NewClient creates an Amp CLI client.
func NewClient(binary, apiKey string, log logger.Logger) *Client {
	if binary == "" {
		binary = "amp"
	}
	return &Client{binary: binary, apiKey: apiKey, log: log}
}

// Execute runs a prompt through Amp CLI with --stream-json and returns the result.
// The onMessage callback is invoked for each streaming message (may be nil).
func (c *Client) Execute(ctx context.Context, prompt string, opt ExecuteOption, onMessage MessageHandler) (*ExecuteResult, error) {
	// Write settings file (permissions + MCP servers) so Amp enforces them
	var settingsPath string
	if len(opt.Permissions) > 0 || len(opt.MCPServers) > 0 {
		var err error
		settingsPath, err = writeSettingsFile(opt.Permissions, opt.MCPServers)
		if err != nil {
			return nil, fmt.Errorf("write settings file: %w", err)
		}
		defer os.Remove(settingsPath)
	}

	args := c.buildArgs(prompt, opt, settingsPath)
	// Log command without the full prompt to avoid huge log entries
	c.log.Debug("amp.execute",
		logger.String("binary", c.binary),
		logger.String("mode", opt.Mode),
		logger.String("workdir", opt.WorkDir),
		logger.Int("prompt_len", len(prompt)),
	)

	cmd := exec.CommandContext(ctx, c.binary, args...)
	if opt.WorkDir != "" {
		cmd.Dir = opt.WorkDir
	}
	cmd.Env = append(cmd.Environ(), "AMP_API_KEY="+c.apiKey)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start amp: %w", err)
	}

	var cmdDone bool
	defer func() {
		if !cmdDone {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	result := &ExecuteResult{}
	toolsUsed := map[string]struct{}{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg StreamMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			c.log.Warn("amp.parse_error", logger.String("line", truncate(string(line), 200)), logger.Err(err))
			continue
		}

		switch msg.Type {
		case "system":
			if msg.Subtype == "init" {
				result.SessionID = msg.SessionID
				c.log.Debug("amp.init", logger.String("session_id", msg.SessionID), logger.Int("tools", len(msg.Tools)))
			}

		case "assistant":
			if msg.Message != nil {
				for _, block := range msg.Message.Content {
					if block.Type == "tool_use" {
						toolsUsed[block.Name] = struct{}{}
						c.log.Debug("amp.tool_use", logger.String("tool", block.Name))
					}
				}
			}

		case "result":
			result.IsError = msg.IsError
			result.DurationMs = msg.DurationMs
			result.NumTurns = msg.NumTurns
			result.Usage = msg.Usage
			if msg.IsError {
				result.Error = msg.Error
			} else {
				result.Result = msg.Result
			}
		}

		if onMessage != nil {
			if err := onMessage(msg); err != nil {
				cmdDone = true
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return result, fmt.Errorf("message handler: %w", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		c.log.Warn("amp.scanner_error", logger.Err(err))
		if result.Result == "" && result.Error == "" {
			return result, fmt.Errorf("amp scanner: %w", err)
		}
	}

	for t := range toolsUsed {
		result.ToolsUsed = append(result.ToolsUsed, t)
	}

	cmdDone = true
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("amp execution cancelled: %w", ctx.Err())
		}
		return result, fmt.Errorf("amp exited with error: %w", err)
	}

	return result, nil
}

func (c *Client) buildArgs(prompt string, opt ExecuteOption, settingsPath string) []string {
	args := []string{"--execute", prompt, "--stream-json"}

	// NOTE: --dangerously-allow-all is intentionally NOT added.
	// Callers must always provide explicit permissions.
	// If no permissions are provided, Amp will use its default behavior
	// (which may prompt for approval — the safe default).

	if settingsPath != "" {
		args = append(args, "--settings-file", settingsPath)
	}

	if opt.Mode != "" {
		args = append(args, "--mode", opt.Mode)
	}

	if opt.Thinking {
		args = append(args, "--stream-json-thinking")
	}

	for _, label := range opt.Labels {
		args = append(args, "--label", label)
	}

	return args
}

// writeSettingsFile creates a temporary JSON settings file with
// the given permission rules and MCP server configs. Returns the file path.
func writeSettingsFile(permissions []string, mcpServers map[string]MCPServerConfig) (string, error) {
	settings := map[string]any{}

	if len(permissions) > 0 {
		rules := make([]map[string]any, 0, len(permissions))
		for _, perm := range permissions {
			rules = append(rules, map[string]any{"rule": perm})
		}
		settings["amp.permissions"] = rules
	}

	if len(mcpServers) > 0 {
		settings["amp.mcpServers"] = mcpServers
	}

	f, err := os.CreateTemp("", "amp-sentinel-settings-*.json")
	if err != nil {
		return "", err
	}

	if err := json.NewEncoder(f).Encode(settings); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}

	f.Close()
	return f.Name(), nil
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}
