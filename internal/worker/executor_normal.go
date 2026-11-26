package worker

import (
	"context"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
)

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
