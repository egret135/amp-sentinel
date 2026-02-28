package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"amp-sentinel/intake"
	"amp-sentinel/logger"
	"amp-sentinel/project"
	"amp-sentinel/scheduler"
	"amp-sentinel/store"
)

// Server implements the Admin API for amp-sentinel.
type Server struct {
	store     store.Store
	registry  *project.Registry
	sched     *scheduler.Scheduler
	log       logger.Logger
	resubmit  func(inc *intake.Incident) (string, error)
	authToken string
}

// NewServer creates a new Admin API server.
func NewServer(
	st store.Store,
	reg *project.Registry,
	sched *scheduler.Scheduler,
	log logger.Logger,
	resubmit func(inc *intake.Incident) (string, error),
	authToken string,
) *Server {
	return &Server{
		store:     st,
		registry:  reg,
		sched:     sched,
		log:       log,
		resubmit:  resubmit,
		authToken: authToken,
	}
}

// Handler returns an http.Handler with all admin routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Dashboard static files (auth handled by frontend via API tokens)
	mux.Handle("/admin/dashboard/", http.StripPrefix("/admin/dashboard/", dashboardHandler()))
	// Redirect bare /admin/dashboard to /admin/dashboard/
	mux.HandleFunc("/admin/dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/dashboard/", http.StatusMovedPermanently)
	})

	// API routes
	mux.HandleFunc("/admin/v1/health", s.handleHealth)
	mux.HandleFunc("/admin/v1/stats", s.handleStats)
	mux.HandleFunc("/admin/v1/projects", s.handleProjects)
	mux.HandleFunc("/admin/v1/incidents", s.handleIncidentsList)
	mux.HandleFunc("/admin/v1/incidents/", s.handleIncidentsDetail)
	mux.HandleFunc("/admin/v1/tasks", s.handleTasksList)
	mux.HandleFunc("/admin/v1/tasks/", s.handleTasksDetail)
	mux.HandleFunc("/admin/v1/reports/", s.handleReports)

	if s.authToken == "" {
		return mux
	}
	return s.authMiddleware(mux)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health check and dashboard static files are always public
		if r.URL.Path == "/admin/v1/health" ||
			strings.HasPrefix(r.URL.Path, "/admin/dashboard") {
			next.ServeHTTP(w, r)
			return
		}
		token := r.Header.Get("Authorization")
		if token != "Bearer "+s.authToken {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	usage, err := s.store.GetUsageSummary(ctx)
	if err != nil {
		s.log.Error("admin.stats_failed", logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to get usage summary")
		return
	}

	schedStats := s.sched.Stats()

	writeJSON(w, http.StatusOK, map[string]any{
		"usage":     usage,
		"scheduler": schedStats,
	})
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projects := s.registry.All()
	sanitized := make([]map[string]any, len(projects))
	for i, p := range projects {
		sanitized[i] = map[string]any{
			"key":         p.Key,
			"name":        p.Name,
			"branch":      p.Branch,
			"language":    p.Language,
			"skills":      p.Skills,
			"owners":      p.Owners,
			"has_webhook": p.FeishuWebhook != "",
		}
	}
	writeJSON(w, http.StatusOK, sanitized)
}

func (s *Server) handleIncidentsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := r.URL.Query()
	filter := store.IncidentFilter{
		ProjectKey: q.Get("project_key"),
		Status:     q.Get("status"),
		Severity:   q.Get("severity"),
		Limit:      parseIntParam(q.Get("limit"), 50),
		Offset:     parseIntParam(q.Get("offset"), 0),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	incidents, err := s.store.ListIncidents(ctx, filter)
	if err != nil {
		s.log.Error("admin.list_incidents_failed", logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to list incidents")
		return
	}
	writeJSON(w, http.StatusOK, incidents)
}

func (s *Server) handleIncidentsDetail(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/v1/incidents/")
	if path == "" {
		writeError(w, http.StatusBadRequest, "incident id required")
		return
	}

	// POST /admin/v1/incidents/{id}/retry
	if strings.HasSuffix(path, "/retry") {
		s.handleRetry(w, r, strings.TrimSuffix(path, "/retry"))
		return
	}

	// GET /admin/v1/incidents/{id}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	inc, err := s.store.GetIncident(ctx, path)
	if err != nil {
		s.log.Error("admin.get_incident_failed", logger.String("id", path), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to get incident")
		return
	}
	if inc == nil {
		writeError(w, http.StatusNotFound, "incident not found")
		return
	}
	writeJSON(w, http.StatusOK, inc)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	storeInc, err := s.store.GetIncident(ctx, id)
	if err != nil {
		s.log.Error("admin.retry_get_failed", logger.String("id", id), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to get incident")
		return
	}
	if storeInc == nil {
		writeError(w, http.StatusNotFound, "incident not found")
		return
	}

	// Check for active tasks (pending/queued/running)
	tasks, err := s.store.ListTasks(ctx, store.TaskFilter{IncidentID: id, Limit: 100})
	if err != nil {
		s.log.Error("admin.retry_list_tasks_failed", logger.String("id", id), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to check active tasks")
		return
	}
	for _, t := range tasks {
		if t.Status == store.StatusPending || t.Status == store.StatusQueued || t.Status == store.StatusRunning {
			writeError(w, http.StatusConflict, "incident has an active diagnosis task: "+t.ID)
			return
		}
	}

	inc := &intake.Incident{
		ID:          storeInc.ID,
		ProjectKey:  storeInc.ProjectKey,
		Title:       storeInc.Title,
		ErrorType:   storeInc.ErrorType,
		ErrorMsg:    storeInc.ErrorMsg,
		Stacktrace:  storeInc.Stacktrace,
		Environment: storeInc.Environment,
		Severity:    storeInc.Severity,
		URL:         storeInc.URL,
		Source:      storeInc.Source,
		Metadata:    storeInc.Metadata,
		OccurredAt:  storeInc.OccurredAt,
		ReportedAt:  storeInc.ReportedAt,
	}

	taskID, err := s.resubmit(inc)
	if err != nil {
		s.log.Error("admin.retry_submit_failed", logger.String("id", id), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to resubmit incident")
		return
	}

	s.log.Info("admin.retry_submitted",
		logger.String("incident_id", id),
		logger.String("task_id", taskID),
	)

	writeJSON(w, http.StatusOK, map[string]string{
		"incident_id": id,
		"task_id":     taskID,
	})
}

func (s *Server) handleTasksList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	q := r.URL.Query()
	filter := store.TaskFilter{
		IncidentID: q.Get("incident_id"),
		ProjectKey: q.Get("project_key"),
		Status:     store.TaskStatus(q.Get("status")),
		Limit:      parseIntParam(q.Get("limit"), 50),
		Offset:     parseIntParam(q.Get("offset"), 0),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	tasks, err := s.store.ListTasks(ctx, filter)
	if err != nil {
		s.log.Error("admin.list_tasks_failed", logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to list tasks")
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleTasksDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/admin/v1/tasks/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "task id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	task, err := s.store.GetTask(ctx, id)
	if err != nil {
		s.log.Error("admin.get_task_failed", logger.String("id", id), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	taskID := strings.TrimPrefix(r.URL.Path, "/admin/v1/reports/")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "task_id required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	report, err := s.store.GetReport(ctx, taskID)
	if err != nil {
		s.log.Error("admin.get_report_failed", logger.String("task_id", taskID), logger.Err(err))
		writeError(w, http.StatusInternalServerError, "failed to get report")
		return
	}
	if report == nil {
		writeError(w, http.StatusNotFound, "report not found")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func parseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return defaultVal
	}
	return v
}
