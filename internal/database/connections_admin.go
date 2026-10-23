package database

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// ConnectionAdminHandler requires the server's protectAdmin middleware on every route.
type ConnectionAdminHandler struct {
	Store     *ConnectionStore
	Manager   *TargetManager
	Templates auth.TemplateRenderer
}

func (h *ConnectionAdminHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, Connection{DBMS: "postgres", Port: 5432}, http.StatusOK, "")
}

func (h *ConnectionAdminHandler) ShowEdit(w http.ResponseWriter, r *http.Request) {
	c, err := h.Store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		connectionHTTPError(w, err)
		return
	}
	h.render(w, r, *c, http.StatusOK, "")
}

func connectionForm(r *http.Request) Connection {
	port, _ := strconv.Atoi(r.PostFormValue("port"))
	return Connection{Name: r.PostFormValue("name"), DBMS: r.PostFormValue("dbms"), Host: r.PostFormValue("host"), Port: port, DBName: r.PostFormValue("db_name"), Username: r.PostFormValue("username")}
}

func (h *ConnectionAdminHandler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	c := connectionForm(r)
	if _, err := h.Store.Create(r.Context(), c, r.PostFormValue("password")); err != nil {
		h.formError(w, r, c, err)
		return
	}
	connectionRedirect(w, r, "success", "Koneksi berhasil disimpan. Gunakan Uji Koneksi untuk memeriksa akses.")
}

func (h *ConnectionAdminHandler) EditConnection(w http.ResponseWriter, r *http.Request) {
	c := connectionForm(r)
	c.ID = r.PathValue("id")
	if err := h.Store.Update(r.Context(), c.ID, c, r.PostFormValue("password")); err != nil {
		h.formError(w, r, c, err)
		return
	}
	h.Manager.Invalidate(c.ID)
	connectionRedirect(w, r, "success", "Koneksi berhasil diperbarui.")
}

func (h *ConnectionAdminHandler) formError(w http.ResponseWriter, r *http.Request, c Connection, err error) {
	if errors.Is(err, ErrInvalidConnection) {
		h.render(w, r, c, http.StatusUnprocessableEntity, err.Error())
		return
	}
	connectionHTTPError(w, err)
}

func (h *ConnectionAdminHandler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Store.Delete(r.Context(), id); err != nil {
		connectionHTTPError(w, err)
		return
	}
	h.Manager.Invalidate(id)
	connectionRedirect(w, r, "success", "Koneksi, hak akses group, dan riwayat migration berhasil dihapus.")
}

func (h *ConnectionAdminHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	if err := h.Manager.Test(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrConnectionNotFound) {
			connectionHTTPError(w, err)
			return
		}
		connectionRedirect(w, r, "error", err.Error())
		return
	}
	connectionRedirect(w, r, "success", "Uji koneksi berhasil. Database target dapat diakses.")
}

func connectionRedirect(w http.ResponseWriter, r *http.Request, key, message string) {
	http.Redirect(w, r, "/admin/connections?"+key+"="+url.QueryEscape(message), http.StatusSeeOther)
}

func connectionHTTPError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrConnectionNotFound) {
		http.Error(w, "Koneksi tidak ditemukan.", http.StatusNotFound)
		return
	}
	http.Error(w, "Koneksi tidak dapat diproses. Coba kembali.", http.StatusInternalServerError)
}

func (h *ConnectionAdminHandler) render(w http.ResponseWriter, r *http.Request, form Connection, status int, message string) {
	connections, err := h.Store.List(r.Context())
	if err != nil {
		connectionHTTPError(w, err)
		return
	}
	if message == "" {
		message = r.URL.Query().Get("error")
	}
	data := map[string]any{
		"CurrentUser": auth.GetUserFromContext(r.Context()), "CSRFToken": auth.GetSessionFromContext(r.Context()).CSRFToken,
		"ActiveTab": "connections", "Connections": connections, "Form": form, "ErrorMessage": message, "SuccessMessage": r.URL.Query().Get("success"),
	}
	var body bytes.Buffer
	if err := h.Templates.ExecuteTemplate(&body, "connections.html", data); err != nil {
		http.Error(w, "Halaman koneksi tidak dapat ditampilkan.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = body.WriteTo(w)
}
