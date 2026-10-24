package migration

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

// GenerateMigrationSQL converts a DiffResult into executable candidate DDL statements
// tailored to the target DBMS (PostgreSQL or MySQL) in safe dependency order.
func GenerateMigrationSQL(diff *DiffResult, dbms string) (string, error) {
	if diff == nil {
		return "", errors.New("hasil perbandingan skema (diff) tidak boleh nil")
	}
	if dbms != "postgres" && dbms != "mysql" {
		return "", database.ErrUnsupportedDBMS
	}
	if diff.IsIdentical || len(diff.Changes) == 0 {
		return "", nil
	}

	// Group changes by action
	buckets := make(map[DiffAction][]SchemaChange)
	for _, c := range diff.Changes {
		buckets[c.Action] = append(buckets[c.Action], c)
	}

	// Order statements in safe dependency order:
	// 1. Create Tables (new tables must exist first)
	// 2. Add Columns (new columns on existing tables)
	// 3. Modify Columns
	// 4. Add Constraints (foreign keys require referenced tables/columns to exist)
	// 5. Create Indexes
	// 6. Drop Indexes
	// 7. Drop Constraints (drop foreign keys before dropping referenced tables/columns)
	// 8. Drop Columns
	// 9. Drop Tables
	orderedActions := []DiffAction{
		ActionCreateTable,
		ActionAddColumn,
		ActionModifyColumn,
		ActionAddConstraint,
		ActionCreateIndex,
		ActionDropIndex,
		ActionDropConstraint,
		ActionDropColumn,
		ActionDropTable,
	}

	var statements []string
	for _, action := range orderedActions {
		changes := buckets[action]
		for _, ch := range changes {
			stmt, err := generateStatement(ch, dbms)
			if err != nil {
				return "", fmt.Errorf("gagal menghasilkan SQL untuk aksi %s pada %s.%s: %w", ch.Action, ch.Schema, ch.Table, err)
			}
			stmt = strings.TrimSpace(stmt)
			if stmt != "" {
				statements = append(statements, stmt)
			}
		}
	}

	if len(statements) == 0 {
		return "", nil
	}

	return strings.Join(statements, "\n\n") + "\n", nil
}

func generateStatement(ch SchemaChange, dbms string) (string, error) {
	schemaQ, err := database.QuoteIdentifier(dbms, ch.Schema)
	if err != nil {
		return "", err
	}
	tableQ, err := database.QuoteIdentifier(dbms, ch.Table)
	if err != nil {
		return "", err
	}
	tableQualified := fmt.Sprintf("%s.%s", schemaQ, tableQ)

	switch ch.Action {
	case ActionCreateTable:
		tableDef, ok := ch.Details.(TableDef)
		if !ok {
			return "", errors.New("metadata TableDef tidak valid")
		}
		var colLines []string
		for _, col := range tableDef.Columns {
			colQ, err := database.QuoteIdentifier(dbms, col.Name)
			if err != nil {
				return "", err
			}
			line := fmt.Sprintf("    %s %s", colQ, col.DataType)
			if !col.Nullable {
				line += " NOT NULL"
			}
			if col.Default != nil {
				line += " DEFAULT " + *col.Default
			}
			colLines = append(colLines, line)
		}

		if len(tableDef.PrimaryKey) > 0 {
			var pkCols []string
			for _, pk := range tableDef.PrimaryKey {
				q, err := database.QuoteIdentifier(dbms, pk)
				if err != nil {
					return "", err
				}
				pkCols = append(pkCols, q)
			}
			colLines = append(colLines, fmt.Sprintf("    PRIMARY KEY (%s)", strings.Join(pkCols, ", ")))
		}

		return fmt.Sprintf("CREATE TABLE %s (\n%s\n);", tableQualified, strings.Join(colLines, ",\n")), nil

	case ActionAddColumn:
		colDef, ok := ch.Details.(ColumnDef)
		if !ok {
			return "", errors.New("metadata ColumnDef tidak valid")
		}
		colQ, err := database.QuoteIdentifier(dbms, colDef.Name)
		if err != nil {
			return "", err
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableQualified, colQ, colDef.DataType)
		if !colDef.Nullable {
			stmt += " NOT NULL"
		}
		if colDef.Default != nil {
			stmt += " DEFAULT " + *colDef.Default
		}
		return stmt + ";", nil

	case ActionModifyColumn:
		colDef, ok := ch.Details.(ColumnDef)
		if !ok {
			return "", errors.New("metadata ColumnDef tidak valid")
		}
		colQ, err := database.QuoteIdentifier(dbms, colDef.Name)
		if err != nil {
			return "", err
		}

		if dbms == "mysql" {
			stmt := fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", tableQualified, colQ, colDef.DataType)
			if !colDef.Nullable {
				stmt += " NOT NULL"
			} else {
				stmt += " NULL"
			}
			if colDef.Default != nil {
				stmt += " DEFAULT " + *colDef.Default
			}
			return stmt + ";", nil
		}

		// PostgreSQL: ALTER COLUMN syntax
		var pgParts []string
		pgParts = append(pgParts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", tableQualified, colQ, colDef.DataType))
		if !colDef.Nullable {
			pgParts = append(pgParts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", tableQualified, colQ))
		} else {
			pgParts = append(pgParts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", tableQualified, colQ))
		}
		if colDef.Default != nil {
			pgParts = append(pgParts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", tableQualified, colQ, *colDef.Default))
		}
		return strings.Join(pgParts, "\n"), nil

	case ActionAddConstraint:
		conDef, ok := ch.Details.(ConstraintDef)
		if !ok {
			return "", errors.New("metadata ConstraintDef tidak valid")
		}
		conQ, err := database.QuoteIdentifier(dbms, conDef.Name)
		if err != nil {
			return "", err
		}
		var conCols []string
		for _, col := range conDef.Columns {
			q, err := database.QuoteIdentifier(dbms, col)
			if err != nil {
				return "", err
			}
			conCols = append(conCols, q)
		}

		var conBody string
		if conDef.Definition != "" {
			conBody = conDef.Definition
		} else if strings.EqualFold(conDef.Type, "PRIMARY KEY") {
			conBody = fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(conCols, ", "))
		} else if strings.EqualFold(conDef.Type, "UNIQUE") {
			conBody = fmt.Sprintf("UNIQUE (%s)", strings.Join(conCols, ", "))
		} else {
			conBody = fmt.Sprintf("%s (%s)", conDef.Type, strings.Join(conCols, ", "))
		}

		return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s %s;", tableQualified, conQ, conBody), nil

	case ActionCreateIndex:
		idxDef, ok := ch.Details.(IndexDef)
		if !ok {
			return "", errors.New("metadata IndexDef tidak valid")
		}
		idxQ, err := database.QuoteIdentifier(dbms, idxDef.Name)
		if err != nil {
			return "", err
		}
		var idxCols []string
		for _, col := range idxDef.Columns {
			q, err := database.QuoteIdentifier(dbms, col)
			if err != nil {
				return "", err
			}
			idxCols = append(idxCols, q)
		}

		uniqueStr := ""
		if idxDef.Unique {
			uniqueStr = "UNIQUE "
		}
		return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);", uniqueStr, idxQ, tableQualified, strings.Join(idxCols, ", ")), nil

	case ActionDropIndex:
		idxQ, err := database.QuoteIdentifier(dbms, ch.Object)
		if err != nil {
			return "", err
		}
		if dbms == "mysql" {
			return fmt.Sprintf("DROP INDEX %s ON %s;", idxQ, tableQualified), nil
		}
		return fmt.Sprintf("DROP INDEX %s.%s;", schemaQ, idxQ), nil

	case ActionDropConstraint:
		conQ, err := database.QuoteIdentifier(dbms, ch.Object)
		if err != nil {
			return "", err
		}
		if dbms == "mysql" {
			if strings.Contains(strings.ToLower(ch.OldValue), "foreign") {
				return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", tableQualified, conQ), nil
			}
			return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tableQualified, conQ), nil
		}
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", tableQualified, conQ), nil

	case ActionDropColumn:
		colQ, err := database.QuoteIdentifier(dbms, ch.Object)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", tableQualified, colQ), nil

	case ActionDropTable:
		return fmt.Sprintf("DROP TABLE %s;", tableQualified), nil

	default:
		return "", fmt.Errorf("tipe perubahan tidak dikenali: %s", ch.Action)
	}
}
