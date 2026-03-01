package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"amp-sentinel/logger"

	_ "github.com/go-sql-driver/mysql"
)

// MySQLConfig holds connection settings for the MySQL store.
type MySQLConfig struct {
	DSN             string        `json:"dsn"`
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
}

// MySQLStore implements Store using MySQL.
type MySQLStore struct {
	db  *sql.DB
	log logger.Logger
}

// NewMySQLStore opens a MySQL database and initializes the schema.
func NewMySQLStore(cfg MySQLConfig, log logger.Logger) (*MySQLStore, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}

	s := &MySQLStore{db: db, log: log}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}

	log.Info("store.mysql.opened")
	return s, nil
}

func (s *MySQLStore) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
    id VARCHAR(64) PRIMARY KEY,
    project_key VARCHAR(128) NOT NULL,
    payload LONGTEXT NOT NULL,
    source VARCHAR(64) NOT NULL DEFAULT 'custom',
    severity VARCHAR(32) NOT NULL DEFAULT 'warning',
    title VARCHAR(512) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    received_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE INDEX idx_events_project_key ON events(project_key)`,
		`CREATE INDEX idx_events_status ON events(status)`,
		`CREATE INDEX idx_events_received_at ON events(received_at)`,
		`CREATE INDEX idx_events_severity ON events(severity)`,

		`CREATE TABLE IF NOT EXISTS diagnosis_tasks (
    id VARCHAR(64) PRIMARY KEY,
    incident_id VARCHAR(64) NOT NULL,
    project_key VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    priority INT NOT NULL DEFAULT 0,
    session_id VARCHAR(128) NOT NULL DEFAULT '',
    num_turns INT NOT NULL DEFAULT 0,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    input_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    error TEXT NOT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    started_at DATETIME(3) NULL,
    finished_at DATETIME(3) NULL,
    CONSTRAINT fk_tasks_event FOREIGN KEY (incident_id) REFERENCES events(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE INDEX idx_tasks_status ON diagnosis_tasks(status)`,
		`CREATE INDEX idx_tasks_incident ON diagnosis_tasks(incident_id)`,

		`CREATE TABLE IF NOT EXISTS diagnosis_reports (
    id VARCHAR(64) PRIMARY KEY,
    task_id VARCHAR(64) NOT NULL,
    incident_id VARCHAR(64) NOT NULL,
    project_key VARCHAR(128) NOT NULL,
    project_name VARCHAR(256) NOT NULL DEFAULT '',
    summary TEXT NOT NULL,
    raw_result LONGTEXT NOT NULL,
    has_issue TINYINT(1) NOT NULL DEFAULT 0,
    confidence VARCHAR(32) NOT NULL DEFAULT 'low',
    tools_used JSON NOT NULL,
    skills_used JSON NOT NULL,
    tainted TINYINT(1) NOT NULL DEFAULT 0,
    notified TINYINT(1) NOT NULL DEFAULT 0,
    diagnosed_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    structured_result JSON NOT NULL,
    quality_score JSON NOT NULL,
    commit_hash VARCHAR(128) NOT NULL DEFAULT '',
    prompt_version VARCHAR(32) NOT NULL DEFAULT '',
    original_confidence DOUBLE NOT NULL DEFAULT 0,
    final_confidence DOUBLE NOT NULL DEFAULT 0,
    final_confidence_label VARCHAR(32) NOT NULL DEFAULT '',
    CONSTRAINT fk_reports_task FOREIGN KEY (task_id) REFERENCES diagnosis_tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE INDEX idx_reports_task ON diagnosis_reports(task_id)`,
		`CREATE INDEX idx_reports_project ON diagnosis_reports(project_key)`,

		// Migration: add new columns to diagnosis_reports for existing tables.
		`ALTER TABLE diagnosis_reports ADD COLUMN structured_result JSON NOT NULL DEFAULT (CAST('null' AS JSON))`,
		`ALTER TABLE diagnosis_reports ADD COLUMN quality_score JSON NOT NULL DEFAULT (CAST('{}' AS JSON))`,
		`ALTER TABLE diagnosis_reports ADD COLUMN commit_hash VARCHAR(128) NOT NULL DEFAULT ''`,
		`ALTER TABLE diagnosis_reports ADD COLUMN prompt_version VARCHAR(32) NOT NULL DEFAULT ''`,
		`ALTER TABLE diagnosis_reports ADD COLUMN original_confidence DOUBLE NOT NULL DEFAULT 0`,
		`ALTER TABLE diagnosis_reports ADD COLUMN final_confidence DOUBLE NOT NULL DEFAULT 0`,
		`ALTER TABLE diagnosis_reports ADD COLUMN final_confidence_label VARCHAR(32) NOT NULL DEFAULT ''`,
		`ALTER TABLE diagnosis_reports ADD COLUMN fingerprint VARCHAR(256) NOT NULL DEFAULT ''`,
		`ALTER TABLE diagnosis_reports ADD COLUMN reused_from_id VARCHAR(64) NOT NULL DEFAULT ''`,
		`CREATE INDEX idx_reports_fingerprint ON diagnosis_reports(project_key, fingerprint, diagnosed_at)`,
	}

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			if isDuplicateKeyError(err) {
				continue
			}
			return fmt.Errorf("exec schema: %w", err)
		}
	}
	return nil
}

func isDuplicateKeyError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Duplicate key name") ||
		strings.Contains(msg, "Duplicate column name") ||
		strings.Contains(msg, "already exists")
}

func (s *MySQLStore) CreateEvent(ctx context.Context, event *Event) error {
	payload := event.Payload
	if payload == nil {
		payload = json.RawMessage("{}")
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO events (id, project_key, payload, source, severity, title, status, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.ProjectKey, string(payload), event.Source, event.Severity,
		event.Title, event.Status, event.ReceivedAt,
	)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetEvent(ctx context.Context, id string) (*Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_key, payload, source, severity, title, status, received_at
		 FROM events WHERE id = ?`, id)

	event, err := s.scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return event, err
}

func (s *MySQLStore) UpdateEvent(ctx context.Context, event *Event) error {
	payload := event.Payload
	if payload == nil {
		payload = json.RawMessage("{}")
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE events SET project_key=?, payload=?, source=?, severity=?, title=?, status=?, received_at=?
		 WHERE id=?`,
		event.ProjectKey, string(payload), event.Source, event.Severity,
		event.Title, event.Status, event.ReceivedAt, event.ID,
	)
	if err != nil {
		return fmt.Errorf("update event: %w", err)
	}
	return nil
}

func (s *MySQLStore) ListEvents(ctx context.Context, filter EventFilter) ([]*Event, error) {
	query := "SELECT id, project_key, payload, source, severity, title, status, received_at FROM events"
	var conditions []string
	var args []any

	if filter.ProjectKey != "" {
		conditions = append(conditions, "project_key = ?")
		args = append(args, filter.ProjectKey)
	}
	if filter.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, filter.Source)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Severity != "" {
		conditions = append(conditions, "severity = ?")
		args = append(args, filter.Severity)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY received_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	var events []*Event
	for rows.Next() {
		event, err := s.scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *MySQLStore) CreateTask(ctx context.Context, task *DiagnosisTask) error {
	var startedAt, finishedAt sql.NullTime
	if task.StartedAt != nil {
		startedAt = sql.NullTime{Time: *task.StartedAt, Valid: true}
	}
	if task.FinishedAt != nil {
		finishedAt = sql.NullTime{Time: *task.FinishedAt, Valid: true}
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnosis_tasks (id, incident_id, project_key, status, priority, session_id, num_turns, duration_ms, input_tokens, output_tokens, error, retry_count, created_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.EventID, task.ProjectKey, string(task.Status), task.Priority,
		task.SessionID, task.NumTurns, task.DurationMs, task.InputTokens, task.OutputTokens,
		task.Error, task.RetryCount, task.CreatedAt, startedAt, finishedAt,
	)
	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetTask(ctx context.Context, id string) (*DiagnosisTask, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, incident_id, project_key, status, priority, session_id, num_turns, duration_ms, input_tokens, output_tokens, error, retry_count, created_at, started_at, finished_at
		 FROM diagnosis_tasks WHERE id = ?`, id)

	task, err := s.scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return task, err
}

func (s *MySQLStore) UpdateTask(ctx context.Context, task *DiagnosisTask) error {
	var startedAt, finishedAt sql.NullTime
	if task.StartedAt != nil {
		startedAt = sql.NullTime{Time: *task.StartedAt, Valid: true}
	}
	if task.FinishedAt != nil {
		finishedAt = sql.NullTime{Time: *task.FinishedAt, Valid: true}
	}

	_, err := s.db.ExecContext(ctx,
		`UPDATE diagnosis_tasks SET incident_id=?, project_key=?, status=?, priority=?, session_id=?, num_turns=?, duration_ms=?, input_tokens=?, output_tokens=?, error=?, retry_count=?, created_at=?, started_at=?, finished_at=?
		 WHERE id=?`,
		task.EventID, task.ProjectKey, string(task.Status), task.Priority,
		task.SessionID, task.NumTurns, task.DurationMs, task.InputTokens, task.OutputTokens,
		task.Error, task.RetryCount, task.CreatedAt, startedAt, finishedAt, task.ID,
	)
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return nil
}

func (s *MySQLStore) ListTasks(ctx context.Context, filter TaskFilter) ([]*DiagnosisTask, error) {
	query := "SELECT id, incident_id, project_key, status, priority, session_id, num_turns, duration_ms, input_tokens, output_tokens, error, retry_count, created_at, started_at, finished_at FROM diagnosis_tasks"
	var conditions []string
	var args []any

	if filter.EventID != "" {
		conditions = append(conditions, "incident_id = ?")
		args = append(args, filter.EventID)
	}
	if filter.ProjectKey != "" {
		conditions = append(conditions, "project_key = ?")
		args = append(args, filter.ProjectKey)
	}
	if filter.Status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, string(filter.Status))
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*DiagnosisTask
	for rows.Next() {
		task, err := s.scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (s *MySQLStore) CountByStatus(ctx context.Context) (map[TaskStatus]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT status, COUNT(*) FROM diagnosis_tasks GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}
	defer rows.Close()

	result := make(map[TaskStatus]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan status count: %w", err)
		}
		result[TaskStatus(status)] = count
	}
	return result, rows.Err()
}

func (s *MySQLStore) SaveReport(ctx context.Context, report *DiagnosisReport) error {
	tools := report.ToolsUsed
	if tools == nil {
		tools = []string{}
	}
	skills := report.SkillsUsed
	if skills == nil {
		skills = []string{}
	}
	toolsUsed, err := json.Marshal(tools)
	if err != nil {
		return fmt.Errorf("marshal tools_used: %w", err)
	}
	skillsUsed, err := json.Marshal(skills)
	if err != nil {
		return fmt.Errorf("marshal skills_used: %w", err)
	}

	structuredResult := json.RawMessage("null")
	if report.StructuredResult != nil {
		structuredResult = report.StructuredResult
	}
	qualityScore := json.RawMessage("null")
	if report.QualityScore != nil {
		qualityScore = report.QualityScore
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO diagnosis_reports (id, task_id, incident_id, project_key, project_name, summary, raw_result, has_issue, confidence, tools_used, skills_used, tainted, notified, diagnosed_at, structured_result, quality_score, commit_hash, prompt_version, original_confidence, final_confidence, final_confidence_label, fingerprint, reused_from_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ID, report.TaskID, report.EventID, report.ProjectKey, report.ProjectName,
		report.Summary, report.RawResult, report.HasIssue, report.Confidence,
		string(toolsUsed), string(skillsUsed), report.Tainted, report.Notified, report.DiagnosedAt,
		string(structuredResult), string(qualityScore), report.CommitHash, report.PromptVersion,
		report.OriginalConfidence, report.FinalConfidence, report.FinalConfLabel,
		report.Fingerprint, report.ReusedFromID,
	)
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetReport(ctx context.Context, taskID string) (*DiagnosisReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, incident_id, project_key, project_name, summary, raw_result, has_issue, confidence, tools_used, skills_used, tainted, notified, diagnosed_at, structured_result, quality_score, commit_hash, prompt_version, original_confidence, final_confidence, final_confidence_label, fingerprint, reused_from_id
		 FROM diagnosis_reports WHERE task_id = ?`, taskID)

	report, err := s.scanReport(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return report, err
}

func (s *MySQLStore) FindRecentReportByFingerprint(ctx context.Context, projectKey, fingerprint string, since time.Time) (*DiagnosisReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, incident_id, project_key, project_name, summary, raw_result, has_issue, confidence, tools_used, skills_used, tainted, notified, diagnosed_at, structured_result, quality_score, commit_hash, prompt_version, original_confidence, final_confidence, final_confidence_label, fingerprint, reused_from_id
		 FROM diagnosis_reports
		 WHERE project_key = ? AND fingerprint = ? AND diagnosed_at > ? AND reused_from_id = ''
		 ORDER BY diagnosed_at DESC LIMIT 1`, projectKey, fingerprint, since)

	report, err := s.scanReport(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return report, err
}

func (s *MySQLStore) GetUsageSummary(ctx context.Context) (*UsageSummary, error) {
	summary := &UsageSummary{
		TasksByStatus: make(map[TaskStatus]int),
	}

	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&summary.TotalEvents)
	if err != nil {
		return nil, fmt.Errorf("count events: %w", err)
	}

	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE DATE(received_at) = CURDATE()").Scan(&summary.TodayEvents)
	if err != nil {
		return nil, fmt.Errorf("count today events: %w", err)
	}

	statusCounts, err := s.CountByStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("count tasks by status: %w", err)
	}
	summary.TasksByStatus = statusCounts

	var inputTokens, outputTokens sql.NullInt64
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0) FROM diagnosis_tasks").Scan(&inputTokens, &outputTokens)
	if err != nil {
		return nil, fmt.Errorf("sum tokens: %w", err)
	}
	summary.TotalInputTokens = inputTokens.Int64
	summary.TotalOutputTokens = outputTokens.Int64

	return summary, nil
}

func (s *MySQLStore) Close() error {
	s.log.Info("store.mysql.closing")
	return s.db.Close()
}

// scan helpers

type mysqlScannable interface {
	Scan(dest ...any) error
}

func (s *MySQLStore) scanEvent(row mysqlScannable) (*Event, error) {
	var event Event
	var payloadStr string
	err := row.Scan(
		&event.ID, &event.ProjectKey, &payloadStr, &event.Source,
		&event.Severity, &event.Title, &event.Status, &event.ReceivedAt,
	)
	if err != nil {
		return nil, err
	}
	event.Payload = json.RawMessage(payloadStr)
	return &event, nil
}

func (s *MySQLStore) scanTask(row mysqlScannable) (*DiagnosisTask, error) {
	var task DiagnosisTask
	var status string
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(
		&task.ID, &task.EventID, &task.ProjectKey, &status, &task.Priority,
		&task.SessionID, &task.NumTurns, &task.DurationMs, &task.InputTokens, &task.OutputTokens,
		&task.Error, &task.RetryCount, &task.CreatedAt, &startedAt, &finishedAt,
	)
	if err != nil {
		return nil, err
	}
	task.Status = TaskStatus(status)
	if startedAt.Valid {
		task.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		task.FinishedAt = &finishedAt.Time
	}
	return &task, nil
}

func (s *MySQLStore) scanReport(row mysqlScannable) (*DiagnosisReport, error) {
	var report DiagnosisReport
	var toolsUsedStr, skillsUsedStr string
	var structuredResult, qualityScore []byte
	err := row.Scan(
		&report.ID, &report.TaskID, &report.EventID, &report.ProjectKey, &report.ProjectName,
		&report.Summary, &report.RawResult, &report.HasIssue, &report.Confidence,
		&toolsUsedStr, &skillsUsedStr, &report.Tainted, &report.Notified, &report.DiagnosedAt,
		&structuredResult, &qualityScore, &report.CommitHash, &report.PromptVersion,
		&report.OriginalConfidence, &report.FinalConfidence, &report.FinalConfLabel,
		&report.Fingerprint, &report.ReusedFromID,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(toolsUsedStr), &report.ToolsUsed); err != nil {
		return nil, fmt.Errorf("unmarshal tools_used: %w", err)
	}
	if err := json.Unmarshal([]byte(skillsUsedStr), &report.SkillsUsed); err != nil {
		return nil, fmt.Errorf("unmarshal skills_used: %w", err)
	}
	report.StructuredResult = json.RawMessage(structuredResult)
	report.QualityScore = json.RawMessage(qualityScore)
	return &report, nil
}
