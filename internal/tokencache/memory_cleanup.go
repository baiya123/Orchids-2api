package tokencache

import "time"

func (c *MemoryCache) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			c.pruneExpiredLocked(time.Now())
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
}

// Close 停止后台清理 goroutine
func (c *MemoryCache) Close() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}
