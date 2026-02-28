package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"amp-sentinel/logger"
)

// SourceManager handles cloning, updating, and providing read-only access
// to project source code.
type SourceManager struct {
	baseDir   string
	sshKey    string
	log       logger.Logger
	mu        sync.Map // per-project locks: project_key -> *sync.Mutex
}

// Lock acquires the per-project mutex and returns an unlock function.
// This must be held for the entire diagnosis lifecycle (Prepare → Amp → safety check)
// to prevent concurrent diagnoses on the same project from racing.
func (s *SourceManager) Lock(projectKey string) func() {
	mu := s.lockFor(projectKey)
	mu.Lock()
	return mu.Unlock
}

// NewSourceManager creates a source manager that stores repos under baseDir.
// The baseDir is created automatically if it does not exist.
func NewSourceManager(baseDir, sshKey string, log logger.Logger) *SourceManager {
	_ = os.MkdirAll(baseDir, 0755)
	return &SourceManager{baseDir: baseDir, sshKey: sshKey, log: log}
}

// Prepare ensures the project source is available and up-to-date.
// Returns the absolute path to the project source directory.
func (s *SourceManager) Prepare(ctx context.Context, p *Project) (string, error) {
	// NOTE: caller must hold Lock(p.Key) for the entire diagnosis lifecycle.
	repoDir := filepath.Join(s.baseDir, p.Key)
	srcDir := filepath.Join(repoDir, p.SourceRoot)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		s.log.Info("source.pulling", logger.String("project", p.Key))
		if err := s.gitPull(ctx, repoDir, p.Branch); err != nil {
			s.log.Warn("source.pull_failed, will re-clone",
				logger.String("project", p.Key), logger.Err(err))
			if err := os.RemoveAll(repoDir); err != nil {
				return "", fmt.Errorf("remove stale repo: %w", err)
			}
			return s.cloneAndReturn(ctx, p, repoDir, srcDir)
		}
	} else {
		// Remove stale/corrupted directory if it exists before cloning
		if _, statErr := os.Stat(repoDir); statErr == nil {
			if removeErr := os.RemoveAll(repoDir); removeErr != nil {
				return "", fmt.Errorf("remove stale repo dir: %w", removeErr)
			}
		}
		return s.cloneAndReturn(ctx, p, repoDir, srcDir)
	}

	return srcDir, nil
}

// CommitHash returns the current HEAD commit hash of the repo.
func (s *SourceManager) CommitHash(ctx context.Context, projectKey string) (string, error) {
	repoDir := filepath.Join(s.baseDir, projectKey)
	out, err := s.git(ctx, repoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

// HasChanges returns true if the repo has uncommitted changes (safety check).
func (s *SourceManager) HasChanges(ctx context.Context, projectKey string) (bool, error) {
	repoDir := filepath.Join(s.baseDir, projectKey)
	out, err := s.git(ctx, repoDir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// ResetChanges discards all uncommitted changes in the repo,
// including untracked files and directories.
func (s *SourceManager) ResetChanges(ctx context.Context, projectKey string) error {
	repoDir := filepath.Join(s.baseDir, projectKey)
	if _, err := s.git(ctx, repoDir, "checkout", "--", "."); err != nil {
		return err
	}
	// Also remove untracked files and directories
	_, err := s.git(ctx, repoDir, "clean", "-fd")
	return err
}

func (s *SourceManager) cloneAndReturn(ctx context.Context, p *Project, repoDir, srcDir string) (string, error) {
	s.log.Info("source.cloning",
		logger.String("project", p.Key),
		logger.String("repo", p.RepoURL),
		logger.String("branch", p.Branch),
	)

	if err := s.gitClone(ctx, p.RepoURL, p.Branch, repoDir); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}

	return srcDir, nil
}

func (s *SourceManager) gitClone(ctx context.Context, repoURL, branch, dest string) error {
	args := []string{"clone", "--depth=1", "--branch", branch, repoURL, dest}
	cmd := exec.CommandContext(ctx, "git", args...)
	s.applySSH(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func (s *SourceManager) gitPull(ctx context.Context, repoDir, branch string) error {
	// Fetch + reset to handle shallow clones properly
	if _, err := s.git(ctx, repoDir, "fetch", "--depth=1", "origin", branch); err != nil {
		return err
	}
	_, err := s.git(ctx, repoDir, "reset", "--hard", "FETCH_HEAD")
	return err
}

func (s *SourceManager) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	s.applySSH(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, string(out))
	}
	return trimOutput(string(out)), nil
}

func (s *SourceManager) applySSH(cmd *exec.Cmd) {
	if s.sshKey != "" {
		// Single-quote the key path to handle spaces; replace any embedded
		// single quotes to prevent shell injection.
		escaped := strings.ReplaceAll(s.sshKey, "'", "'\"'\"'")
		cmd.Env = append(cmd.Environ(),
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i '%s' -o StrictHostKeyChecking=no", escaped))
	}
}

func (s *SourceManager) lockFor(key string) *sync.Mutex {
	v, _ := s.mu.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func trimOutput(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
