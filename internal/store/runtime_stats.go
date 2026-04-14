package store

import "time"

// RuntimeStatsReporter is implemented by stores that can expose runtime status
// for observability surfaces such as bot-admin diagnostics commands.
type RuntimeStatsReporter interface {
	RuntimeStats() RuntimeStats
}

type RuntimeStats struct {
	Mode               string
	Degraded           bool
	QueueDepth         int
	QueueDepthError    string
	LastFlushBatchSize int
	LastFlushDuration  time.Duration
	LastFlushAt        time.Time
	LastFlushError     string
	LastReplayAt       time.Time
	LastReplayError    string
	LastRebuildAt      time.Time
	LastRebuildError   string
}
