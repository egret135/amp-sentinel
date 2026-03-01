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
	Events  map[string]*Event           `json:"events"`
	Tasks   map[string]*DiagnosisTask   `json:"tasks"`
	Reports map[string]*DiagnosisReport `json:"reports"`
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
			Events:  make(map[string]*Event),
			Tasks:   make(map[string]*DiagnosisTask),
			Reports: make(map[string]*DiagnosisReport),
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
	if d.Events == nil {
		d.Events = make(map[string]*Event)
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

// ---------- Event ----------

func (s *JSONStore) CreateEvent(_ context.Context, event *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Events[event.ID]; exists {
		return fmt.Errorf("event %s already exists", event.ID)
	}
	clone := *event
	if clone.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	s.data.Events[event.ID] = &clone
	return nil
}

func (s *JSONStore) GetEvent(_ context.Context, id string) (*Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	event, ok := s.data.Events[id]
	if !ok {
		return nil, nil
	}
	clone := *event
	if event.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	return &clone, nil
}

func (s *JSONStore) UpdateEvent(_ context.Context, event *Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data.Events[event.ID]; !exists {
		return fmt.Errorf("event %s not found", event.ID)
	}
	clone := *event
	if clone.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	s.data.Events[event.ID] = &clone
	return nil
}

func (s *JSONStore) ListEvents(_ context.Context, filter EventFilter) ([]*Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Event
	for _, event := range s.data.Events {
		if filter.ProjectKey != "" && event.ProjectKey != filter.ProjectKey {
			continue
		}
		if filter.Source != "" && event.Source != filter.Source {
			continue
		}
		if filter.Status != "" && event.Status != filter.Status {
			continue
		}
		if filter.Severity != "" && event.Severity != filter.Severity {
			continue
		}
		clone := *event
		if event.Payload != nil {
			clone.Payload = append(json.RawMessage(nil), event.Payload...)
		}
		result = append(result, &clone)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ReceivedAt.After(result[j].ReceivedAt)
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
		if filter.EventID != "" && task.EventID != filter.EventID {
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
	if clone.StructuredResult != nil {
		clone.StructuredResult = append(json.RawMessage(nil), report.StructuredResult...)
	}
	if clone.QualityScore != nil {
		clone.QualityScore = append(json.RawMessage(nil), report.QualityScore...)
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
			if report.StructuredResult != nil {
				clone.StructuredResult = append(json.RawMessage(nil), report.StructuredResult...)
			}
			if report.QualityScore != nil {
				clone.QualityScore = append(json.RawMessage(nil), report.QualityScore...)
			}
			return &clone, nil
		}
	}
	return nil, nil
}

func (s *JSONStore) FindRecentReportByFingerprint(_ context.Context, projectKey, fingerprint string, since time.Time) (*DiagnosisReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var best *DiagnosisReport
	for _, report := range s.data.Reports {
		if report.ProjectKey != projectKey {
			continue
		}
		if report.Fingerprint != fingerprint {
			continue
		}
		if report.DiagnosedAt.Before(since) {
			continue
		}
		// Skip reports that are themselves reused (only match originals)
		if report.ReusedFromID != "" {
			continue
		}
		if best == nil || report.DiagnosedAt.After(best.DiagnosedAt) {
			clone := *report
			if report.ToolsUsed != nil {
				clone.ToolsUsed = append([]string(nil), report.ToolsUsed...)
			}
			if report.SkillsUsed != nil {
				clone.SkillsUsed = append([]string(nil), report.SkillsUsed...)
			}
			if report.StructuredResult != nil {
				clone.StructuredResult = append(json.RawMessage(nil), report.StructuredResult...)
			}
			if report.QualityScore != nil {
				clone.QualityScore = append(json.RawMessage(nil), report.QualityScore...)
			}
			best = &clone
		}
	}
	return best, nil
}

// ---------- Queries ----------

func (s *JSONStore) GetUsageSummary(_ context.Context) (*UsageSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	today := time.Now().Truncate(24 * time.Hour)
	summary := &UsageSummary{
		TotalEvents: len(s.data.Events),
		TasksByStatus:  make(map[TaskStatus]int),
	}

	for _, event := range s.data.Events {
		if !event.ReceivedAt.Before(today) {
			summary.TodayEvents++
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
