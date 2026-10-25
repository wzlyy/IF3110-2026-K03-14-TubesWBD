package migration

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

// MySQL column, constraint and index names are case-insensitive.
// Table/schema names use the server setting in tableKey instead.
func objectKey(dbms, name string) string {
	if dbms == "mysql" {
		return strings.ToLower(name)
	}
	return name
}

func tableKey(catalog *database.Catalog, schema, table string) string {
	if catalog.DBMS == "mysql" && catalog.LowerCaseTableNames != 0 {
		schema = strings.ToLower(schema)
		table = strings.ToLower(table)
	}
	return schema + "\x00" + table
}

func validateTargetSchema(def *TargetSchemaDefinition, dbms string, catalogs ...*database.Catalog) error {
	invalid := func(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidTargetSchema, reason) }
	if def.DBMS != "" && def.DBMS != "postgres" && def.DBMS != "mysql" {
		return invalid("DBMS harus postgres atau mysql")
	}
	tables := map[string]bool{}
	relations := map[string]bool{}
	for _, sc := range def.Schemas {
		for _, tbl := range sc.Tables {
			schema := tbl.Schema
			if schema == "" {
				schema = sc.Name
			}
			if tbl.Schema != "" && sc.Name != "" && tbl.Schema != sc.Name {
				return invalid("schema tabel harus sesuai dengan container schemas")
			}
			key := schema + "\x00" + tbl.Name
			if tables[key] || strings.TrimSpace(tbl.Name) == "" || strings.ContainsRune(schema+tbl.Name, 0) {
				return invalid("nama tabel kosong, duplikat, atau tidak valid")
			}
			tables[key] = true
			if dbms == "postgres" && relations[key] {
				return invalid("nama tabel/index berkonflik dalam schema")
			}
			relations[key] = true
			if strings.EqualFold(tbl.Name, "_dbplatform_migration_receipts") {
				return invalid("tabel bukti migration internal tidak boleh diubah")
			}
			if len(tbl.Columns) == 0 {
				return invalid("tabel harus mempunyai kolom")
			}
			columns := map[string]bool{}
			for _, col := range tbl.Columns {
				k := objectKey(dbms, col.Name)
				if columns[k] || strings.TrimSpace(k) == "" || strings.ContainsRune(k, 0) || strings.TrimSpace(col.DataType) == "" {
					return invalid("nama kolom kosong/duplikat atau tipe tidak valid")
				}
				columns[k] = true
			}
			checkColumns := func(names []string) bool {
				seen := map[string]bool{}
				for _, name := range names {
					k := objectKey(dbms, name)
					if !columns[k] || seen[k] {
						return false
					}
					seen[k] = true
				}
				return true
			}
			if !checkColumns(tbl.PrimaryKey) {
				return invalid("primary key merujuk kolom yang tidak ada atau duplikat")
			}
			names := map[string]bool{}
			primary := len(tbl.PrimaryKey) > 0
			for _, con := range tbl.Constraints {
				k, kind := objectKey(dbms, con.Name), strings.ToUpper(strings.TrimSpace(con.Type))
				if strings.TrimSpace(k) == "" || names[k] || strings.ContainsRune(k, 0) || !checkColumns(con.Columns) {
					return invalid("nama atau kolom constraint tidak valid/duplikat")
				}
				names[k] = true
				switch kind {
				case "PRIMARY KEY":
					if primary {
						return invalid("primary key didefinisikan lebih dari sekali")
					}
					primary = true
					fallthrough
				case "UNIQUE":
					if len(con.Columns) == 0 {
						return invalid("key harus mempunyai kolom")
					}
				case "CHECK", "FOREIGN KEY", "EXCLUDE":
					if strings.TrimSpace(con.Definition) == "" {
						return invalid("constraint ini memerlukan definition SQL")
					}
					if kind == "EXCLUDE" && dbms == "mysql" {
						return invalid("MySQL tidak mendukung EXCLUDE")
					}
				default:
					return invalid("jenis constraint belum didukung")
				}
				if dbms == "postgres" && (kind == "PRIMARY KEY" || kind == "UNIQUE" || kind == "EXCLUDE") {
					rk := schema + "\x00" + con.Name
					if relations[rk] {
						return invalid("nama index milik constraint berkonflik dalam schema")
					}
					relations[rk] = true
				}
			}
			if len(tbl.PrimaryKey) > 0 {
				name := tbl.Name + "_pkey"
				if dbms == "mysql" {
					name = "PRIMARY"
				}
				if names[objectKey(dbms, name)] {
					return invalid("nama primary key berkonflik dengan constraint")
				}
				names[objectKey(dbms, name)] = true
				if dbms == "postgres" {
					rk := schema + "\x00" + name
					if relations[rk] {
						return invalid("nama index primary key berkonflik")
					}
					relations[rk] = true
				}
			}
			for _, idx := range tbl.Indexes {
				k := objectKey(dbms, idx.Name)
				if strings.TrimSpace(k) == "" || names[k] || strings.ContainsRune(k, 0) || len(idx.Columns) == 0 || !checkColumns(idx.Columns) {
					return invalid("nama atau kolom index tidak valid/duplikat")
				}
				names[k] = true
				if dbms == "postgres" {
					rk := schema + "\x00" + idx.Name
					if relations[rk] {
						return invalid("nama index berkonflik dalam schema")
					}
					relations[rk] = true
				}
			}
		}
	}
	// FK definitions are SQL fragments in the existing model. Validate the
	// supported identifier-list form, never silently accept an unparsed reference.
	if dbms == "" {
		return nil
	} // CompareCatalogs supplies the actual dialect.
	for _, sc := range def.Schemas {
		for _, tbl := range sc.Tables {
			for _, con := range tbl.Constraints {
				if !strings.EqualFold(con.Type, "FOREIGN KEY") {
					continue
				}
				local, refSchema, refTable, remote, err := foreignKeyColumns(dbms, con.Definition)
				if err != nil {
					return invalid(err.Error())
				}
				if len(con.Columns) > 0 && !equalColumns(dbms, local, con.Columns) {
					return invalid("kolom FK berbeda dari definition")
				}
				if len(local) != len(remote) {
					return invalid("jumlah kolom FK dan referensinya berbeda")
				}
				if refSchema == "" {
					refSchema = sc.Name
					if dbms != "mysql" {
						refSchema = "public"
					}
				}
				var parent *TableDef
				catalog := &database.Catalog{DBMS: dbms}
				if len(catalogs) > 0 {
					catalog = catalogs[0]
				}
				for _, ps := range def.Schemas {
					for i := range ps.Tables {
						p := &ps.Tables[i]
						pSchema := p.Schema
						if pSchema == "" {
							pSchema = ps.Name
						}
						if tableKey(catalog, pSchema, p.Name) == tableKey(catalog, refSchema, refTable) {
							parent = p
						}
					}
				}
				if parent == nil && dbms == "mysql" && len(catalogs) == 0 {
					continue
				} // Wait for the actual server setting.
				if parent == nil {
					return invalid("tabel referensi FK tidak ada pada skema tujuan")
				}
				for _, pair := range []struct {
					table   *TableDef
					columns []string
				}{{&tbl, local}, {parent, remote}} {
					seen := map[string]bool{}
					for _, name := range pair.columns {
						found := false
						for _, col := range pair.table.Columns {
							found = found || objectKey(dbms, col.Name) == objectKey(dbms, name)
						}
						k := objectKey(dbms, name)
						if !found || seen[k] {
							return invalid("kolom referensi FK tidak ada atau duplikat")
						}
						seen[k] = true
					}
				}
			}
		}
	}
	return nil
}

// Recognize only FOREIGN KEY (identifiers) REFERENCES [schema.]table (identifiers).
// Quoted names (including escaped quote characters) remain single identifiers.
const schemaIdentifierPattern = `(?:"(?:[^"]|"")+"|` + "`(?:[^`]|``)+`" + `|[\pL_][\pL\pN_$]*)`

var foreignKeyPattern = regexp.MustCompile(`(?is)^\s*FOREIGN\s+KEY\s*\((` + schemaIdentifierPattern + `(?:\s*,\s*` + schemaIdentifierPattern + `)*)\)\s+REFERENCES\s+(` + schemaIdentifierPattern + `)(?:\s*\.\s*(` + schemaIdentifierPattern + `))?\s*\((` + schemaIdentifierPattern + `(?:\s*,\s*` + schemaIdentifierPattern + `)*)\)(.*)$`)
var schemaIdentifierRegexp = regexp.MustCompile(schemaIdentifierPattern)

func foreignKeyColumns(dbms, definition string) (local []string, schema, table string, remote []string, err error) {
	match := foreignKeyPattern.FindStringSubmatch(definition)
	if match == nil {
		return nil, "", "", nil, fmt.Errorf("definition FK belum didukung; gunakan FOREIGN KEY (kolom) REFERENCES [schema.]tabel (kolom)")
	}
	decode := func(value string) string {
		if value[0] == '"' || value[0] == '`' {
			quote := value[:1]
			return strings.ReplaceAll(value[1:len(value)-1], quote+quote, quote)
		}
		if dbms != "mysql" {
			return strings.ToLower(value)
		}
		return value
	}
	for _, name := range schemaIdentifierRegexp.FindAllString(match[1], -1) {
		local = append(local, decode(name))
	}
	table = decode(match[2])
	if match[3] != "" {
		schema = table
		table = decode(match[3])
	}
	for _, name := range schemaIdentifierRegexp.FindAllString(match[4], -1) {
		remote = append(remote, decode(name))
	}
	return
}

func desiredConstraints(table TableDef, dbms string, current []database.Constraint) []ConstraintDef {
	constraints := slices.Clone(table.Constraints)
	for i := range constraints {
		constraints[i].Type = strings.ToUpper(strings.TrimSpace(constraints[i].Type))
		if constraints[i].Type == "PRIMARY KEY" {
			for _, con := range current {
				if con.Type == "PRIMARY KEY" {
					constraints[i].Name = con.Name
					break
				}
			}
		}
	}
	if len(table.PrimaryKey) > 0 {
		name := table.Name + "_pkey"
		if dbms == "mysql" {
			name = "PRIMARY"
		}
		// A shorthand requests a key on these columns, not a constraint rename.
		for _, con := range current {
			if con.Type == "PRIMARY KEY" {
				name = con.Name
				break
			}
		}
		constraints = append(constraints, ConstraintDef{Name: name, Type: "PRIMARY KEY", Columns: table.PrimaryKey})
	}
	return constraints
}

func equalColumns(dbms string, a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if objectKey(dbms, a[i]) != objectKey(dbms, b[i]) {
			return false
		}
	}
	return true
}

func equalConstraint(dbms string, old database.Constraint, next ConstraintDef, defaultSchema string, tableCaseMode int) bool {
	if !strings.EqualFold(old.Type, next.Type) || next.Type != "CHECK" && len(next.Columns) > 0 && !equalColumns(dbms, old.Columns, next.Columns) {
		return false
	}
	if next.Definition != "" {
		if strings.EqualFold(next.Type, "FOREIGN KEY") {
			a, as, at, ar, ae := foreignKeyColumns(dbms, old.Definition)
			b, bs, bt, br, be := foreignKeyColumns(dbms, next.Definition)
			if ae != nil || be != nil {
				return false
			}
			if dbms != "mysql" {
				defaultSchema = "public"
			}
			if as == "" {
				as = defaultSchema
			}
			if bs == "" {
				bs = defaultSchema
			}
			catalog := &database.Catalog{DBMS: dbms, LowerCaseTableNames: tableCaseMode}
			if tableKey(catalog, as, at) != tableKey(catalog, bs, bt) || !equalColumns(dbms, a, b) || !equalColumns(dbms, ar, br) {
				return false
			}
			// References and ordered columns were compared structurally above.
			// Compare only the remaining FK options, preserving action changes.
			return canonicalConstraint(dbms, "FOREIGN KEY", foreignKeyPattern.FindStringSubmatch(old.Definition)[5]) == canonicalConstraint(dbms, "FOREIGN KEY", foreignKeyPattern.FindStringSubmatch(next.Definition)[5])
		}
		return canonicalConstraint(dbms, old.Type, old.Definition) == canonicalConstraint(dbms, next.Type, next.Definition)
	}
	return next.Type == "PRIMARY KEY" || next.Type == "UNIQUE"
}

func hasIndex(indexes []IndexDef, name, dbms string) bool {
	for _, idx := range indexes {
		if objectKey(dbms, idx.Name) == objectKey(dbms, name) {
			return true
		}
	}
	return false
}

func supportsForeignKey(index database.Index, constraints []ConstraintDef) bool {
	for _, con := range constraints {
		if con.Type == "FOREIGN KEY" && len(con.Columns) > 0 && len(index.Columns) >= len(con.Columns) && equalColumns("mysql", index.Columns[:len(con.Columns)], con.Columns) {
			return true
		}
	}
	return false
}

func reattachForeignKeys(current *database.Catalog, desired map[string]TableDef, result *DiffResult) {
	rebuild := map[string]bool{}
	dropping, adding := map[string]bool{}, map[string]bool{}
	for _, change := range result.Changes {
		key := change.Schema + "\x00" + change.Table + "\x00" + change.Object
		if change.Action == ActionDropConstraint {
			dropping[key] = true
		}
		if change.Action == ActionAddConstraint {
			adding[key] = true
		}
		if change.Action == ActionModifyColumn || change.Action == ActionDropColumn || change.Action == ActionDropIndex || change.Action == ActionDropConstraint && change.OldValue != "FOREIGN KEY" {
			rebuild[tableKey(current, change.Schema, change.Table)] = true
		}
	}
	if len(rebuild) == 0 {
		return
	}
	for _, schema := range current.Schemas {
		for _, table := range schema.Tables {
			next, retained := desired[tableKey(current, table.Schema, table.Name)]
			if !retained {
				continue
			}
			for _, con := range table.Constraints {
				if con.Type != "FOREIGN KEY" {
					continue
				}
				_, refSchema, refTable, _, err := foreignKeyColumns(result.DBMS, con.Definition)
				if err != nil {
					continue
				} // Unsupported desired definitions were rejected by validation.
				if refSchema == "" {
					refSchema = "public"
					if result.DBMS == "mysql" {
						refSchema = table.Schema
					}
				}
				if !rebuild[tableKey(current, table.Schema, table.Name)] && !rebuild[tableKey(current, refSchema, refTable)] {
					continue
				}
				key := table.Schema + "\x00" + table.Name + "\x00" + con.Name
				if dropping[key] {
					continue
				}
				for _, wanted := range next.Constraints {
					if objectKey(result.DBMS, wanted.Name) != objectKey(result.DBMS, con.Name) || adding[key] {
						continue
					}
					result.Changes = append(result.Changes,
						SchemaChange{Action: ActionDropConstraint, Schema: table.Schema, Table: table.Name, Object: con.Name, OldValue: con.Type, Destructive: true, Description: "Lepas sementara foreign key " + con.Name, Details: con},
						SchemaChange{Action: ActionAddConstraint, Schema: table.Schema, Table: table.Name, Object: wanted.Name, Description: "Pasang kembali foreign key " + wanted.Name, Details: wanted})
				}
			}
		}
	}
}

func equalIndex(dbms string, old database.Index, next IndexDef) bool {
	return old.Unique == next.Unique && equalColumns(dbms, old.Columns, next.Columns)
}

func constraintOwnsIndex(constraints []database.Constraint, name string) bool {
	for _, con := range constraints {
		if con.Name == name && (con.Type == "PRIMARY KEY" || con.Type == "UNIQUE" || con.Type == "EXCLUDE") {
			return true
		}
	}
	return false
}

// Catalogs add harmless quoting, whitespace, CHECK parentheses and explicit
// default FK actions. Normalize only those forms; do not rewrite expressions.
func canonicalConstraint(dbms, kind, value string) string {
	var tokens []string
	for i := 0; i < len(value); {
		c := value[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			start, quote := i, c
			i++
			for i < len(value) {
				if value[i] == quote {
					i++
					if i < len(value) && value[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			token := value[start:i]
			if quote != '\'' && len(token) >= 2 {
				name := token[1 : len(token)-1]
				plain := name != "" && !(name[0] >= '0' && name[0] <= '9')
				for j := 0; j < len(name); j++ {
					plain = plain && isIdentifier(name[j])
				}
				if plain && (dbms == "mysql" || name == strings.ToLower(name)) {
					token = strings.ToLower(name)
				}
			}
			tokens = append(tokens, token)
			continue
		}
		if isIdentifier(c) {
			start := i
			for i < len(value) && isIdentifier(value[i]) {
				i++
			}
			tokens = append(tokens, strings.ToLower(value[start:i]))
		} else {
			tokens = append(tokens, string(c))
			i++
		}
	}
	if strings.EqualFold(kind, "FOREIGN KEY") {
		var kept []string
		for i := 0; i < len(tokens); {
			if dbms == "postgres" && tokens[i] == "references" && i+2 < len(tokens) && tokens[i+2] == "(" {
				// Unqualified input uses the documented PostgreSQL default schema.
				kept = append(kept, "references", "public", ".", tokens[i+1])
				i += 2
				continue
			}
			if i+2 < len(tokens) && tokens[i] == "on" && (tokens[i+1] == "update" || tokens[i+1] == "delete") {
				if i+3 < len(tokens) && tokens[i+2] == "no" && tokens[i+3] == "action" {
					i += 4
					continue
				}
				if dbms == "mysql" && tokens[i+2] == "restrict" {
					i += 3
					continue
				}
			}
			kept = append(kept, tokens[i])
			i++
		}
		tokens = kept
	}
	if strings.EqualFold(kind, "CHECK") && len(tokens) > 1 && tokens[0] == "check" {
		expr := tokens[1:]
		for len(expr) >= 2 && expr[0] == "(" && expr[len(expr)-1] == ")" {
			depth, wraps := 0, true
			for i, token := range expr {
				if token == "(" {
					depth++
				}
				if token == ")" {
					depth--
				}
				if depth == 0 && i != len(expr)-1 {
					wraps = false
					break
				}
			}
			if !wraps {
				break
			}
			expr = expr[1 : len(expr)-1]
		}
		tokens = append([]string{"check"}, expr...)
	}
	return strings.Join(tokens, "\x1f")
}
