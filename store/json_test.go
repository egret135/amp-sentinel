package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"amp-sentinel/logger"
)

func newTestStore(t *testing.T) *JSONStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.json")
	s, err := NewJSONStore(path, time.Hour, logger.Nop())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestJSONStore_CreateEvent_GetEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	ev := &Event{
		ID:         "ev-1",
		ProjectKey: "proj-a",
		Payload:    json.RawMessage(`{"key":"value"}`),
		Source:     "sentry",
		Severity:   "error",
		Title:      "NullPointerException",
		Status:     "open",
		ReceivedAt: now,
	}

	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	got, err := s.GetEvent(ctx, "ev-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got == nil {
		t.Fatal("GetEvent returned nil")
	}
	if got.ID != ev.ID || got.ProjectKey != ev.ProjectKey || got.Source != ev.Source ||
		got.Severity != ev.Severity || got.Title != ev.Title || got.Status != ev.Status ||
		!got.ReceivedAt.Equal(ev.ReceivedAt) || string(got.Payload) != string(ev.Payload) {
		t.Errorf("fields mismatch: got %+v", got)
	}
}

func TestJSONStore_CreateEvent_Duplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := &Event{ID: "ev-dup", ProjectKey: "p"}
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := s.CreateEvent(ctx, ev); err == nil {
		t.Fatal("expected error on duplicate create")
	}
}

func TestJSONStore_GetEvent_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.GetEvent(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestJSONStore_UpdateEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := &Event{ID: "ev-upd", ProjectKey: "p", Status: "open"}
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	ev.Status = "resolved"
	ev.Title = "updated title"
	if err := s.UpdateEvent(ctx, ev); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	got, err := s.GetEvent(ctx, "ev-upd")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Status != "resolved" || got.Title != "updated title" {
		t.Errorf("update not persisted: %+v", got)
	}
}

func TestJSONStore_UpdateEvent_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	ev := &Event{ID: "no-such-event"}
	if err := s.UpdateEvent(ctx, ev); err == nil {
		t.Fatal("expected error updating nonexistent event")
	}
}

func TestJSONStore_ListEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	events := []*Event{
		{ID: "e1", ProjectKey: "proj-a", Severity: "error", ReceivedAt: now},
		{ID: "e2", ProjectKey: "proj-a", Severity: "warning", ReceivedAt: now.Add(-time.Second)},
		{ID: "e3", ProjectKey: "proj-b", Severity: "error", ReceivedAt: now.Add(-2 * time.Second)},
	}
	for _, ev := range events {
		if err := s.CreateEvent(ctx, ev); err != nil {
			t.Fatalf("CreateEvent %s: %v", ev.ID, err)
		}
	}

	// No filter – returns all, ordered by ReceivedAt DESC
	all, err := s.ListEvents(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}
	if all[0].ID != "e1" || all[1].ID != "e2" || all[2].ID != "e3" {
		t.Errorf("wrong order: %s, %s, %s", all[0].ID, all[1].ID, all[2].ID)
	}

	// Filter by ProjectKey
	projA, err := s.ListEvents(ctx, EventFilter{ProjectKey: "proj-a"})
	if err != nil {
		t.Fatalf("ListEvents ProjectKey: %v", err)
	}
	if len(projA) != 2 {
		t.Fatalf("expected 2 events for proj-a, got %d", len(projA))
	}

	// Filter by Severity
	errs, err := s.ListEvents(ctx, EventFilter{Severity: "error"})
	if err != nil {
		t.Fatalf("ListEvents Severity: %v", err)
	}
	if len(errs) != 2 {
		t.Fatalf("expected 2 error events, got %d", len(errs))
	}
}

func TestJSONStore_ListEvents_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	for i := 0; i < 5; i++ {
		ev := &Event{
			ID:         "ep-" + string(rune('a'+i)),
			ProjectKey: "p",
			ReceivedAt: now.Add(-time.Duration(i) * time.Second),
		}
		if err := s.CreateEvent(ctx, ev); err != nil {
			t.Fatalf("CreateEvent: %v", err)
		}
	}

	// Limit
	page, err := s.ListEvents(ctx, EventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListEvents limit: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("expected 2, got %d", len(page))
	}

	// Offset
	page2, err := s.ListEvents(ctx, EventFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListEvents offset: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("expected 2, got %d", len(page2))
	}
	if page2[0].ID == page[0].ID {
		t.Error("offset did not skip events")
	}

	// Offset past end
	page3, err := s.ListEvents(ctx, EventFilter{Limit: 2, Offset: 10})
	if err != nil {
		t.Fatalf("ListEvents offset past end: %v", err)
	}
	if page3 != nil {
		t.Fatalf("expected nil for offset past end, got %d items", len(page3))
	}
}

func TestJSONStore_CreateTask_GetTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	started := now.Add(-time.Minute)
	finished := now

	task := &DiagnosisTask{
		ID:           "t-1",
		EventID:      "ev-1",
		ProjectKey:   "proj-a",
		Status:       StatusRunning,
		Priority:     5,
		SessionID:    "sess-abc",
		NumTurns:     3,
		DurationMs:   1500,
		InputTokens:  100,
		OutputTokens: 200,
		Error:        "",
		RetryCount:   1,
		CreatedAt:    now.Add(-2 * time.Minute),
		StartedAt:    &started,
		FinishedAt:   &finished,
	}

	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "t-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil")
	}
	if got.ID != task.ID || got.EventID != task.EventID || got.ProjectKey != task.ProjectKey ||
		got.Status != task.Status || got.Priority != task.Priority || got.SessionID != task.SessionID ||
		got.NumTurns != task.NumTurns || got.DurationMs != task.DurationMs ||
		got.InputTokens != task.InputTokens || got.OutputTokens != task.OutputTokens ||
		got.RetryCount != task.RetryCount {
		t.Errorf("scalar fields mismatch: got %+v", got)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("StartedAt mismatch: got %v", got.StartedAt)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Errorf("FinishedAt mismatch: got %v", got.FinishedAt)
	}
}

func TestJSONStore_ListTasks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	tasks := []*DiagnosisTask{
		{ID: "t1", ProjectKey: "proj-a", Status: StatusPending, CreatedAt: now},
		{ID: "t2", ProjectKey: "proj-a", Status: StatusCompleted, CreatedAt: now.Add(-time.Second)},
		{ID: "t3", ProjectKey: "proj-b", Status: StatusPending, CreatedAt: now.Add(-2 * time.Second)},
	}
	for _, tk := range tasks {
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.ID, err)
		}
	}

	// Filter by Status
	pending, err := s.ListTasks(ctx, TaskFilter{Status: StatusPending})
	if err != nil {
		t.Fatalf("ListTasks Status: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}

	// Filter by ProjectKey
	projA, err := s.ListTasks(ctx, TaskFilter{ProjectKey: "proj-a"})
	if err != nil {
		t.Fatalf("ListTasks ProjectKey: %v", err)
	}
	if len(projA) != 2 {
		t.Fatalf("expected 2 for proj-a, got %d", len(projA))
	}
}

func TestJSONStore_CountByStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now()
	tasks := []*DiagnosisTask{
		{ID: "c1", Status: StatusPending, CreatedAt: now},
		{ID: "c2", Status: StatusPending, CreatedAt: now},
		{ID: "c3", Status: StatusRunning, CreatedAt: now},
		{ID: "c4", Status: StatusCompleted, CreatedAt: now},
	}
	for _, tk := range tasks {
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	counts, err := s.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[StatusPending] != 2 {
		t.Errorf("pending: got %d, want 2", counts[StatusPending])
	}
	if counts[StatusRunning] != 1 {
		t.Errorf("running: got %d, want 1", counts[StatusRunning])
	}
	if counts[StatusCompleted] != 1 {
		t.Errorf("completed: got %d, want 1", counts[StatusCompleted])
	}
}

func TestJSONStore_SaveReport_GetReport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	report := &DiagnosisReport{
		ID:                 "r-1",
		TaskID:             "t-1",
		EventID:            "ev-1",
		ProjectKey:         "proj-a",
		ProjectName:        "Project A",
		Summary:            "Found a bug",
		RawResult:          "raw output",
		Confidence:         "high",
		HasIssue:           true,
		Tainted:            false,
		Notified:           true,
		ToolsUsed:          []string{"grep", "read"},
		SkillsUsed:         []string{"code-review"},
		DiagnosedAt:        now,
		StructuredResult:   json.RawMessage(`{"root_cause":"nil pointer"}`),
		QualityScore:       json.RawMessage(`{"score":0.95}`),
		CommitHash:         "abc123",
		PromptVersion:      "v2",
		OriginalConfidence: 0.8,
		FinalConfidence:    0.95,
		FinalConfLabel:     "high",
		Fingerprint:        "fp-abc",
		ReusedFromID:       "",
	}

	if err := s.SaveReport(ctx, report); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := s.GetReport(ctx, "t-1")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got == nil {
		t.Fatal("GetReport returned nil")
	}
	if got.ID != report.ID || got.TaskID != report.TaskID || got.Summary != report.Summary ||
		got.Confidence != report.Confidence || !got.HasIssue || got.Tainted || !got.Notified ||
		got.Fingerprint != report.Fingerprint || got.ReusedFromID != "" ||
		got.FinalConfidence != report.FinalConfidence ||
		string(got.StructuredResult) != string(report.StructuredResult) ||
		string(got.QualityScore) != string(report.QualityScore) {
		t.Errorf("report fields mismatch: got %+v", got)
	}
	if len(got.ToolsUsed) != 2 || got.ToolsUsed[0] != "grep" {
		t.Errorf("ToolsUsed mismatch: %v", got.ToolsUsed)
	}
	if len(got.SkillsUsed) != 1 || got.SkillsUsed[0] != "code-review" {
		t.Errorf("SkillsUsed mismatch: %v", got.SkillsUsed)
	}
}

func TestJSONStore_GetReport_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.GetReport(ctx, "no-such-task")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestJSONStore_FindRecentReportByFingerprint(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	since := now.Add(-24 * time.Hour)

	reports := []*DiagnosisReport{
		{ID: "r1", TaskID: "t1", ProjectKey: "proj-a", Fingerprint: "fp-1", DiagnosedAt: now.Add(-time.Hour)},
		{ID: "r2", TaskID: "t2", ProjectKey: "proj-a", Fingerprint: "fp-1", DiagnosedAt: now},                                                // most recent
		{ID: "r3", TaskID: "t3", ProjectKey: "proj-a", Fingerprint: "fp-1", DiagnosedAt: now.Add(-time.Minute), ReusedFromID: "r1"},            // reused – should be skipped
		{ID: "r4", TaskID: "t4", ProjectKey: "proj-b", Fingerprint: "fp-1", DiagnosedAt: now},                                                 // wrong project
		{ID: "r5", TaskID: "t5", ProjectKey: "proj-a", Fingerprint: "fp-2", DiagnosedAt: now},                                                 // wrong fingerprint
		{ID: "r6", TaskID: "t6", ProjectKey: "proj-a", Fingerprint: "fp-1", DiagnosedAt: now.Add(-48 * time.Hour)},                            // too old
	}
	for _, r := range reports {
		if err := s.SaveReport(ctx, r); err != nil {
			t.Fatalf("SaveReport %s: %v", r.ID, err)
		}
	}

	got, err := s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-1", since)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint: %v", err)
	}
	if got == nil {
		t.Fatal("expected a report, got nil")
	}
	if got.ID != "r2" {
		t.Errorf("expected r2 (most recent), got %s", got.ID)
	}

	// No match
	got2, err := s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-nonexistent", since)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint no match: %v", err)
	}
	if got2 != nil {
		t.Fatalf("expected nil for no match, got %+v", got2)
	}
}

func TestJSONStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.json")
	ctx := context.Background()

	// Create store, add data, close
	s1, err := NewJSONStore(path, time.Hour, logger.Nop())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	if err := s1.CreateEvent(ctx, &Event{ID: "persist-ev", ProjectKey: "p", ReceivedAt: time.Now()}); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := s1.CreateTask(ctx, &DiagnosisTask{ID: "persist-task", Status: StatusPending, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen from same file
	s2, err := NewJSONStore(path, time.Hour, logger.Nop())
	if err != nil {
		t.Fatalf("NewJSONStore reopen: %v", err)
	}
	defer s2.Close()

	ev, err := s2.GetEvent(ctx, "persist-ev")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if ev == nil {
		t.Fatal("event not persisted")
	}

	tk, err := s2.GetTask(ctx, "persist-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if tk == nil {
		t.Fatal("task not persisted")
	}
}

func TestJSONStore_GetUsageSummary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Millisecond)
	yesterday := now.Add(-25 * time.Hour)

	// Events: 2 today, 1 yesterday
	for i, ts := range []time.Time{now, now.Add(-time.Minute), yesterday} {
		ev := &Event{ID: "u-ev-" + string(rune('a'+i)), ProjectKey: "p", ReceivedAt: ts}
		if err := s.CreateEvent(ctx, ev); err != nil {
			t.Fatalf("CreateEvent: %v", err)
		}
	}

	// Tasks with tokens
	tasks := []*DiagnosisTask{
		{ID: "u-t1", Status: StatusCompleted, InputTokens: 100, OutputTokens: 50, CreatedAt: now},
		{ID: "u-t2", Status: StatusCompleted, InputTokens: 200, OutputTokens: 100, CreatedAt: now},
		{ID: "u-t3", Status: StatusPending, InputTokens: 0, OutputTokens: 0, CreatedAt: now},
	}
	for _, tk := range tasks {
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	summary, err := s.GetUsageSummary(ctx)
	if err != nil {
		t.Fatalf("GetUsageSummary: %v", err)
	}

	if summary.TotalEvents != 3 {
		t.Errorf("TotalEvents: got %d, want 3", summary.TotalEvents)
	}
	if summary.TodayEvents != 2 {
		t.Errorf("TodayEvents: got %d, want 2", summary.TodayEvents)
	}
	if summary.TasksByStatus[StatusCompleted] != 2 {
		t.Errorf("completed tasks: got %d, want 2", summary.TasksByStatus[StatusCompleted])
	}
	if summary.TasksByStatus[StatusPending] != 1 {
		t.Errorf("pending tasks: got %d, want 1", summary.TasksByStatus[StatusPending])
	}
	if summary.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens: got %d, want 300", summary.TotalInputTokens)
	}
	if summary.TotalOutputTokens != 150 {
		t.Errorf("TotalOutputTokens: got %d, want 150", summary.TotalOutputTokens)
	}
}
