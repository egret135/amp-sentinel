package project

import "fmt"

// Project describes a registered project that Sentinel monitors.
type Project struct {
	Key           string   `json:"key" yaml:"key"`
	Name          string   `json:"name" yaml:"name"`
	RepoURL       string   `json:"repo_url" yaml:"repo_url"`
	Branch        string   `json:"branch" yaml:"branch"`
	Language      string   `json:"language" yaml:"language"`
	SourceRoot    string   `json:"source_root" yaml:"source_root"`
	Skills        []string `json:"skills" yaml:"skills"`
	Owners        []string `json:"owners" yaml:"owners"`
	FeishuWebhook string   `json:"feishu_webhook" yaml:"feishu_webhook"`
}

// Registry holds all registered projects and provides lookup by key.
type Registry struct {
	projects map[string]*Project
}

// NewRegistry creates a registry from a list of project configs.
func NewRegistry(projects []Project) *Registry {
	m := make(map[string]*Project, len(projects))
	for i := range projects {
		p := &projects[i]
		if p.Branch == "" {
			p.Branch = "main"
		}
		if p.SourceRoot == "" {
			p.SourceRoot = "."
		}
		m[p.Key] = p
	}
	return &Registry{projects: m}
}

// Exists returns true if the project key is registered.
func (r *Registry) Exists(key string) bool {
	_, ok := r.projects[key]
	return ok
}

// Lookup returns the project for the given key, or an error if not found.
func (r *Registry) Lookup(key string) (*Project, error) {
	p, ok := r.projects[key]
	if !ok {
		return nil, fmt.Errorf("project not registered: %s", key)
	}
	return p, nil
}

// Len returns the number of registered projects.
func (r *Registry) Len() int {
	return len(r.projects)
}

// All returns all registered projects.
func (r *Registry) All() []*Project {
	out := make([]*Project, 0, len(r.projects))
	for _, p := range r.projects {
		out = append(out, p)
	}
	return out
}

// Keys returns all registered project keys.
func (r *Registry) Keys() []string {
	keys := make([]string, 0, len(r.projects))
	for k := range r.projects {
		keys = append(keys, k)
	}
	return keys
}
