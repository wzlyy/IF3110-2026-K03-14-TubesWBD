package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
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
// serta cursorCol memiliki PRIMARY KEY atau UNIQUE satu kolom.
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
	colExists := false
	for _, c := range tbl.Columns {
		if c.Name == cursorCol {
			colExists = true
			break
		}
	}
	if !colExists {
		return fmt.Errorf("%w: kolom cursor %q tidak ditemukan pada tabel %s.%s", ErrBatchColumnInvalid, cursorCol, schema, tableName)
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
			if (idx.Primary || idx.Unique) && len(idx.Columns) == 1 && strings.Trim(idx.Columns[0], `"`) == cursorCol {
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
