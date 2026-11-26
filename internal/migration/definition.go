package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

const (
	ModeNormal = "normal"
	ModeBatch  = "batch"

	MaxBatchSize = 1_000_000
)

var (
	ErrDefinitionNotFound    = errors.New("definisi migration tidak ditemukan")
	ErrImmutableDefinition   = errors.New("definisi migration bersifat immutable karena sudah pernah dieksekusi; buat migration baru jika perlu perbaikan")
	ErrInvalidDefinition     = errors.New("definisi migration tidak valid")
	ErrBatchColumnInvalid    = errors.New("tabel atau kolom cursor batch tidak valid atau bukan kolom unik/primary key")
	ErrUnsupportedTargetDBMS = errors.New("fitur Milestone 2 hanya mendukung target database PostgreSQL")
	ErrDefinitionHasActiveReq = errors.New("definisi migration memiliki change request aktif yang sedang diproses")
)

// Definition merepresentasikan definisi migrasi berversi (immutable setelah dieksekusi).
type Definition struct {
	ID                 string    `json:"id"`
	DatabaseID         string    `json:"database_id"`
	Version            int       `json:"version"`
	Revision           int       `json:"revision"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	SQLUp              string    `json:"sql_up"`
	SQLDown            string    `json:"sql_down,omitempty"`
	IsIrreversible     bool      `json:"is_irreversible"`
	IrreversibleReason string    `json:"irreversible_reason,omitempty"`
	ExecutionMode      string    `json:"execution_mode"`
	BatchTable         string    `json:"batch_table,omitempty"`
	BatchCursorColumn  string    `json:"batch_cursor_column,omitempty"`
	BatchSize          *int      `json:"batch_size,omitempty"`
	IsImmutable        bool      `json:"is_immutable"`
	CreatedBy          string    `json:"created_by,omitempty"`
	CreatedByUsername  string    `json:"created_by_username,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DefinitionInput menampung payload untuk membuat atau mengubah draf definisi migrasi.
type DefinitionInput struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	SQLUp              string `json:"sql_up"`
	SQLDown            string `json:"sql_down"`
	IsIrreversible     bool   `json:"is_irreversible"`
	IrreversibleReason string `json:"irreversible_reason"`
	ExecutionMode      string `json:"execution_mode"`
	BatchTable         string `json:"batch_table"`
	BatchCursorColumn  string `json:"batch_cursor_column"`
	BatchSize          *int   `json:"batch_size"`
}

// ValidationReport merangkum hasil validasi SQL statis dan target batch.
type ValidationReport struct {
	Valid              bool   `json:"valid"`
	UpStatementCount   int    `json:"up_statement_count"`
	DownStatementCount int    `json:"down_statement_count"`
	BatchValidated     bool   `json:"batch_validated"`
	Message            string `json:"message,omitempty"`
}

var (
	batchIdentRe      = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	batchUpdateRe     = regexp.MustCompile(`(?is)^UPDATE\s+((?:"[a-zA-Z_][a-zA-Z0-9_]*"|[a-zA-Z_][a-zA-Z0-9_]*)(?:\s*\.\s*(?:"[a-zA-Z_][a-zA-Z0-9_]*"|[a-zA-Z_][a-zA-Z0-9_]*))?)\s+SET\s+(.+)$`)
	indexWhereRe      = regexp.MustCompile(`(?i)\bWHERE\b`)
	positionalParamRe = regexp.MustCompile(`\$(?:[1-9][0-9]*|0)`)
)

// maskSQL replaces SQL string literals (and optionally double-quoted identifiers)
// with spaces of identical byte length while preserving comments verbatim so
// keyword checks never false-positive inside literals and byte offsets align.
func maskSQL(s string, maskDoubleQuotes bool) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			start := i
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				i = len(s)
			} else {
				i += end + 1
			}
			out.WriteString(s[start:i])
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			start := i
			i += 2
			depth := 1
			for i < len(s) && depth > 0 {
				if i+1 < len(s) && s[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(s) && s[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			out.WriteString(s[start:i])
			continue
		}
		if c == '\'' || c == '"' {
			quote := c
			shouldMask := quote == '\'' || maskDoubleQuotes
			backslash := quote == '\'' && i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && (i < 2 || !isIdentifier(s[i-2]))
			out.WriteByte(quote)
			i++
			for i < len(s) {
				if s[i] == '\\' && backslash {
					if i+1 < len(s) {
						if shouldMask {
							out.WriteString("  ")
						} else {
							out.WriteString(s[i : i+2])
						}
						i += 2
					} else {
						if shouldMask {
							out.WriteByte(' ')
						} else {
							out.WriteByte(s[i])
						}
						i++
					}
					continue
				}
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						if shouldMask {
							out.WriteString("  ")
						} else {
							out.WriteString(s[i : i+2])
						}
						i += 2
						continue
					}
					out.WriteByte(quote)
					i++
					break
				}
				if shouldMask {
					out.WriteByte(' ')
				} else {
					out.WriteByte(s[i])
				}
				i++
			}
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(s) && isIdentifier(s[j]) && s[j] != '$' {
				j++
			}
			if j < len(s) && s[j] == '$' && (j == i+1 || s[i+1] < '0' || s[i+1] > '9') {
				tag := s[i : j+1]
				if end := strings.Index(s[j+1:], tag); end >= 0 {
					out.WriteString(tag)
					for k := 0; k < end; k++ {
						out.WriteByte(' ')
					}
					out.WriteString(tag)
					i = j + 1 + end + len(tag)
					continue
				}
			}
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func maskSQLStringLiterals(s string) string {
	return maskSQL(s, false)
}

func indexHasPredicate(def string) bool {
	return indexWhereRe.MatchString(maskSQL(def, true))
}

func stripLeadingSQLComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "--") {
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				return ""
			}
			s = s[idx+1:]
			continue
		}
		if strings.HasPrefix(s, "/*") {
			i := 2
			depth := 1
			for i < len(s) && depth > 0 {
				if i+1 < len(s) && s[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(s) && s[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return ""
			}
			s = s[i:]
			continue
		}
		break
	}
	return strings.TrimSpace(s)
}

// BatchQueryPlan menyimpan identifier tersanitasi, predikat WHERE tambahan (jika ada),
// serta statement UPDATE terkanonisasi dengan filter cursor BETWEEN $1 AND $2.
type BatchQueryPlan struct {
	Table      string
	Column     string
	ExtraWhere string
	Statement  string
}

func stripSQLCommentsOutsideLiterals(s string) (string, error) {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				i = len(s)
			} else {
				i += end + 1
			}
			out.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			i += 2
			depth := 1
			for i < len(s) && depth > 0 {
				if i+1 < len(s) && s[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(s) && s[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return "", errors.New("unterminated block comment in batch SQL")
			}
			out.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' {
			start := i
			quote := c
			backslash := quote == '\'' && i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && (i < 2 || !isIdentifier(s[i-2]))
			i++
			for i < len(s) {
				if s[i] == '\\' && backslash {
					if i+1 < len(s) {
						i += 2
					} else {
						i++
					}
					continue
				}
				if s[i] == quote {
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out.WriteString(s[start:i])
			continue
		}
		if c == '$' {
			j := i + 1
			for j < len(s) && isIdentifier(s[j]) && s[j] != '$' {
				j++
			}
			if j < len(s) && s[j] == '$' && (j == i+1 || s[i+1] < '0' || s[i+1] > '9') {
				tag := s[i : j+1]
				if end := strings.Index(s[j+1:], tag); end >= 0 {
					endIdx := j + 1 + end + len(tag)
					out.WriteString(s[i:endIdx])
					i = endIdx
					continue
				}
			}
		}
		out.WriteByte(c)
		i++
	}
	return strings.TrimSpace(out.String()), nil
}

func isSQLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

func findTopLevelSQLKeyword(masked, kw string) int {
	depth := 0
	n := len(kw)
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth != 0 || i+n > len(masked) {
			continue
		}
		if (i > 0 && !isSQLSpace(masked[i-1]) && masked[i-1] != ')') ||
			(i+n < len(masked) && !isSQLSpace(masked[i+n]) && masked[i+n] != '(') {
			continue
		}
		if strings.EqualFold(masked[i:i+n], kw) {
			return i
		}
	}
	return -1
}

func normalizeBatchTableToken(tok string) string {
	parts := strings.Split(tok, ".")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 && p[0] == '"' && p[len(p)-1] == '"' {
			p = p[1 : len(p)-1]
		}
		parts[i] = p
	}
	return strings.Join(parts, ".")
}

func hasBalancedParens(masked string) bool {
	depth := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

func unwrapBalancedOuterParens(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s
	}
	masked := maskSQLStringLiterals(s)
	depth := 0
	for i := 0; i < len(masked); i++ {
		switch masked[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 && i < len(masked)-1 {
				return s
			}
		}
	}
	if depth == 0 {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// ParseBatchUpdateSQL memvalidasi satu statement UPDATE mode batch, mengizinkan komentar SQL,
// identifier bertanda petik ganda, serta predikat WHERE selektif opsional, lalu menyusun BatchQueryPlan.
func ParseBatchUpdateSQL(query, batchTable, cursorCol string) (BatchQueryPlan, error) {
	stmts, err := ValidateSQL("postgres", query)
	if err != nil {
		return BatchQueryPlan{}, err
	}
	return buildBatchQueryPlan(stmts, batchTable, cursorCol)
}

// ParseWorkerBatchUpdateSQL menyusun BatchQueryPlan untuk snapshot SQL yang telah disetujui
// di worker tanpa berbagi jalur ValidateSQL milik HTTP agar konteks taint CodeQL tetap terpisah.
func ParseWorkerBatchUpdateSQL(query, batchTable, cursorCol string) (BatchQueryPlan, error) {
	stmts, err := SplitPostgresStatements(query)
	if err != nil {
		return BatchQueryPlan{}, err
	}
	return buildBatchQueryPlan(stmts, batchTable, cursorCol)
}

func buildBatchQueryPlan(stmts []Statement, batchTable, cursorCol string) (BatchQueryPlan, error) {
	var plan BatchQueryPlan
	batchTable = strings.TrimSpace(batchTable)
	cursorCol = strings.TrimSpace(cursorCol)
	names := strings.Split(batchTable, ".")
	if len(names) == 0 || len(names) > 2 {
		return plan, errors.New("invalid batch table")
	}
	for _, n := range append(append([]string{}, names...), cursorCol) {
		if !batchIdentRe.MatchString(n) {
			return plan, errors.New("invalid batch identifier")
		}
	}
	plan.Table = pgx.Identifier(names).Sanitize()
	plan.Column = pgx.Identifier{cursorCol}.Sanitize()

	if len(stmts) != 1 {
		return plan, errors.New("batch requires one UPDATE")
	}

	cleanSQL, err := stripSQLCommentsOutsideLiterals(stripLeadingSQLComments(stmts[0].SQL))
	if err != nil {
		return plan, err
	}
	match := batchUpdateRe.FindStringSubmatch(cleanSQL)
	if len(match) != 3 {
		return plan, errors.New("batch requires an UPDATE ... SET statement")
	}
	targetTok := normalizeBatchTableToken(match[1])
	if !(strings.EqualFold(targetTok, batchTable) || strings.EqualFold("public."+targetTok, batchTable) || strings.EqualFold(targetTok, "public."+batchTable)) {
		return plan, errors.New("batch UPDATE must target configured table")
	}

	rawRest := match[2]
	maskedRest := maskSQLStringLiterals(rawRest)
	whereIdx := findTopLevelSQLKeyword(maskedRest, "WHERE")
	setRaw, setMasked := rawRest, maskedRest
	var extraWhere string

	if whereIdx >= 0 {
		setRaw = rawRest[:whereIdx]
		setMasked = maskedRest[:whereIdx]
		rawWhere := strings.TrimSpace(rawRest[whereIdx+len("WHERE"):])
		maskedWhere := strings.TrimSpace(maskedRest[whereIdx+len("WHERE"):])
		if rawWhere == "" {
			return plan, errors.New("batch WHERE clause cannot be empty")
		}
		if findTopLevelSQLKeyword(maskedWhere, "RETURNING") >= 0 {
			return plan, errors.New("unsupported RETURNING clause in batch UPDATE")
		}
		colPat := `(?:` + regexp.QuoteMeta(plan.Column) + `|\b` + regexp.QuoteMeta(cursorCol) + `\b)`
		cursorOnlyRe := regexp.MustCompile(`(?is)^\s*(?:\(\s*` + colPat + `\s+BETWEEN\s+\$1\s+AND\s+\$2\s*\)|` + colPat + `\s+BETWEEN\s+\$1\s+AND\s+\$2)\s*$`)
		wrappedAndRe := regexp.MustCompile(`(?is)^\s*\(\s*` + colPat + `\s+BETWEEN\s+\$1\s+AND\s+\$2\s*\)\s+AND\s+(.+)$`)
		bareAndRe := regexp.MustCompile(`(?is)^\s*` + colPat + `\s+BETWEEN\s+\$1\s+AND\s+\$2\s+AND\s+(.+)$`)

		if cursorOnlyRe.MatchString(maskedWhere) {
			extraWhere = ""
		} else if m := wrappedAndRe.FindStringSubmatch(rawWhere); len(m) == 2 {
			tail := strings.TrimSpace(m[1])
			if findTopLevelSQLKeyword(maskSQLStringLiterals(tail), "OR") >= 0 {
				return plan, errors.New("batch WHERE with cursor must parenthesize boolean predicates")
			}
			extraWhere = unwrapBalancedOuterParens(tail)
		} else if m := bareAndRe.FindStringSubmatch(rawWhere); len(m) == 2 {
			tail := strings.TrimSpace(m[1])
			if findTopLevelSQLKeyword(maskSQLStringLiterals(tail), "OR") >= 0 {
				return plan, errors.New("batch WHERE with cursor must parenthesize boolean predicates")
			}
			extraWhere = unwrapBalancedOuterParens(tail)
		} else {
			extraWhere = unwrapBalancedOuterParens(rawWhere)
		}
		if extraWhere != "" {
			maskedExtra := maskSQLStringLiterals(extraWhere)
			if !hasBalancedParens(maskedExtra) {
				return plan, errors.New("unbalanced parentheses in batch WHERE")
			}
			if positionalParamRe.MatchString(maskedExtra) {
				return plan, errors.New("batch WHERE predicate cannot contain positional parameters")
			}
		}
	}

	if findTopLevelSQLKeyword(setMasked, "FROM") >= 0 || findTopLevelSQLKeyword(setMasked, "RETURNING") >= 0 {
		return plan, errors.New("unsupported batch UPDATE clause")
	}
	if positionalParamRe.MatchString(setMasked) {
		return plan, errors.New("batch SET cannot contain positional parameters")
	}
	// Batch statements cannot change the key used for streaming and undo.
	if regexp.MustCompile(`(?i)(^|,)\s*"?`+regexp.QuoteMeta(cursorCol)+`"?\s*=`).MatchString(setMasked) || strings.HasPrefix(strings.TrimSpace(setMasked), "(") {
		return plan, errors.New("batch cursor must remain immutable")
	}

	plan.ExtraWhere = extraWhere
	if extraWhere != "" {
		plan.Statement = "UPDATE " + plan.Table + " SET " + strings.TrimSpace(setRaw) + " WHERE (" + plan.Column + " BETWEEN $1 AND $2) AND (" + extraWhere + ")"
	} else {
		plan.Statement = "UPDATE " + plan.Table + " SET " + strings.TrimSpace(setRaw) + " WHERE " + plan.Column + " BETWEEN $1 AND $2"
	}
	return plan, nil
}

// BuildBatchUpdateSQL memvalidasi satu statement UPDATE mode batch serta aturan cursor BETWEEN $1 AND $2
// agar validasi API/dry-run selaras penuh dengan eksekutor worker, lalu mengembalikan identifier dan SQL tersanitasi.
func BuildBatchUpdateSQL(query, batchTable, cursorCol string) (sanitizedTable, sanitizedCol, statement string, err error) {
	plan, err := ParseBatchUpdateSQL(query, batchTable, cursorCol)
	if err != nil {
		return "", "", "", err
	}
	return plan.Table, plan.Column, plan.Statement, nil
}

// ValidateDefinitionInput memvalidasi struktur dan SQL pada definisi migrasi.
func ValidateDefinitionInput(in DefinitionInput) (DefinitionInput, *ValidationReport, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	in.SQLUp = strings.TrimSpace(in.SQLUp)
	in.SQLDown = strings.TrimSpace(in.SQLDown)
	in.IrreversibleReason = strings.TrimSpace(in.IrreversibleReason)
	in.ExecutionMode = strings.ToLower(strings.TrimSpace(in.ExecutionMode))
	if in.ExecutionMode == "" {
		in.ExecutionMode = ModeNormal
	}
	in.BatchTable = strings.TrimSpace(in.BatchTable)
	in.BatchCursorColumn = strings.TrimSpace(in.BatchCursorColumn)

	if in.Name == "" || utf8.RuneCountInString(in.Name) > 200 {
		return in, nil, fmt.Errorf("%w: nama migration wajib diisi (maks 200 karakter)", ErrInvalidDefinition)
	}
	upStmts, err := ValidateSQL("postgres", in.SQLUp)
	if err != nil {
		return in, nil, fmt.Errorf("%w: sql_up tidak valid (%v)", ErrInvalidDefinition, err)
	}

	var downStmts []Statement
	if in.IsIrreversible {
		if in.IrreversibleReason == "" {
			return in, nil, fmt.Errorf("%w: alasan irreversible wajib diisi jika migration ditandai irreversible", ErrInvalidDefinition)
		}
		in.SQLDown = ""
	} else {
		if in.SQLDown == "" {
			return in, nil, fmt.Errorf("%w: sql_down wajib diisi untuk migration reversible (atau tandai irreversible beserta alasannya)", ErrInvalidDefinition)
		}
		downStmts, err = ValidateSQL("postgres", in.SQLDown)
		if err != nil {
			return in, nil, fmt.Errorf("%w: sql_down tidak valid (%v)", ErrInvalidDefinition, err)
		}
		in.IrreversibleReason = ""
	}

	switch in.ExecutionMode {
	case ModeNormal:
		in.BatchTable = ""
		in.BatchCursorColumn = ""
		in.BatchSize = nil
	case ModeBatch:
		if in.BatchTable == "" || in.BatchCursorColumn == "" {
			return in, nil, fmt.Errorf("%w: batch_table dan batch_cursor_column wajib diisi pada mode batch", ErrInvalidDefinition)
		}
		if in.BatchSize == nil || *in.BatchSize <= 0 || *in.BatchSize > MaxBatchSize {
			return in, nil, fmt.Errorf("%w: batch_size harus antara 1 dan %d", ErrInvalidDefinition, MaxBatchSize)
		}
		if _, _, _, err := BuildBatchUpdateSQL(in.SQLUp, in.BatchTable, in.BatchCursorColumn); err != nil {
			return in, nil, fmt.Errorf("%w: sql_up mode batch tidak valid (%v)", ErrInvalidDefinition, err)
		}
		if !in.IsIrreversible {
			if _, _, _, err := BuildBatchUpdateSQL(in.SQLDown, in.BatchTable, in.BatchCursorColumn); err != nil {
				return in, nil, fmt.Errorf("%w: sql_down mode batch tidak valid (%v)", ErrInvalidDefinition, err)
			}
		}
	default:
		return in, nil, fmt.Errorf("%w: execution_mode harus 'normal' atau 'batch'", ErrInvalidDefinition)
	}

	return in, &ValidationReport{
		Valid:              true,
		UpStatementCount:   len(upStmts),
		DownStatementCount: len(downStmts),
		Message:            "Validasi SQL berhasil.",
	}, nil
}

// ValidateBatchTargetCatalog memastikan batchTable dan cursorCol ada di katalog PostgreSQL
// serta cursorCol bersifat NOT NULL dan memiliki PRIMARY KEY atau UNIQUE satu kolom.
func ValidateBatchTargetCatalog(catalog *database.Catalog, batchTable, cursorCol string) error {
	if catalog == nil {
		return fmt.Errorf("%w: katalog database target tidak tersedia", ErrBatchColumnInvalid)
	}
	schema := "public"
	tableName := strings.TrimSpace(batchTable)
	if parts := strings.SplitN(tableName, ".", 2); len(parts) == 2 {
		schema = strings.TrimSpace(parts[0])
		tableName = strings.TrimSpace(parts[1])
	}
	tbl, ok := catalog.FindTable(schema, tableName)
	if !ok {
		return fmt.Errorf("%w: tabel %s.%s tidak ditemukan pada target database", ErrBatchColumnInvalid, schema, tableName)
	}
	var cursorColMeta *database.Column
	for i := range tbl.Columns {
		if tbl.Columns[i].Name == cursorCol {
			cursorColMeta = &tbl.Columns[i]
			break
		}
	}
	if cursorColMeta == nil {
		return fmt.Errorf("%w: kolom cursor %q tidak ditemukan pada tabel %s.%s", ErrBatchColumnInvalid, cursorCol, schema, tableName)
	}
	if cursorColMeta.Nullable {
		return fmt.Errorf("%w: kolom cursor %q pada %s.%s harus bersifat NOT NULL", ErrBatchColumnInvalid, cursorCol, schema, tableName)
	}

	isUniqueKey := false
	for _, con := range tbl.Constraints {
		if (con.Type == "PRIMARY KEY" || con.Type == "UNIQUE") && len(con.Columns) == 1 && con.Columns[0] == cursorCol {
			isUniqueKey = true
			break
		}
	}
	if !isUniqueKey {
		for _, idx := range tbl.Indexes {
			if (idx.Primary || idx.Unique) && !idx.Partial && !idx.Invalid && !indexHasPredicate(idx.Definition) && len(idx.Columns) == 1 && strings.Trim(idx.Columns[0], `"`) == cursorCol {
				isUniqueKey = true
				break
			}
		}
	}
	if !isUniqueKey {
		return fmt.Errorf("%w: kolom cursor %q pada %s.%s harus merupakan kolom PRIMARY KEY atau UNIQUE tunggal", ErrBatchColumnInvalid, cursorCol, schema, tableName)
	}
	return nil
}

const definitionColumns = `
	d.id, d.database_id, d.version, d.revision, d.name, d.description,
	d.sql_up, COALESCE(d.sql_down, ''), d.is_irreversible, COALESCE(d.irreversible_reason, ''),
	d.execution_mode, COALESCE(d.batch_table, ''), COALESCE(d.batch_cursor_column, ''),
	d.batch_size, d.is_immutable, COALESCE(d.created_by, ''), COALESCE(u.username, ''),
	d.created_at, d.updated_at
`

func scanDefinition(row interface{ Scan(...any) error }) (*Definition, error) {
	var d Definition
	var batchSize sql.NullInt32
	if err := row.Scan(
		&d.ID, &d.DatabaseID, &d.Version, &d.Revision, &d.Name, &d.Description,
		&d.SQLUp, &d.SQLDown, &d.IsIrreversible, &d.IrreversibleReason,
		&d.ExecutionMode, &d.BatchTable, &d.BatchCursorColumn,
		&batchSize, &d.IsImmutable, &d.CreatedBy, &d.CreatedByUsername,
		&d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDefinitionNotFound
		}
		return nil, err
	}
	if batchSize.Valid {
		v := int(batchSize.Int32)
		d.BatchSize = &v
	}
	return &d, nil
}

// CreateDefinition menyimpan draf definisi migrasi baru berurutan linear untuk target PostgreSQL.
func CreateDefinition(ctx context.Context, db *sql.DB, databaseID, userID string, in DefinitionInput) (*Definition, error) {
	clean, _, err := ValidateDefinitionInput(in)
	if err != nil {
		return nil, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create definition tx: %w", err)
	}
	defer tx.Rollback()

	var dbms string
	if err := tx.QueryRowContext(ctx, `SELECT dbms_type FROM database_connections WHERE id = $1 FOR NO KEY UPDATE`, databaseID).Scan(&dbms); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, database.ErrConnectionNotFound
		}
		return nil, err
	}
	if dbms != "postgres" {
		return nil, ErrUnsupportedTargetDBMS
	}

	var nextVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM migration_definitions WHERE database_id = $1`, databaseID).Scan(&nextVersion); err != nil {
		return nil, err
	}

	id := uuid.NewString()
	var sqlDown, irrevReason, batchTable, batchCol any
	if !clean.IsIrreversible {
		sqlDown = clean.SQLDown
	} else {
		irrevReason = clean.IrreversibleReason
	}
	if clean.ExecutionMode == ModeBatch {
		batchTable = clean.BatchTable
		batchCol = clean.BatchCursorColumn
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO migration_definitions (
			id, database_id, version, revision, name, description,
			sql_up, sql_down, is_irreversible, irreversible_reason,
			execution_mode, batch_table, batch_cursor_column, batch_size,
			is_immutable, created_by
		) VALUES (
			$1, $2, $3, 1, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12, $13,
			FALSE, $14
		)
	`, id, databaseID, nextVersion, clean.Name, clean.Description,
		clean.SQLUp, sqlDown, clean.IsIrreversible, irrevReason,
		clean.ExecutionMode, batchTable, batchCol, clean.BatchSize,
		nullableString(userID),
	)
	if err != nil {
		return nil, fmt.Errorf("insert migration definition: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit migration definition: %w", err)
	}
	return GetDefinition(ctx, db, databaseID, id)
}

// UpdateDefinition mengubah draf definisi yang belum pernah dieksekusi dan menaikkan revisi.
// Mengembalikan ErrImmutableDefinition jika migrasi sudah pernah dijalankan.
func UpdateDefinition(ctx context.Context, db *sql.DB, databaseID, migrationID string, in DefinitionInput) (*Definition, error) {
	clean, _, err := ValidateDefinitionInput(in)
	if err != nil {
		return nil, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var isImmutable bool
	err = tx.QueryRowContext(ctx, `
		SELECT is_immutable FROM migration_definitions
		WHERE id = $1 AND database_id = $2
		FOR UPDATE
	`, migrationID, databaseID).Scan(&isImmutable)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDefinitionNotFound
		}
		return nil, err
	}
	if isImmutable {
		return nil, ErrImmutableDefinition
	}

	var hasExec bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM migration_executions WHERE migration_id = $1)`, migrationID).Scan(&hasExec); err != nil {
		return nil, err
	}
	if hasExec {
		_, _ = tx.ExecContext(ctx, `UPDATE migration_definitions SET is_immutable = TRUE WHERE id = $1`, migrationID)
		_ = tx.Commit()
		return nil, ErrImmutableDefinition
	}

	var hasActiveReq bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM migration_requests
			WHERE migration_id = $1 AND status IN ('pending_review', 'approved', 'pending_queue', 'running')
		)
	`, migrationID).Scan(&hasActiveReq); err != nil {
		return nil, err
	}
	if hasActiveReq {
		return nil, ErrDefinitionHasActiveReq
	}

	var sqlDown, irrevReason, batchTable, batchCol any
	if !clean.IsIrreversible {
		sqlDown = clean.SQLDown
	} else {
		irrevReason = clean.IrreversibleReason
	}
	if clean.ExecutionMode == ModeBatch {
		batchTable = clean.BatchTable
		batchCol = clean.BatchCursorColumn
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE migration_definitions
		SET name = $1,
		    description = $2,
		    sql_up = $3,
		    sql_down = $4,
		    is_irreversible = $5,
		    irreversible_reason = $6,
		    execution_mode = $7,
		    batch_table = $8,
		    batch_cursor_column = $9,
		    batch_size = $10,
		    revision = revision + 1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $11 AND database_id = $12
	`, clean.Name, clean.Description, clean.SQLUp, sqlDown, clean.IsIrreversible, irrevReason,
		clean.ExecutionMode, batchTable, batchCol, clean.BatchSize, migrationID, databaseID)
	if err != nil {
		return nil, fmt.Errorf("update migration definition: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetDefinition(ctx, db, databaseID, migrationID)
}

// GetDefinition mengambil satu definisi migrasi milik databaseID.
func GetDefinition(ctx context.Context, db *sql.DB, databaseID, migrationID string) (*Definition, error) {
	return scanDefinition(db.QueryRowContext(ctx, `
		SELECT `+definitionColumns+`
		FROM migration_definitions d
		LEFT JOIN users u ON u.id = d.created_by
		WHERE d.database_id = $1 AND d.id = $2
	`, databaseID, migrationID))
}

// ListDefinitions mengambil seluruh definisi migrasi suatu database dalam urutan versi linear.
func ListDefinitions(ctx context.Context, db *sql.DB, databaseID string) ([]Definition, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT `+definitionColumns+`
		FROM migration_definitions d
		LEFT JOIN users u ON u.id = d.created_by
		WHERE d.database_id = $1
		ORDER BY d.version ASC
	`, databaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	defs := make([]Definition, 0)
	for rows.Next() {
		d, err := scanDefinition(rows)
		if err != nil {
			return nil, err
		}
		defs = append(defs, *d)
	}
	return defs, rows.Err()
}

// MarkDefinitionImmutable mengunci SQL up/down secara permanen saat eksekusi dimulai (dipanggil worker).
func MarkDefinitionImmutable(ctx context.Context, db *sql.DB, migrationID string) error {
	_, err := db.ExecContext(ctx, `UPDATE migration_definitions SET is_immutable = TRUE, updated_at = CURRENT_TIMESTAMP WHERE id = $1`, migrationID)
	return err
}

func nullableString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
