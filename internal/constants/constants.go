// Package constants defines shared constants across the application.
package constants

import "time"

// Buffer sizes
const (
	DefaultBufferSize    = 4096
	LargeBufferSize      = 32768
	MaxDescriptionLength = 9216
	MaxToolInputLength   = 65536
)

// Concurrency thresholds
const (
	ParallelThreshold = 8  // Min items for parallel processing
	LRUUpdateRatio    = 16 // 1/16 probability for LRU updates
)

// Timeouts
const (
	DefaultTimeout       = 30 * time.Second
	UpstreamTimeout      = 120 * time.Second
	ShutdownTimeout      = 30 * time.Second
	SessionCleanupPeriod = 1 * time.Hour
)

// Token estimation
const (
	TokensPerWord    = 1.3
	TokensPerCJK     = 1.5
	DefaultMaxTokens = 4096
)

// Cache settings
const (
	DefaultCacheTTL = 5 * time.Second
	SummaryCacheTTL = 30 * time.Minute
	TokenCacheTTL   = 5 * time.Minute
)
