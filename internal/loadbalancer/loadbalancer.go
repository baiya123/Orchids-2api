package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"orchids-api/internal/auth"
	"orchids-api/internal/store"
)

const defaultCacheTTL = 5 * time.Second

type LoadBalancer struct {
	Store          *store.Store
	mu             sync.RWMutex
	cachedAccounts []*store.Account
	cacheExpires   time.Time
	cacheTTL       time.Duration
	activeConns    sync.Map // map[int64]*atomic.Int64
}

func New(s *store.Store) *LoadBalancer {
	return NewWithCacheTTL(s, defaultCacheTTL)
}

func NewWithCacheTTL(s *store.Store, cacheTTL time.Duration) *LoadBalancer {
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	return &LoadBalancer{
		Store:    s,
		cacheTTL: cacheTTL,
	}
}

func (lb *LoadBalancer) GetModelChannel(ctx context.Context, modelID string) string {
	if lb.Store == nil {
		return ""
	}
	m, err := lb.Store.GetModelByModelID(ctx, modelID)
	if err != nil || m == nil {
		return ""
	}
	return m.Channel
}

func (lb *LoadBalancer) GetNextAccount(ctx context.Context) (*store.Account, error) {
	return lb.GetNextAccountExcludingByChannel(ctx, nil, "")
}

func (lb *LoadBalancer) GetNextAccountByChannel(ctx context.Context, channel string) (*store.Account, error) {
	return lb.GetNextAccountExcludingByChannel(ctx, nil, channel)
}

func (lb *LoadBalancer) GetNextAccountExcluding(ctx context.Context, excludeIDs []int64) (*store.Account, error) {
	return lb.GetNextAccountExcludingByChannel(ctx, excludeIDs, "")
}

func (lb *LoadBalancer) GetNextAccountExcludingByChannel(ctx context.Context, excludeIDs []int64, channel string) (*store.Account, error) {
	accounts, err := lb.getEnabledAccounts(ctx)
	if err != nil {
		return nil, err
	}

	var filtered []*store.Account
	excludeSet := make(map[int64]bool)
	for _, id := range excludeIDs {
		excludeSet[id] = true
	}

	for _, acc := range accounts {
		if excludeSet[acc.ID] {
			continue
		}
		if channel != "" && !strings.EqualFold(acc.AgentMode, channel) {
			continue
		}
		filtered = append(filtered, acc)
	}
	accounts = filtered

	if len(accounts) == 0 {
		return nil, fmt.Errorf("no enabled accounts available for channel: %s", channel)
	}

	account := lb.selectAccount(accounts)

	slog.Info("Selected account", "name", account.Name, "email", account.Email, "session", auth.MaskSensitive(account.SessionID))

	if err := lb.Store.IncrementRequestCount(ctx, account.ID); err != nil {
		return nil, err
	}

	return account, nil
}

func (lb *LoadBalancer) getEnabledAccounts(ctx context.Context) ([]*store.Account, error) {
	now := time.Now()

	lb.mu.RLock()
	if len(lb.cachedAccounts) > 0 && now.Before(lb.cacheExpires) {
		accounts := make([]*store.Account, len(lb.cachedAccounts))
		copy(accounts, lb.cachedAccounts)
		lb.mu.RUnlock()
		return accounts, nil
	}
	lb.mu.RUnlock()

	accounts, err := lb.Store.GetEnabledAccounts(ctx)
	if err != nil {
		return nil, err
	}

	lb.mu.Lock()
	lb.cachedAccounts = accounts
	lb.cacheExpires = now.Add(lb.cacheTTL)
	lb.mu.Unlock()

	cached := make([]*store.Account, len(accounts))
	copy(cached, accounts)
	return cached, nil
}

func (lb *LoadBalancer) selectAccount(accounts []*store.Account) *store.Account {
	if len(accounts) == 1 {
		return accounts[0]
	}

	var bestAccount *store.Account
	minScore := float64(-1)

	for _, acc := range accounts {
		weight := acc.Weight
		if weight <= 0 {
			weight = 1
		}

		var conns int64
		if val, ok := lb.activeConns.Load(acc.ID); ok {
			conns = val.(*atomic.Int64).Load()
		}
		score := float64(conns) / float64(weight)

		if bestAccount == nil || score < minScore {
			bestAccount = acc
			minScore = score
		}
	}

	if bestAccount != nil {
		return bestAccount
	}
	return accounts[0]
}

func (lb *LoadBalancer) GetStats() map[int64]int {
	stats := make(map[int64]int)
	lb.activeConns.Range(func(key, value interface{}) bool {
		stats[key.(int64)] = int(value.(*atomic.Int64).Load())
		return true
	})
	return stats
}

func (lb *LoadBalancer) AcquireConnection(accountID int64) {
	val, _ := lb.activeConns.LoadOrStore(accountID, &atomic.Int64{})
	val.(*atomic.Int64).Add(1)
}

func (lb *LoadBalancer) ReleaseConnection(accountID int64) {
	if val, ok := lb.activeConns.Load(accountID); ok {
		counter := val.(*atomic.Int64)
		for {
			current := counter.Load()
			if current <= 0 {
				break
			}
			if counter.CompareAndSwap(current, current-1) {
				break
			}
		}
	}
}
