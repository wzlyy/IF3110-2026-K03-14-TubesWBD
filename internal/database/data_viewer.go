package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

const DataPageSize = 50

// DataCell preserves SQL NULL separately from a real empty string.
type DataCell struct {
	Text string
	Null bool
}
type TableData struct {
	Columns             []string
	Rows                [][]DataCell
	Page                int64
	HasNext             bool
	OrderedByPrimaryKey bool
}

func ParseDataPage(raw string) (int64, error) {
	if raw == "" {
		return 1, nil
	}
	page, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, ErrInvalidPage
	}
	if _, err = pageOffset(page); err != nil {
		return 0, err
	}
	return page, nil
}
func pageOffset(page int64) (int64, error) {
	if page < 1 || page-1 > (math.MaxInt64-DataPageSize)/DataPageSize {
		return 0, ErrInvalidPage
	}
	return (page - 1) * DataPageSize, nil
}
func displayCell(value any) DataCell {
	if value == nil {
		return DataCell{Null: true}
	}
	switch v := value.(type) {
	case []byte:
		if utf8.Valid(v) && !strings.ContainsRune(string(v), 0) {
			return DataCell{Text: string(v)}
		}
		return DataCell{Text: `\x` + hex.EncodeToString(v)}
	case time.Time:
		return DataCell{Text: v.Format(time.RFC3339Nano)}
	default:
		return DataCell{Text: fmt.Sprint(v)}
	}
}

// ReadTableData accepts only a Table obtained from LoadCatalog/FindTable.
// It executes a single schema-qualified SELECT in a read-only transaction with
// a five-second deadline, fetching one lookahead row beyond the 50-row page.
func ReadTableData(ctx context.Context, target *sql.DB, dbms string, catalogTable Table, page int64) (*TableData, error) {
	offset, err := pageOffset(page)
	if err != nil {
		return nil, err
	}
	if dbms != "postgres" && dbms != "mysql" {
		return nil, ErrUnsupportedDBMS
	}
	if !catalogTable.validated {
		return nil, ErrTableNotFound
	}
	schema, err := QuoteIdentifier(dbms, catalogTable.Schema)
	if err != nil {
		return nil, err
	}
	name, err := QuoteIdentifier(dbms, catalogTable.Name)
	if err != nil {
		return nil, err
	}
	query := "SELECT * FROM " + schema + "." + name
	ordered := false
	for _, con := range catalogTable.Constraints {
		if con.Type == "PRIMARY KEY" && len(con.Columns) > 0 {
			var keys []string
			for _, col := range con.Columns {
				q, e := QuoteIdentifier(dbms, col)
				if e != nil {
					return nil, e
				}
				keys = append(keys, q)
			}
			query += " ORDER BY " + strings.Join(keys, ", ")
			ordered = true
			break
		}
	}
	if dbms == "postgres" {
		query += " LIMIT $1 OFFSET $2"
	} else {
		query += " LIMIT ? OFFSET ?"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := target.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	defer tx.Rollback()
	// Server-side timeout remains effective even if a driver cancellation fails.
	if dbms == "postgres" {
		if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
			return nil, ErrTargetUnavailable
		}
	}
	rows, err := tx.QueryContext(ctx, query, DataPageSize+1, offset)
	if err != nil {
		var pgerr *pgconn.PgError
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &pgerr) && (pgerr.Code == "42P01" || pgerr.Code == "3F000") {
			return nil, ErrTableNotFound
		}
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1146 {
			return nil, ErrTableNotFound
		}
		return nil, ErrTargetUnavailable
	}
	defer rows.Close()
	result := &TableData{Page: page, OrderedByPrimaryKey: ordered}
	result.Columns, err = rows.Columns()
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	for rows.Next() {
		values := make([]any, len(result.Columns))
		dest := make([]any, len(values))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, ErrTargetUnavailable
		}
		if len(result.Rows) == DataPageSize {
			result.HasNext = true
			break
		}
		cells := make([]DataCell, len(values))
		for i, v := range values {
			cells[i] = displayCell(v)
		}
		result.Rows = append(result.Rows, cells)
	}
	if rows.Err() != nil {
		return nil, ErrTargetUnavailable
	}
	if err = rows.Close(); err != nil {
		return nil, ErrTargetUnavailable
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrTargetUnavailable
	}
	return result, nil
}
