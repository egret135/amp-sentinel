package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"amp-sentinel/logger"
)

// JSONStore implements Store using an in-memory map backed by a JSON file.
type JSONStore struct {
	path          string
	mu            sync.RWMutex
	data          jsonData
	log           logger.Logger
	flushInterval time.Duration
	stopFlush     chan struct{}
	closeOnce     sync.Once
}

type jsonData struct {
	Incidents map[string]*Incident        `json:"incidents"`
	Tasks     map[string]*DiagnosisTask   `json:"tasks"`
	Reports   map[string]*DiagnosisReport `json:"reports"`
}

// NewJSONStore creates a new JSONStore. If the file at path exists it is loaded.
// A background goroutine flushes to disk at the given interval.
func NewJSONStore(path string, flushInterval time.Duration, log logger.Logger) (*JSONStore, error) {
	s := &JSONStore{
		path:          path,
		log:           log,
		flushInterval: flushInterval,
		stopFlush:     make(chan struct{}),
		data: jsonData{
			Incidents: make(map[string]*Incident),
			Tasks:     make(map[string]*DiagnosisTask),
			Reports:   make(map[string]*DiagnosisReport),
		},
	}

	if err := s.loadFromFile(); err != nil {
		return nil, err
	}

	go s.flushLoop()

	log.Info("store.json.opened", logger.String("path", path))
	return s, nil
}

func (s *JSONStore) loadFromFile() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read json store: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var d jsonData
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("unmarshal json store: %w", err)
	}
	if d.Incidents == nil {
		d.Incidents = make(map[string]*Incident)
	}
	if d.Tasks == nil {
		d.Tasks = make(map[string]*DiagnosisTask)
	}
	if d.Reports == nil {
		d.Reports = make(map[string]*DiagnosisReport)
	}
	s.data = d
	return nil
}

func (s *JSONStore) flush() error {
	s.mu.RLock()
	data, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal json store: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return fmt.Errorf("write json store: %w", err)
	}
	return nil
}

func (s *JSONStore) flushLoop() {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := s.flush(); err != nil {
				s.log.Error("store.json.flush_failed", logger.Err(err))
			}
		case <-s.stopFlush:
			return
		}
	}
}

// ---------- Incident ----------

func (s *JSONStore) CreateIncident(_ context.Context, inc *Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Incidents[inc.ID]; exists {
		return fmt.Errorf("incident %s already exists", inc.ID)
	}
	clone := *inc
	if clone.Metadata != nil {
		clone.Metadata = copyMap(inc.Metadata)
	}
	s.data.Incidents[inc.ID] = &clone
	return nil
}

func (s *JSONStore) GetIncident(_ context.Context, id string) (*Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inc, ok := s.data.Incidents[id]
	if !ok {
		return nil, nil
	}
	clone := *inc
	clone.Metadata = copyMap(inc.Metadata)
	return &clone, nil
}

func (s *JSONStore) UpdateIncident(_ context.Context, inc *Incident) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Incidents[inc.ID]; !exists {
		return fmt.Errorf("incident %s not found", inc.ID)
	}
	clone := *inc
	if clone.Metadata != nil {
		clone.Metadata = copyMap(inc.Metadata)
	}
	s.data.Incidents[inc.ID] = &clone
	return nil
}

func (s *JSONStore) ListIncidents(_ context.Context, filter IncidentFilter) ([]*Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Incident
	for _, inc := range s.data.Incidents {
		if filter.ProjectKey != "" && inc.ProjectKey != filter.ProjectKey {
			continue
		}
		if filter.Status != "" && inc.Status != filter.Status {
			continue
		}
		if filter.Severity != "" && inc.Severity != filter.Severity {
			continue
		}
		clone := *inc
		clone.Metadata = copyMap(inc.Metadata)
		result = append(result, &clone)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].OccurredAt.After(result[j].OccurredAt)
	})

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

// ---------- Task ----------

func (s *JSONStore) CreateTask(_ context.Context, task *DiagnosisTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}
	clone := *task
	s.data.Tasks[task.ID] = &clone
	return nil
}

func (s *JSONStore) GetTask(_ context.Context, id string) (*DiagnosisTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.data.Tasks[id]
	if !ok {
		return nil, nil
	}
	clone := *task
	if task.StartedAt != nil {
		v := *task.StartedAt
		clone.StartedAt = &v
	}
	if task.FinishedAt != nil {
		v := *task.FinishedAt
		clone.FinishedAt = &v
	}
	return &clone, nil
}

func (s *JSONStore) UpdateTask(_ context.Context, task *DiagnosisTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Tasks[task.ID]; !exists {
		return fmt.Errorf("task %s not found", task.ID)
	}
	clone := *task
	s.data.Tasks[task.ID] = &clone
	return nil
}

func (s *JSONStore) ListTasks(_ context.Context, filter TaskFilter) ([]*DiagnosisTask, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*DiagnosisTask
	for _, task := range s.data.Tasks {
		if filter.IncidentID != "" && task.IncidentID != filter.IncidentID {
			continue
		}
		if filter.ProjectKey != "" && task.ProjectKey != filter.ProjectKey {
			continue
		}
		if filter.Status != "" && task.Status != filter.Status {
			continue
		}
		clone := *task
		if task.StartedAt != nil {
			v := *task.StartedAt
			clone.StartedAt = &v
		}
		if task.FinishedAt != nil {
			v := *task.FinishedAt
			clone.FinishedAt = &v
		}
		result = append(result, &clone)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(result) {
		return nil, nil
	}
	end := offset + limit
	if end > len(result) {
		end = len(result)
	}
	return result[offset:end], nil
}

func (s *JSONStore) CountByStatus(_ context.Context) (map[TaskStatus]int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[TaskStatus]int)
	for _, task := range s.data.Tasks {
		result[task.Status]++
	}
	return result, nil
}

// ---------- Report ----------

func (s *JSONStore) SaveReport(_ context.Context, report *DiagnosisReport) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := *report
	if clone.ToolsUsed != nil {
		clone.ToolsUsed = append([]string(nil), report.ToolsUsed...)
	}
	if clone.SkillsUsed != nil {
		clone.SkillsUsed = append([]string(nil), report.SkillsUsed...)
	}
	s.data.Reports[report.ID] = &clone
	return nil
}

func (s *JSONStore) GetReport(_ context.Context, taskID string) (*DiagnosisReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, report := range s.data.Reports {
		if report.TaskID == taskID {
			clone := *report
			if report.ToolsUsed != nil {
				clone.ToolsUsed = append([]string(nil), report.ToolsUsed...)
			}
			if report.SkillsUsed != nil {
				clone.SkillsUsed = append([]string(nil), report.SkillsUsed...)
			}
			return &clone, nil
		}
	}
	return nil, nil
}

// ---------- Queries ----------

func (s *JSONStore) FindRecentIncident(_ context.Context, projectKey, errorMsg string, window time.Duration) (*Incident, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cutoff := time.Now().Add(-window)
	var best *Incident
	for _, inc := range s.data.Incidents {
		if inc.ProjectKey != projectKey || inc.ErrorMsg != errorMsg {
			continue
		}
		if inc.OccurredAt.Before(cutoff) {
			continue
		}
		if best == nil || inc.OccurredAt.After(best.OccurredAt) {
			best = inc
		}
	}
	if best == nil {
		return nil, nil
	}
	clone := *best
	clone.Metadata = copyMap(best.Metadata)
	return &clone, nil
}

func (s *JSONStore) GetUsageSummary(_ context.Context) (*UsageSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	today := time.Now().Truncate(24 * time.Hour)
	summary := &UsageSummary{
		TotalIncidents: len(s.data.Incidents),
		TasksByStatus:  make(map[TaskStatus]int),
	}

	for _, inc := range s.data.Incidents {
		if !inc.ReportedAt.Before(today) {
			summary.TodayIncidents++
		}
	}
	for _, task := range s.data.Tasks {
		summary.TasksByStatus[task.Status]++
		summary.TotalInputTokens += int64(task.InputTokens)
		summary.TotalOutputTokens += int64(task.OutputTokens)
	}
	return summary, nil
}

// ---------- Lifecycle ----------

func (s *JSONStore) Close() error {
	var flushErr error
	s.closeOnce.Do(func() {
		s.log.Info("store.json.closing")
		close(s.stopFlush)
		flushErr = s.flush()
	})
	return flushErr
}

// ---------- helpers ----------

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
