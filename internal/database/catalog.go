package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Catalog is a database snapshot. Names retain their original case and schema.
type Catalog struct {
	DBMS, Name string
	Schemas    []Schema
}
type Schema struct {
	Name   string
	Tables []Table
}

// Table describes an ordinary or partitioned table, including ordered columns.
type Table struct {
	Schema, Name string
	Columns      []Column
	Constraints  []Constraint
	Indexes      []Index
	validated    bool
}
type Column struct {
	Name, DataType      string
	Position            int
	Nullable            bool
	Default             *string
	Identity, Generated string
}

// Constraint.Type is PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK, or EXCLUDE.
// Columns follow key order; Definition is the DBMS-specific SQL definition.
type Constraint struct {
	Name, Type, Definition string
	Columns                []string
}

// Index.Columns contains ordered SQL expressions, including expression keys.
// Definition also retains predicates, included columns, and sort/null ordering.
type Index struct {
	Name, Type, Definition string
	Columns                []string
	Unique, Primary        bool
}

var (
	ErrUnsupportedDBMS   = errors.New("unsupported database type")
	ErrInvalidIdentifier = errors.New("invalid identifier")
	ErrInvalidPage       = errors.New("invalid data page")
	ErrTableNotFound     = errors.New("table not found")
	ErrTargetUnavailable = errors.New("target database unavailable")
)

// FindTable performs exact, schema-qualified lookup; never normalize identifiers.
func (c *Catalog) FindTable(schema, name string) (*Table, bool) {
	for i := range c.Schemas {
		if c.Schemas[i].Name == schema {
			for j := range c.Schemas[i].Tables {
				if c.Schemas[i].Tables[j].Name == name {
					return &c.Schemas[i].Tables[j], true
				}
			}
		}
	}
	return nil, false
}

// QuoteIdentifier quotes one identifier, never a dotted qualified name.
func QuoteIdentifier(dbms, identifier string) (string, error) {
	if dbms != "postgres" && dbms != "mysql" {
		return "", ErrUnsupportedDBMS
	}
	if identifier == "" || strings.ContainsRune(identifier, 0) {
		return "", ErrInvalidIdentifier
	}
	if dbms == "mysql" {
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
	}
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`, nil
}

// LoadCatalog reads user schemas and tables in a consistent read-only snapshot.
// Caller cancellation is honored; all catalog work has a five-second ceiling.
func LoadCatalog(ctx context.Context, target *sql.DB, dbms, dbName string) (*Catalog, error) {
	if dbms != "postgres" && dbms != "mysql" {
		return nil, ErrUnsupportedDBMS
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := target.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	defer tx.Rollback()
	var catalog *Catalog
	if dbms == "postgres" {
		catalog, err = loadPostgresCatalog(ctx, tx, dbName)
	} else {
		catalog, err = loadMySQLCatalog(ctx, tx, dbName)
	}
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	if err = tx.Commit(); err != nil {
		return nil, ErrTargetUnavailable
	}
	return catalog, nil
}
