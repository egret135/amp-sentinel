package amp

import "encoding/json"

// StreamMessage represents a single line of Amp's --stream-json NDJSON output.
// The Type field determines which group of fields is populated.
type StreamMessage struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	Message         *MessagePayload `json:"message,omitempty"`

	// system/init fields
	Cwd        string      `json:"cwd,omitempty"`
	Tools      []string    `json:"tools,omitempty"`
	MCPServers []MCPServer `json:"mcp_servers,omitempty"`

	// result fields
	IsError          bool     `json:"is_error,omitempty"`
	Result           string   `json:"result,omitempty"`
	Error            string   `json:"error,omitempty"`
	DurationMs       int64    `json:"duration_ms,omitempty"`
	NumTurns         int      `json:"num_turns,omitempty"`
	Usage            *Usage   `json:"usage,omitempty"`
	PermissionDenials []string `json:"permission_denials,omitempty"`
}

// MessagePayload is the inner message object for assistant and user messages.
type MessagePayload struct {
	Type       string         `json:"type,omitempty"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason *string        `json:"stop_reason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
}

// ContentBlock represents one block inside a message's content array.
type ContentBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// tool_use block
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result block
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// thinking block
	Thinking string `json:"thinking,omitempty"`

	// redacted_thinking block
	Data string `json:"data,omitempty"`
}

// Usage contains token consumption statistics.
type Usage struct {
	InputTokens              int    `json:"input_tokens"`
	OutputTokens             int    `json:"output_tokens"`
	MaxTokens                int    `json:"max_tokens"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int    `json:"cache_read_input_tokens,omitempty"`
	ServiceTier              string `json:"service_tier,omitempty"`
}

// MCPServer describes the status of an MCP server in the init message.
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// UserInputMessage is the format for sending messages via --stream-json-input.
type UserInputMessage struct {
	Type    string              `json:"type"`
	Message UserInputPayload    `json:"message"`
}

// UserInputPayload is the inner payload of a user input message.
type UserInputPayload struct {
	Role    string              `json:"role"`
	Content []UserInputContent  `json:"content"`
}

// UserInputContent is a single content block in a user input message.
type UserInputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// NewUserInputMessage creates a properly formatted user input message.
func NewUserInputMessage(text string) UserInputMessage {
	return UserInputMessage{
		Type: "user",
		Message: UserInputPayload{
			Role: "user",
			Content: []UserInputContent{
				{Type: "text", Text: text},
			},
		},
	}
}
