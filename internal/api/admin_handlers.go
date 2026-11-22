package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

// AdminAPIHandler melayani endpoint JSON Platform Admin (Pengguna, Grup + Leader, Grant, Koneksi).
type AdminAPIHandler struct {
	DB      *sql.DB
	Store   *database.ConnectionStore
	Manager *database.TargetManager
}

type adminUserDTO struct {
	ID              string    `json:"id"`
	Username        string    `json:"username"`
	IsPlatformAdmin bool      `json:"is_platform_admin"`
	IsActive        bool      `json:"is_active"`
	CreatedAt       time.Time `json:"created_at"`
	Groups          []string  `json:"groups"`
}

type groupMemberDTO struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

type adminGroupDTO struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	LeaderUserID   string           `json:"leader_user_id"`
	LeaderUsername string           `json:"leader_username"`
	CreatedAt      time.Time        `json:"created_at"`
	Members        []groupMemberDTO `json:"members"`
}

type adminGrantDTO struct {
	GroupID      string             `json:"group_id"`
	GroupName    string             `json:"group_name"`
	DatabaseID   string             `json:"database_id"`
	DatabaseName string             `json:"database_name"`
	DBMS         string             `json:"dbms"`
	AccessLevel  access.AccessLevel `json:"access_level"`
}

// --- Pengguna ---

func (h *AdminAPIHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT u.id, u.username, u.is_platform_admin, u.is_active, u.created_at,
		       COALESCE(string_agg(g.name, ', ' ORDER BY g.name), '')
		FROM users u
		LEFT JOIN user_groups ug ON u.id = ug.user_id
		LEFT JOIN groups g ON ug.group_id = g.id
		GROUP BY u.id
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat daftar pengguna")
		return
	}
	defer rows.Close()

	users := make([]adminUserDTO, 0)
	for rows.Next() {
		var u adminUserDTO
		var rawGroups string
		if err := rows.Scan(&u.ID, &u.Username, &u.IsPlatformAdmin, &u.IsActive, &u.CreatedAt, &rawGroups); err != nil {
			continue
		}
		u.Groups = []string{}
		if rawGroups != "" {
			u.Groups = strings.Split(rawGroups, ", ")
		}
		users = append(users, u)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (h *AdminAPIHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		IsPlatformAdmin bool   `json:"is_platform_admin"`
	}
	if err := DecodeJSON(w, r, 16*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		WriteError(w, http.StatusUnprocessableEntity, "Username dan password wajib diisi")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "Password minimal 6 karakter")
		return
	}
	id := uuid.NewString()
	_, err = h.DB.ExecContext(r.Context(), `
		INSERT INTO users (id, username, password_hash, is_platform_admin, is_active, created_at)
		VALUES ($1, $2, $3, $4, TRUE, CURRENT_TIMESTAMP)
	`, id, req.Username, hash, req.IsPlatformAdmin)
	if err != nil {
		if isUniqueViolation(err) {
			WriteError(w, http.StatusConflict, "Username sudah terdaftar")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Gagal membuat pengguna")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.user.create", "ok", slog.String("target_user_id", id))
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id":                id,
		"username":          req.Username,
		"is_platform_admin": req.IsPlatformAdmin,
		"is_active":         true,
	})
}

func (h *AdminAPIHandler) ToggleUserActive(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		WriteError(w, http.StatusBadRequest, "ID pengguna tidak valid")
		return
	}
	var newActive bool
	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var cur bool
		if err := tx.QueryRowContext(r.Context(), `SELECT is_active FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&cur); err != nil {
			return err
		}
		newActive = !cur
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET is_active = $1 WHERE id = $2`, newActive, userID); err != nil {
			return err
		}
		if cur {
			return auth.RevokeAllUserSessions(r.Context(), tx, userID)
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Pengguna tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mengubah status pengguna")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.user.toggle_active", "ok", slog.String("target_user_id", userID), slog.Bool("is_active", newActive))
	WriteJSON(w, http.StatusOK, map[string]any{"id": userID, "is_active": newActive})
}

func (h *AdminAPIHandler) ToggleUserAdmin(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	if userID == "" {
		WriteError(w, http.StatusBadRequest, "ID pengguna tidak valid")
		return
	}
	var newAdmin bool
	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var cur bool
		if err := tx.QueryRowContext(r.Context(), `SELECT is_platform_admin FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&cur); err != nil {
			return err
		}
		newAdmin = !cur
		if _, err := tx.ExecContext(r.Context(), `UPDATE users SET is_platform_admin = $1 WHERE id = $2`, newAdmin, userID); err != nil {
			return err
		}
		if cur {
			return auth.RevokeAllUserSessions(r.Context(), tx, userID)
		}
		return auth.RotateUserSessions(r.Context(), tx, userID)
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Pengguna tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mengubah peran admin")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.user.toggle_admin", "ok", slog.String("target_user_id", userID), slog.Bool("is_platform_admin", newAdmin))
	WriteJSON(w, http.StatusOK, map[string]any{"id": userID, "is_platform_admin": newAdmin})
}

func (h *AdminAPIHandler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req struct {
		NewPassword string `json:"new_password"`
	}
	if err := DecodeJSON(w, r, 8*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if userID == "" || len(req.NewPassword) < 6 {
		WriteError(w, http.StatusUnprocessableEntity, "Password baru minimal 6 karakter")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "Gagal memproses password")
		return
	}
	err = runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(), `UPDATE users SET password_hash = $1 WHERE id = $2`, hash, userID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		return auth.RevokeAllUserSessions(r.Context(), tx, userID)
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Pengguna tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mengubah password")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.user.reset_password", "ok", slog.String("target_user_id", userID))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "password_updated"})
}

// --- Grup dan Pemimpin Grup ---

func (h *AdminAPIHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT g.id, g.name, COALESCE(g.leader_user_id, ''), COALESCE(u.username, ''), g.created_at
		FROM groups g
		LEFT JOIN users u ON u.id = g.leader_user_id
		ORDER BY g.name ASC
	`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat daftar group")
		return
	}
	defer rows.Close()

	groups := make([]adminGroupDTO, 0)
	byID := make(map[string]*adminGroupDTO)
	for rows.Next() {
		var g adminGroupDTO
		if err := rows.Scan(&g.ID, &g.Name, &g.LeaderUserID, &g.LeaderUsername, &g.CreatedAt); err != nil {
			continue
		}
		g.Members = []groupMemberDTO{}
		groups = append(groups, g)
	}
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
	}

	mRows, err := h.DB.QueryContext(r.Context(), `
		SELECT ug.group_id, u.id, u.username
		FROM user_groups ug
		JOIN users u ON u.id = ug.user_id
		ORDER BY u.username ASC
	`)
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var gid, uid, uname string
			if err := mRows.Scan(&gid, &uid, &uname); err == nil {
				if g, ok := byID[gid]; ok {
					g.Members = append(g.Members, groupMemberDTO{UserID: uid, Username: uname})
				}
			}
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (h *AdminAPIHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		LeaderUserID string `json:"leader_user_id"`
	}
	if err := DecodeJSON(w, r, 16*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LeaderUserID = strings.TrimSpace(req.LeaderUserID)
	if req.Name == "" || req.LeaderUserID == "" {
		WriteError(w, http.StatusUnprocessableEntity, "Nama group dan leader_user_id wajib diisi")
		return
	}

	id := uuid.NewString()
	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var active bool
		if err := tx.QueryRowContext(r.Context(), `SELECT is_active FROM users WHERE id = $1`, req.LeaderUserID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("leader_not_found")
			}
			return err
		}
		if !active {
			return errors.New("leader_inactive")
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO groups (id, name, leader_user_id, created_at)
			VALUES ($1, $2, $3, CURRENT_TIMESTAMP)
		`, id, req.Name, req.LeaderUserID); err != nil {
			return err
		}
		// Pastikan leader yang ditunjuk otomatis menjadi anggota grup.
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, req.LeaderUserID, id); err != nil {
			return err
		}
		return auth.RotateUserSessions(r.Context(), tx, req.LeaderUserID)
	})
	if err != nil {
		switch {
		case err.Error() == "leader_not_found":
			WriteError(w, http.StatusUnprocessableEntity, "Leader yang dipilih tidak ditemukan")
		case err.Error() == "leader_inactive":
			WriteError(w, http.StatusUnprocessableEntity, "Leader group harus merupakan pengguna yang aktif")
		case isUniqueViolation(err):
			WriteError(w, http.StatusConflict, "Nama group sudah digunakan")
		default:
			WriteError(w, http.StatusInternalServerError, "Gagal membuat group")
		}
		return
	}

	telemetry.LogAudit(r.Context(), "admin.group.create", "ok", slog.String("group_id", id), slog.String("leader_user_id", req.LeaderUserID))
	WriteJSON(w, http.StatusCreated, map[string]any{
		"id":             id,
		"name":           req.Name,
		"leader_user_id": req.LeaderUserID,
	})
}

func (h *AdminAPIHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	var req struct {
		Name         string `json:"name"`
		LeaderUserID string `json:"leader_user_id"`
	}
	if err := DecodeJSON(w, r, 16*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.LeaderUserID = strings.TrimSpace(req.LeaderUserID)
	if groupID == "" || req.Name == "" || req.LeaderUserID == "" {
		WriteError(w, http.StatusUnprocessableEntity, "Nama group dan leader_user_id wajib diisi")
		return
	}

	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var existing string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&existing); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRowContext(r.Context(), `SELECT is_active FROM users WHERE id = $1`, req.LeaderUserID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("leader_not_found")
			}
			return err
		}
		if !active {
			return errors.New("leader_inactive")
		}
		if _, err := tx.ExecContext(r.Context(), `
			UPDATE groups SET name = $1, leader_user_id = $2 WHERE id = $3
		`, req.Name, req.LeaderUserID, groupID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, req.LeaderUserID, groupID); err != nil {
			return err
		}
		return auth.RotateUserSessions(r.Context(), tx, req.LeaderUserID)
	})
	if err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			WriteError(w, http.StatusNotFound, "Group tidak ditemukan")
		case err.Error() == "leader_not_found":
			WriteError(w, http.StatusUnprocessableEntity, "Leader yang dipilih tidak ditemukan")
		case err.Error() == "leader_inactive":
			WriteError(w, http.StatusUnprocessableEntity, "Leader group harus merupakan pengguna yang aktif")
		case isUniqueViolation(err):
			WriteError(w, http.StatusConflict, "Nama group sudah digunakan")
		default:
			WriteError(w, http.StatusInternalServerError, "Gagal memperbarui group")
		}
		return
	}

	telemetry.LogAudit(r.Context(), "admin.group.update", "ok", slog.String("group_id", groupID), slog.String("leader_user_id", req.LeaderUserID))
	WriteJSON(w, http.StatusOK, map[string]any{
		"id":             groupID,
		"name":           req.Name,
		"leader_user_id": req.LeaderUserID,
	})
}

func (h *AdminAPIHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		WriteError(w, http.StatusBadRequest, "ID group tidak valid")
		return
	}
	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var existing string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&existing); err != nil {
			return err
		}
		memberIDs, err := queryGroupMembers(r.Context(), tx, groupID)
		if err != nil {
			return err
		}
		for _, uid := range memberIDs {
			if err := auth.RevokeAllUserSessions(r.Context(), tx, uid); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(r.Context(), `DELETE FROM groups WHERE id = $1`, groupID)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Group tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal menghapus group")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.group.delete", "ok", slog.String("group_id", groupID))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *AdminAPIHandler) AddGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := DecodeJSON(w, r, 8*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if groupID == "" || req.UserID == "" {
		WriteError(w, http.StatusUnprocessableEntity, "group_id dan user_id wajib diisi")
		return
	}

	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var existing string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&existing); err != nil {
			return err
		}
		res, err := tx.ExecContext(r.Context(), `
			INSERT INTO user_groups (user_id, group_id) VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, req.UserID, groupID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return auth.RotateUserSessions(r.Context(), tx, req.UserID)
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Group tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal menambahkan anggota ke group")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.group.member_add", "ok", slog.String("group_id", groupID), slog.String("target_user_id", req.UserID))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "added"})
}

func (h *AdminAPIHandler) RemoveGroupMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	userID := r.PathValue("user_id")
	if groupID == "" || userID == "" {
		WriteError(w, http.StatusBadRequest, "group_id dan user_id wajib diisi")
		return
	}

	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var leaderID sql.NullString
		if err := tx.QueryRowContext(r.Context(), `SELECT leader_user_id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&leaderID); err != nil {
			return err
		}
		if leaderID.Valid && leaderID.String == userID {
			return errors.New("cannot_remove_leader")
		}
		res, err := tx.ExecContext(r.Context(), `DELETE FROM user_groups WHERE user_id = $1 AND group_id = $2`, userID, groupID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		return auth.RevokeAllUserSessions(r.Context(), tx, userID)
	})
	if err != nil {
		switch {
		case err.Error() == "cannot_remove_leader":
			WriteError(w, http.StatusConflict, "Ganti leader group terlebih dahulu sebelum menghapus user ini dari group")
		case errors.Is(err, sql.ErrNoRows):
			WriteError(w, http.StatusNotFound, "Keanggotaan tidak ditemukan")
		default:
			WriteError(w, http.StatusInternalServerError, "Gagal menghapus anggota dari group")
		}
		return
	}
	telemetry.LogAudit(r.Context(), "admin.group.member_remove", "ok", slog.String("group_id", groupID), slog.String("target_user_id", userID))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// --- Hak Akses Database ---

func (h *AdminAPIHandler) ListGrants(w http.ResponseWriter, r *http.Request) {
	rows, err := h.DB.QueryContext(r.Context(), `
		SELECT g.id, g.name, dc.id, dc.name, dc.dbms_type, gdg.access_level
		FROM group_database_grants gdg
		JOIN groups g ON gdg.group_id = g.id
		JOIN database_connections dc ON gdg.database_id = dc.id
		ORDER BY g.name ASC, dc.name ASC
	`)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat hak akses database")
		return
	}
	defer rows.Close()

	grants := make([]adminGrantDTO, 0)
	for rows.Next() {
		var gr adminGrantDTO
		var rawLevel string
		if err := rows.Scan(&gr.GroupID, &gr.GroupName, &gr.DatabaseID, &gr.DatabaseName, &gr.DBMS, &rawLevel); err == nil {
			gr.AccessLevel = access.ParseAccessLevel(rawLevel)
			grants = append(grants, gr)
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"grants": grants})
}

func (h *AdminAPIHandler) AssignGrant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID     string `json:"group_id"`
		DatabaseID  string `json:"database_id"`
		AccessLevel string `json:"access_level"`
	}
	if err := DecodeJSON(w, r, 8*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	targetLevel := access.ParseAccessLevel(req.AccessLevel)
	if strings.TrimSpace(req.GroupID) == "" || strings.TrimSpace(req.DatabaseID) == "" || targetLevel == access.AccessNone {
		WriteError(w, http.StatusUnprocessableEntity, "group_id, database_id, dan access_level (VIEW_ONLY/VIEW_EDIT) wajib valid")
		return
	}

	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var existing string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, req.GroupID).Scan(&existing); err != nil {
			return err
		}
		var prevRaw string
		err := tx.QueryRowContext(r.Context(), `
			SELECT access_level FROM group_database_grants WHERE group_id = $1 AND database_id = $2
		`, req.GroupID, req.DatabaseID).Scan(&prevRaw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		prevLevel := access.ParseAccessLevel(prevRaw)

		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO group_database_grants (group_id, database_id, access_level)
			VALUES ($1, $2, $3)
			ON CONFLICT (group_id, database_id)
			DO UPDATE SET access_level = EXCLUDED.access_level
		`, req.GroupID, req.DatabaseID, string(targetLevel)); err != nil {
			return err
		}
		if targetLevel == prevLevel {
			return nil
		}
		memberIDs, err := queryGroupMembers(r.Context(), tx, req.GroupID)
		if err != nil {
			return err
		}
		for _, uid := range memberIDs {
			if access.IsHigherOrEqual(targetLevel, prevLevel) {
				err = auth.RotateUserSessions(r.Context(), tx, uid)
			} else {
				err = auth.RevokeAllUserSessions(r.Context(), tx, uid)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Group tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal menyimpan hak akses database")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.grant.assign", "ok",
		slog.String("group_id", req.GroupID),
		slog.String("database_id", req.DatabaseID),
		slog.String("access_level", string(targetLevel)),
	)
	WriteJSON(w, http.StatusOK, map[string]any{
		"group_id":     req.GroupID,
		"database_id":  req.DatabaseID,
		"access_level": targetLevel,
	})
}

func (h *AdminAPIHandler) RevokeGrant(w http.ResponseWriter, r *http.Request) {
	groupID := r.URL.Query().Get("group_id")
	databaseID := r.URL.Query().Get("database_id")
	if groupID == "" || databaseID == "" {
		var req struct {
			GroupID    string `json:"group_id"`
			DatabaseID string `json:"database_id"`
		}
		if err := DecodeJSON(w, r, 8*1024, &req); err == nil {
			groupID = req.GroupID
			databaseID = req.DatabaseID
		}
	}
	if strings.TrimSpace(groupID) == "" || strings.TrimSpace(databaseID) == "" {
		WriteError(w, http.StatusUnprocessableEntity, "group_id dan database_id wajib diisi")
		return
	}

	err := runTx(r.Context(), h.DB, func(tx *sql.Tx) error {
		var existing string
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&existing); err != nil {
			return err
		}
		memberIDs, err := queryGroupMembers(r.Context(), tx, groupID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(r.Context(), `DELETE FROM group_database_grants WHERE group_id = $1 AND database_id = $2`, groupID, databaseID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows
		}
		for _, uid := range memberIDs {
			if err := auth.RevokeAllUserSessions(r.Context(), tx, uid); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "Hak akses tidak ditemukan")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mencabut hak akses")
		return
	}
	telemetry.LogAudit(r.Context(), "admin.grant.revoke", "ok", slog.String("group_id", groupID), slog.String("database_id", databaseID))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// --- Koneksi Database Target ---

type connectionPayload struct {
	Name     string `json:"name"`
	DBMS     string `json:"dbms"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"db_name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AdminAPIHandler) ListConnections(w http.ResponseWriter, r *http.Request) {
	conns, err := h.Store.List(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal memuat koneksi database")
		return
	}
	if conns == nil {
		conns = []database.Connection{}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"connections": conns})
}

func (h *AdminAPIHandler) CreateConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionPayload
	if err := DecodeJSON(w, r, 16*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DBMS == "" {
		req.DBMS = "postgres"
	}
	if req.DBMS != "postgres" {
		WriteError(w, http.StatusUnprocessableEntity, "Milestone 2 hanya mendukung target database PostgreSQL")
		return
	}
	created, err := h.Store.Create(r.Context(), database.Connection{
		Name: req.Name, DBMS: req.DBMS, Host: req.Host, Port: req.Port, DBName: req.DBName, Username: req.Username,
	}, req.Password)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	telemetry.LogAudit(r.Context(), "admin.connection.create", "ok", slog.String("database_id", created.ID), slog.String("database_name", created.Name))
	WriteJSON(w, http.StatusCreated, created)
}

func (h *AdminAPIHandler) UpdateConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req connectionPayload
	if err := DecodeJSON(w, r, 16*1024, &req); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.DBMS == "" {
		req.DBMS = "postgres"
	}
	if req.DBMS != "postgres" {
		WriteError(w, http.StatusUnprocessableEntity, "Milestone 2 hanya mendukung target database PostgreSQL")
		return
	}
	err := h.Store.Update(r.Context(), id, database.Connection{
		Name: req.Name, DBMS: req.DBMS, Host: req.Host, Port: req.Port, DBName: req.DBName, Username: req.Username,
	}, req.Password)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	h.Manager.Invalidate(id)
	updated, err := h.Store.Get(r.Context(), id)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	telemetry.LogAudit(r.Context(), "admin.connection.update", "ok", slog.String("database_id", id))
	WriteJSON(w, http.StatusOK, updated)
}

func (h *AdminAPIHandler) DeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Store.Delete(r.Context(), id); err != nil {
		HandleDomainError(w, err)
		return
	}
	h.Manager.Invalidate(id)
	telemetry.LogAudit(r.Context(), "admin.connection.delete", "ok", slog.String("database_id", id))
	WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *AdminAPIHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Manager.Test(r.Context(), id); err != nil {
		if errors.Is(err, database.ErrConnectionNotFound) {
			WriteError(w, http.StatusNotFound, "Koneksi tidak ditemukan")
			return
		}
		WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "Uji koneksi berhasil. Database target dapat diakses."})
}

func runTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func queryGroupMembers(ctx context.Context, tx *sql.Tx, groupID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM user_groups WHERE group_id = $1 ORDER BY user_id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	return ids, rows.Err()
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unique") || strings.Contains(s, "duplicate") || strings.Contains(s, "23505")
}
