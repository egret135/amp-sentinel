package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"amp-sentinel/logger"
)

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := NewSQLiteStore(dbPath, logger.Nop())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeEvent(id, projectKey, severity string, receivedAt time.Time) *Event {
	return &Event{
		ID:         id,
		ProjectKey: projectKey,
		Payload:    json.RawMessage(`{"key":"value"}`),
		Source:     "test",
		Severity:   severity,
		Title:      "event-" + id,
		Status:     "pending",
		ReceivedAt: receivedAt,
	}
}

func makeTask(id, eventID, projectKey string, status TaskStatus) *DiagnosisTask {
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-10 * time.Minute)
	finished := now
	return &DiagnosisTask{
		ID:           id,
		EventID:      eventID,
		ProjectKey:   projectKey,
		Status:       status,
		Priority:     1,
		SessionID:    "sess-" + id,
		NumTurns:     5,
		DurationMs:   1200,
		InputTokens:  100,
		OutputTokens: 200,
		Error:        "",
		RetryCount:   0,
		CreatedAt:    now,
		StartedAt:    &started,
		FinishedAt:   &finished,
	}
}

func makeReport(id, taskID, eventID, projectKey string) *DiagnosisReport {
	return &DiagnosisReport{
		ID:                 id,
		TaskID:             taskID,
		EventID:            eventID,
		ProjectKey:         projectKey,
		ProjectName:        "proj-" + projectKey,
		Summary:            "summary-" + id,
		RawResult:          "raw-" + id,
		HasIssue:           true,
		Confidence:         "high",
		ToolsUsed:          []string{"tool1", "tool2"},
		SkillsUsed:         []string{"skill1"},
		Tainted:            false,
		Notified:           true,
		DiagnosedAt:        time.Now().UTC().Truncate(time.Second),
		StructuredResult:   json.RawMessage(`{"result":"ok"}`),
		QualityScore:       json.RawMessage(`{"score":0.95}`),
		CommitHash:         "abc123",
		PromptVersion:      "v1",
		OriginalConfidence: 0.8,
		FinalConfidence:    0.92,
		FinalConfLabel:     "high",
		Fingerprint:        "fp-123",
		ReusedFromID:       "",
	}
}

func TestSQLiteStore_CreateEvent_GetEvent(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("evt-1", "proj-a", "error", now)

	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	got, err := s.GetEvent(ctx, "evt-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got == nil {
		t.Fatal("GetEvent returned nil")
	}
	if got.ID != ev.ID {
		t.Errorf("ID = %q, want %q", got.ID, ev.ID)
	}
	if got.ProjectKey != ev.ProjectKey {
		t.Errorf("ProjectKey = %q, want %q", got.ProjectKey, ev.ProjectKey)
	}
	if string(got.Payload) != string(ev.Payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, ev.Payload)
	}
	if got.Source != ev.Source {
		t.Errorf("Source = %q, want %q", got.Source, ev.Source)
	}
	if got.Severity != ev.Severity {
		t.Errorf("Severity = %q, want %q", got.Severity, ev.Severity)
	}
	if got.Title != ev.Title {
		t.Errorf("Title = %q, want %q", got.Title, ev.Title)
	}
	if got.Status != ev.Status {
		t.Errorf("Status = %q, want %q", got.Status, ev.Status)
	}
	if !got.ReceivedAt.Equal(ev.ReceivedAt) {
		t.Errorf("ReceivedAt = %v, want %v", got.ReceivedAt, ev.ReceivedAt)
	}
}

func TestSQLiteStore_GetEvent_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	got, err := s.GetEvent(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSQLiteStore_UpdateEvent(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("evt-u1", "proj-a", "warning", now)
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	ev.Status = "resolved"
	if err := s.UpdateEvent(ctx, ev); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	got, err := s.GetEvent(ctx, "evt-u1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Status != "resolved" {
		t.Errorf("Status = %q, want %q", got.Status, "resolved")
	}
}

func TestSQLiteStore_ListEvents(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []*Event{
		makeEvent("e1", "proj-a", "error", base.Add(1*time.Hour)),
		makeEvent("e2", "proj-a", "warning", base.Add(2*time.Hour)),
		makeEvent("e3", "proj-b", "error", base.Add(3*time.Hour)),
		makeEvent("e4", "proj-a", "error", base.Add(4*time.Hour)),
	}
	for _, ev := range events {
		if err := s.CreateEvent(ctx, ev); err != nil {
			t.Fatalf("CreateEvent(%s): %v", ev.ID, err)
		}
	}

	// Filter by ProjectKey
	got, err := s.ListEvents(ctx, EventFilter{ProjectKey: "proj-a"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	// Filter by Severity
	got, err = s.ListEvents(ctx, EventFilter{Severity: "error"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	// Verify ORDER BY received_at DESC
	got, err = s.ListEvents(ctx, EventFilter{ProjectKey: "proj-a"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if got[0].ID != "e4" || got[1].ID != "e2" || got[2].ID != "e1" {
		t.Errorf("order = [%s, %s, %s], want [e4, e2, e1]", got[0].ID, got[1].ID, got[2].ID)
	}

	// Pagination: Limit + Offset
	got, err = s.ListEvents(ctx, EventFilter{ProjectKey: "proj-a", Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "e4" || got[1].ID != "e2" {
		t.Errorf("page1 = [%s, %s], want [e4, e2]", got[0].ID, got[1].ID)
	}

	got, err = s.ListEvents(ctx, EventFilter{ProjectKey: "proj-a", Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != "e1" {
		t.Errorf("page2 = [%s], want [e1]", got[0].ID)
	}
}

func TestSQLiteStore_CreateTask_GetTask(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Create parent event first (foreign key)
	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("evt-t1", "proj-a", "error", now)
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	task := makeTask("task-1", "evt-t1", "proj-a", StatusRunning)

	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask returned nil")
	}
	if got.ID != task.ID {
		t.Errorf("ID = %q, want %q", got.ID, task.ID)
	}
	if got.EventID != task.EventID {
		t.Errorf("EventID = %q, want %q", got.EventID, task.EventID)
	}
	if got.Status != task.Status {
		t.Errorf("Status = %q, want %q", got.Status, task.Status)
	}
	if got.InputTokens != task.InputTokens {
		t.Errorf("InputTokens = %d, want %d", got.InputTokens, task.InputTokens)
	}
	if got.OutputTokens != task.OutputTokens {
		t.Errorf("OutputTokens = %d, want %d", got.OutputTokens, task.OutputTokens)
	}

	// Verify pointer fields roundtrip
	if got.StartedAt == nil {
		t.Fatal("StartedAt is nil")
	}
	if !got.StartedAt.Truncate(time.Second).Equal(task.StartedAt.Truncate(time.Second)) {
		t.Errorf("StartedAt = %v, want %v", *got.StartedAt, *task.StartedAt)
	}
	if got.FinishedAt == nil {
		t.Fatal("FinishedAt is nil")
	}
	if !got.FinishedAt.Truncate(time.Second).Equal(task.FinishedAt.Truncate(time.Second)) {
		t.Errorf("FinishedAt = %v, want %v", *got.FinishedAt, *task.FinishedAt)
	}
}

func TestSQLiteStore_GetTask_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	got, err := s.GetTask(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSQLiteStore_ListTasks(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	// Create parent events
	for _, id := range []string{"evt-l1", "evt-l2"} {
		ev := makeEvent(id, "proj-a", "error", now)
		if id == "evt-l2" {
			ev.ProjectKey = "proj-b"
		}
		if err := s.CreateEvent(ctx, ev); err != nil {
			t.Fatalf("CreateEvent: %v", err)
		}
	}

	tasks := []*DiagnosisTask{
		makeTask("t1", "evt-l1", "proj-a", StatusPending),
		makeTask("t2", "evt-l1", "proj-a", StatusCompleted),
		makeTask("t3", "evt-l2", "proj-b", StatusPending),
	}
	// Stagger created_at so ordering is deterministic
	tasks[0].CreatedAt = now.Add(-3 * time.Hour)
	tasks[1].CreatedAt = now.Add(-2 * time.Hour)
	tasks[2].CreatedAt = now.Add(-1 * time.Hour)

	for _, task := range tasks {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", task.ID, err)
		}
	}

	// Filter by EventID
	got, err := s.ListTasks(ctx, TaskFilter{EventID: "evt-l1"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	// Filter by Status
	got, err = s.ListTasks(ctx, TaskFilter{Status: StatusPending})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	// Filter by ProjectKey
	got, err = s.ListTasks(ctx, TaskFilter{ProjectKey: "proj-b"})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != "t3" {
		t.Errorf("ID = %q, want %q", got[0].ID, "t3")
	}

	// Pagination
	got, err = s.ListTasks(ctx, TaskFilter{EventID: "evt-l1", Limit: 1, Offset: 0})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}

	got, err = s.ListTasks(ctx, TaskFilter{EventID: "evt-l1", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

func TestSQLiteStore_CountByStatus(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("evt-c1", "proj-a", "error", now)
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	statuses := []TaskStatus{StatusPending, StatusPending, StatusRunning, StatusCompleted, StatusFailed}
	for i, st := range statuses {
		task := makeTask("tc-"+string(rune('0'+i)), "evt-c1", "proj-a", st)
		task.CreatedAt = now.Add(time.Duration(i) * time.Minute)
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	counts, err := s.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[StatusPending] != 2 {
		t.Errorf("pending = %d, want 2", counts[StatusPending])
	}
	if counts[StatusRunning] != 1 {
		t.Errorf("running = %d, want 1", counts[StatusRunning])
	}
	if counts[StatusCompleted] != 1 {
		t.Errorf("completed = %d, want 1", counts[StatusCompleted])
	}
	if counts[StatusFailed] != 1 {
		t.Errorf("failed = %d, want 1", counts[StatusFailed])
	}
}

func TestSQLiteStore_SaveReport_GetReport(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	ev := makeEvent("evt-r1", "proj-a", "error", now)
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	task := makeTask("task-r1", "evt-r1", "proj-a", StatusCompleted)
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	report := makeReport("rpt-1", "task-r1", "evt-r1", "proj-a")
	if err := s.SaveReport(ctx, report); err != nil {
		t.Fatalf("SaveReport: %v", err)
	}

	got, err := s.GetReport(ctx, "task-r1")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got == nil {
		t.Fatal("GetReport returned nil")
	}
	if got.ID != report.ID {
		t.Errorf("ID = %q, want %q", got.ID, report.ID)
	}
	if got.Summary != report.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, report.Summary)
	}
	if got.HasIssue != report.HasIssue {
		t.Errorf("HasIssue = %v, want %v", got.HasIssue, report.HasIssue)
	}
	if got.Confidence != report.Confidence {
		t.Errorf("Confidence = %q, want %q", got.Confidence, report.Confidence)
	}
	if len(got.ToolsUsed) != 2 || got.ToolsUsed[0] != "tool1" || got.ToolsUsed[1] != "tool2" {
		t.Errorf("ToolsUsed = %v, want [tool1 tool2]", got.ToolsUsed)
	}
	if len(got.SkillsUsed) != 1 || got.SkillsUsed[0] != "skill1" {
		t.Errorf("SkillsUsed = %v, want [skill1]", got.SkillsUsed)
	}

	// Verify json.RawMessage roundtrip
	if string(got.StructuredResult) != `{"result":"ok"}` {
		t.Errorf("StructuredResult = %s, want %s", got.StructuredResult, `{"result":"ok"}`)
	}
	if string(got.QualityScore) != `{"score":0.95}` {
		t.Errorf("QualityScore = %s, want %s", got.QualityScore, `{"score":0.95}`)
	}

	// Verify fingerprint fields
	if got.Fingerprint != report.Fingerprint {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, report.Fingerprint)
	}
	if got.ReusedFromID != report.ReusedFromID {
		t.Errorf("ReusedFromID = %q, want %q", got.ReusedFromID, report.ReusedFromID)
	}
	if got.OriginalConfidence != report.OriginalConfidence {
		t.Errorf("OriginalConfidence = %f, want %f", got.OriginalConfidence, report.OriginalConfidence)
	}
	if got.FinalConfidence != report.FinalConfidence {
		t.Errorf("FinalConfidence = %f, want %f", got.FinalConfidence, report.FinalConfidence)
	}
	if got.FinalConfLabel != report.FinalConfLabel {
		t.Errorf("FinalConfLabel = %q, want %q", got.FinalConfLabel, report.FinalConfLabel)
	}
}

func TestSQLiteStore_GetReport_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	got, err := s.GetReport(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetReport: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestSQLiteStore_FindRecentReportByFingerprint(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-24 * time.Hour)

	// Setup: create event and task
	ev := makeEvent("evt-fp1", "proj-a", "error", now)
	if err := s.CreateEvent(ctx, ev); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	for _, tid := range []string{"task-fp1", "task-fp2", "task-fp3", "task-fp4"} {
		task := makeTask(tid, "evt-fp1", "proj-a", StatusCompleted)
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask(%s): %v", tid, err)
		}
	}

	// Report 1: older, matching fingerprint
	r1 := makeReport("rpt-fp1", "task-fp1", "evt-fp1", "proj-a")
	r1.Fingerprint = "fp-match"
	r1.DiagnosedAt = now.Add(-12 * time.Hour)
	if err := s.SaveReport(ctx, r1); err != nil {
		t.Fatalf("SaveReport(r1): %v", err)
	}

	// Report 2: newer, matching fingerprint (should be returned as most recent)
	r2 := makeReport("rpt-fp2", "task-fp2", "evt-fp1", "proj-a")
	r2.Fingerprint = "fp-match"
	r2.DiagnosedAt = now.Add(-6 * time.Hour)
	if err := s.SaveReport(ctx, r2); err != nil {
		t.Fatalf("SaveReport(r2): %v", err)
	}

	// Report 3: matching fingerprint but reused (should be skipped)
	r3 := makeReport("rpt-fp3", "task-fp3", "evt-fp1", "proj-a")
	r3.Fingerprint = "fp-match"
	r3.DiagnosedAt = now.Add(-1 * time.Hour)
	r3.ReusedFromID = "rpt-fp1"
	if err := s.SaveReport(ctx, r3); err != nil {
		t.Fatalf("SaveReport(r3): %v", err)
	}

	// Test: match by projectKey + fingerprint + since → returns r2 (most recent non-reused)
	got, err := s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-match", since)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint: %v", err)
	}
	if got == nil {
		t.Fatal("expected report, got nil")
	}
	if got.ID != "rpt-fp2" {
		t.Errorf("ID = %q, want %q (most recent non-reused)", got.ID, "rpt-fp2")
	}

	// Test: no match for different fingerprint
	got, err = s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-nope", since)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for non-matching fingerprint, got %+v", got)
	}

	// Test: no match when all are before `since`
	got, err = s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-match", now)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when all before since, got %+v", got)
	}

	// Report 4: older than `since` window
	r4 := makeReport("rpt-fp4", "task-fp4", "evt-fp1", "proj-a")
	r4.Fingerprint = "fp-old"
	r4.DiagnosedAt = now.Add(-48 * time.Hour)
	if err := s.SaveReport(ctx, r4); err != nil {
		t.Fatalf("SaveReport(r4): %v", err)
	}

	got, err = s.FindRecentReportByFingerprint(ctx, "proj-a", "fp-old", since)
	if err != nil {
		t.Fatalf("FindRecentReportByFingerprint: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for old report, got %+v", got)
	}
}

func TestSQLiteStore_GetUsageSummary(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)

	// Create events: one today, one yesterday
	ev1 := makeEvent("evt-u1", "proj-a", "error", now)
	ev2 := makeEvent("evt-u2", "proj-a", "warning", now.Add(-48*time.Hour))
	if err := s.CreateEvent(ctx, ev1); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if err := s.CreateEvent(ctx, ev2); err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}

	// Create tasks with tokens
	t1 := makeTask("tu-1", "evt-u1", "proj-a", StatusCompleted)
	t1.InputTokens = 500
	t1.OutputTokens = 1000
	t2 := makeTask("tu-2", "evt-u1", "proj-a", StatusPending)
	t2.InputTokens = 300
	t2.OutputTokens = 600
	for _, task := range []*DiagnosisTask{t1, t2} {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	summary, err := s.GetUsageSummary(ctx)
	if err != nil {
		t.Fatalf("GetUsageSummary: %v", err)
	}

	if summary.TotalEvents != 2 {
		t.Errorf("TotalEvents = %d, want 2", summary.TotalEvents)
	}
	if summary.TasksByStatus[StatusCompleted] != 1 {
		t.Errorf("TasksByStatus[completed] = %d, want 1", summary.TasksByStatus[StatusCompleted])
	}
	if summary.TasksByStatus[StatusPending] != 1 {
		t.Errorf("TasksByStatus[pending] = %d, want 1", summary.TasksByStatus[StatusPending])
	}
	if summary.TotalInputTokens != 800 {
		t.Errorf("TotalInputTokens = %d, want 800", summary.TotalInputTokens)
	}
	if summary.TotalOutputTokens != 1600 {
		t.Errorf("TotalOutputTokens = %d, want 1600", summary.TotalOutputTokens)
	}
}
