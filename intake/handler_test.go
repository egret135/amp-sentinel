package intake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"amp-sentinel/logger"
)

func newTestHandler(cfg HandlerConfig) *Handler {
	return NewHandler(
		cfg,
		logger.Nop(),
		func(key string) bool { return key == "proj-a" },
		func(e *RawEvent) (string, error) { return "task-123", nil },
		func(projectKey string) *DedupConfig { return nil },
	)
}

func TestHandlerServeHTTP_StandardMode(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"test"},"severity":"warning"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if resp["event_id"] == nil || resp["event_id"] == "" {
		t.Error("expected event_id in response")
	}
	if resp["task_id"] != "task-123" {
		t.Errorf("expected task_id=task-123, got %v", resp["task_id"])
	}
	if resp["status"] != "queued" {
		t.Errorf("expected status=queued, got %v", resp["status"])
	}
}

func TestHandlerServeHTTP_SimpleMode(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	body := `{"error":"simple test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events?project=proj-a&severity=warning", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if resp["status"] != "queued" {
		t.Errorf("expected status=queued, got %v", resp["status"])
	}
}

func TestHandlerServeHTTP_MissingProjectKey(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	body := `{"payload":{"error":"test"},"severity":"warning"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerServeHTTP_InvalidSeverity(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"test"},"severity":"banana"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerServeHTTP_MethodNotAllowed(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandlerServeHTTP_AuthRequired(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100, AuthToken: "secret-token"})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"test"},"severity":"warning"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerServeHTTP_AuthPasses(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100, AuthToken: "secret-token"})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"test"},"severity":"warning"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlerServeHTTP_Dedup(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100, DedupWindow: 10 * time.Minute})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"dup-test"},"severity":"warning"}`

	// First request — should be accepted
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first request: expected 202, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Second request with same payload — should be deduplicated
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	var resp map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if resp["status"] != "deduplicated" {
		t.Errorf("expected status=deduplicated, got %v", resp["status"])
	}
}

func TestHandlerServeHTTP_RateLimit(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 2, DedupWindow: 0})
	defer h.StopCleanup()

	var gotTooMany bool
	for i := 0; i < 10; i++ {
		// Use different payloads to avoid dedup
		body := `{"project_key":"proj-a","payload":{"i":` + strings.Repeat("1", i+1) + `},"severity":"warning"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			gotTooMany = true
			break
		}
	}
	if !gotTooMany {
		t.Error("expected at least one 429 response after exceeding rate limit")
	}
}

func TestHandlerServeHTTP_SeverityFilter(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100, MinSeverity: "warning"})
	defer h.StopCleanup()

	body := `{"project_key":"proj-a","payload":{"error":"test"},"severity":"info"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}
	if resp["status"] != "filtered" {
		t.Errorf("expected status=filtered, got %v", resp["status"])
	}
}

func TestHandlerServeHTTP_UnknownProject(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	body := `{"project_key":"unknown-proj","payload":{"error":"test"},"severity":"warning"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestComputeFingerprint_SamePayload(t *testing.T) {
	payload1 := json.RawMessage(`{"error":"connection refused"}`)
	payload2 := json.RawMessage(`{"error":"connection refused"}`)
	payloadDiff := json.RawMessage(`{"error":"timeout"}`)

	defaultFields := []string{"error_msg", "error", "message", "msg"}

	fp1 := ComputeFingerprint("proj-a", payload1, nil, defaultFields)
	fp2 := ComputeFingerprint("proj-a", payload2, nil, defaultFields)
	fpDiff := ComputeFingerprint("proj-a", payloadDiff, nil, defaultFields)

	if fp1 != fp2 {
		t.Errorf("same payload should produce same fingerprint: %q vs %q", fp1, fp2)
	}
	if fp1 == fpDiff {
		t.Errorf("different payload should produce different fingerprint: both %q", fp1)
	}
}

func TestComputeFingerprint_WithProjectDedupConfig(t *testing.T) {
	payload := json.RawMessage(`{"error":"connection refused","service":"api","host":"web-01"}`)
	defaultFields := []string{"error_msg", "error", "message", "msg"}

	fpDefault := ComputeFingerprint("proj-a", payload, nil, defaultFields)
	fpCustom := ComputeFingerprint("proj-a", payload, &DedupConfig{
		Fields: []string{"service", "host"},
	}, defaultFields)

	if fpDefault == fpCustom {
		t.Errorf("custom dedup fields should produce different fingerprint from default: both %q", fpDefault)
	}
}

func TestFillDefaults(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	event := &RawEvent{
		ProjectKey: "proj-a",
		Payload:    json.RawMessage(`{"error":"test"}`),
	}

	h.fillDefaults(event)

	if event.ID == "" {
		t.Error("expected ID to be filled")
	}
	if !strings.HasPrefix(event.ID, "evt-") {
		t.Errorf("expected ID to start with evt-, got %s", event.ID)
	}
	if event.Severity != "warning" {
		t.Errorf("expected default severity=warning, got %s", event.Severity)
	}
	if event.Source != "custom" {
		t.Errorf("expected default source=custom, got %s", event.Source)
	}
	if event.ReceivedAt.IsZero() {
		t.Error("expected ReceivedAt to be filled")
	}
}

func TestServeBatch(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 100})
	defer h.StopCleanup()

	ndjson := `{"error":"line1"}
{"error":"line2"}
{"error":"line3"}`

	req := httptest.NewRequest(http.MethodPost, "/api/v1/events/batch?project=proj-a&severity=warning", strings.NewReader(ndjson))
	rec := httptest.NewRecorder()

	h.ServeBatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid json response: %v", err)
	}

	total, _ := resp["total"].(float64)
	if total != 3 {
		t.Errorf("expected total=3, got %v", resp["total"])
	}

	results, ok := resp["results"].([]any)
	if !ok {
		t.Fatalf("expected results array, got %T", resp["results"])
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// Check that at least the first result is queued (others may be deduped)
	first := results[0].(map[string]any)
	if first["status"] != "queued" {
		t.Errorf("expected first result status=queued, got %v", first["status"])
	}
}

func TestCheckDedupWithWindow_ConcurrentSafety(t *testing.T) {
	h := newTestHandler(HandlerConfig{RateLimit: 1000, DedupWindow: 10 * time.Minute})
	defer h.StopCleanup()

	const goroutines = 50
	key := "test-dedup-key"
	window := 10 * time.Minute

	var allowedCount int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if h.checkDedupWithWindow(key, window) {
				mu.Lock()
				allowedCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowedCount != 1 {
		t.Errorf("expected exactly 1 goroutine allowed through dedup, got %d", allowedCount)
	}
}
