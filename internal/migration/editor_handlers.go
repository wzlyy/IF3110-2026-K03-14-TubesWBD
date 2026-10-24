package migration

import (
	"bytes"
	"database/sql"
	"errors"
	"net/http"
	"net/url"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

type Handler struct {
	DB        *sql.DB
	Store     *database.ConnectionStore
	Runner    *Runner
	Templates auth.TemplateRenderer
}

// RegisterRoutes applies authentication, body limits and CSRF to every route.
// Handlers evaluate live grants; platform admins receive no bypass.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, requireAuth func(http.Handler) http.Handler) {
	for _, route := range []struct {
		pattern string
		handler http.HandlerFunc
	}{
		{"GET /databases/{database_id}/migration", h.Show},
		{"POST /databases/{database_id}/migration/preview", h.Preview},
		{"POST /databases/{database_id}/migration/execute", h.Execute},
		{"GET /databases/{database_id}/migration/history", h.History},
		{"GET /databases/{database_id}/migration/{migration_id}", h.Detail},
		{"POST /databases/{database_id}/migration/{migration_id}/retry", h.Retry},
		{"POST /databases/{database_id}/migration/{migration_id}/reconcile", h.Reconcile},
	} {
		mux.Handle(route.pattern, requireAuth(database.LimitExplainBody(auth.CSRFProtectionMiddleware(route.handler))))
	}
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request, edit bool) (map[string]any, bool) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Silakan masuk terlebih dahulu.", 401)
		return nil, false
	}
	id := r.PathValue("database_id")
	level, err := access.GetEffectiveAccess(r.Context(), h.DB, user.ID, id)
	if err != nil {
		http.Error(w, "Hak akses tidak dapat dimuat.", 500)
		return nil, false
	}
	required := access.AccessViewOnly
	if edit {
		required = access.AccessViewEdit
	}
	if !access.IsHigherOrEqual(level, required) {
		http.Error(w, "Akses database tidak mencukupi.", 403)
		return nil, false
	}
	c, err := h.Store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, "Koneksi database tidak ditemukan.", 404)
		return nil, false
	}
	base := "/databases/" + url.PathEscape(id)
	data := map[string]any{"Connection": c, "Access": level, "CanEdit": level == access.AccessViewEdit,
		"BaseURL": base + "/migration", "HistoryURL": base + "/migration/history", "ExplainURL": base + "/explain",
		"SchemaURL": base + "/schema", "MySQLWarning": MySQLWarning, "DDLMessage": database.ExplainDDLMessage}
	return data, true
}

func (h *Handler) Show(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, true)
	if !ok {
		return
	}
	h.render(w, r, "editor", 200, data)
}

// Preview is phase 3B's entry point: POST sql_query to the selected target.
// It renders editable SQL without persisting an attempt or opening the target.
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, true)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form SQL tidak dapat dibaca.", 400)
		return
	}
	query := r.PostForm.Get("sql_query")
	data["SQLQuery"] = query
	statements, err := ValidateSQL(data["Connection"].(*database.Connection).DBMS, query)
	if err != nil {
		data["ErrorMessage"] = err.Error()
		h.render(w, r, "editor", 422, data)
		return
	}
	data["Preview"], data["StatementCount"] = true, len(statements)
	h.render(w, r, "editor", 200, data)
}

func (h *Handler) Execute(w http.ResponseWriter, r *http.Request) { h.execute(w, r, false) }
func (h *Handler) Retry(w http.ResponseWriter, r *http.Request)   { h.execute(w, r, true) }

func (h *Handler) execute(w http.ResponseWriter, r *http.Request, retry bool) {
	data, ok := h.page(w, r, true)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Form SQL tidak dapat dibaca.", 400)
		return
	}
	query, retryID := r.PostForm.Get("sql_query"), ""
	if retry {
		retryID = r.PathValue("migration_id")
	}
	a, err := h.Runner.Execute(r.Context(), r.PathValue("database_id"), auth.GetUserFromContext(r.Context()).ID, query, retryID)
	if a != nil {
		http.Redirect(w, r, data["BaseURL"].(string)+"/"+url.PathEscape(a.ID), http.StatusSeeOther)
		return
	}
	data["SQLQuery"], data["ErrorMessage"] = query, err.Error()
	status := 500
	if errors.Is(err, ErrInvalidSQL) {
		status = 422
	} else if IsConflict(err) {
		status = 409
	} else if errors.Is(err, ErrNotFound) {
		status = 404
	}
	h.render(w, r, "editor", status, data)
}

func (h *Handler) History(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, false)
	if !ok {
		return
	}
	history, err := ListHistory(r.Context(), h.DB, r.PathValue("database_id"))
	if err != nil {
		data["ErrorMessage"] = err.Error()
		h.render(w, r, "history", 500, data)
		return
	}
	data["History"] = history
	h.render(w, r, "history", 200, data)
}

func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, false)
	if !ok {
		return
	}
	a, err := GetAttempt(r.Context(), h.DB, r.PathValue("database_id"), r.PathValue("migration_id"))
	if err != nil {
		status := 500
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		data["ErrorMessage"] = err.Error()
		h.render(w, r, "detail", status, data)
		return
	}
	data["Attempt"], data["PendingMessage"] = a, ErrPending.Error()
	h.render(w, r, "detail", 200, data)
}

func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	data, ok := h.page(w, r, true)
	if !ok {
		return
	}
	a, err := h.Runner.Reconcile(r.Context(), r.PathValue("database_id"), r.PathValue("migration_id"))
	if err == nil {
		http.Redirect(w, r, data["BaseURL"].(string)+"/"+url.PathEscape(a.ID), http.StatusSeeOther)
		return
	}
	data["Attempt"], data["PendingMessage"], data["ErrorMessage"] = a, ErrPending.Error(), err.Error()
	status := 500
	if IsConflict(err) {
		status = 409
	} else if errors.Is(err, ErrNotFound) {
		status = 404
	}
	h.render(w, r, "detail", status, data)
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, page string, status int, data map[string]any) {
	data["CurrentUser"], data["ActiveTab"] = auth.GetUserFromContext(r.Context()), "databases"
	if session := auth.GetSessionFromContext(r.Context()); session != nil {
		data["CSRFToken"] = session.CSRFToken
	}
	var body bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&body, "migration-"+page+".html", data); err != nil {
		http.Error(w, "Halaman migration tidak dapat ditampilkan.", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}
