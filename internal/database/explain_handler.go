package database

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// ExplainHandler requires RequireAuth, RequireDatabaseAccess(VIEW_ONLY), and
// CSRFProtectionMiddleware for POST at registration. ANALYZE is checked again
// against the current grant before a target connection is opened.
type ExplainHandler struct {
	Store     *ConnectionStore
	Manager   *TargetManager
	Templates auth.TemplateRenderer
}

// LimitExplainBody must wrap CSRF middleware because CSRF parses form fields.
func LimitExplainBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const limit = 20 * 1024
		if r.ContentLength > limit {
			http.Error(w, "Form query terlalu besar.", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func (h *ExplainHandler) Show(w http.ResponseWriter, r *http.Request) {
	data, status := h.pageData(r)
	h.render(w, r, status, data)
}

// ShowMigrationDraft exposes the migration form's safe EXPLAIN shortcut.
// Executing or persisting migrations belongs to the migration module.
func (h *ExplainHandler) ShowMigrationDraft(w http.ResponseWriter, r *http.Request) {
	data, status := h.pageData(r)
	if status == http.StatusOK && !access.IsHigherOrEqual(data["Access"].(access.AccessLevel), access.AccessViewEdit) {
		data["ErrorMessage"] = "Draf migration memerlukan hak Lihat & edit pada database ini."
		status = http.StatusForbidden
	}
	data["CanEdit"] = status == http.StatusOK
	h.renderPage(w, r, "migration-editor.html", status, data)
}

func (h *ExplainHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	data, status := h.pageData(r)
	if status != http.StatusOK {
		h.render(w, r, status, data)
		return
	}
	if err := r.ParseForm(); err != nil {
		data["ErrorMessage"] = "Form query tidak dapat dibaca."
		h.render(w, r, http.StatusBadRequest, data)
		return
	}
	query := r.PostForm.Get("sql_query")
	analyze := r.PostForm.Get("analyze") == "1"
	data["SQLQuery"], data["Analyze"] = query, analyze
	data["ConfirmAnalyze"] = r.PostForm.Get("confirm_analyze") == "1"
	if analyze {
		level := data["Access"].(access.AccessLevel)
		if !access.IsHigherOrEqual(level, access.AccessViewEdit) {
			data["ErrorMessage"] = "Mode ANALYZE memerlukan hak Lihat & edit pada database ini."
			h.render(w, r, http.StatusForbidden, data)
			return
		}
		if r.PostForm.Get("confirm_analyze") != "1" {
			data["ErrorMessage"] = "Konfirmasi eksekusi fisik query sebelum menjalankan mode ANALYZE."
			h.render(w, r, http.StatusUnprocessableEntity, data)
			return
		}
	}
	connection := data["Connection"].(*Connection)
	if _, err := ValidateExplainSQL(connection.DBMS, query, analyze); err != nil {
		data["ErrorMessage"] = err.Error()
		h.render(w, r, http.StatusUnprocessableEntity, data)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	target, dbms, err := h.Manager.Open(ctx, connection.ID)
	if err != nil {
		data["ErrorMessage"] = "Database target tidak dapat diakses. Coba kembali atau hubungi administrator."
		h.render(w, r, http.StatusServiceUnavailable, data)
		return
	}
	plan, err := RunExplain(ctx, target, dbms, query, analyze)
	if err != nil {
		data["ErrorMessage"] = err.Error()
		status = http.StatusUnprocessableEntity
		if errors.Is(err, ErrExplainUnavailable) || errors.Is(err, ErrExplainMalformedPlan) {
			status = http.StatusBadGateway
		}
	} else {
		data["Plan"] = plan
	}
	h.render(w, r, status, data)
}

func (h *ExplainHandler) pageData(r *http.Request) (map[string]any, int) {
	data := map[string]any{"AnalyzeWarning": ExplainAnalyzeWarning}
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		data["ErrorMessage"] = "Silakan masuk terlebih dahulu."
		return data, http.StatusUnauthorized
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	connection, err := h.Store.Get(ctx, r.PathValue("database_id"))
	if err != nil {
		if errors.Is(err, ErrConnectionNotFound) {
			data["ErrorMessage"] = "Koneksi tidak ditemukan."
			return data, http.StatusNotFound
		}
		data["ErrorMessage"] = "Koneksi tidak dapat dimuat. Coba kembali."
		return data, http.StatusInternalServerError
	}
	level, err := access.GetEffectiveAccess(ctx, h.Store.db, user.ID, connection.ID)
	if err != nil {
		data["ErrorMessage"] = "Hak akses tidak dapat dimuat. Coba kembali."
		return data, http.StatusInternalServerError
	}
	if !access.IsHigherOrEqual(level, access.AccessViewOnly) {
		data["ErrorMessage"] = "Akses database telah dicabut. Hubungi administrator."
		return data, http.StatusForbidden
	}
	data["Connection"], data["Access"] = connection, level
	base := "/databases/" + url.PathEscape(connection.ID)
	data["SchemaURL"], data["DataURL"], data["ExplainURL"] = base+"/schema", base+"/data", base+"/explain"
	return data, http.StatusOK
}

func (h *ExplainHandler) render(w http.ResponseWriter, r *http.Request, status int, data map[string]any) {
	h.renderPage(w, r, "database-explain.html", status, data)
}

func (h *ExplainHandler) renderPage(w http.ResponseWriter, r *http.Request, name string, status int, data map[string]any) {
	data["CurrentUser"] = auth.GetUserFromContext(r.Context())
	if session := auth.GetSessionFromContext(r.Context()); session != nil {
		data["CSRFToken"] = session.CSRFToken
	}
	data["ActiveTab"] = "databases"
	var body bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "Halaman analisis query tidak dapat ditampilkan.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}
