package database

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// ViewerHandler's schema/data endpoints require RequireAuth followed by
// access.RequireDatabaseAccess(AccessViewOnly). Admins require group grants too.
type ViewerHandler struct {
	Store     *ConnectionStore
	Manager   *TargetManager
	Templates auth.TemplateRenderer
}
type AccessibleConnection struct {
	Connection
	Access access.AccessLevel
}
type TableLink struct {
	Schema, Name, URL string
	Selected          bool
}

func (h *ViewerHandler) ListDatabases(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		http.Error(w, "Silakan masuk terlebih dahulu.", http.StatusUnauthorized)
		return
	}
	// Filter in SQL before scanning connection metadata into any view model.
	rows, err := h.Store.db.QueryContext(r.Context(), `SELECT `+connectionColumns+` FROM database_connections dc WHERE EXISTS (
 SELECT 1 FROM group_database_grants gdg JOIN user_groups ug ON ug.group_id=gdg.group_id
 WHERE ug.user_id=$1 AND gdg.database_id=dc.id AND gdg.access_level IN ('VIEW_ONLY','VIEW_EDIT')) ORDER BY name,id`, user.ID)
	if err != nil {
		h.render(w, r, "database-list.html", http.StatusInternalServerError, map[string]any{"ErrorMessage": "Daftar database tidak dapat dimuat. Coba kembali."})
		return
	}
	var candidates []Connection
	for rows.Next() {
		c, e := scanConnection(rows)
		if e != nil {
			err = e
			break
		}
		candidates = append(candidates, *c)
	}
	if e := rows.Err(); e != nil {
		err = e
	}
	rows.Close()
	var connections []AccessibleConnection
	if err == nil {
		for _, c := range candidates {
			level, e := access.GetEffectiveAccess(r.Context(), h.Store.db, user.ID, c.ID)
			if e != nil {
				err = e
				break
			}
			if access.IsHigherOrEqual(level, access.AccessViewOnly) {
				connections = append(connections, AccessibleConnection{c, level})
			}
		}
	}
	if err != nil {
		h.render(w, r, "database-list.html", http.StatusInternalServerError, map[string]any{"ErrorMessage": "Daftar database tidak dapat dimuat. Coba kembali."})
		return
	}
	h.render(w, r, "database-list.html", http.StatusOK, map[string]any{"Connections": connections})
}

func (h *ViewerHandler) ShowSchema(w http.ResponseWriter, r *http.Request) { h.showTarget(w, r, false) }
func (h *ViewerHandler) ShowData(w http.ResponseWriter, r *http.Request)   { h.showTarget(w, r, true) }

func (h *ViewerHandler) showTarget(w http.ResponseWriter, r *http.Request, dataPage bool) {
	templateName := "database-schema.html"
	if dataPage {
		templateName = "database-data.html"
	}
	data := map[string]any{}
	fail := func(status int, message string) {
		data["ErrorMessage"] = message
		h.render(w, r, templateName, status, data)
	}
	// One shared deadline keeps open + catalog + data inside server's timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	c, err := h.Store.Get(ctx, r.PathValue("database_id"))
	if err != nil {
		if errors.Is(err, ErrConnectionNotFound) {
			fail(404, "Koneksi tidak ditemukan.")
		} else {
			fail(500, "Koneksi tidak dapat dimuat. Coba kembali.")
		}
		return
	}
	data["Connection"] = c
	data["SchemaURL"] = "/databases/" + url.PathEscape(c.ID) + "/schema"
	data["DataURL"] = "/databases/" + url.PathEscape(c.ID) + "/data"
	level, err := access.GetEffectiveAccess(ctx, h.Store.db, auth.GetUserFromContext(ctx).ID, c.ID)
	if err != nil {
		fail(500, "Hak akses tidak dapat dimuat. Coba kembali.")
		return
	}
	if !access.IsHigherOrEqual(level, access.AccessViewOnly) {
		fail(403, "Akses database telah dicabut. Hubungi administrator.")
		return
	}
	data["Access"] = level
	page, err := ParseDataPage(r.URL.Query().Get("page"))
	if dataPage && err != nil {
		fail(400, "Nomor halaman tidak valid. Gunakan bilangan bulat mulai dari 1.")
		return
	}
	target, dbms, err := h.Manager.Open(ctx, c.ID)
	if err != nil {
		fail(503, "Database target tidak dapat diakses. Coba kembali atau hubungi administrator.")
		return
	}
	catalog, err := LoadCatalog(ctx, target, dbms, c.DBName)
	if err != nil {
		fail(503, "Skema database tidak dapat dimuat. Coba kembali atau hubungi administrator.")
		return
	}
	data["Catalog"] = catalog
	schema, name := r.URL.Query().Get("schema"), r.URL.Query().Get("table")
	var selected *Table
	if schema != "" || name != "" {
		var ok bool
		selected, ok = catalog.FindTable(schema, name)
		if !ok {
			fail(404, "Tabel tidak ditemukan. Pilih tabel yang tersedia.")
			return
		}
	}
	// Schema overview shows every table. Data initially selects the first table.
	if dataPage && selected == nil {
		for i := range catalog.Schemas {
			if len(catalog.Schemas[i].Tables) > 0 {
				selected = &catalog.Schemas[i].Tables[0]
				break
			}
		}
	}
	data["Selected"] = selected
	if selected != nil {
		data["SchemaURL"] = tableURL(c.ID, "schema", selected.Schema, selected.Name, 1)
		data["DataURL"] = tableURL(c.ID, "data", selected.Schema, selected.Name, 1)
	}
	var links []TableLink
	for _, s := range catalog.Schemas {
		for _, t := range s.Tables {
			path := "schema"
			if dataPage {
				path = "data"
			}
			links = append(links, TableLink{Schema: s.Name, Name: t.Name, URL: tableURL(c.ID, path, t.Schema, t.Name, 1), Selected: selected != nil && selected.Schema == t.Schema && selected.Name == t.Name})
		}
	}
	data["TableLinks"] = links
	if dataPage && selected != nil {
		result, e := ReadTableData(ctx, target, dbms, *selected, page)
		if e != nil {
			if errors.Is(e, ErrTableNotFound) {
				fail(404, "Tabel tidak lagi tersedia. Muat ulang daftar tabel.")
			} else {
				fail(503, "Data tabel tidak dapat dimuat. Coba kembali atau hubungi administrator.")
			}
			return
		}
		data["Data"] = result
		if page > 1 {
			data["PreviousURL"] = tableURL(c.ID, "data", selected.Schema, selected.Name, page-1)
		}
		if result.HasNext {
			data["NextURL"] = tableURL(c.ID, "data", selected.Schema, selected.Name, page+1)
		}
	}
	h.render(w, r, templateName, 200, data)
}

func tableURL(id, view, schema, table string, page int64) string {
	v := url.Values{"schema": {schema}, "table": {table}}
	if view == "data" {
		v.Set("page", strconv.FormatInt(page, 10))
	}
	return "/databases/" + url.PathEscape(id) + "/" + view + "?" + v.Encode()
}

func (h *ViewerHandler) render(w http.ResponseWriter, r *http.Request, name string, status int, data map[string]any) {
	data["CurrentUser"] = auth.GetUserFromContext(r.Context())
	if s := auth.GetSessionFromContext(r.Context()); s != nil {
		data["CSRFToken"] = s.CSRFToken
	}
	data["ActiveTab"] = "databases"
	var body bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&body, name, data); err != nil {
		http.Error(w, "Halaman database tidak dapat ditampilkan.", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}
