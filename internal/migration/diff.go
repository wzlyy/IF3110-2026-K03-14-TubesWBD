package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

var (
	ErrInvalidTargetSchema = errors.New("format skema tujuan tidak valid: format JSON tidak sesuai")
	ErrEmptyTargetSchema   = errors.New("skema tujuan tidak boleh kosong")
)

// TargetSchemaDefinition represents the desired schema structure provided as JSON input.
type TargetSchemaDefinition struct {
	DBMS    string      `json:"dbms"`
	Schemas []SchemaDef `json:"schemas"`
	Tables  []TableDef  `json:"tables"` // Convenient flat format if schemas is omitted
}

// SchemaDef defines a database schema container.
type SchemaDef struct {
	Name   string     `json:"name"`
	Tables []TableDef `json:"tables"`
}

// TableDef defines a desired table structure.
type TableDef struct {
	Schema      string          `json:"schema"`
	Name        string          `json:"name"`
	Columns     []ColumnDef     `json:"columns"`
	PrimaryKey  []string        `json:"primary_key"`
	Constraints []ConstraintDef `json:"constraints"`
	Indexes     []IndexDef      `json:"indexes"`
}

// ColumnDef defines a desired column.
type ColumnDef struct {
	Name     string  `json:"name"`
	DataType string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default"`
}

// ConstraintDef defines a table constraint (PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK).
type ConstraintDef struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Columns    []string `json:"columns"`
	Definition string   `json:"definition"`
}

// IndexDef defines a desired index.
type IndexDef struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
}

// DiffAction indicates the type of schema alteration
type DiffAction string

const (
	ActionCreateTable    DiffAction = "CREATE_TABLE"
	ActionDropTable      DiffAction = "DROP_TABLE"
	ActionAddColumn      DiffAction = "ADD_COLUMN"
	ActionDropColumn     DiffAction = "DROP_COLUMN"
	ActionModifyColumn   DiffAction = "MODIFY_COLUMN"
	ActionAddConstraint  DiffAction = "ADD_CONSTRAINT"
	ActionDropConstraint DiffAction = "DROP_CONSTRAINT"
	ActionCreateIndex    DiffAction = "CREATE_INDEX"
	ActionDropIndex      DiffAction = "DROP_INDEX"
)

// SchemaChange represents a single detected structural difference
type SchemaChange struct {
	Action      DiffAction
	Schema      string
	Table       string
	Object      string
	OldValue    string
	NewValue    string
	Destructive bool
	Description string
	Details     any
}

// DiffResult holds the overall outcome of a schema comparison
type DiffResult struct {
	DBMS        string
	Changes     []SchemaChange
	IsIdentical bool
}

// ParseTargetSchemaJSON parses and validates the JSON representation of the desired schema
func ParseTargetSchemaJSON(raw []byte, defaultSchema string) (*TargetSchemaDefinition, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, ErrEmptyTargetSchema
	}

	var def TargetSchemaDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTargetSchema, err)
	}

	if defaultSchema == "" {
		defaultSchema = "public"
	}

	// Normalize flat tables into Schemas array if tables was provided directly
	if len(def.Schemas) == 0 && len(def.Tables) == 0 {
		return nil, ErrEmptyTargetSchema
	}

	if len(def.Schemas) == 0 && len(def.Tables) > 0 {
		schemaMap := make(map[string][]TableDef)
		for _, tbl := range def.Tables {
			sc := tbl.Schema
			if sc == "" {
				sc = defaultSchema
			}
			tbl.Schema = sc
			schemaMap[sc] = append(schemaMap[sc], tbl)
		}
		for scName, tbls := range schemaMap {
			def.Schemas = append(def.Schemas, SchemaDef{
				Name:   scName,
				Tables: tbls,
			})
		}
	} else {
		// Ensure tables inside schemas have their Schema field populated
		for i := range def.Schemas {
			if def.Schemas[i].Name == "" {
				def.Schemas[i].Name = defaultSchema
			}
			for j := range def.Schemas[i].Tables {
				if def.Schemas[i].Tables[j].Schema == "" {
					def.Schemas[i].Tables[j].Schema = def.Schemas[i].Name
				}
			}
		}
	}

	// Validate tables and columns
	for _, sc := range def.Schemas {
		for _, tbl := range sc.Tables {
			if strings.TrimSpace(tbl.Name) == "" {
				return nil, fmt.Errorf("%w: terdapat tabel tanpa nama", ErrInvalidTargetSchema)
			}
			for _, col := range tbl.Columns {
				if strings.TrimSpace(col.Name) == "" || strings.TrimSpace(col.DataType) == "" {
					return nil, fmt.Errorf("%w: kolom pada tabel %s harus memiliki nama dan tipe data", ErrInvalidTargetSchema, tbl.Name)
				}
			}
		}
	}

	return &def, nil
}

// CompareCatalogs compares the current database catalog with the desired schema definition
func CompareCatalogs(current *database.Catalog, desired *TargetSchemaDefinition) (*DiffResult, error) {
	if current == nil {
		return nil, errors.New("katalog saat ini tidak boleh bernilai nil")
	}
	if desired == nil {
		return nil, errors.New("skema tujuan tidak boleh bernilai nil")
	}

	result := &DiffResult{
		DBMS:    current.DBMS,
		Changes: make([]SchemaChange, 0),
	}

	// Build map of desired tables: "schema.table" -> TableDef
	desiredTables := make(map[string]TableDef)
	for _, sc := range desired.Schemas {
		scName := sc.Name
		if scName == "" {
			if current.DBMS == "mysql" {
				scName = current.Name
			} else {
				scName = "public"
			}
		}
		for _, tbl := range sc.Tables {
			tblSchema := tbl.Schema
			if tblSchema == "" {
				tblSchema = scName
			}
			tbl.Schema = tblSchema
			key := strings.ToLower(tblSchema) + "." + strings.ToLower(tbl.Name)
			desiredTables[key] = tbl
		}
	}

	// Build map of current tables: "schema.table" -> database.Table
	currentTables := make(map[string]database.Table)
	for _, sc := range current.Schemas {
		for _, tbl := range sc.Tables {
			key := strings.ToLower(tbl.Schema) + "." + strings.ToLower(tbl.Name)
			currentTables[key] = tbl
		}
	}

	// 1. Detect Tables to CREATE (in desired but not in current)
	var desiredKeys []string
	for k := range desiredTables {
		desiredKeys = append(desiredKeys, k)
	}
	sort.Strings(desiredKeys)

	for _, key := range desiredKeys {
		desTable := desiredTables[key]
		if _, exists := currentTables[key]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionCreateTable,
				Schema:      desTable.Schema,
				Table:       desTable.Name,
				Object:      desTable.Name,
				NewValue:    fmt.Sprintf("%d kolom", len(desTable.Columns)),
				Destructive: false,
				Description: fmt.Sprintf("Buat tabel baru %s.%s", desTable.Schema, desTable.Name),
				Details:     desTable,
			})
		} else {
			curTable := currentTables[key]
			compareTableDetails(&curTable, &desTable, result)
		}
	}

	// 2. Detect Tables to DROP (in current but not in desired)
	var currentKeys []string
	for k := range currentTables {
		currentKeys = append(currentKeys, k)
	}
	sort.Strings(currentKeys)

	for _, key := range currentKeys {
		curTable := currentTables[key]
		if _, exists := desiredTables[key]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionDropTable,
				Schema:      curTable.Schema,
				Table:       curTable.Name,
				Object:      curTable.Name,
				OldValue:    fmt.Sprintf("%d kolom", len(curTable.Columns)),
				Destructive: true,
				Description: fmt.Sprintf("Hapus tabel %s.%s", curTable.Schema, curTable.Name),
				Details:     curTable,
			})
		}
	}

	result.IsIdentical = len(result.Changes) == 0
	return result, nil
}

func compareTableDetails(cur *database.Table, des *TableDef, result *DiffResult) {
	curColMap := make(map[string]database.Column)
	for _, c := range cur.Columns {
		curColMap[strings.ToLower(c.Name)] = c
	}

	desColMap := make(map[string]ColumnDef)
	for _, c := range des.Columns {
		desColMap[strings.ToLower(c.Name)] = c
	}

	// A. Check for Columns to ADD
	for _, col := range des.Columns {
		k := strings.ToLower(col.Name)
		if _, exists := curColMap[k]; !exists {
			nullDesc := "NULL"
			if !col.Nullable {
				nullDesc = "NOT NULL"
			}
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionAddColumn,
				Schema:      des.Schema,
				Table:       des.Name,
				Object:      col.Name,
				NewValue:    fmt.Sprintf("%s %s", col.DataType, nullDesc),
				Destructive: false,
				Description: fmt.Sprintf("Tambah kolom %s (%s, %s)", col.Name, col.DataType, nullDesc),
				Details:     col,
			})
		} else {
			curCol := curColMap[k]
			if isColumnModified(curCol, col) {
				oldDesc := formatColumnSnapshot(curCol)
				newDesc := formatColumnDef(col)
				result.Changes = append(result.Changes, SchemaChange{
					Action:      ActionModifyColumn,
					Schema:      des.Schema,
					Table:       des.Name,
					Object:      col.Name,
					OldValue:    oldDesc,
					NewValue:    newDesc,
					Destructive: false,
					Description: fmt.Sprintf("Ubah kolom %s: %s -> %s", col.Name, oldDesc, newDesc),
					Details:     col,
				})
			}
		}
	}

	// B. Check for Columns to DROP
	for _, curCol := range cur.Columns {
		k := strings.ToLower(curCol.Name)
		if _, exists := desColMap[k]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionDropColumn,
				Schema:      cur.Schema,
				Table:       cur.Name,
				Object:      curCol.Name,
				OldValue:    curCol.DataType,
				Destructive: true,
				Description: fmt.Sprintf("Hapus kolom %s dari tabel %s.%s", curCol.Name, cur.Schema, cur.Name),
				Details:     curCol,
			})
		}
	}

	// C. Check Constraints
	curConMap := make(map[string]database.Constraint)
	for _, con := range cur.Constraints {
		curConMap[strings.ToLower(con.Name)] = con
	}

	desConMap := make(map[string]ConstraintDef)
	for _, con := range des.Constraints {
		desConMap[strings.ToLower(con.Name)] = con
	}

	// Primary key shorthand in TableDef
	if len(des.PrimaryKey) > 0 {
		pkName := strings.ToLower(des.Name + "_pkey")
		if _, exists := desConMap[pkName]; !exists {
			pkCon := ConstraintDef{
				Name:    des.Name + "_pkey",
				Type:    "PRIMARY KEY",
				Columns: des.PrimaryKey,
			}
			desConMap[pkName] = pkCon
			des.Constraints = append(des.Constraints, pkCon)
		}
	}

	for _, con := range des.Constraints {
		k := strings.ToLower(con.Name)
		if _, exists := curConMap[k]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionAddConstraint,
				Schema:      des.Schema,
				Table:       des.Name,
				Object:      con.Name,
				NewValue:    fmt.Sprintf("%s (%s)", con.Type, strings.Join(con.Columns, ", ")),
				Destructive: false,
				Description: fmt.Sprintf("Tambah constraint %s (%s)", con.Name, con.Type),
				Details:     con,
			})
		}
	}

	for _, curCon := range cur.Constraints {
		k := strings.ToLower(curCon.Name)
		if _, exists := desConMap[k]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionDropConstraint,
				Schema:      cur.Schema,
				Table:       cur.Name,
				Object:      curCon.Name,
				OldValue:    curCon.Type,
				Destructive: true,
				Description: fmt.Sprintf("Hapus constraint %s dari tabel %s.%s", curCon.Name, cur.Schema, cur.Name),
				Details:     curCon,
			})
		}
	}

	// D. Check Indexes
	curIdxMap := make(map[string]database.Index)
	for _, idx := range cur.Indexes {
		curIdxMap[strings.ToLower(idx.Name)] = idx
	}

	desIdxMap := make(map[string]IndexDef)
	for _, idx := range des.Indexes {
		desIdxMap[strings.ToLower(idx.Name)] = idx
	}

	for _, idx := range des.Indexes {
		k := strings.ToLower(idx.Name)
		if _, exists := curIdxMap[k]; !exists {
			idxType := "INDEX"
			if idx.Unique {
				idxType = "UNIQUE INDEX"
			}
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionCreateIndex,
				Schema:      des.Schema,
				Table:       des.Name,
				Object:      idx.Name,
				NewValue:    fmt.Sprintf("%s (%s)", idxType, strings.Join(idx.Columns, ", ")),
				Destructive: false,
				Description: fmt.Sprintf("Buat indeks %s pada (%s)", idx.Name, strings.Join(idx.Columns, ", ")),
				Details:     idx,
			})
		}
	}

	for _, curIdx := range cur.Indexes {
		// Ignore automatic primary key indexes
		if curIdx.Primary {
			continue
		}
		k := strings.ToLower(curIdx.Name)
		if _, exists := desIdxMap[k]; !exists {
			result.Changes = append(result.Changes, SchemaChange{
				Action:      ActionDropIndex,
				Schema:      cur.Schema,
				Table:       cur.Name,
				Object:      curIdx.Name,
				OldValue:    curIdx.Type,
				Destructive: true,
				Description: fmt.Sprintf("Hapus indeks %s dari tabel %s.%s", curIdx.Name, cur.Schema, cur.Name),
				Details:     curIdx,
			})
		}
	}
}

func isColumnModified(cur database.Column, des ColumnDef) bool {
	// 1. Compare normalized data types
	if !equalDataTypes(cur.DataType, des.DataType) {
		return true
	}
	// 2. Compare nullability
	if cur.Nullable != des.Nullable {
		return true
	}
	// 3. Compare defaults
	if (cur.Default == nil && des.Default != nil) || (cur.Default != nil && des.Default == nil) {
		return true
	}
	if cur.Default != nil && des.Default != nil {
		if strings.TrimSpace(*cur.Default) != strings.TrimSpace(*des.Default) {
			return true
		}
	}
	return false
}

func equalDataTypes(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, " ", "")
		switch s {
		case "int", "int4", "integer":
			return "integer"
		case "int8", "bigint":
			return "bigint"
		case "int2", "smallint":
			return "smallint"
		case "bool", "boolean":
			return "boolean"
		case "varchar", "character varying":
			return "varchar"
		}
		return s
	}
	return norm(a) == norm(b)
}

func formatColumnSnapshot(c database.Column) string {
	nullStr := "NULL"
	if !c.Nullable {
		nullStr = "NOT NULL"
	}
	defStr := ""
	if c.Default != nil {
		defStr = " DEFAULT " + *c.Default
	}
	return fmt.Sprintf("%s %s%s", c.DataType, nullStr, defStr)
}

func formatColumnDef(c ColumnDef) string {
	nullStr := "NULL"
	if !c.Nullable {
		nullStr = "NOT NULL"
	}
	defStr := ""
	if c.Default != nil {
		defStr = " DEFAULT " + *colDefaultStr(c.Default)
	}
	return fmt.Sprintf("%s %s%s", c.DataType, nullStr, defStr)
}

func colDefaultStr(d *string) *string {
	if d == nil {
		return nil
	}
	return d
}

// HasDestructiveChanges returns true if any change in the slice is marked as destructive (e.g. DROP).
func HasDestructiveChanges(changes []SchemaChange) bool {
	for _, c := range changes {
		if c.Destructive {
			return true
		}
	}
	return false
}
