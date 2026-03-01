package intake

import (
	"bufio"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"amp-sentinel/logger"

	"github.com/google/uuid"
)

// ProjectValidator checks whether a project key is registered.
type ProjectValidator func(key string) bool

// Handler handles incoming event reports via HTTP.
type Handler struct {
	authToken       string
	dedupWindow     time.Duration
	dedupFields     []string
	rateLimit       int
	minSeverity     string
	maxPayloadSize  int
	log             logger.Logger
	onEvent         func(*RawEvent) (string, error)
	validateProject ProjectValidator
	dedupConfig     func(projectKey string) *DedupConfig

	// dedup: fingerprint -> last reported time
	dedup     sync.Map
	dedupSize atomic.Int64 // approximate count for OOM protection
	// rate limit: sharded by project key to avoid global mutex
	rateShards  [rateLimitShards]rateShard
	stopCleanup chan struct{}
	stopOnce    sync.Once
}

const rateLimitShards = 64

type rateShard struct {
	mu    sync.Mutex
	count map[string]*rateEntry
}

// maxDedupEntries is a safety cap to prevent OOM under high-cardinality traffic.
const maxDedupEntries = 500_000

// DedupConfig holds per-project deduplication settings.
type DedupConfig struct {
	Fields []string
	Window time.Duration
}

type rateEntry struct {
	count    int
	windowAt time.Time
}

type dedupEntry struct {
	at     time.Time
	window time.Duration
}

// HandlerConfig configures the intake handler.
type HandlerConfig struct {
	AuthToken      string
	DedupWindow    time.Duration
	DedupFields    []string
	RateLimit      int
	MinSeverity    string
	MaxPayloadSize int
}

// NewHandler creates an intake handler.
// Call StopCleanup to release the background cleanup goroutine.
func NewHandler(
	cfg HandlerConfig,
	log logger.Logger,
	validateProject ProjectValidator,
	onEvent func(*RawEvent) (string, error),
	dedupConfig func(projectKey string) *DedupConfig,
) *Handler {
	if cfg.DedupWindow == 0 {
		cfg.DedupWindow = 10 * time.Minute
	}
	if cfg.RateLimit == 0 {
		cfg.RateLimit = 10
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "warning"
	}
	if cfg.MaxPayloadSize == 0 {
		cfg.MaxPayloadSize = 65536
	}
	if len(cfg.DedupFields) == 0 {
		cfg.DedupFields = []string{"error_msg", "error", "message", "msg"}
	}
	h := &Handler{
		authToken:       cfg.AuthToken,
		dedupWindow:     cfg.DedupWindow,
		dedupFields:     cfg.DedupFields,
		rateLimit:       cfg.RateLimit,
		minSeverity:     cfg.MinSeverity,
		maxPayloadSize:  cfg.MaxPayloadSize,
		log:             log,
		onEvent:         onEvent,
		validateProject: validateProject,
		dedupConfig:     dedupConfig,
		stopCleanup:     make(chan struct{}),
	}
	for i := range h.rateShards {
		h.rateShards[i].count = make(map[string]*rateEntry)
	}
	go h.cleanupLoop()
	return h
}

// StopCleanup stops the background cleanup goroutine. Safe to call multiple times.
func (h *Handler) StopCleanup() {
	h.stopOnce.Do(func() { close(h.stopCleanup) })
}

func (h *Handler) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-h.stopCleanup:
			return
		case <-ticker.C:
			now := time.Now()
			var deleted int64
			h.dedup.Range(func(key, value any) bool {
				entry := value.(dedupEntry)
				if now.Sub(entry.at) >= entry.window {
					h.dedup.Delete(key)
					deleted++
				}
				return true
			})
			if deleted > 0 {
				h.dedupSize.Add(-deleted)
			}
			for i := range h.rateShards {
				shard := &h.rateShards[i]
				shard.mu.Lock()
				for k, entry := range shard.count {
					if now.Sub(entry.windowAt) >= time.Hour {
						delete(shard.count, k)
					}
				}
				shard.mu.Unlock()
			}
		}
	}
}

// ServeHTTP handles POST /api/v1/events (standard and simple modes).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.checkAuth(w, r) {
		return
	}

	body := http.MaxBytesReader(w, r.Body, 1<<20)
	rawBody, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	q := r.URL.Query()
	projectFromQuery := q.Get("project")
	severityFromQuery := q.Get("severity")

	var event *RawEvent

	if projectFromQuery != "" {
		// Simple mode: query params provide envelope, body = payload
		event = &RawEvent{
			ProjectKey: projectFromQuery,
			Severity:   severityFromQuery,
			Payload:    json.RawMessage(rawBody),
			Source:     "custom",
		}
	} else {
		// Standard mode: body contains {project_key, payload, ...}
		var envelope struct {
			ProjectKey string          `json:"project_key"`
			Payload    json.RawMessage `json:"payload"`
			Source     string          `json:"source"`
			Severity   string          `json:"severity"`
			Title      string          `json:"title"`
		}
		if err := json.Unmarshal(rawBody, &envelope); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		event = &RawEvent{
			ProjectKey: envelope.ProjectKey,
			Payload:    envelope.Payload,
			Source:     envelope.Source,
			Severity:   envelope.Severity,
			Title:      envelope.Title,
		}
		if len(event.Payload) == 0 {
			http.Error(w, "payload is required", http.StatusBadRequest)
			return
		}
	}

	h.processEvent(w, event)
}

// ServeBatch handles POST /api/v1/events/batch (NDJSON).
func (h *Handler) ServeBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.checkAuth(w, r) {
		return
	}

	q := r.URL.Query()
	projectKey := q.Get("project")
	if projectKey == "" {
		http.Error(w, "project query parameter is required for batch", http.StatusBadRequest)
		return
	}
	severity := q.Get("severity")

	body := http.MaxBytesReader(w, r.Body, 10<<20) // 10MB for batch
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1<<20)

	var results []map[string]any
	lineNum := 0
	accepted := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		if !json.Valid(line) {
			results = append(results, map[string]any{
				"line":   lineNum,
				"status": "error",
				"error":  "invalid json",
			})
			continue
		}

		if h.maxPayloadSize > 0 && len(line) > h.maxPayloadSize {
			results = append(results, map[string]any{
				"line":   lineNum,
				"status": "error",
				"error":  fmt.Sprintf("payload too large: %d bytes (max %d)", len(line), h.maxPayloadSize),
			})
			continue
		}

		event := &RawEvent{
			ProjectKey: projectKey,
			Severity:   severity,
			Payload:    json.RawMessage(append([]byte(nil), line...)),
			Source:     "batch",
		}
		h.fillDefaults(event)

		if !ValidSeverities[event.Severity] {
			results = append(results, map[string]any{
				"line":     lineNum,
				"event_id": event.ID,
				"status":   "error",
				"error":    fmt.Sprintf("invalid severity: %s", event.Severity),
			})
			continue
		}

		if !h.meetsMinSeverity(event.Severity) {
			results = append(results, map[string]any{
				"line":     lineNum,
				"event_id": event.ID,
				"status":   "filtered",
				"message":  fmt.Sprintf("severity %s is below minimum %s", event.Severity, h.minSeverity),
			})
			continue
		}

		if event.Title == "" {
			event.Title = ExtractTitle(event.Payload)
		}

		if h.validateProject != nil && !h.validateProject(event.ProjectKey) {
			results = append(results, map[string]any{
				"line":     lineNum,
				"event_id": event.ID,
				"status":   "error",
				"error":    fmt.Sprintf("unknown project: %s", event.ProjectKey),
			})
			continue
		}

		taskID, submitErr := h.submitEvent(event)
		if submitErr != nil {
			results = append(results, map[string]any{
				"line":     lineNum,
				"event_id": event.ID,
				"status":   "error",
				"error":    submitErr.Error(),
			})
			continue
		}

		accepted++
		results = append(results, map[string]any{
			"line":     lineNum,
			"event_id": event.ID,
			"task_id":  taskID,
			"status":   "queued",
		})
	}

	if scanErr := scanner.Err(); scanErr != nil {
		results = append(results, map[string]any{
			"line":   lineNum + 1,
			"status": "error",
			"error":  "read error: " + scanErr.Error(),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"total":    lineNum,
		"accepted": accepted,
		"results":  results,
	})
}

// ServeIncidentCompat handles POST /api/v1/incidents (legacy compat).
// Response uses "incident_id" key for backward compatibility.
func (h *Handler) ServeIncidentCompat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !h.checkAuth(w, r) {
		return
	}

	body := http.MaxBytesReader(w, r.Body, 1<<20)
	rawBody, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var envelope struct {
		ProjectKey string `json:"project_key"`
		Severity   string `json:"severity"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	event := &RawEvent{
		ProjectKey: envelope.ProjectKey,
		Severity:   envelope.Severity,
		Title:      envelope.Title,
		Payload:    json.RawMessage(rawBody),
		Source:     "legacy",
	}

	h.processLegacyEvent(w, event)
}

// processLegacyEvent is like processEvent but returns "incident_id" in responses
// for backward compatibility with old API callers.
func (h *Handler) processLegacyEvent(w http.ResponseWriter, event *RawEvent) {
	h.fillDefaults(event)

	if event.ProjectKey == "" {
		http.Error(w, "project_key is required", http.StatusBadRequest)
		return
	}
	if !ValidSeverities[event.Severity] {
		http.Error(w, fmt.Sprintf("invalid severity: %s (must be critical, warning, or info)", event.Severity), http.StatusBadRequest)
		return
	}
	// Validate payload size
	if h.maxPayloadSize > 0 && len(event.Payload) > h.maxPayloadSize {
		http.Error(w, fmt.Sprintf("payload too large: %d bytes (max %d)", len(event.Payload), h.maxPayloadSize), http.StatusRequestEntityTooLarge)
		return
	}
	if h.validateProject != nil && !h.validateProject(event.ProjectKey) {
		http.Error(w, fmt.Sprintf("unknown project: %s", event.ProjectKey), http.StatusBadRequest)
		return
	}
	if event.Title == "" {
		event.Title = ExtractTitle(event.Payload)
	}
	if !h.meetsMinSeverity(event.Severity) {
		writeJSON(w, http.StatusOK, map[string]any{
			"incident_id": event.ID,
			"status":      "filtered",
			"message":     fmt.Sprintf("severity %s is below minimum %s", event.Severity, h.minSeverity),
		})
		return
	}

	taskID, submitErr := h.submitEvent(event)
	if submitErr != nil {
		if strings.Contains(submitErr.Error(), "deduplicated") {
			writeJSON(w, http.StatusOK, map[string]any{
				"incident_id": event.ID,
				"status":      "deduplicated",
				"message":     submitErr.Error(),
			})
			return
		}
		status := http.StatusServiceUnavailable
		if strings.Contains(submitErr.Error(), "rate_limited") {
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, map[string]any{
			"incident_id": event.ID,
			"status":      "error",
			"message":     submitErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"incident_id": event.ID,
		"task_id":     taskID,
		"status":      "queued",
		"message":     "故障已受理，正在排队等待诊断",
	})
}

func (h *Handler) processEvent(w http.ResponseWriter, event *RawEvent) {
	// Fill defaults
	h.fillDefaults(event)

	// Validate required fields
	if event.ProjectKey == "" {
		http.Error(w, "project_key is required", http.StatusBadRequest)
		return
	}

	// Validate severity
	if !ValidSeverities[event.Severity] {
		http.Error(w, fmt.Sprintf("invalid severity: %s (must be critical, warning, or info)", event.Severity), http.StatusBadRequest)
		return
	}

	// Validate payload size
	if h.maxPayloadSize > 0 && len(event.Payload) > h.maxPayloadSize {
		http.Error(w, fmt.Sprintf("payload too large: %d bytes (max %d)", len(event.Payload), h.maxPayloadSize), http.StatusRequestEntityTooLarge)
		return
	}

	// Validate project key
	if h.validateProject != nil && !h.validateProject(event.ProjectKey) {
		http.Error(w, fmt.Sprintf("unknown project: %s", event.ProjectKey), http.StatusBadRequest)
		return
	}

	// Auto-extract title if empty
	if event.Title == "" {
		event.Title = ExtractTitle(event.Payload)
	}

	// Severity filter
	if !h.meetsMinSeverity(event.Severity) {
		h.log.Info("event.filtered_by_severity",
			logger.String("project", event.ProjectKey),
			logger.String("severity", event.Severity),
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"event_id": event.ID,
			"status":   "filtered",
			"message":  fmt.Sprintf("severity %s is below minimum %s", event.Severity, h.minSeverity),
		})
		return
	}

	taskID, submitErr := h.submitEvent(event)
	if submitErr != nil {
		status := http.StatusServiceUnavailable
		if strings.Contains(submitErr.Error(), "deduplicated") {
			status = http.StatusOK
			writeJSON(w, status, map[string]any{
				"event_id": event.ID,
				"status":   "deduplicated",
				"message":  submitErr.Error(),
			})
			return
		}
		if strings.Contains(submitErr.Error(), "rate_limited") {
			status = http.StatusTooManyRequests
		}
		writeJSON(w, status, map[string]any{
			"event_id": event.ID,
			"status":   "error",
			"message":  submitErr.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"event_id": event.ID,
		"task_id":  taskID,
		"status":   "queued",
		"message":  "事件已受理，正在排队等待诊断",
	})
}

func (h *Handler) submitEvent(event *RawEvent) (string, error) {
	// Dedup check
	var projDedup *DedupConfig
	if h.dedupConfig != nil {
		projDedup = h.dedupConfig(event.ProjectKey)
	}
	dedupKey := ComputeFingerprint(event.ProjectKey, event.Payload, projDedup, h.dedupFields)
	dedupWindow := h.dedupWindow
	if projDedup != nil && projDedup.Window > 0 {
		dedupWindow = projDedup.Window
	}
	if !h.checkDedupWithWindow(dedupKey, dedupWindow) {
		h.log.Info("event.deduplicated",
			logger.String("project", event.ProjectKey),
			logger.String("event_id", event.ID),
		)
		return "", fmt.Errorf("deduplicated: duplicate event within dedup window")
	}

	// Rate limit check
	if !h.checkRateLimit(event.ProjectKey) {
		h.log.Warn("event.rate_limited",
			logger.String("project", event.ProjectKey),
			logger.String("event_id", event.ID),
		)
		return "", fmt.Errorf("rate_limited: project %s exceeded %d diagnoses per hour", event.ProjectKey, h.rateLimit)
	}

	h.log.Info("event.received",
		logger.String("event_id", event.ID),
		logger.String("project", event.ProjectKey),
		logger.String("severity", event.Severity),
		logger.String("source", event.Source),
	)

	taskID, err := h.onEvent(event)
	if err != nil {
		h.log.Error("event.submit_failed",
			logger.String("event_id", event.ID),
			logger.Err(err),
		)
		return "", err
	}

	return taskID, nil
}

func (h *Handler) fillDefaults(event *RawEvent) {
	if event.ID == "" {
		event.ID = "evt-" + uuid.New().String()[:8]
	}
	if event.Severity == "" {
		event.Severity = "warning"
	}
	if event.Source == "" {
		event.Source = "custom"
	}
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = time.Now()
	}
}

func (h *Handler) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if h.authToken == "" {
		return true
	}
	token := r.Header.Get("Authorization")
	expected := "Bearer " + h.authToken
	if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func (h *Handler) meetsMinSeverity(severity string) bool {
	return SeverityPriority(severity) >= SeverityPriority(h.minSeverity)
}

func (h *Handler) checkDedupWithWindow(dedupKey string, window time.Duration) bool {
	now := time.Now()
	entry := dedupEntry{at: now, window: window}
	existing, loaded := h.dedup.LoadOrStore(dedupKey, entry)
	if !loaded {
		// New entry — check OOM safety cap
		if h.dedupSize.Add(1) > maxDedupEntries {
			// Over cap: accept the entry (already stored) but log once
			// The cleanup loop will bring it back down
		}
		return true // first occurrence — allow
	}
	prev := existing.(dedupEntry)
	if time.Since(prev.at) >= window {
		// Window expired — use CompareAndSwap so only one goroutine wins
		if h.dedup.CompareAndSwap(dedupKey, existing, entry) {
			return true
		}
		// Another goroutine already refreshed — this one is a duplicate
		return false
	}
	return false
}

func (h *Handler) checkRateLimit(projectKey string) bool {
	shard := &h.rateShards[shardIndex(projectKey)]
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()
	entry, ok := shard.count[projectKey]
	if !ok || now.Sub(entry.windowAt) >= time.Hour {
		shard.count[projectKey] = &rateEntry{count: 1, windowAt: now}
		return true
	}
	if entry.count >= h.rateLimit {
		return false
	}
	entry.count++
	return true
}

func shardIndex(key string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(key))
	return h.Sum32() % rateLimitShards
}

// ComputeFingerprint calculates a dedup fingerprint for the given event.
func ComputeFingerprint(projectKey string, payload json.RawMessage, cfg *DedupConfig, defaultFields []string) string {
	// Fast path: when using default top-level fields (no nested paths),
	// use incremental hashing to avoid full json.Unmarshal + json.Marshal overhead.
	if cfg == nil || len(cfg.Fields) == 0 {
		if fp, ok := fastFingerprint(projectKey, payload, defaultFields); ok {
			return fp
		}
	}

	// Slow path: full unmarshal for nested fields or fast path miss
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return projectKey + ":" + hashBytes(payload)
	}

	// Strategy 1: project-level explicit fields
	if cfg != nil && len(cfg.Fields) > 0 {
		fp, matched := hashFieldsIncremental(m, cfg.Fields)
		if matched > 0 {
			return projectKey + ":" + fp
		}
		return projectKey + ":" + hashBytes(payload)
	}

	// Strategy 2: global default fields (take up to 2 matching scalar fields)
	fp, matched := hashDefaultFieldsIncremental(m, defaultFields, 2)
	if matched > 0 {
		return projectKey + ":" + fp
	}

	// Strategy 3: hash entire payload
	return projectKey + ":" + hashBytes(payload)
}

// fastFingerprint extracts top-level scalar fields directly from JSON bytes
// using json.Decoder, avoiding a full map[string]any allocation.
// Returns ("", false) if the payload is not a simple JSON object or no fields match.
func fastFingerprint(projectKey string, payload json.RawMessage, fields []string) (string, bool) {
	// Only attempt fast path for top-level fields (no dots)
	for _, f := range fields {
		if strings.Contains(f, ".") {
			return "", false
		}
	}

	// Build a lookup set
	wanted := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		wanted[f] = struct{}{}
	}

	dec := json.NewDecoder(strings.NewReader(string(payload)))
	tok, err := dec.Token()
	if err != nil {
		return "", false
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return "", false
	}

	h := sha256.New()
	matched := 0
	maxMatch := 2

	for dec.More() && matched < maxMatch {
		// Read key
		keyTok, err := dec.Token()
		if err != nil {
			return "", false
		}
		key, ok := keyTok.(string)
		if !ok {
			return "", false
		}

		if _, want := wanted[key]; !want {
			// Skip value
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return "", false
			}
			continue
		}

		// Read value — only accept scalars
		valTok, err := dec.Token()
		if err != nil {
			return "", false
		}
		switch v := valTok.(type) {
		case string:
			h.Write([]byte(key))
			h.Write([]byte("="))
			h.Write([]byte(v))
			h.Write([]byte("|"))
			matched++
		case float64:
			h.Write([]byte(key))
			h.Write([]byte("="))
			h.Write([]byte(fmt.Sprintf("%g", v)))
			h.Write([]byte("|"))
			matched++
		case bool:
			h.Write([]byte(key))
			h.Write([]byte("="))
			if v {
				h.Write([]byte("true"))
			} else {
				h.Write([]byte("false"))
			}
			h.Write([]byte("|"))
			matched++
		default:
			// json.Delim (object/array start) — not a scalar, skip
			if _, isDelim := valTok.(json.Delim); isDelim {
				// Need to skip the entire nested value
				depth := 1
				for depth > 0 {
					t, err := dec.Token()
					if err != nil {
						return "", false
					}
					if d, ok := t.(json.Delim); ok {
						switch d {
						case '{', '[':
							depth++
						case '}', ']':
							depth--
						}
					}
				}
			}
		}
	}

	if matched == 0 {
		return "", false
	}
	return projectKey + ":" + hex.EncodeToString(h.Sum(nil)[:8]), true
}

// hashFieldsIncremental hashes fields directly into SHA256, avoiding
// intermediate string allocations from json.Marshal + strings.Join.
func hashFieldsIncremental(m map[string]any, fields []string) (string, int) {
	h := sha256.New()
	matched := 0
	for _, f := range fields {
		v := resolveField(m, f)
		if v == nil {
			continue
		}
		matched++
		h.Write([]byte(f))
		h.Write([]byte("="))
		writeScalarToHash(h, v)
		h.Write([]byte("|"))
	}
	if matched == 0 {
		return "", 0
	}
	return hex.EncodeToString(h.Sum(nil)[:8]), matched
}

// hashDefaultFieldsIncremental is like hashFieldsIncremental but only
// considers scalar values and stops after maxFields matches.
func hashDefaultFieldsIncremental(m map[string]any, defaultFields []string, maxFields int) (string, int) {
	h := sha256.New()
	matched := 0
	for _, field := range defaultFields {
		if matched >= maxFields {
			break
		}
		v := resolveField(m, field)
		if v == nil || !isScalar(v) {
			continue
		}
		matched++
		h.Write([]byte(field))
		h.Write([]byte("="))
		writeScalarToHash(h, v)
		h.Write([]byte("|"))
	}
	if matched == 0 {
		return "", 0
	}
	return hex.EncodeToString(h.Sum(nil)[:8]), matched
}

// writeScalarToHash writes a value's string representation directly to a hash writer.
func writeScalarToHash(h io.Writer, v any) {
	switch val := v.(type) {
	case string:
		h.Write([]byte(val))
	case float64:
		h.Write([]byte(fmt.Sprintf("%g", val)))
	case bool:
		if val {
			h.Write([]byte("true"))
		} else {
			h.Write([]byte("false"))
		}
	default:
		b, _ := json.Marshal(val)
		h.Write(b)
	}
}

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
