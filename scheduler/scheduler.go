package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"amp-sentinel/intake"
	"amp-sentinel/logger"

	"github.com/google/uuid"
)

// DiagnoseFunc is the function the scheduler calls to run a diagnosis.
// taskID is the scheduler-assigned task identifier for tracking.
type DiagnoseFunc func(ctx context.Context, taskID string, event *intake.RawEvent) error

// Config holds scheduler configuration.
type Config struct {
	MaxConcurrency int
	QueueSize      int
	DefaultTimeout time.Duration
	RetryCount     int
	RetryDelay     time.Duration
}

// Scheduler manages a pool of workers that process diagnosis tasks
// using a priority queue (critical > warning > info).
type Scheduler struct {
	cfg       Config
	diagnose  DiagnoseFunc
	log       logger.Logger
	pq        *priorityQueue
	wg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc
	stopMu    sync.Mutex // guards stopped + pq.close() atomically
	stopped   bool
	running   atomic.Int32
	completed atomic.Int64
	failed    atomic.Int64
}

// New creates a scheduler with the given config and diagnosis function.
func New(cfg Config, diagnose DiagnoseFunc, log logger.Logger) *Scheduler {
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 3
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 100
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 15 * time.Minute
	}
	if cfg.RetryCount < 0 {
		cfg.RetryCount = 2
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 10 * time.Second
	}
	return &Scheduler{
		cfg:      cfg,
		diagnose: diagnose,
		log:      log,
		pq:       newPriorityQueue(cfg.QueueSize),
	}
}

// Start launches the worker goroutines. Call Stop to shut down.
func (s *Scheduler) Start() {
	s.ctx, s.cancel = context.WithCancel(context.Background())

	s.log.Info("scheduler.started",
		logger.Int("max_concurrency", s.cfg.MaxConcurrency),
		logger.Int("queue_size", s.cfg.QueueSize),
	)

	for i := 0; i < s.cfg.MaxConcurrency; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
}

// Submit enqueues an incident for diagnosis. Returns the task ID.
// Tasks are dequeued in priority order (critical before warning before info).
func (s *Scheduler) Submit(event *intake.RawEvent) (string, error) {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()

	if s.stopped {
		return "", fmt.Errorf("scheduler is stopped, cannot accept new tasks")
	}

	task := &Task{
		ID:         "task-" + uuid.New().String()[:8],
		Event:      event,
		Priority:   intake.SeverityPriority(event.Severity),
		Status:     StatusPending,
		MaxRetries: s.cfg.RetryCount,
		CreatedAt:  time.Now(),
	}

	if !s.pq.push(task) {
		return "", fmt.Errorf("diagnosis queue is full (capacity: %d)", s.cfg.QueueSize)
	}

	s.log.Info("task.submitted",
		logger.String("task_id", task.ID),
		logger.String("incident_id", event.ID),
		logger.String("project", event.ProjectKey),
		logger.Int("priority", task.Priority),
	)
	return task.ID, nil
}

// Stop gracefully shuts down the scheduler, waiting for in-flight tasks.
func (s *Scheduler) Stop() {
	s.log.Info("scheduler.stopping")

	// 1. Atomically prevent new submissions and close the queue
	s.stopMu.Lock()
	s.stopped = true
	s.pq.close()
	s.stopMu.Unlock()

	// 2. Cancel in-flight work
	if s.cancel != nil {
		s.cancel()
	}

	// 3. Wait for all workers to finish
	s.wg.Wait()

	s.log.Info("scheduler.stopped")
}

// Stats returns current scheduler statistics.
func (s *Scheduler) Stats() map[string]any {
	return map[string]any{
		"queue_length": s.pq.len(),
		"running":      s.running.Load(),
		"completed":    s.completed.Load(),
		"failed":       s.failed.Load(),
	}
}

func (s *Scheduler) worker(id int) {
	defer s.wg.Done()

	for {
		task := s.pq.pop()
		if task == nil {
			// Queue is closed and drained
			return
		}
		s.running.Add(1)
		s.safeProcessTask(task)
		s.running.Add(-1)
	}
}

func (s *Scheduler) safeProcessTask(task *Task) {
	defer func() {
		if r := recover(); r != nil {
			task.Status = StatusFailed
			task.Error = fmt.Sprintf("panic: %v", r)
			task.FinishedAt = time.Now()
			s.failed.Add(1)
			s.log.Error("task.panic",
				logger.String("task_id", task.ID),
				logger.Any("panic", r),
			)
		}
	}()
	s.processTask(task)
}

func (s *Scheduler) processTask(task *Task) {
	log := s.log.WithFields(
		logger.String("task_id", task.ID),
		logger.String("incident_id", task.Event.ID),
		logger.String("project", task.Event.ProjectKey),
	)

	for attempt := 0; attempt <= task.MaxRetries; attempt++ {
		if attempt > 0 {
			log.Warn("task.retrying",
				logger.Int("attempt", attempt),
				logger.Int("max_retries", task.MaxRetries),
			)
			select {
			case <-s.ctx.Done():
				task.Status = StatusFailed
				task.Error = "scheduler shutting down"
				task.FinishedAt = time.Now()
				s.failed.Add(1)
				log.Warn("task.cancelled_during_retry")
				return
			case <-time.After(s.cfg.RetryDelay):
			}
		}

		task.Status = StatusRunning
		task.StartedAt = time.Now()

		taskCtx, cancel := context.WithTimeout(s.ctx, s.cfg.DefaultTimeout)
		err := s.diagnose(taskCtx, task.ID, task.Event)
		cancel()

		if err == nil {
			task.Status = StatusCompleted
			task.FinishedAt = time.Now()
			s.completed.Add(1)
			log.Info("task.completed",
				logger.Int64("duration_ms", time.Since(task.StartedAt).Milliseconds()),
			)
			return
		}

		task.Error = err.Error()
		task.RetryCount = attempt + 1

		if s.ctx.Err() != nil {
			task.Status = StatusFailed
			task.FinishedAt = time.Now()
			s.failed.Add(1)
			log.Warn("task.cancelled", logger.Err(err))
			return
		}

		log.Error("task.attempt_failed", logger.Int("attempt", attempt), logger.Err(err))
	}

	task.Status = StatusFailed
	task.FinishedAt = time.Now()
	s.failed.Add(1)
	log.Error("task.failed", logger.String("error", task.Error))
}
