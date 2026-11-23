package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

type checkpoint struct {
	number          int
	start, end, max string
	rows            int64
}
type batchSpec struct {
	table, col, statement string
	size                  int
}

var ident = regexp.MustCompile("^[a-zA-Z_][a-zA-Z0-9_]*$")
var update = regexp.MustCompile(`(?is)^UPDATE\s+([a-zA-Z_][\w]*(?:\.[a-zA-Z_][\w]*)?)\s+SET\s+(.+)$`)
var where = regexp.MustCompile(`(?i)\s+WHERE\s+`)

func (a *attempt) batchSpec() (batchSpec, error) {
	var b batchSpec
	names := strings.Split(a.d.BatchTable, ".")
	if len(names) > 2 {
		return b, errors.New("invalid batch table")
	}
	for _, n := range append(append([]string{}, names...), a.d.BatchCursorColumn) {
		if !ident.MatchString(n) {
			return b, errors.New("invalid batch identifier")
		}
	}
	b.table = pgx.Identifier(names).Sanitize()
	b.col = pgx.Identifier{a.d.BatchCursorColumn}.Sanitize()
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

	stmts, e := migration.ValidateSQL("postgres", a.snapshot)
	if e != nil {
		return b, e
	}
	if len(stmts) != 1 {
		return b, errors.New("batch requires one UPDATE")
	}
	match := update.FindStringSubmatch(stmts[0].SQL)
	if len(match) != 3 || !(strings.EqualFold(match[1], a.d.BatchTable) || strings.EqualFold("public."+match[1], a.d.BatchTable) || strings.EqualFold(match[1], "public."+a.d.BatchTable)) {
		return b, errors.New("batch UPDATE must target configured table")
	}
	parts := where.Split(match[2], -1)
	if len(parts) > 2 {
		return b, errors.New("unsupported nested WHERE in batch UPDATE")
	}
	if len(parts) == 2 && !regexp.MustCompile(`(?i)^\s*`+regexp.QuoteMeta(a.d.BatchCursorColumn)+`\s+BETWEEN\s+\$1\s+AND\s+\$2\s*$`).MatchString(parts[1]) {
		return b, errors.New("batch WHERE must use cursor BETWEEN $1 AND $2")
	}
	if strings.Contains(parts[0], "--") || strings.Contains(parts[0], "/*") || regexp.MustCompile(`(?i)\b(FROM|RETURNING)\b`).MatchString(parts[0]) {
		return b, errors.New("unsupported batch UPDATE clause")
	}
	// Batch statements cannot change the key used for streaming and undo.
	if regexp.MustCompile(`(?i)(^|,)\s*"?`+regexp.QuoteMeta(a.d.BatchCursorColumn)+`"?\s*=`).MatchString(parts[0]) || strings.HasPrefix(strings.TrimSpace(parts[0]), "(") {
		return b, errors.New("batch cursor must remain immutable")
	}
	b.statement = "UPDATE " + b.table + " SET " + parts[0] + " WHERE " + b.col + " BETWEEN $1 AND $2"
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
		e = a.target.QueryRowContext(ctx, "SELECT min("+b.col+")::text,max("+b.col+")::text FROM "+b.table).Scan(&min, &max)
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
	// Hold writers only for this transaction so snapshots and the scoped UPDATE see identical rows.
	if _, e := tx.ExecContext(ctx, "LOCK TABLE "+b.table+" IN SHARE ROW EXCLUSIVE MODE"); e != nil {
		return c, false, e
	}
	op, lower := ">", last.end
	if last.number == 0 {
		op, lower = ">=", min.String
	}
	q := "SELECT min(k)::text,max(k)::text,count(*) FROM (SELECT " + b.col + " AS k FROM " + b.table + " WHERE " + b.col + op + "$1 AND " + b.col + "<=$2 ORDER BY " + b.col + " LIMIT $3) s"
	var lo, hi sql.NullString
	var count int64
	if e := tx.QueryRowContext(ctx, q, lower, last.max, b.size).Scan(&lo, &hi, &count); e != nil {
		return c, false, e
	}
	if count == 0 {
		return c, true, nil
	}
	c.start, c.end = lo.String, hi.String
	if !a.d.IsIrreversible {
		q = "INSERT INTO _platform_meta.batch_undo_logs(execution_id,batch_number,row_key,before_data) SELECT $1,$2,t." + b.col + "::text,to_jsonb(t) FROM " + b.table + " t WHERE " + b.col + " BETWEEN $3 AND $4"
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
		if done {
			e = a.proof(ctx, tx)
		} else if e == nil {
			e = a.revertCheckpoint(ctx, tx, b, c)
			if e == nil {
				_, e = tx.ExecContext(ctx, "UPDATE _platform_meta.batch_checkpoints SET is_reverted=true,reverted_at=now(),revert_execution=$3 WHERE execution_id=$1 AND batch_number=$2", a.root, c.number, a.j.ExecutionID)
			}
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = commit(tx); e != nil {
			return e
		}
		if done {
			return nil
		}
		if e = a.syncOne(ctx, c, "revert"); e != nil {
			return e
		}
	}
}

// Apply the immutable compensating SQL only to captured keys. Fail closed if
// any row has been changed or deleted since this migration committed.
func (a *attempt) revertCheckpoint(ctx context.Context, tx *telemetry.InstrumentedTx, b batchSpec, c checkpoint) error {
	if _, e := tx.ExecContext(ctx, "LOCK TABLE "+b.table+" IN SHARE ROW EXCLUSIVE MODE"); e != nil {
		return e
	}
	var matched int64
	q := "SELECT count(*) FROM " + b.table + " AS t JOIN _platform_meta.batch_undo_logs u ON t." + b.col + "::text=u.row_key WHERE u.execution_id=$1 AND u.batch_number=$2 AND t." + b.col + " BETWEEN $3 AND $4 AND u.after_data=to_jsonb(t)"
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
