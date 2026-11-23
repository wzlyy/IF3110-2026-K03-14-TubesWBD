package worker

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
)

func TestRetryKeepsJobAndCreatesOneAuditIDPerAttempt(t *testing.T) {
	original := queue.JobMessage{JobID: "job", ExecutionID: "execution", AttemptNumber: 1}
	first := nextAttempt(original)
	if first.JobID != original.JobID || first.AttemptNumber != 2 || first.ExecutionID == original.ExecutionID {
		t.Fatalf("unexpected retry: %+v", first)
	}
	if nextAttempt(original).ExecutionID != first.ExecutionID || nextAttempt(first).ExecutionID == first.ExecutionID {
		t.Fatal("redelivery must reuse the same ID, a subsequent attempt must get a new ID")
	}
}

func TestUnavailableProofKeepsVerificationPending(t *testing.T) {
	ctx := t.Context()
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	db, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = (&Service{DB: db}).verify(ctx, "db", "execution", "lease")
	if !errors.Is(err, errVerificationPending) || !errors.Is(err, context.Canceled) {
		t.Fatalf("verification lost its retry or cause: %v", err)
	}
}
