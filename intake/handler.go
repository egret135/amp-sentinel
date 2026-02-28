package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"amp-sentinel/logger"

	"github.com/google/uuid"
)

// ProjectValidator checks whether a project key is registered.
type ProjectValidator func(key string) bool

// Handler handles incoming incident reports via HTTP.
type Handler struct {
	authToken        string
	dedupWindow      time.Duration
	rateLimit        int // max incidents per project per hour
	minSeverity      string
	log              logger.Logger
	onIncident       func(*Incident) (string, error)
	validateProject  ProjectValidator

	// dedup: project_key:error_msg_hash -> last reported time
	dedup sync.Map
	// rate limit: project_key -> count in current window
	rateMu      sync.Mutex
	rateCount   map[string]*rateEntry
	stopCleanup chan struct{}
	stopOnce    sync.Once
}

type rateEntry struct {
	count    int
	windowAt time.Time
}

// HandlerConfig configures the intake handler.
type HandlerConfig struct {
	AuthToken   string
	DedupWindow time.Duration
	RateLimit   int
	MinSeverity string
}

// NewHandler creates an intake handler.
// The validateProject function is called to reject unknown project keys early.
// Call StopCleanup to release the background cleanup goroutine.
func NewHandler(cfg HandlerConfig, log logger.Logger, validateProject ProjectValidator, onIncident func(*Incident) (string, error)) *Handler {
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 10 * time.Minute
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 10
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "warning"
	}
	h := &Handler{
		authToken:       cfg.AuthToken,
		dedupWindow:     cfg.DedupWindow,
		rateLimit:       cfg.RateLimit,
		minSeverity:     cfg.MinSeverity,
		log:             log,
		onIncident:      onIncident,
		validateProject: validateProject,
		rateCount:       make(map[string]*rateEntry),
		stopCleanup:     make(chan struct{}),
	}
	go h.cleanupLoop()
	return h
}

// StopCleanup stops the background cleanup goroutine. Safe to call multiple times.
func (h *Handler) StopCleanup() {
	h.stopOnce.Do(func() { close(h.stopCleanup) })
}

// cleanupLoop periodically removes expired dedup and rate-limit entries.
func (h *Handler) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCleanup:
			return
		case <-ticker.C:
			now := time.Now()
			h.dedup.Range(func(key, value any) bool {
				if now.Sub(value.(time.Time)) >= h.dedupWindow {
					h.dedup.Delete(key)
				}
				return true
			})
			h.rateMu.Lock()
			for k, entry := range h.rateCount {
				if now.Sub(entry.windowAt) >= time.Hour {
					delete(h.rateCount, k)
				}
			}
			h.rateMu.Unlock()
		}
	}
}

// ServeHTTP handles POST /api/v1/incidents.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth check
	if h.authToken != "" {
		token := r.Header.Get("Authorization")
		if token != "Bearer "+h.authToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Limit request body to 1MB to prevent abuse
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	var inc Incident
	if err := json.NewDecoder(body).Decode(&inc); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate required fields
	if inc.ProjectKey == "" {
		http.Error(w, "project_key is required", http.StatusBadRequest)
		return
	}
	if inc.ErrorMsg == "" && inc.Title == "" {
		http.Error(w, "error_msg or title is required", http.StatusBadRequest)
		return
	}

	// Fill defaults
	if inc.ID == "" {
		inc.ID = "inc-" + uuid.New().String()[:8]
	}
	if inc.Severity == "" {
		inc.Severity = "warning"
	}
	if inc.Environment == "" {
		inc.Environment = "production"
	}
	if inc.Source == "" {
		inc.Source = "custom"
	}
	if inc.OccurredAt.IsZero() {
		inc.OccurredAt = time.Now()
	}
	inc.ReportedAt = time.Now()

	// Validate severity
	if !ValidSeverities[inc.Severity] {
		http.Error(w, fmt.Sprintf("invalid severity: %s (must be critical, warning, or info)", inc.Severity), http.StatusBadRequest)
		return
	}

	// Validate project key against registry (reject unknown projects early
	// to prevent dedup/rate-limit maps from being polluted by invalid keys)
	if h.validateProject != nil && !h.validateProject(inc.ProjectKey) {
		http.Error(w, fmt.Sprintf("unknown project: %s", inc.ProjectKey), http.StatusBadRequest)
		return
	}

	// Severity filter
	if !h.meetsMinSeverity(inc.Severity) {
		h.log.Info("incident.filtered_by_severity",
			logger.String("project", inc.ProjectKey),
			logger.String("severity", inc.Severity),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"incident_id": inc.ID,
			"status":      "filtered",
			"message":     fmt.Sprintf("severity %s is below minimum %s", inc.Severity, h.minSeverity),
		})
		return
	}

	// Dedup check — hash error_msg (or title as fallback) to keep key small
	dedupContent := inc.ErrorMsg
	if dedupContent == "" {
		dedupContent = inc.Title
	}
	dedupKey := inc.ProjectKey + ":" + hashString(dedupContent)
	if last, ok := h.dedup.Load(dedupKey); ok {
		if time.Since(last.(time.Time)) < h.dedupWindow {
			h.log.Info("incident.deduplicated",
				logger.String("project", inc.ProjectKey),
				logger.String("incident_id", inc.ID),
			)
			writeJSON(w, http.StatusOK, map[string]any{
				"incident_id": inc.ID,
				"status":      "deduplicated",
				"message":     "duplicate incident within dedup window",
			})
			return
		}
	}
	h.dedup.Store(dedupKey, time.Now())

	// Rate limit check
	if !h.checkRateLimit(inc.ProjectKey) {
		h.log.Warn("incident.rate_limited",
			logger.String("project", inc.ProjectKey),
			logger.String("incident_id", inc.ID),
		)
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"incident_id": inc.ID,
			"status":      "rate_limited",
			"message":     fmt.Sprintf("project %s exceeded %d diagnoses per hour", inc.ProjectKey, h.rateLimit),
		})
		return
	}

	h.log.Info("incident.received",
		logger.String("incident_id", inc.ID),
		logger.String("project", inc.ProjectKey),
		logger.String("severity", inc.Severity),
		logger.String("title", inc.Title),
	)

	// Dispatch to diagnosis pipeline
	taskID, submitErr := h.onIncident(&inc)
	if submitErr != nil {
		h.log.Error("incident.submit_failed",
			logger.String("incident_id", inc.ID),
			logger.Err(submitErr),
		)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"incident_id": inc.ID,
			"status":      "error",
			"message":     "故障受理失败: " + submitErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"incident_id": inc.ID,
		"task_id":     taskID,
		"status":      "queued",
		"message":     "故障已受理，正在排队等待诊断",
	})
}

func (h *Handler) meetsMinSeverity(severity string) bool {
	return SeverityPriority(severity) >= SeverityPriority(h.minSeverity)
}

func (h *Handler) checkRateLimit(projectKey string) bool {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()

	now := time.Now()
	entry, ok := h.rateCount[projectKey]
	if !ok || now.Sub(entry.windowAt) >= time.Hour {
		h.rateCount[projectKey] = &rateEntry{count: 1, windowAt: now}
		return true
	}
	if entry.count >= h.rateLimit {
		return false
	}
	entry.count++
	return true
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
