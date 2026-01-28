package loadbalancer

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"orchids-api/internal/store"
)

const defaultCacheTTL = 5 * time.Second

type LoadBalancer struct {
	store          *store.Store
	mu             sync.RWMutex
	cachedAccounts []*store.Account
	cacheExpires   time.Time
	cacheTTL       time.Duration
}

func New(s *store.Store) *LoadBalancer {
	return &LoadBalancer{
		store:    s,
		cacheTTL: defaultCacheTTL,
	}
}

func (lb *LoadBalancer) GetNextAccount() (*store.Account, error) {
	return lb.GetNextAccountExcluding(nil)
}

func (lb *LoadBalancer) GetNextAccountExcluding(excludeIDs []int64) (*store.Account, error) {
	accounts, err := lb.getEnabledAccounts()
	if err != nil {
		return nil, err
	}

	if len(excludeIDs) > 0 {
		excludeSet := make(map[int64]bool)
		for _, id := range excludeIDs {
			excludeSet[id] = true
		}
		var filtered []*store.Account
		for _, acc := range accounts {
			if !excludeSet[acc.ID] {
				filtered = append(filtered, acc)
			}
		}
		accounts = filtered
	}

	if len(accounts) == 0 {
		return nil, errors.New("no enabled accounts available")
	}

	account := lb.selectAccount(accounts)

	if err := lb.store.IncrementRequestCount(account.ID); err != nil {
		return nil, err
	}

	return account, nil
}

func (lb *LoadBalancer) getEnabledAccounts() ([]*store.Account, error) {
	now := time.Now()

	lb.mu.RLock()
	if len(lb.cachedAccounts) > 0 && now.Before(lb.cacheExpires) {
		accounts := make([]*store.Account, len(lb.cachedAccounts))
		copy(accounts, lb.cachedAccounts)
		lb.mu.RUnlock()
		return accounts, nil
	}
	lb.mu.RUnlock()

	accounts, err := lb.store.GetEnabledAccounts()
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

	var totalWeight int
	for _, acc := range accounts {
		totalWeight += acc.Weight
	}

	randomWeight := rand.Intn(totalWeight)
	currentWeight := 0

	for _, acc := range accounts {
		currentWeight += acc.Weight
		if currentWeight > randomWeight {
			return acc
		}
	}

	return accounts[0]
}
