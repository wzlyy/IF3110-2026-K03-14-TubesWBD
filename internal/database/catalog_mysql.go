package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// loadMySQLCatalog confines every metadata query to the connection's configured
// database. MySQL's schema and database names are the same namespace.
func loadMySQLCatalog(ctx context.Context, tx *sql.Tx, dbName string) (*Catalog, error) {
	var selected sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&selected); err != nil {
		return nil, err
	}
	if !selected.Valid || selected.String != dbName {
		return nil, ErrTargetUnavailable
	}
	c := &Catalog{DBMS: "mysql", Name: dbName}
	var schemaName string
	err := tx.QueryRowContext(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", dbName).Scan(&schemaName)
	if err == sql.ErrNoRows {
		return nil, ErrTargetUnavailable
	}
	if err != nil {
		return nil, err
	}
	c.Schemas = []Schema{{Name: schemaName}}
	tables := map[string]*Table{}
	var tableNames []string
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=? AND TABLE_TYPE='BASE TABLE' ORDER BY TABLE_NAME", dbName)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		tables[name] = &Table{Schema: dbName, Name: name, validated: true}
		tableNames = append(tableNames, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT TABLE_NAME,COLUMN_NAME,ORDINAL_POSITION,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,EXTRA,GENERATION_EXPRESSION
 FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME,ORDINAL_POSITION`, dbName)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, nullable, extra, expression string
		var col Column
		if err = rows.Scan(&table, &col.Name, &col.Position, &col.DataType, &nullable, &col.Default, &extra, &expression); err != nil {
			break
		}
		col.Nullable = nullable == "YES"
		if strings.Contains(extra, "auto_increment") {
			col.Identity = "auto_increment"
		}
		if expression != "" {
			col.Generated = extra + " (" + expression + ")"
		}
		if t := tables[table]; t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}

	type constraintParts struct {
		table, enforced, refSchema, refTable, updateRule, deleteRule, check string
		constraint                                                          Constraint
		refColumns                                                          []string
	}
	var constraints []*constraintParts
	byConstraint := map[string]*constraintParts{}
	rows, err = tx.QueryContext(ctx, `SELECT tc.TABLE_NAME,tc.CONSTRAINT_NAME,tc.CONSTRAINT_TYPE,tc.ENFORCED,
 k.COLUMN_NAME,k.REFERENCED_TABLE_SCHEMA,k.REFERENCED_TABLE_NAME,k.REFERENCED_COLUMN_NAME,
 rc.UPDATE_RULE,rc.DELETE_RULE,cc.CHECK_CLAUSE
 FROM information_schema.TABLE_CONSTRAINTS tc
 LEFT JOIN information_schema.KEY_COLUMN_USAGE k ON k.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA
   AND k.CONSTRAINT_NAME=tc.CONSTRAINT_NAME AND k.TABLE_SCHEMA=tc.TABLE_SCHEMA AND k.TABLE_NAME=tc.TABLE_NAME
   AND ((tc.CONSTRAINT_TYPE='FOREIGN KEY' AND k.REFERENCED_TABLE_NAME IS NOT NULL)
     OR (tc.CONSTRAINT_TYPE IN ('PRIMARY KEY','UNIQUE') AND k.REFERENCED_TABLE_NAME IS NULL))
 LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS rc ON rc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA
   AND rc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME AND rc.TABLE_NAME=tc.TABLE_NAME AND tc.CONSTRAINT_TYPE='FOREIGN KEY'
 LEFT JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA=tc.CONSTRAINT_SCHEMA
   AND cc.CONSTRAINT_NAME=tc.CONSTRAINT_NAME AND tc.CONSTRAINT_TYPE='CHECK'
 WHERE tc.TABLE_SCHEMA=? ORDER BY tc.TABLE_NAME,tc.CONSTRAINT_NAME,tc.CONSTRAINT_TYPE,k.ORDINAL_POSITION`, dbName)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, kind, enforced string
		var column, refSchema, refTable, refColumn, update, deletion, check sql.NullString
		if err = rows.Scan(&table, &name, &kind, &enforced, &column, &refSchema, &refTable, &refColumn, &update, &deletion, &check); err != nil {
			break
		}
		if tables[table] == nil {
			continue
		}
		key := table + "\x00" + name + "\x00" + kind
		parts := byConstraint[key]
		if parts == nil {
			parts = &constraintParts{table: table, enforced: enforced, constraint: Constraint{Name: name, Type: kind}}
			byConstraint[key] = parts
			constraints = append(constraints, parts)
		}
		if column.Valid {
			parts.constraint.Columns = append(parts.constraint.Columns, column.String)
		}
		if refColumn.Valid {
			parts.refColumns = append(parts.refColumns, refColumn.String)
		}
		parts.refSchema, parts.refTable = refSchema.String, refTable.String
		parts.updateRule, parts.deleteRule, parts.check = update.String, deletion.String, check.String
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	type indexParts struct {
		table string
		index Index
		keys  []string
	}
	var indexes []*indexParts
	byIndex := map[string]*indexParts{}
	rows, err = tx.QueryContext(ctx, `SELECT TABLE_NAME,INDEX_NAME,INDEX_TYPE,NON_UNIQUE,COLUMN_NAME,EXPRESSION,SUB_PART,COLLATION
 FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=? ORDER BY TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX`, dbName)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var table, name, kind string
		var nonUnique int
		var column, expression, collation sql.NullString
		var subPart sql.NullInt64
		if err = rows.Scan(&table, &name, &kind, &nonUnique, &column, &expression, &subPart, &collation); err != nil {
			break
		}
		if tables[table] == nil {
			continue
		}
		key := table + "\x00" + name
		parts := byIndex[key]
		if parts == nil {
			parts = &indexParts{table: table, index: Index{Name: name, Type: kind, Unique: nonUnique == 0, Primary: name == "PRIMARY"}}
			byIndex[key] = parts
			indexes = append(indexes, parts)
		}
		var keyPart string
		if column.Valid {
			parts.index.Columns = append(parts.index.Columns, column.String)
			keyPart, err = QuoteIdentifier("mysql", column.String)
			if err != nil {
				break
			}
		} else if expression.Valid {
			parts.index.Columns = append(parts.index.Columns, expression.String)
			keyPart = "(" + expression.String + ")"
		}
		if subPart.Valid {
			keyPart += fmt.Sprintf("(%d)", subPart.Int64)
		}
		if collation.String == "D" {
			keyPart += " DESC"
		}
		parts.keys = append(parts.keys, keyPart)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, p := range indexes {
		quoted, err := QuoteIdentifier("mysql", p.index.Name)
		if err != nil {
			return nil, err
		}
		prefix := "INDEX "
		if p.index.Primary {
			prefix = "PRIMARY KEY "
		} else if p.index.Unique {
			prefix = "UNIQUE INDEX "
		}
		p.index.Definition = prefix + quoted + " USING " + p.index.Type + " (" + strings.Join(p.keys, ", ") + ")"
		tables[p.table].Indexes = append(tables[p.table].Indexes, p.index)
	}
	for _, p := range constraints {
		// KEY_COLUMN_USAGE omits functional key parts. STATISTICS has the full
		// ordered index definition for both ordinary and functional UNIQUE keys.
		if p.constraint.Type == "UNIQUE" {
			if index := byIndex[p.table+"\x00"+p.constraint.Name]; index != nil && index.index.Unique {
				p.constraint.Columns = append([]string(nil), index.index.Columns...)
				p.constraint.Definition = index.index.Definition
			}
		}
		if p.constraint.Definition == "" {
			def, err := mysqlConstraintDefinition(p.constraint, p.refSchema, p.refTable, p.refColumns, p.updateRule, p.deleteRule, p.check, p.enforced)
			if err != nil {
				return nil, err
			}
			p.constraint.Definition = def
		}
		tables[p.table].Constraints = append(tables[p.table].Constraints, p.constraint)
	}
	// Preserve the TABLE_NAME ordering returned by information_schema.
	for _, name := range tableNames {
		c.Schemas[0].Tables = append(c.Schemas[0].Tables, *tables[name])
	}
	return c, nil
}

func mysqlConstraintDefinition(c Constraint, refSchema, refTable string, refColumns []string, updateRule, deleteRule, check, enforced string) (string, error) {
	var keys []string
	for _, name := range c.Columns {
		q, err := QuoteIdentifier("mysql", name)
		if err != nil {
			return "", err
		}
		keys = append(keys, q)
	}
	switch c.Type {
	case "PRIMARY KEY", "UNIQUE":
		return c.Type + " (" + strings.Join(keys, ", ") + ")", nil
	case "CHECK":
		def := "CHECK (" + check + ")"
		if enforced == "NO" {
			def += " NOT ENFORCED"
		}
		return def, nil
	case "FOREIGN KEY":
		schema, err := QuoteIdentifier("mysql", refSchema)
		if err != nil {
			return "", err
		}
		table, err := QuoteIdentifier("mysql", refTable)
		if err != nil {
			return "", err
		}
		var references []string
		for _, name := range refColumns {
			q, e := QuoteIdentifier("mysql", name)
			if e != nil {
				return "", e
			}
			references = append(references, q)
		}
		return "FOREIGN KEY (" + strings.Join(keys, ", ") + ") REFERENCES " + schema + "." + table + " (" + strings.Join(references, ", ") + ") ON UPDATE " + updateRule + " ON DELETE " + deleteRule, nil
	default:
		return c.Type + " (" + strings.Join(keys, ", ") + ")", nil
	}
}
