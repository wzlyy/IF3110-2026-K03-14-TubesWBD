package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

// DatabaseAPIHandler melayani endpoint JSON explorer DB, Schema/Data Viewer, Explain, dan Schema Diff.
type DatabaseAPIHandler struct {
	DB      *sql.DB
	Store   *database.ConnectionStore
	Manager *database.TargetManager
	Locks   *lock.Manager
}

type accessibleDatabaseDTO struct {
	database.Connection
	AccessLevel access.AccessLevel `json:"access_level"`
	LockState   string             `json:"lock_state,omitempty"`
}

// ListDatabases menangani GET /api/v1/databases.
func (h *DatabaseAPIHandler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return
	}

	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT dc.id, dc.name, dc.dbms_type, dc.host, dc.port, dc.db_name, dc.username, dc.created_at, dc.updated_at,
		       COALESCE(dl.hold_state, ''),
		       CASE
		           WHEN bool_or(gdg.access_level = 'VIEW_EDIT') THEN 'VIEW_EDIT'
		           ELSE 'VIEW_ONLY'
		       END AS access_level
		FROM database_connections dc
		JOIN group_database_grants gdg ON gdg.database_id = dc.id
		JOIN user_groups ug ON ug.group_id = gdg.group_id
		LEFT JOIN database_locks dl ON dl.database_id = dc.id
		WHERE ug.user_id = $1
		  AND gdg.access_level IN ('VIEW_ONLY', 'VIEW_EDIT')
		GROUP BY dc.id, dc.name, dc.dbms_type, dc.host, dc.port, dc.db_name, dc.username, dc.created_at, dc.updated_at, dl.hold_state
		ORDER BY dc.name ASC, dc.id ASC
	`, user.ID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat daftar database")
		return
	}
	defer rows.Close()

	list := make([]accessibleDatabaseDTO, 0)
	for rows.Next() {
		var item accessibleDatabaseDTO
		var rawLevel string
		if err := rows.Scan(
			&item.ID, &item.Name, &item.DBMS, &item.Host, &item.Port,
			&item.DBName, &item.Username, &item.CreatedAt, &item.UpdatedAt, &item.LockState, &rawLevel,
		); err != nil {
			WriteError(w, http.StatusInternalServerError, "Gagal memuat daftar database")
			return
		}
		item.AccessLevel = access.ParseAccessLevel(rawLevel)
		if access.IsHigherOrEqual(item.AccessLevel, access.AccessViewOnly) {
			list = append(list, item)
		}
	}
	if err := rows.Err(); err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat daftar database")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"databases": list})
}

func (h *DatabaseAPIHandler) resolveTargetAccess(w http.ResponseWriter, r *http.Request, required access.AccessLevel) (*database.Connection, access.AccessLevel, context.Context, bool) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return nil, access.AccessNone, r.Context(), false
	}
	dbID := r.PathValue("database_id")
	level, err := access.GetEffectiveAccess(r.Context(), h.DB, user.ID, dbID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mengevaluasi hak akses")
		return nil, access.AccessNone, r.Context(), false
	}
	if !access.IsHigherOrEqual(level, required) {
		telemetry.LogAudit(r.Context(), "access.check", "denied")
		WriteError(w, http.StatusForbidden, "Hak akses tidak mencukupi untuk database ini")
		return nil, access.AccessNone, r.Context(), false
	}
	conn, err := h.Store.Get(r.Context(), dbID)
	if err != nil {
		HandleDomainError(w, err)
		return nil, access.AccessNone, r.Context(), false
	}
	ctx := telemetry.WithDatabase(r.Context(), conn.Name)
	return conn, level, ctx, true
}

// GetSchema menangani GET /api/v1/databases/{database_id}/schema.
func (h *DatabaseAPIHandler) GetSchema(w http.ResponseWriter, r *http.Request) {
	conn, level, ctx, ok := h.resolveTargetAccess(w, r, access.AccessViewOnly)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	target, dbms, err := h.Manager.Open(ctx, conn.ID)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Database target tidak dapat diakses")
		return
	}
	catalog, err := database.LoadCatalog(ctx, target, dbms, conn.DBName)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Skema database target tidak dapat dimuat")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"database":     conn,
		"access_level": level,
		"catalog":      catalog,
	})
}

// GetData menangani GET /api/v1/databases/{database_id}/data?schema=&table=&page=.
func (h *DatabaseAPIHandler) GetData(w http.ResponseWriter, r *http.Request) {
	conn, level, ctx, ok := h.resolveTargetAccess(w, r, access.AccessViewOnly)
	if !ok {
		return
	}
	page, err := database.ParseDataPage(r.URL.Query().Get("page"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Nomor halaman tidak valid. Gunakan bilangan bulat mulai dari 1.")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	target, dbms, err := h.Manager.Open(ctx, conn.ID)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Database target tidak dapat diakses")
		return
	}
	catalog, err := database.LoadCatalog(ctx, target, dbms, conn.DBName)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Skema database target tidak dapat dimuat")
		return
	}

	schema := r.URL.Query().Get("schema")
	tableName := r.URL.Query().Get("table")
	var selected *database.Table
	if schema != "" || tableName != "" {
		if schema == "" {
			schema = "public"
		}
		var found bool
		selected, found = catalog.FindTable(schema, tableName)
		if !found {
			WriteError(w, http.StatusNotFound, "Tabel tidak ditemukan")
			return
		}
	} else {
		for i := range catalog.Schemas {
			if len(catalog.Schemas[i].Tables) > 0 {
				selected = &catalog.Schemas[i].Tables[0]
				break
			}
		}
	}

	var data *database.TableData
	if selected != nil {
		data, err = database.ReadTableData(ctx, target, dbms, *selected, page)
		if err != nil {
			HandleDomainError(w, err)
			return
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"database":     conn,
		"access_level": level,
		"selected":     selected,
		"data":         data,
		"page_size":    database.DataPageSize,
	})
}

// Explain menangani POST /api/v1/databases/{database_id}/explain.
func (h *DatabaseAPIHandler) Explain(w http.ResponseWriter, r *http.Request) {
	conn, level, ctx, ok := h.resolveTargetAccess(w, r, access.AccessViewOnly)
	if !ok {
		return
	}
	var req struct {
		SQLQuery       string `json:"sql_query"`
		Analyze        bool   `json:"analyze"`
		ConfirmAnalyze bool   `json:"confirm_analyze"`
	}
	if err := DecodeJSON(w, r, 24*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Analyze {
		if !access.IsHigherOrEqual(level, access.AccessViewEdit) {
			WriteError(w, http.StatusForbidden, "Mode ANALYZE memerlukan hak akses VIEW_EDIT")
			return
		}
		if !req.ConfirmAnalyze {
			WriteError(w, http.StatusUnprocessableEntity, "Konfirmasi eksekusi fisik query wajib disetujui sebelum menjalankan ANALYZE")
			return
		}
	}
	if _, err := database.ValidateExplainSQL(conn.DBMS, req.SQLQuery, req.Analyze); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	target, dbms, err := h.Manager.Open(ctx, conn.ID)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Database target tidak dapat diakses")
		return
	}
	plan, err := database.RunExplain(ctx, target, dbms, req.SQLQuery, req.Analyze)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, database.ErrExplainUnavailable) || errors.Is(err, database.ErrExplainMalformedPlan) {
			status = http.StatusBadGateway
		}
		WriteError(w, status, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"plan": plan})
}

// Diff menangani POST /api/v1/databases/{database_id}/diff.
func (h *DatabaseAPIHandler) Diff(w http.ResponseWriter, r *http.Request) {
	conn, _, ctx, ok := h.resolveTargetAccess(w, r, access.AccessViewOnly)
	if !ok {
		return
	}
	var req struct {
		TargetSchemaJSON string `json:"target_schema_json"`
	}
	if err := DecodeJSON(w, r, 64*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.TargetSchemaJSON) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "target_schema_json wajib diisi")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target, dbms, err := h.Manager.Open(ctx, conn.ID)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Database target tidak dapat diakses")
		return
	}
	currentCatalog, err := database.LoadCatalog(ctx, target, dbms, conn.DBName)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, "Gagal memuat katalog skema saat ini")
		return
	}
	defaultSchema := "public"
	if dbms == "mysql" {
		defaultSchema = conn.DBName
	}
	desired, err := migration.ParseTargetSchemaJSON([]byte(req.TargetSchemaJSON), defaultSchema)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	diffResult, err := migration.CompareCatalogs(currentCatalog, desired)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	genSQL, err := migration.GenerateMigrationSQL(diffResult, dbms)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal menghasilkan SQL DDL")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"diff_result":     diffResult,
		"generated_sql":   genSQL,
		"has_destructive": migration.HasDestructiveChanges(diffResult.Changes),
	})
}
