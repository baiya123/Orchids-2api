package tokencache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type Cache interface {
	Get(ctx context.Context, key string) (int, bool)
	Put(ctx context.Context, key string, tokens int)
	GetStats(ctx context.Context) (int64, int64, error)
	Clear(ctx context.Context) error
	SetTTL(ttl time.Duration)
}

type MemoryCache struct {
	mu         sync.RWMutex
	ttl        time.Duration
	maxEntries int
	items      map[string]cacheItem
	sizeBytes  int64
	done       chan struct{}
}

type cacheItem struct {
	tokens    int
	expiresAt time.Time
	size      int64
}

func NewMemoryCache(ttl time.Duration, maxEntries ...int) *MemoryCache {
	if ttl < 0 {
		ttl = 0
	}
	max := 0
	if len(maxEntries) > 0 && maxEntries[0] > 0 {
		max = maxEntries[0]
	}
	c := &MemoryCache{
		ttl:        ttl,
		maxEntries: max,
		items:      make(map[string]cacheItem),
		done:       make(chan struct{}),
	}
	// Start background cleanup
	go c.cleanupLoop()
	return c
}

func CacheKey(strategy, model, text string) string {
	useModel := normalizeStrategy(strategy) == "split"
	hasher := sha256.New()
	if useModel {
		model = strings.ToLower(strings.TrimSpace(model))
		hasher.Write([]byte(model))
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (c *MemoryCache) SetTTL(ttl time.Duration) {
	if c == nil {
		return
	}
	if ttl < 0 {
		ttl = 0
	}
	c.mu.Lock()
	if c.ttl != ttl {
		c.ttl = ttl
		c.items = make(map[string]cacheItem)
		c.sizeBytes = 0
	}
	c.mu.Unlock()
}

func (c *MemoryCache) Get(ctx context.Context, key string) (int, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.RLock()
	item, ok := c.items[key]
	if !ok {
		c.mu.RUnlock()
		return 0, false
	}
	if c.ttl > 0 && !item.expiresAt.IsZero() && time.Now().After(item.expiresAt) {
		c.mu.RUnlock()
		c.mu.Lock()
		if current, ok := c.items[key]; ok {
			if c.ttl > 0 && !current.expiresAt.IsZero() && time.Now().After(current.expiresAt) {
				c.sizeBytes -= current.size
				delete(c.items, key)
			}
		}
		c.mu.Unlock()
		return 0, false
	}
	c.mu.RUnlock()
	return item.tokens, true
}

func (c *MemoryCache) Put(ctx context.Context, key string, tokens int) {
	if c == nil {
		return
	}
	expiresAt := time.Time{}
	if c.ttl > 0 {
		expiresAt = time.Now().Add(c.ttl)
	}
	size := int64(len(key)) + 8
	c.mu.Lock()
	if existing, ok := c.items[key]; ok {
		c.sizeBytes -= existing.size
	} else if c.maxEntries > 0 && len(c.items) >= c.maxEntries {
		c.evictOldestLocked()
	}
	c.items[key] = cacheItem{
		tokens:    tokens,
		expiresAt: expiresAt,
		size:      size,
	}
	c.sizeBytes += size
	c.mu.Unlock()
}

func (c *MemoryCache) evictOldestLocked() {
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, item := range c.items {
		if first || item.expiresAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = item.expiresAt
			first = false
		}
	}
	if !first {
		c.sizeBytes -= c.items[oldestKey].size
		delete(c.items, oldestKey)
	}
}

func (c *MemoryCache) GetStats(ctx context.Context) (int64, int64, error) {
	if c == nil {
		return 0, 0, nil
	}
	c.mu.Lock()
	c.pruneExpiredLocked(time.Now())
	count := int64(len(c.items))
	size := c.sizeBytes
	c.mu.Unlock()
	return count, size, nil
}

func (c *MemoryCache) Clear(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.items = make(map[string]cacheItem)
	c.sizeBytes = 0
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) pruneExpiredLocked(now time.Time) {
	if c.ttl <= 0 {
		return
	}
	for key, item := range c.items {
		if !item.expiresAt.IsZero() && now.After(item.expiresAt) {
			c.sizeBytes -= item.size
			delete(c.items, key)
		}
	}
}

func normalizeStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	switch strategy {
	case "split":
		return "split"
	case "mix", "mixed":
		return "mix"
	default:
		return "mix"
	}
}
