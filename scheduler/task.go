package scheduler

import (
	"container/heap"
	"sync"
	"time"

	"amp-sentinel/intake"
)

// TaskStatus represents the lifecycle state of a diagnosis task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
	StatusTimeout   TaskStatus = "timeout"
)

// Task wraps an incident as a schedulable diagnosis unit.
type Task struct {
	ID         string
	Event      *intake.RawEvent
	Priority   int
	Status     TaskStatus
	RetryCount int
	MaxRetries int
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Error      string

	index int // position in heap, managed by container/heap
}

// taskHeap implements heap.Interface for priority-based task scheduling.
// Higher Priority values are dequeued first (max-heap).
type taskHeap []*Task

func (h taskHeap) Len() int { return len(h) }

func (h taskHeap) Less(i, j int) bool {
	// Higher priority first; break ties by earlier creation time (FIFO within same priority)
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority
	}
	return h[i].CreatedAt.Before(h[j].CreatedAt)
}

func (h taskHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *taskHeap) Push(x any) {
	t := x.(*Task)
	t.index = len(*h)
	*h = append(*h, t)
}

func (h *taskHeap) Pop() any {
	old := *h
	n := len(old)
	t := old[n-1]
	old[n-1] = nil // avoid memory leak
	t.index = -1
	*h = old[:n-1]
	return t
}

// priorityQueue is a thread-safe priority queue for tasks.
type priorityQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	heap    taskHeap
	maxSize int
	closed  bool
}

func newPriorityQueue(maxSize int) *priorityQueue {
	pq := &priorityQueue{
		heap:    make(taskHeap, 0, maxSize),
		maxSize: maxSize,
	}
	pq.cond = sync.NewCond(&pq.mu)
	heap.Init(&pq.heap)
	return pq
}

// push adds a task to the queue. Returns false if the queue is full or closed.
func (pq *priorityQueue) push(t *Task) bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if pq.closed || pq.heap.Len() >= pq.maxSize {
		return false
	}
	heap.Push(&pq.heap, t)
	pq.cond.Signal()
	return true
}

// pop removes and returns the highest-priority task, blocking until one is available.
// Returns nil when the queue is closed and drained.
func (pq *priorityQueue) pop() *Task {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	for pq.heap.Len() == 0 && !pq.closed {
		pq.cond.Wait()
	}
	if pq.heap.Len() == 0 {
		return nil
	}
	return heap.Pop(&pq.heap).(*Task)
}

// close signals that no more tasks will be pushed.
func (pq *priorityQueue) close() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.closed = true
	pq.cond.Broadcast()
}

// len returns the current queue length.
func (pq *priorityQueue) len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.heap.Len()
}
