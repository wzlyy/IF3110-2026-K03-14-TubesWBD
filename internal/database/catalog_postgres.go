package database

import (
	"context"
	"database/sql"
	"encoding/json"
)

const userSchemaPredicate = `n.nspname <> 'information_schema' AND n.nspname !~ '^pg_'`

func loadPostgresCatalog(ctx context.Context, tx *sql.Tx, dbName string) (*Catalog, error) {
	c := &Catalog{DBMS: "postgres", Name: dbName}
	schemas := map[string]int{}
	tables := map[int64]*Table{}
	// Keep empty schemas visible, and only expose actual user tables (not views
	// whose functions/rules might introduce side effects during browsing).
	rows, err := tx.QueryContext(ctx, `SELECT n.nspname FROM pg_catalog.pg_namespace n WHERE `+userSchemaPredicate+` ORDER BY n.nspname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		schemas[name] = len(c.Schemas)
		c.Schemas = append(c.Schemas, Schema{Name: name})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.oid::bigint,n.nspname,t.relname FROM pg_catalog.pg_class t JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace WHERE t.relkind IN ('r','p') AND `+userSchemaPredicate+` ORDER BY n.nspname,t.relname`)
	if err != nil {
		return nil, err
	}
	// Store temporary pointers independently of growing schema slices.
	type relation struct {
		oid    int64
		schema string
	}
	var relations []relation
	for rows.Next() {
		var oid int64
		var schema, name string
		if err = rows.Scan(&oid, &schema, &name); err != nil {
			rows.Close()
			return nil, err
		}
		tables[oid] = &Table{Schema: schema, Name: name, validated: true}
		relations = append(relations, relation{oid, schema})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.oid::bigint,a.attname,a.attnum,pg_catalog.format_type(a.atttypid,a.atttypmod),NOT (a.attnotnull OR typ.typnotnull),pg_catalog.pg_get_expr(d.adbin,d.adrelid),a.attidentity::text,a.attgenerated::text
 FROM pg_catalog.pg_class t JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid JOIN pg_catalog.pg_type typ ON typ.oid=a.atttypid LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=t.oid AND d.adnum=a.attnum
 WHERE t.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped AND `+userSchemaPredicate+` ORDER BY t.oid,a.attnum`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var oid int64
		var col Column
		if err = rows.Scan(&oid, &col.Name, &col.Position, &col.DataType, &col.Nullable, &col.Default, &col.Identity, &col.Generated); err != nil {
			rows.Close()
			return nil, err
		}
		if t := tables[oid]; t != nil {
			t.Columns = append(t.Columns, col)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.oid::bigint,con.conname,CASE con.contype WHEN 'p' THEN 'PRIMARY KEY' WHEN 'f' THEN 'FOREIGN KEY' WHEN 'u' THEN 'UNIQUE' WHEN 'c' THEN 'CHECK' WHEN 'x' THEN 'EXCLUDE' ELSE con.contype::text END,pg_catalog.pg_get_constraintdef(con.oid,true),
 COALESCE((SELECT json_agg(a.attname ORDER BY k.ordinality)::text FROM unnest(con.conkey) WITH ORDINALITY k(attnum,ordinality) JOIN pg_catalog.pg_attribute a ON a.attrelid=t.oid AND a.attnum=k.attnum),'[]')
 FROM pg_catalog.pg_constraint con JOIN pg_catalog.pg_class t ON t.oid=con.conrelid JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace WHERE t.relkind IN ('r','p') AND `+userSchemaPredicate+` ORDER BY t.oid,con.conname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var oid int64
		var con Constraint
		var columns string
		if err = rows.Scan(&oid, &con.Name, &con.Type, &con.Definition, &columns); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(columns), &con.Columns); err != nil {
			rows.Close()
			return nil, err
		}
		if t := tables[oid]; t != nil {
			t.Constraints = append(t.Constraints, con)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT t.oid::bigint,i.relname,am.amname,ix.indisunique,ix.indisprimary,pg_catalog.pg_get_indexdef(i.oid),
 COALESCE((SELECT json_agg(pg_catalog.pg_get_indexdef(i.oid,k,true) ORDER BY k)::text FROM generate_series(1,ix.indnkeyatts) k),'[]')
 FROM pg_catalog.pg_index ix JOIN pg_catalog.pg_class t ON t.oid=ix.indrelid JOIN pg_catalog.pg_namespace n ON n.oid=t.relnamespace JOIN pg_catalog.pg_class i ON i.oid=ix.indexrelid JOIN pg_catalog.pg_am am ON am.oid=i.relam
 WHERE t.relkind IN ('r','p') AND `+userSchemaPredicate+` ORDER BY t.oid,i.relname`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var oid int64
		var index Index
		var columns string
		if err = rows.Scan(&oid, &index.Name, &index.Type, &index.Unique, &index.Primary, &index.Definition, &columns); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(columns), &index.Columns); err != nil {
			rows.Close()
			return nil, err
		}
		if t := tables[oid]; t != nil {
			t.Indexes = append(t.Indexes, index)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, r := range relations {
		idx := schemas[r.schema]
		c.Schemas[idx].Tables = append(c.Schemas[idx].Tables, *tables[r.oid])
	}
	return c, nil
}
