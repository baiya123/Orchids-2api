package middleware

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

// ConcurrencyLimiter limits concurrent request processing using a weighted semaphore.
// This is more efficient than channel-based semaphore for high-throughput scenarios.
type ConcurrencyLimiter struct {
	sem           *semaphore.Weighted
	maxConcurrent int64
	timeout       time.Duration
	activeCount   int64
	totalReqs     int64
	rejectedReqs  int64
}

// NewConcurrencyLimiter creates a new limiter with the specified max concurrent requests and timeout.
func NewConcurrencyLimiter(maxConcurrent int, timeout time.Duration) *ConcurrencyLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 100
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &ConcurrencyLimiter{
		sem:           semaphore.NewWeighted(int64(maxConcurrent)),
		maxConcurrent: int64(maxConcurrent),
		timeout:       timeout,
	}
}

// Limit wraps a handler with concurrency limiting.
func (cl *ConcurrencyLimiter) Limit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&cl.totalReqs, 1)

		ctx, cancel := context.WithTimeout(r.Context(), cl.timeout)
		defer cancel()

		// Try to acquire semaphore with timeout
		if err := cl.sem.Acquire(ctx, 1); err != nil {
			atomic.AddInt64(&cl.rejectedReqs, 1)
			http.Error(w, "Request timeout or server busy", http.StatusServiceUnavailable)
			return
		}

		atomic.AddInt64(&cl.activeCount, 1)
		defer func() {
			cl.sem.Release(1)
			atomic.AddInt64(&cl.activeCount, -1)
		}()

		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// Stats returns current limiter statistics.
func (cl *ConcurrencyLimiter) Stats() (active, total, rejected int64) {
	return atomic.LoadInt64(&cl.activeCount),
		atomic.LoadInt64(&cl.totalReqs),
		atomic.LoadInt64(&cl.rejectedReqs)
}

// TryAcquire attempts to acquire the semaphore without blocking.
// Returns true if acquired, false otherwise.
func (cl *ConcurrencyLimiter) TryAcquire() bool {
	return cl.sem.TryAcquire(1)
}

// Release releases one slot in the semaphore.
func (cl *ConcurrencyLimiter) Release() {
	cl.sem.Release(1)
}
