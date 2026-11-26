package worker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
)

const DefaultNormalStatementTimeout = 5 * time.Minute

func normalStatementTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("WORKER_STATEMENT_TIMEOUT_MS")); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return DefaultNormalStatementTimeout
}

func (a *attempt) normal(ctx context.Context) error {
	statements, err := migration.SplitPostgresStatements(a.snapshot)
	if err != nil {
		return err
	}
	tx, err := a.targetTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if timeout := normalStatementTimeout(); timeout > 0 {
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", timeout.Milliseconds())); err != nil {
			return err
		}
	}
	for _, st := range statements {
		if _, err = tx.ExecContext(ctx, st.SQL); err != nil {
			return err
		}
	}
	if err = a.proof(ctx, tx); err != nil {
		return err
	}
	return commit(tx)
}
