package migration

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

// ShowDiff handles GET /databases/{database_id}/diff
// Accessible to users with VIEW_ONLY or VIEW_EDIT permission.
func (h *Handler) ShowDiff(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, false)
	if !ok {
		return
	}

	data["PreviewURL"] = data["BaseURL"].(string) + "/preview"
	h.render(w, r, "diff", 200, data)
}

// ProcessDiff handles POST /databases/{database_id}/diff
// Compares the target database's live catalog with the submitted JSON schema definition,
// generates candidate DDL migration SQL, and displays the result.
func (h *Handler) ProcessDiff(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, false)
	if !ok {
		return
	}

	conn, _ := data["Connection"].(*database.Connection)
	dbms := "postgres"
	dbName := ""
	if conn != nil {
		dbms = conn.DBMS
		dbName = conn.DBName
	}
	data["PreviewURL"] = data["BaseURL"].(string) + "/preview"

	// Read input JSON from either file upload or textarea form
	var rawJSON string
	var uploaded bool
	if err := r.ParseMultipartForm(32 * 1024); err == nil && r.MultipartForm != nil {
		if fileHeaders, exists := r.MultipartForm.File["schema_file"]; exists && len(fileHeaders) > 0 {
			uploaded = true
			file, err := fileHeaders[0].Open()
			if err == nil {
				defer file.Close()
				buf, _ := io.ReadAll(io.LimitReader(file, 32*1024))
				rawJSON = string(buf)
			}
		}
	}
	if uploaded && strings.TrimSpace(r.PostForm.Get("target_schema_json")) != "" {
		data["RawInputJSON"] = r.PostForm.Get("target_schema_json")
		data["ErrorMessage"] = "Pilih satu sumber skema: textarea JSON atau berkas JSON."
		h.render(w, r, "diff", 422, data)
		return
	}
	if strings.TrimSpace(rawJSON) == "" {
		if err := r.ParseForm(); err == nil {
			rawJSON = r.PostForm.Get("target_schema_json")
		}
	}

	data["RawInputJSON"] = rawJSON

	if strings.TrimSpace(rawJSON) == "" {
		data["ErrorMessage"] = "Silakan masukkan definisi skema tujuan dalam format JSON atau unggah berkas JSON."
		h.render(w, r, "diff", 422, data)
		return
	}

	// 1. Fetch current live catalog from target database
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	target, targetDBMS, err := h.Runner.Manager.Open(ctx, conn.ID)
	if err != nil {
		data["ErrorMessage"] = "Database target tidak dapat dihubungi untuk membaca skema saat ini."
		h.render(w, r, "diff", 503, data)
		return
	}

	currentCatalog, err := database.LoadCatalog(ctx, target, targetDBMS, dbName)
	if err != nil {
		data["ErrorMessage"] = "Gagal memuat katalog skema saat ini dari database target: " + err.Error()
		h.render(w, r, "diff", 503, data)
		return
	}

	// 2. Parse submitted desired schema JSON
	defaultSchema := "public"
	if dbms == "mysql" {
		defaultSchema = dbName
	}
	desiredSchema, err := ParseTargetSchemaJSON([]byte(rawJSON), defaultSchema)
	if err != nil {
		data["ErrorMessage"] = err.Error()
		h.render(w, r, "diff", 422, data)
		return
	}

	// 3. Compare Catalogs
	diffResult, err := CompareCatalogs(currentCatalog, desiredSchema)
	if err != nil {
		data["ErrorMessage"] = "Gagal membandingkan skema: " + err.Error()
		h.render(w, r, "diff", 422, data)
		return
	}

	// 4. Generate Candidate Migration SQL
	generatedSQL, err := GenerateMigrationSQL(diffResult, dbms)
	if err != nil {
		data["ErrorMessage"] = "Gagal menghasilkan SQL DDL: " + err.Error()
		h.render(w, r, "diff", 500, data)
		return
	}

	data["DiffPerformed"] = true
	data["DiffResult"] = diffResult
	data["GeneratedSQL"] = generatedSQL
	data["HasDestructive"] = HasDestructiveChanges(diffResult.Changes)

	h.render(w, r, "diff", 200, data)
}
