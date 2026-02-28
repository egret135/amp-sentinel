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
		`CREATE TABLE IF NOT EXISTS incidents (
    id VARCHAR(64) PRIMARY KEY,
    project_key VARCHAR(128) NOT NULL,
    title VARCHAR(512) NOT NULL,
    error_type VARCHAR(256) NOT NULL DEFAULT '',
    error_msg TEXT NOT NULL,
    stacktrace LONGTEXT NOT NULL,
    environment VARCHAR(64) NOT NULL DEFAULT 'production',
    severity VARCHAR(32) NOT NULL DEFAULT 'warning',
    url VARCHAR(2048) NOT NULL DEFAULT '',
    metadata JSON NOT NULL,
    source VARCHAR(64) NOT NULL DEFAULT 'custom',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    occurred_at DATETIME(3) NOT NULL,
    reported_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE INDEX idx_incidents_project_key ON incidents(project_key)`,
		`CREATE INDEX idx_incidents_status ON incidents(status)`,
		`CREATE INDEX idx_incidents_occurred_at ON incidents(occurred_at)`,
		`CREATE INDEX idx_incidents_dedup ON incidents(project_key, error_msg(255), occurred_at)`,

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
    CONSTRAINT fk_tasks_incident FOREIGN KEY (incident_id) REFERENCES incidents(id)
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
    CONSTRAINT fk_reports_task FOREIGN KEY (task_id) REFERENCES diagnosis_tasks(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,

		`CREATE INDEX idx_reports_task ON diagnosis_reports(task_id)`,
		`CREATE INDEX idx_reports_project ON diagnosis_reports(project_key)`,
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
	return strings.Contains(err.Error(), "Duplicate key name") || strings.Contains(err.Error(), "already exists")
}

func (s *MySQLStore) CreateIncident(ctx context.Context, inc *Incident) error {
	md := inc.Metadata
	if md == nil {
		md = map[string]string{}
	}
	metadata, err := json.Marshal(md)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO incidents (id, project_key, title, error_type, error_msg, stacktrace, environment, severity, url, metadata, source, status, occurred_at, reported_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		inc.ID, inc.ProjectKey, inc.Title, inc.ErrorType, inc.ErrorMsg, inc.Stacktrace,
		inc.Environment, inc.Severity, inc.URL, string(metadata), inc.Source, inc.Status,
		inc.OccurredAt, inc.ReportedAt,
	)
	if err != nil {
		return fmt.Errorf("insert incident: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetIncident(ctx context.Context, id string) (*Incident, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_key, title, error_type, error_msg, stacktrace, environment, severity, url, metadata, source, status, occurred_at, reported_at
		 FROM incidents WHERE id = ?`, id)

	inc, err := s.scanIncident(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inc, err
}

func (s *MySQLStore) UpdateIncident(ctx context.Context, inc *Incident) error {
	md := inc.Metadata
	if md == nil {
		md = map[string]string{}
	}
	metadata, err := json.Marshal(md)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`UPDATE incidents SET project_key=?, title=?, error_type=?, error_msg=?, stacktrace=?, environment=?, severity=?, url=?, metadata=?, source=?, status=?, occurred_at=?, reported_at=?
		 WHERE id=?`,
		inc.ProjectKey, inc.Title, inc.ErrorType, inc.ErrorMsg, inc.Stacktrace,
		inc.Environment, inc.Severity, inc.URL, string(metadata), inc.Source, inc.Status,
		inc.OccurredAt, inc.ReportedAt, inc.ID,
	)
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	return nil
}

func (s *MySQLStore) ListIncidents(ctx context.Context, filter IncidentFilter) ([]*Incident, error) {
	query := "SELECT id, project_key, title, error_type, error_msg, stacktrace, environment, severity, url, metadata, source, status, occurred_at, reported_at FROM incidents"
	var conditions []string
	var args []any

	if filter.ProjectKey != "" {
		conditions = append(conditions, "project_key = ?")
		args = append(args, filter.ProjectKey)
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

	query += " ORDER BY occurred_at DESC"

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
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()

	var incidents []*Incident
	for rows.Next() {
		inc, err := s.scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		incidents = append(incidents, inc)
	}
	return incidents, rows.Err()
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
		task.ID, task.IncidentID, task.ProjectKey, string(task.Status), task.Priority,
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
		task.IncidentID, task.ProjectKey, string(task.Status), task.Priority,
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

	if filter.IncidentID != "" {
		conditions = append(conditions, "incident_id = ?")
		args = append(args, filter.IncidentID)
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

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO diagnosis_reports (id, task_id, incident_id, project_key, project_name, summary, raw_result, has_issue, confidence, tools_used, skills_used, tainted, notified, diagnosed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		report.ID, report.TaskID, report.IncidentID, report.ProjectKey, report.ProjectName,
		report.Summary, report.RawResult, report.HasIssue, report.Confidence,
		string(toolsUsed), string(skillsUsed), report.Tainted, report.Notified, report.DiagnosedAt,
	)
	if err != nil {
		return fmt.Errorf("insert report: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetReport(ctx context.Context, taskID string) (*DiagnosisReport, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, incident_id, project_key, project_name, summary, raw_result, has_issue, confidence, tools_used, skills_used, tainted, notified, diagnosed_at
		 FROM diagnosis_reports WHERE task_id = ?`, taskID)

	report, err := s.scanReport(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return report, err
}

func (s *MySQLStore) FindRecentIncident(ctx context.Context, projectKey, errorMsg string, window time.Duration) (*Incident, error) {
	cutoff := time.Now().Add(-window)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, project_key, title, error_type, error_msg, stacktrace, environment, severity, url, metadata, source, status, occurred_at, reported_at
		 FROM incidents WHERE project_key = ? AND error_msg = ? AND occurred_at > ? ORDER BY occurred_at DESC LIMIT 1`,
		projectKey, errorMsg, cutoff)

	inc, err := s.scanIncident(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inc, err
}

func (s *MySQLStore) GetUsageSummary(ctx context.Context) (*UsageSummary, error) {
	summary := &UsageSummary{
		TasksByStatus: make(map[TaskStatus]int),
	}

	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM incidents").Scan(&summary.TotalIncidents)
	if err != nil {
		return nil, fmt.Errorf("count incidents: %w", err)
	}

	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM incidents WHERE DATE(reported_at) = CURDATE()").Scan(&summary.TodayIncidents)
	if err != nil {
		return nil, fmt.Errorf("count today incidents: %w", err)
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

func (s *MySQLStore) scanIncident(row mysqlScannable) (*Incident, error) {
	var inc Incident
	var metadataStr string
	err := row.Scan(
		&inc.ID, &inc.ProjectKey, &inc.Title, &inc.ErrorType, &inc.ErrorMsg, &inc.Stacktrace,
		&inc.Environment, &inc.Severity, &inc.URL, &metadataStr, &inc.Source, &inc.Status,
		&inc.OccurredAt, &inc.ReportedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadataStr), &inc.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal metadata: %w", err)
	}
	return &inc, nil
}

func (s *MySQLStore) scanTask(row mysqlScannable) (*DiagnosisTask, error) {
	var task DiagnosisTask
	var status string
	var startedAt, finishedAt sql.NullTime
	err := row.Scan(
		&task.ID, &task.IncidentID, &task.ProjectKey, &status, &task.Priority,
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
	err := row.Scan(
		&report.ID, &report.TaskID, &report.IncidentID, &report.ProjectKey, &report.ProjectName,
		&report.Summary, &report.RawResult, &report.HasIssue, &report.Confidence,
		&toolsUsedStr, &skillsUsedStr, &report.Tainted, &report.Notified, &report.DiagnosedAt,
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
	return &report, nil
}
