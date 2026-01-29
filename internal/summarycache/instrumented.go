package summarycache

import (
	"context"
	"orchids-api/internal/prompt"
)

type InstrumentedCache struct {
	cache prompt.SummaryCache
	stats *Stats
}

func NewInstrumentedCache(cache prompt.SummaryCache, stats *Stats) *InstrumentedCache {
	if cache == nil {
		return nil
	}
	return &InstrumentedCache{
		cache: cache,
		stats: stats,
	}
}

func (c *InstrumentedCache) Get(ctx context.Context, key string) (prompt.SummaryCacheEntry, bool) {
	if c == nil || c.cache == nil {
		return prompt.SummaryCacheEntry{}, false
	}
	entry, ok := c.cache.Get(ctx, key)
	if ok {
		if c.stats != nil {
			c.stats.Hit()
		}
		return entry, true
	}
	if c.stats != nil {
		c.stats.Miss()
	}
	return prompt.SummaryCacheEntry{}, false
}

func (c *InstrumentedCache) Put(ctx context.Context, key string, entry prompt.SummaryCacheEntry) {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.Put(ctx, key, entry)
}
