// Package reliability provides circuit breaker and retry utilities for upstream calls.
package reliability

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/sony/gobreaker"
)

// Common errors
var (
	ErrCircuitOpen     = errors.New("circuit breaker is open")
	ErrMaxRetries      = errors.New("max retries exceeded")
	ErrContextCanceled = errors.New("context canceled")
)

// CircuitBreaker wraps gobreaker with sensible defaults for API calls.
type CircuitBreaker struct {
	cb *gobreaker.CircuitBreaker
}

// CircuitBreakerConfig configures the circuit breaker.
type CircuitBreakerConfig struct {
	Name         string
	MaxRequests  uint32        // Requests allowed in half-open state
	Interval     time.Duration // Cyclic period for clearing counters
	Timeout      time.Duration // Time to wait before half-open
	FailureRatio float64       // Ratio of failures to trip
	MinRequests  uint32        // Min requests before evaluating ratio
}

// DefaultCircuitConfig returns sensible defaults.
func DefaultCircuitConfig(name string) CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Name:         name,
		MaxRequests:  3,
		Interval:     60 * time.Second,
		Timeout:      30 * time.Second,
		FailureRatio: 0.5,
		MinRequests:  5,
	}
}

// NewCircuitBreaker creates a circuit breaker with the given config.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	settings := gobreaker.Settings{
		Name:        cfg.Name,
		MaxRequests: cfg.MaxRequests,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < cfg.MinRequests {
				return false
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= cfg.FailureRatio
		},
	}
	return &CircuitBreaker{
		cb: gobreaker.NewCircuitBreaker(settings),
	}
}

// Execute runs the given function through the circuit breaker.
func (c *CircuitBreaker) Execute(fn func() (interface{}, error)) (interface{}, error) {
	return c.cb.Execute(fn)
}

// State returns the current state of the circuit breaker.
func (c *CircuitBreaker) State() gobreaker.State {
	return c.cb.State()
}

// RetryConfig configures retry behavior.
type RetryConfig struct {
	MaxRetries     int
	InitialDelay   time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	Jitter         float64 // 0.0 to 1.0
	RetryableCheck func(error) bool
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:   3,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     10 * time.Second,
		Multiplier:   2.0,
		Jitter:       0.1,
		RetryableCheck: func(err error) bool {
			return err != nil && !errors.Is(err, context.Canceled)
		},
	}
}

// Retry executes fn with exponential backoff.
func Retry(ctx context.Context, cfg RetryConfig, fn func() error) error {
	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return ErrContextCanceled
		}

		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if !cfg.RetryableCheck(lastErr) {
			return lastErr
		}

		if attempt == cfg.MaxRetries {
			break
		}

		// Calculate delay with jitter
		jitter := 1.0 + (rand.Float64()*2-1)*cfg.Jitter
		actualDelay := time.Duration(float64(delay) * jitter)
		if actualDelay > cfg.MaxDelay {
			actualDelay = cfg.MaxDelay
		}

		select {
		case <-ctx.Done():
			return ErrContextCanceled
		case <-time.After(actualDelay):
		}

		delay = time.Duration(float64(delay) * cfg.Multiplier)
	}

	return ErrMaxRetries
}

// RetryWithResult executes fn with exponential backoff and returns a result.
func RetryWithResult[T any](ctx context.Context, cfg RetryConfig, fn func() (T, error)) (T, error) {
	var result T
	var lastErr error
	delay := cfg.InitialDelay

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return result, ErrContextCanceled
		}

		result, lastErr = fn()
		if lastErr == nil {
			return result, nil
		}

		if !cfg.RetryableCheck(lastErr) {
			return result, lastErr
		}

		if attempt == cfg.MaxRetries {
			break
		}

		jitter := 1.0 + (rand.Float64()*2-1)*cfg.Jitter
		actualDelay := time.Duration(float64(delay) * jitter)
		if actualDelay > cfg.MaxDelay {
			actualDelay = cfg.MaxDelay
		}

		select {
		case <-ctx.Done():
			return result, ErrContextCanceled
		case <-time.After(actualDelay):
		}

		delay = time.Duration(float64(delay) * cfg.Multiplier)
	}

	return result, ErrMaxRetries
}
