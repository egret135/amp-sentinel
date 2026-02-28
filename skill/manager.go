package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"amp-sentinel/logger"

	"gopkg.in/yaml.v3"
)

type Manager struct {
	skills map[string]*Skill
	dir    string
	env    map[string]string
	log    logger.Logger
}

func NewManager(dir string, env map[string]string, log logger.Logger) *Manager {
	if env == nil {
		env = map[string]string{}
	}
	return &Manager{
		skills: make(map[string]*Skill),
		dir:    dir,
		env:    env,
		log:    log,
	}
}

type skillFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Globs       []string `yaml:"globs"`
}

func (m *Manager) LoadAll() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("read skills directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(m.dir, entry.Name())
		sk, err := m.loadSkill(skillDir)
		if err != nil {
			m.log.Warn("skill.parse_failed",
				logger.String("dir", entry.Name()),
				logger.Err(err),
			)
			continue
		}

		m.skills[sk.Name] = sk
		m.log.Info("skill.loaded", logger.String("name", sk.Name))
	}

	return nil
}

func (m *Manager) loadSkill(dir string) (*Skill, error) {
	mdPath := filepath.Join(dir, "SKILL.md")
	content, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}

	fm, err := parseFrontmatter(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	if fm.Name == "" {
		return nil, fmt.Errorf("missing name in frontmatter")
	}

	sk := &Skill{
		Name:        fm.Name,
		Description: fm.Description,
		Globs:       fm.Globs,
		Dir:         dir,
		Content:     string(content),
	}

	mcpPath := filepath.Join(dir, "mcp.json")
	if data, err := os.ReadFile(mcpPath); err == nil {
		var servers map[string]MCPServerConfig
		if err := json.Unmarshal(data, &servers); err != nil {
			return nil, fmt.Errorf("parse mcp.json: %w", err)
		}
		for name, srv := range servers {
			srv.URL = os.Expand(srv.URL, os.Getenv)
			for k, v := range srv.Env {
				srv.Env[k] = os.Expand(v, os.Getenv)
			}
			servers[name] = srv
		}
		sk.MCPServers = servers
	}

	return sk, nil
}

func parseFrontmatter(content string) (*skillFrontmatter, error) {
	// Frontmatter must start at the beginning of the file with "---"
	trimmed := strings.TrimLeft(content, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return nil, fmt.Errorf("file does not start with frontmatter delimiter")
	}

	// Find the closing delimiter on its own line
	rest := trimmed[3:]
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return nil, fmt.Errorf("no closing frontmatter delimiter found")
	}

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:endIdx]), &fm); err != nil {
		return nil, fmt.Errorf("unmarshal yaml: %w", err)
	}
	return &fm, nil
}

func (m *Manager) Resolve(names []string) []*Skill {
	out := make([]*Skill, 0, len(names))
	for _, name := range names {
		sk, ok := m.skills[name]
		if !ok {
			m.log.Debug("skill.unknown", logger.String("name", name))
			continue
		}
		out = append(out, sk)
	}
	return out
}

func (m *Manager) MCPConfig(skills []*Skill) map[string]MCPServerConfig {
	merged := make(map[string]MCPServerConfig)
	for _, sk := range skills {
		for name, srv := range sk.MCPServers {
			if srv.Env == nil {
				srv.Env = make(map[string]string)
			}
			for k, v := range m.env {
				if _, exists := srv.Env[k]; !exists {
					srv.Env[k] = v
				}
			}
			merged[name] = srv
		}
	}
	return merged
}

func (m *Manager) SkillDirs(skills []*Skill) []string {
	dirs := make([]string, len(skills))
	for i, sk := range skills {
		dirs[i] = sk.Dir
	}
	return dirs
}

func (m *Manager) Get(name string) (*Skill, bool) {
	sk, ok := m.skills[name]
	return sk, ok
}

func (m *Manager) All() []*Skill {
	out := make([]*Skill, 0, len(m.skills))
	for _, sk := range m.skills {
		out = append(out, sk)
	}
	return out
}

func (m *Manager) Len() int {
	return len(m.skills)
}
