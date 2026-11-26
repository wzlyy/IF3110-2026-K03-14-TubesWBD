package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

type checkpoint struct {
	number          int
	start, end, max string
	rows            int64
}
type batchSpec struct {
	table, col, extraWhere, statement string
	size                              int
}

func (a *attempt) batchSpec() (batchSpec, error) {
	var b batchSpec
	if a.d.BatchSize == nil {
		return b, errors.New("missing batch size")
	}
	b.size = *a.d.BatchSize
	if a.j.BatchSizeOverride != nil {
		b.size = *a.j.BatchSizeOverride
	}
	if b.size < 1 || b.size > migration.MaxBatchSize {
		return b, errors.New("invalid batch size")
	}
	plan, err := migration.ParseBatchUpdateSQL(a.snapshot, a.d.BatchTable, a.d.BatchCursorColumn)
	if err != nil {
		return b, err
	}
	b.table, b.col, b.extraWhere, b.statement = plan.Table, plan.Column, plan.ExtraWhere, plan.Statement
	return b, nil
}

func (a *attempt) batch(ctx context.Context) error {
	b, e := a.batchSpec()
	if e != nil {
		return e
	}
	var valid bool
	e = a.target.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_index i JOIN pg_attribute k ON k.attrelid=i.indrelid AND k.attnum=i.indkey[0] WHERE i.indrelid=$1::regclass AND i.indisunique AND i.indisvalid AND i.indpred IS NULL AND i.indnkeyatts=1 AND k.attnotnull AND k.attname=$2)`, b.table, a.d.BatchCursorColumn).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return errors.New("batch cursor requires a non-null single-column unique key")
	}
	if a.j.ActionType == "revert_batch" {
		return a.revert(ctx, b)
	}
	var min, max sql.NullString
	var last checkpoint
	e = a.target.QueryRowContext(ctx, "SELECT batch_number,cursor_end,target_max_cursor FROM _platform_meta.batch_checkpoints WHERE execution_id=$1 ORDER BY batch_number DESC LIMIT 1", a.root).Scan(&last.number, &last.end, &last.max)
	if errors.Is(e, sql.ErrNoRows) {
		whereClause := ""
		if b.extraWhere != "" {
			whereClause = " WHERE (" + b.extraWhere + ")"
		}
		boundsQuery := "SELECT " +
			"(SELECT " + b.col + "::text FROM " + b.table + whereClause + " ORDER BY " + b.col + " ASC LIMIT 1)," +
			"(SELECT " + b.col + "::text FROM " + b.table + whereClause + " ORDER BY " + b.col + " DESC LIMIT 1)"
		e = a.target.QueryRowContext(ctx, boundsQuery).Scan(&min, &max)
		last.max = max.String
	}
	if e != nil {
		return e
	}
	for {
		meta := telemetry.JobMetaFromContext(ctx)
		meta.BatchNumber = last.number + 1
		batchCtx := telemetry.WithJobMeta(ctx, meta)
		tx, e := a.targetTx(batchCtx)
		if e != nil {
			return e
		}
		c, done, e := a.forwardBatch(batchCtx, tx, b, min, last)
		if e == nil && done {
			e = a.proof(ctx, tx)
		}
		if e == nil {
			e = commit(tx)
		} else {
			tx.Rollback()
		}
		if e != nil {
			if !done && !errors.Is(e, errUncertain) {
				telemetry.GlobalMetrics().RecordBatchProcessed("failed", 0)
			}
			return e
		}
		if done {
			return nil
		}
		if e = a.syncOne(ctx, c, "forward"); e != nil {
			return e
		}
		last = c
	}
}

func (a *attempt) forwardBatch(ctx context.Context, tx *telemetry.InstrumentedTx, b batchSpec, min sql.NullString, last checkpoint) (checkpoint, bool, error) {
	c := checkpoint{number: last.number + 1, max: last.max}
	if last.number == 0 && !min.Valid {
		return c, true, nil
	}
	op, lower := ">", last.end
	if last.number == 0 {
		op, lower = ">=", min.String
	}
	filter := b.col + op + "$1 AND " + b.col + "<=$2"
	if b.extraWhere != "" {
		filter = "(" + filter + ") AND (" + b.extraWhere + ")"
	}
	q := "SELECT " + b.col + "::text FROM " + b.table + " WHERE " + filter + " ORDER BY " + b.col + " ASC LIMIT $3 FOR NO KEY UPDATE"
	rows, e := tx.QueryContext(ctx, q, lower, last.max, b.size)
	if e != nil {
		return c, false, e
	}
	var lo, hi string
	var count int64
	for rows.Next() {
		var key string
		if e := rows.Scan(&key); e != nil {
			rows.Close()
			return c, false, e
		}
		if count == 0 {
			lo = key
		}
		hi = key
		count++
	}
	if e := rows.Err(); e != nil {
		rows.Close()
		return c, false, e
	}
	rows.Close()
	if count == 0 {
		return c, true, nil
	}
	c.start, c.end = lo, hi
	if !a.d.IsIrreversible {
		undoFilter := b.col + " BETWEEN $3 AND $4"
		if b.extraWhere != "" {
			undoFilter = "(" + undoFilter + ") AND (" + b.extraWhere + ")"
		}
		q = "WITH _wbd_undo_src AS (SELECT * FROM " + b.table + " WHERE " + undoFilter + ") " +
			"INSERT INTO _platform_meta.batch_undo_logs(execution_id,batch_number,row_key,before_data) " +
			"SELECT $1,$2," + b.col + "::text,to_jsonb(_wbd_undo_src) FROM _wbd_undo_src"
		if _, e := tx.ExecContext(ctx, q, a.root, c.number, c.start, c.end); e != nil {
			return c, false, e
		}
	}
	res, e := tx.ExecContext(ctx, b.statement, c.start, c.end)
	if e != nil {
		return c, false, e
	}
	c.rows, e = res.RowsAffected()
	if e != nil {
		return c, false, e
	}
	if c.rows != count {
		return c, false, fmt.Errorf("batch updated %d of %d selected rows", c.rows, count)
	}
	if !a.d.IsIrreversible {
		res, e = tx.ExecContext(ctx, "UPDATE _platform_meta.batch_undo_logs u SET after_data=to_jsonb(t) FROM "+b.table+" AS t WHERE u.execution_id=$1 AND u.batch_number=$2 AND t."+b.col+" BETWEEN $3 AND $4 AND u.row_key=t."+b.col+"::text", a.root, c.number, c.start, c.end)
		if e != nil {
			return c, false, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return c, false, e
		}
		if n != count {
			return c, false, errors.New("batch changed its cursor keys")
		}
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO _platform_meta.batch_checkpoints(execution_id,batch_number,cursor_start,cursor_end,target_max_cursor,rows_affected,forward_execution) VALUES($1,$2,$3,$4,$5,$6,$7)", a.root, c.number, c.start, c.end, c.max, c.rows, a.j.ExecutionID)
	return c, false, e
}

func (a *attempt) revert(ctx context.Context, b batchSpec) error {
	for {
		tx, e := a.targetTx(ctx)
		if e != nil {
			return e
		}
		var c checkpoint
		e = tx.QueryRowContext(ctx, "SELECT batch_number,cursor_start,cursor_end,target_max_cursor,rows_affected FROM _platform_meta.batch_checkpoints WHERE execution_id=$1 AND NOT is_reverted ORDER BY batch_number DESC LIMIT 1 FOR UPDATE", a.root).Scan(&c.number, &c.start, &c.end, &c.max, &c.rows)
		done := errors.Is(e, sql.ErrNoRows)
		batchCtx := ctx
		if done {
			e = a.proof(ctx, tx)
		} else if e == nil {
			meta := telemetry.JobMetaFromContext(ctx)
			meta.BatchNumber = c.number
			batchCtx = telemetry.WithJobMeta(ctx, meta)
			e = a.revertCheckpoint(batchCtx, tx, b, c)
			if e == nil {
				_, e = tx.ExecContext(batchCtx, "UPDATE _platform_meta.batch_checkpoints SET is_reverted=true,reverted_at=now(),revert_execution=$3 WHERE execution_id=$1 AND batch_number=$2", a.root, c.number, a.j.ExecutionID)
			}
		}
		if e != nil {
			tx.Rollback()
			if !done && !errors.Is(e, errUncertain) {
				telemetry.GlobalMetrics().RecordBatchProcessed("failed", 0)
			}
			return e
		}
		if e = commit(tx); e != nil {
			if !done && !errors.Is(e, errUncertain) {
				telemetry.GlobalMetrics().RecordBatchProcessed("failed", 0)
			}
			return e
		}
		if done {
			return nil
		}
		if e = a.syncOne(batchCtx, c, "revert"); e != nil {
			return e
		}
	}
}

// Apply the immutable compensating SQL only to captured keys. Fail closed if
// any row has been changed or deleted since this migration committed.
func (a *attempt) revertCheckpoint(ctx context.Context, tx *telemetry.InstrumentedTx, b batchSpec, c checkpoint) error {
	if c.rows == 0 {
		return nil
	}
	var matched int64
	q := "WITH locked AS (" +
		"SELECT t." + b.col + " FROM " + b.table + " AS t " +
		"JOIN _platform_meta.batch_undo_logs u ON t." + b.col + "::text=u.row_key " +
		"WHERE u.execution_id=$1 AND u.batch_number=$2 AND t." + b.col + " BETWEEN $3 AND $4 AND u.after_data=to_jsonb(t) " +
		"FOR NO KEY UPDATE OF t" +
		") SELECT count(*) FROM locked"
	if e := tx.QueryRowContext(ctx, q, a.root, c.number, c.start, c.end).Scan(&matched); e != nil {
		return e
	}
	if matched != c.rows {
		return errors.New("batch undo unsafe: rows changed, missing, or incomplete snapshots")
	}
	res, e := tx.ExecContext(ctx, b.statement+" AND "+b.col+"::text IN (SELECT row_key FROM _platform_meta.batch_undo_logs WHERE execution_id=$3 AND batch_number=$4)", c.start, c.end, a.root, c.number)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != c.rows {
		return errors.New("compensating SQL did not update the captured keys")
	}
	return nil
}

func (a *attempt) syncOne(ctx context.Context, c checkpoint, dir string) error {
	status := "committed"
	if dir == "revert" {
		status = "reverted"
	}
	tx, e := a.s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `INSERT INTO execution_batch_progress(id,execution_id,batch_number,cursor_start,cursor_end,target_max_cursor,rows_affected,direction,status)
 VALUES(gen_random_uuid()::text,$1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(execution_id,batch_number,direction) DO NOTHING`, a.j.ExecutionID, c.number, c.start, c.end, c.max, c.rows, dir, status)
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n > 0 {
		_, e = tx.ExecContext(ctx, "UPDATE migration_executions SET total_batches_committed=total_batches_committed+1,total_rows_affected=total_rows_affected+$2 WHERE execution_id=$1", a.j.ExecutionID, c.rows)
		if e != nil {
			return e
		}
	}
	if e = tx.Commit(); e == nil && n > 0 {
		telemetry.GlobalMetrics().RecordBatchProcessed(status, c.rows)
	}
	return e
}
