package skill

type Skill struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Globs       []string                  `json:"globs,omitempty"`
	Dir         string                    `json:"dir"`
	MCPServers  map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	Content     string                    `json:"-"`
}

type MCPServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}
