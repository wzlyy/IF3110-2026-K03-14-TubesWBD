package migration

import (
	"errors"
	"fmt"
	"sort"
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
	if diff.DBMS != "" && diff.DBMS != dbms {
		return "", fmt.Errorf("%w: DBMS diff tidak sesuai target", ErrInvalidTargetSchema)
	}
	if diff.IsIdentical || len(diff.Changes) == 0 {
		return "", nil
	}

	// Group changes by action
	buckets := make(map[DiffAction][]SchemaChange)
	for _, c := range diff.Changes {
		switch c.Action {
		case ActionDropConstraint, ActionDropIndex, ActionCreateTable, ActionAddColumn, ActionModifyColumn, ActionDropColumn, ActionDropTable, ActionAddConstraint, ActionCreateIndex:
		default:
			return "", fmt.Errorf("aksi perubahan belum didukung: %s", c.Action)
		}
		if strings.EqualFold(c.Table, "_dbplatform_migration_receipts") {
			return "", fmt.Errorf("%w: tabel bukti migration internal tidak boleh diubah", ErrInvalidTargetSchema)
		}
		buckets[c.Action] = append(buckets[c.Action], c)
		if c.Action == ActionCreateTable {
			table, ok := c.Details.(TableDef)
			if !ok {
				return "", errors.New("metadata TableDef tidak valid")
			}
			// Create every table before attaching foreign keys and other objects.
			for _, con := range table.Constraints {
				buckets[ActionAddConstraint] = append(buckets[ActionAddConstraint], SchemaChange{Action: ActionAddConstraint, Schema: c.Schema, Table: c.Table, Object: con.Name, Details: con})
			}
			for _, idx := range table.Indexes {
				buckets[ActionCreateIndex] = append(buckets[ActionCreateIndex], SchemaChange{Action: ActionCreateIndex, Schema: c.Schema, Table: c.Table, Object: idx.Name, Details: idx})
			}
		}
	}

	// Remove old dependencies before replacing objects with the same names.
	// Attach keys after table/column changes, then restore foreign keys.
	orderedActions := []DiffAction{
		ActionDropConstraint,
		ActionDropIndex,
		ActionCreateTable,
		ActionAddColumn,
		ActionModifyColumn,
		ActionDropColumn,
		ActionDropTable,
		ActionAddConstraint,
		ActionCreateIndex,
		DiffAction("ADD_FOREIGN_KEY"),
	}
	// A foreign key can reference a standalone UNIQUE index, so all indexes
	// must exist before attaching foreign keys as well as primary/unique keys.
	var keys []SchemaChange
	for _, change := range buckets[ActionAddConstraint] {
		con, _ := change.Details.(ConstraintDef)
		if strings.EqualFold(con.Type, "FOREIGN KEY") {
			buckets[DiffAction("ADD_FOREIGN_KEY")] = append(buckets[DiffAction("ADD_FOREIGN_KEY")], change)
		} else {
			keys = append(keys, change)
		}
	}
	buckets[ActionAddConstraint] = keys
	// Foreign keys must be removed before their referenced keys, then restored
	// after primary/unique keys. Stable sorting preserves deterministic output.
	sort.SliceStable(buckets[ActionDropConstraint], func(i, j int) bool {
		return strings.EqualFold(buckets[ActionDropConstraint][i].OldValue, "FOREIGN KEY") && !strings.EqualFold(buckets[ActionDropConstraint][j].OldValue, "FOREIGN KEY")
	})

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
		} else {
			pgParts = append(pgParts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP DEFAULT;", tableQualified, colQ))
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
			if strings.EqualFold(ch.OldValue, "PRIMARY KEY") {
				return fmt.Sprintf("ALTER TABLE %s DROP PRIMARY KEY;", tableQualified), nil
			}
			if strings.EqualFold(ch.OldValue, "UNIQUE") {
				return fmt.Sprintf("ALTER TABLE %s DROP INDEX %s;", tableQualified, conQ), nil
			}
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
