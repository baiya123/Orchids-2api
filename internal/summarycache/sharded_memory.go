package summarycache

import (
	"container/list"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"orchids-api/internal/prompt"
)

// ShardedMemoryCache 是一个分片的内存缓存，用于减少锁竞争
type ShardedMemoryCache struct {
	shards     []*memoryShard
	shardCount int
	stopCh     chan struct{}
}

type memoryShard struct {
	mu          sync.RWMutex
	maxEntries  int
	ttl         time.Duration
	ll          *list.List
	items       map[string]*list.Element
	accessCount uint64 // 原子计数器，用于概率性 LRU 更新
}

// shardCacheItem 存储缓存项
// 注意：key 字段是必需的，因为 removeElement 需要从 map 中删除对应条目
type shardCacheItem struct {
	key       string
	value     prompt.SummaryCacheEntry
	expiresAt time.Time
}

// NewShardedMemoryCache 创建一个新的分片内存缓存
func NewShardedMemoryCache(maxEntries int, ttl time.Duration, shardCount int) *ShardedMemoryCache {
	if shardCount <= 0 {
		shardCount = 16
	}
	if maxEntries < 0 {
		maxEntries = 0
	}

	entriesPerShard := maxEntries / shardCount
	if entriesPerShard == 0 {
		entriesPerShard = 1
	}

	shards := make([]*memoryShard, shardCount)
	for i := 0; i < shardCount; i++ {
		shards[i] = &memoryShard{
			maxEntries: entriesPerShard,
			ttl:        ttl,
			ll:         list.New(),
			items:      make(map[string]*list.Element, entriesPerShard),
		}
	}

	cache := &ShardedMemoryCache{
		shards:     shards,
		shardCount: shardCount,
		stopCh:     make(chan struct{}),
	}

	// 启动后台过期清理 goroutine
	if ttl > 0 {
		cleanupInterval := ttl / 2
		if cleanupInterval < time.Minute {
			cleanupInterval = time.Minute
		}
		go cache.startEvictionLoop(cleanupInterval)
	}

	return cache
}

// Close 关闭缓存，停止后台清理 goroutine
func (c *ShardedMemoryCache) Close() {
	close(c.stopCh)
}

// startEvictionLoop 定期清理过期项
func (c *ShardedMemoryCache) startEvictionLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			for _, shard := range c.shards {
				shard.evictExpired()
			}
		case <-c.stopCh:
			return
		}
	}
}

func (c *ShardedMemoryCache) getShard(key string) *memoryShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	return c.shards[h.Sum32()%uint32(c.shardCount)]
}

// Get 从缓存获取值
func (c *ShardedMemoryCache) Get(key string) (prompt.SummaryCacheEntry, bool) {
	shard := c.getShard(key)
	return shard.get(key)
}

// Put 向缓存写入值
func (c *ShardedMemoryCache) Put(key string, entry prompt.SummaryCacheEntry) {
	shard := c.getShard(key)
	shard.put(key, entry)
}

func (s *memoryShard) get(key string) (prompt.SummaryCacheEntry, bool) {
	if s.maxEntries <= 0 {
		return prompt.SummaryCacheEntry{}, false
	}

	s.mu.RLock()
	el, ok := s.items[key]
	if !ok {
		s.mu.RUnlock()
		return prompt.SummaryCacheEntry{}, false
	}

	item := el.Value.(*shardCacheItem)

	// 检查是否过期
	if s.ttl > 0 && time.Now().After(item.expiresAt) {
		s.mu.RUnlock()
		// 异步删除过期项，不阻塞读操作
		go s.tryRemoveExpired(key, el)
		return prompt.SummaryCacheEntry{}, false
	}

	value := item.value
	needsMove := s.ll.Front() != el
	s.mu.RUnlock()

	// 概率性 LRU 更新：只有 1/16 的概率执行 MoveToFront
	// 大幅降低写锁竞争，同时保持近似 LRU 语义
	if needsMove && atomic.AddUint64(&s.accessCount, 1)%16 == 0 {
		s.mu.Lock()
		// 再次验证元素仍在缓存中且是同一个元素
		if el2, ok2 := s.items[key]; ok2 && el2 == el {
			s.ll.MoveToFront(el)
		}
		s.mu.Unlock()
	}

	return value, true
}

// tryRemoveExpired 尝试删除过期项（用于异步调用）
func (s *memoryShard) tryRemoveExpired(key string, el *list.Element) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 再次验证是否是同一个元素
	if el2, ok := s.items[key]; ok && el2 == el {
		s.removeElement(el)
	}
}

func (s *memoryShard) put(key string, entry prompt.SummaryCacheEntry) {
	if s.maxEntries <= 0 {
		return
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if el, ok := s.items[key]; ok {
		item := el.Value.(*shardCacheItem)
		item.value = entry
		item.expiresAt = s.expiryTime()
		s.ll.MoveToFront(el)
		return
	}

	item := &shardCacheItem{
		key:       key,
		value:     entry,
		expiresAt: s.expiryTime(),
	}
	el := s.ll.PushFront(item)
	s.items[key] = el

	if s.ll.Len() > s.maxEntries {
		s.removeOldest()
	}
}

func (s *memoryShard) expiryTime() time.Time {
	if s.ttl <= 0 {
		return time.Time{}
	}
	return time.Now().Add(s.ttl)
}

func (s *memoryShard) removeOldest() {
	el := s.ll.Back()
	if el != nil {
		s.removeElement(el)
	}
}

func (s *memoryShard) removeElement(el *list.Element) {
	s.ll.Remove(el)
	item := el.Value.(*shardCacheItem)
	delete(s.items, item.key)
}

// evictExpired 清理所有过期项（由后台 goroutine 调用）
func (s *memoryShard) evictExpired() {
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// 从尾部开始遍历（最旧的项）
	for el := s.ll.Back(); el != nil; {
		item := el.Value.(*shardCacheItem)
		prev := el.Prev()

		if s.ttl > 0 && now.After(item.expiresAt) {
			s.ll.Remove(el)
			delete(s.items, item.key)
		}

		el = prev
	}
}
