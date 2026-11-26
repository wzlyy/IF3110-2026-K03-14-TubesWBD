package queue

import (
	"context"
	"time"
)

const (
	ExchangeName    = "migration.exchange"
	QueueJobs       = "migration.jobs"
	QueueRetry      = "migration.jobs.retry"
	QueueDLQ        = "migration.jobs.dlq"
	RoutingKeyJobs  = "migration.job"
	RoutingKeyRetry = "migration.job.retry"
	RoutingKeyDLQ   = "migration.job.dlq"

	MaxTransientRetries = 3
)

// JobMessage adalah kontrak JSON persisten yang dikirim API Go dan dikonsumsi Worker Go.
type JobMessage struct {
	JobID             string    `json:"job_id"`
	ExecutionID       string    `json:"execution_id"`
	RequestID         string    `json:"request_id"`
	DatabaseID          string    `json:"database_id"`
	DatabaseName        string    `json:"database_name,omitempty"`
	MigrationID         string    `json:"migration_id"`
	ActionType          string    `json:"action_type"` // Jenis aksi: up, rollback, forward, resume_batch, revert_batch
	TargetExecutionID   string    `json:"target_execution_id,omitempty"`
	ExecutionMode       string    `json:"execution_mode"` // Mode eksekusi: normal atau batch
	BatchSizeOverride   *int      `json:"batch_size_override,omitempty"`
	RequestedBy         string    `json:"requested_by"`
	RequestedByUsername string    `json:"requested_by_username,omitempty"`
	AttemptNumber     int        `json:"attempt_number"`
	RetryCount        int        `json:"retry_count"`
	EnqueuedAt        time.Time  `json:"enqueued_at"`
	LockWaitStartedAt *time.Time `json:"lock_wait_started_at,omitempty"`
}

// Publisher adalah antarmuka pengiriman pesan persisten (mudah di-mock pada unit test).
type Publisher interface {
	PublishJob(ctx context.Context, msg JobMessage) error
	Close() error
}

// RetryBackoff menghitung jeda eksponensial terbatas untuk kegagalan sementara.
func RetryBackoff(retryCount int) time.Duration {
	if retryCount < 0 {
		retryCount = 0
	}
	base := 2 * time.Second
	delay := base << retryCount // Kelipatan jeda: 2d, 4d, 8d, 16d...
	const maxDelay = 30 * time.Second
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}
