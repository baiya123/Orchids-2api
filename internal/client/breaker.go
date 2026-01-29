// Package client provides upstream client with circuit breaker integration.
package client

import (
	"sync"

	"orchids-api/internal/reliability"
)

// upstreamBreakers holds circuit breakers per account.
var upstreamBreakers = struct {
	sync.RWMutex
	breakers map[string]*reliability.CircuitBreaker
}{
	breakers: make(map[string]*reliability.CircuitBreaker),
}

// GetAccountBreaker returns or creates a circuit breaker for the given account.
func GetAccountBreaker(accountName string) *reliability.CircuitBreaker {
	upstreamBreakers.RLock()
	if cb, ok := upstreamBreakers.breakers[accountName]; ok {
		upstreamBreakers.RUnlock()
		return cb
	}
	upstreamBreakers.RUnlock()

	upstreamBreakers.Lock()
	defer upstreamBreakers.Unlock()

	// Double-check after acquiring write lock
	if cb, ok := upstreamBreakers.breakers[accountName]; ok {
		return cb
	}

	cfg := reliability.DefaultCircuitConfig("upstream-" + accountName)
	cb := reliability.NewCircuitBreaker(cfg)
	upstreamBreakers.breakers[accountName] = cb
	return cb
}

// IsCircuitOpen checks if the circuit breaker for an account is open.
func IsCircuitOpen(accountName string) bool {
	upstreamBreakers.RLock()
	cb, ok := upstreamBreakers.breakers[accountName]
	upstreamBreakers.RUnlock()
	if !ok {
		return false
	}
	return cb.State() == 1 // StateOpen = 1
}

// GetBreakerStats returns stats for all breakers.
func GetBreakerStats() map[string]string {
	upstreamBreakers.RLock()
	defer upstreamBreakers.RUnlock()

	stats := make(map[string]string, len(upstreamBreakers.breakers))
	for name, cb := range upstreamBreakers.breakers {
		state := "closed"
		switch cb.State() {
		case 1:
			state = "open"
		case 2:
			state = "half-open"
		}
		stats[name] = state
	}
	return stats
}
