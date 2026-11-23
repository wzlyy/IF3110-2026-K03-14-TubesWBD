package worker

import (
	"context"
	"errors"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

const metaDDL = `
CREATE SCHEMA IF NOT EXISTS _platform_meta;
CREATE TABLE IF NOT EXISTS _platform_meta.execution_proofs (
 execution_id varchar(36) PRIMARY KEY, migration_id varchar(36) NOT NULL, action_type varchar(24) NOT NULL, committed_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS _platform_meta.batch_checkpoints (
 execution_id varchar(36) NOT NULL, batch_number int NOT NULL, cursor_start text, cursor_end text NOT NULL, target_max_cursor text NOT NULL, rows_affected bigint NOT NULL,
 is_reverted boolean NOT NULL DEFAULT false, committed_at timestamptz NOT NULL DEFAULT now(), reverted_at timestamptz, PRIMARY KEY(execution_id,batch_number));
ALTER TABLE _platform_meta.batch_checkpoints ADD COLUMN IF NOT EXISTS forward_execution varchar(36), ADD COLUMN IF NOT EXISTS revert_execution varchar(36);
CREATE TABLE IF NOT EXISTS _platform_meta.batch_undo_logs (
 execution_id varchar(36) NOT NULL, batch_number int NOT NULL,row_key text NOT NULL,before_data jsonb NOT NULL, PRIMARY KEY(execution_id,batch_number,row_key));
ALTER TABLE _platform_meta.batch_undo_logs ADD COLUMN IF NOT EXISTS after_data jsonb;`

var errUncertain = errors.New("target commit requires verification")

// Serialize recovery with any transaction still completing on an old connection.
// After entering the target fence, check that the platform lease is still ours.
func (a *attempt) targetTx(ctx context.Context) (*telemetry.InstrumentedTx, error) {
	tx, err := a.target.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended(current_database(), 471901))"); err == nil {
		var owns bool
		err = a.s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM database_locks WHERE database_id=$1 AND lock_token=$2 AND (hold_state='held_uncertain' OR expires_at>now()))", a.j.DatabaseID, a.token).Scan(&owns)
		if err == nil && !owns {
			err = errors.New("worker lease expired")
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func commit(tx *telemetry.InstrumentedTx) error {
	if err := tx.Commit(); err != nil {
		return errors.Join(errUncertain, err)
	}
	return nil
}

func (a *attempt) proof(ctx context.Context, tx *telemetry.InstrumentedTx) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO _platform_meta.execution_proofs(execution_id,migration_id,action_type) VALUES($1,$2,$3)", a.j.ExecutionID, a.j.MigrationID, a.j.ActionType)
	return err
}

func (a *attempt) metadata(ctx context.Context) error {
	tx, e := a.targetTx(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, metaDDL); e != nil {
		return e
	}
	return tx.Commit()
}
