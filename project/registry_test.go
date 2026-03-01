package project

import (
	"sort"
	"testing"
)

func TestNewRegistry_Defaults(t *testing.T) {
	reg := NewRegistry([]Project{
		{Key: "p1", Branch: "", SourceRoot: ""},
		{Key: "p2", Branch: "develop", SourceRoot: "/src"},
	})

	p1, _ := reg.Lookup("p1")
	if p1.Branch != "main" {
		t.Errorf("expected default branch 'main', got %q", p1.Branch)
	}
	if p1.SourceRoot != "." {
		t.Errorf("expected default source root '.', got %q", p1.SourceRoot)
	}

	p2, _ := reg.Lookup("p2")
	if p2.Branch != "develop" {
		t.Errorf("expected branch 'develop', got %q", p2.Branch)
	}
	if p2.SourceRoot != "/src" {
		t.Errorf("expected source root '/src', got %q", p2.SourceRoot)
	}
}

func TestRegistry_Exists(t *testing.T) {
	reg := NewRegistry([]Project{{Key: "alpha"}})

	if !reg.Exists("alpha") {
		t.Error("expected Exists('alpha') to be true")
	}
	if reg.Exists("unknown") {
		t.Error("expected Exists('unknown') to be false")
	}
}

func TestRegistry_Lookup(t *testing.T) {
	reg := NewRegistry([]Project{{Key: "svc", Name: "Service"}})

	p, err := reg.Lookup("svc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "Service" {
		t.Errorf("expected name 'Service', got %q", p.Name)
	}

	_, err = reg.Lookup("missing")
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestRegistry_Len(t *testing.T) {
	reg := NewRegistry([]Project{{Key: "a"}, {Key: "b"}})
	if reg.Len() != 2 {
		t.Errorf("expected Len() == 2, got %d", reg.Len())
	}
}

func TestRegistry_All(t *testing.T) {
	reg := NewRegistry([]Project{{Key: "x"}, {Key: "y"}})
	all := reg.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(all))
	}

	keys := make([]string, len(all))
	for i, p := range all {
		keys[i] = p.Key
	}
	sort.Strings(keys)
	if keys[0] != "x" || keys[1] != "y" {
		t.Errorf("expected keys [x y], got %v", keys)
	}
}

func TestRegistry_Keys(t *testing.T) {
	reg := NewRegistry([]Project{{Key: "k1"}, {Key: "k2"}})
	keys := reg.Keys()
	sort.Strings(keys)
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Errorf("expected [k1 k2], got %v", keys)
	}
}

func TestRegistry_Empty(t *testing.T) {
	reg := NewRegistry(nil)

	if reg.Len() != 0 {
		t.Errorf("expected Len() == 0, got %d", reg.Len())
	}
	if reg.Exists("any") {
		t.Error("expected Exists to be false on empty registry")
	}
	if _, err := reg.Lookup("any"); err == nil {
		t.Fatal("expected error from Lookup on empty registry")
	}
}

func TestRegistry_MultipleProjects(t *testing.T) {
	projects := []Project{
		{Key: "web", Name: "Web App", Language: "go"},
		{Key: "api", Name: "API Server", Language: "python"},
		{Key: "cli", Name: "CLI Tool", Language: "rust"},
	}
	reg := NewRegistry(projects)

	if reg.Len() != 3 {
		t.Fatalf("expected Len() == 3, got %d", reg.Len())
	}

	for _, p := range projects {
		if !reg.Exists(p.Key) {
			t.Errorf("expected Exists(%q) to be true", p.Key)
		}
		got, err := reg.Lookup(p.Key)
		if err != nil {
			t.Errorf("unexpected error looking up %q: %v", p.Key, err)
			continue
		}
		if got.Name != p.Name {
			t.Errorf("key %q: expected name %q, got %q", p.Key, p.Name, got.Name)
		}
		if got.Language != p.Language {
			t.Errorf("key %q: expected language %q, got %q", p.Key, p.Language, got.Language)
		}
	}
}
