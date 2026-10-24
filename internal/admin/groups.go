package admin

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// GroupMember represents a user belonging to a group.
type GroupMember struct {
	UserID   string
	Username string
}

// GroupViewModel represents a group with its members for admin display.
type GroupViewModel struct {
	ID        string
	Name      string
	CreatedAt time.Time
	Members   []GroupMember
}

// GroupAdminHandler handles administration operations on groups and memberships.
type GroupAdminHandler struct {
	DB        *sql.DB
	Templates auth.TemplateRenderer
}

// ListGroups renders the group management page.
func (h *GroupAdminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	currentUser := auth.GetUserFromContext(ctx)
	currentSession := auth.GetSessionFromContext(ctx)

	// Fetch all groups
	rows, err := h.DB.QueryContext(ctx, "SELECT id, name, created_at FROM groups ORDER BY name ASC")
	if err != nil {
		log.Printf("Failed to load groups: %v", err)
		http.Error(w, "Failed to load groups", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var groups []GroupViewModel
	groupMap := make(map[string]*GroupViewModel)

	for rows.Next() {
		var g GroupViewModel
		if err := rows.Scan(&g.ID, &g.Name, &g.CreatedAt); err != nil {
			continue
		}
		groups = append(groups, g)
	}

	for i := range groups {
		groupMap[groups[i].ID] = &groups[i]
	}

	// Fetch all group members
	memberRows, err := h.DB.QueryContext(ctx, `
		SELECT ug.group_id, u.id, u.username
		FROM user_groups ug
		JOIN users u ON ug.user_id = u.id
		ORDER BY u.username ASC
	`)
	if err == nil {
		defer memberRows.Close()
		for memberRows.Next() {
			var groupID, userID, username string
			if err := memberRows.Scan(&groupID, &userID, &username); err == nil {
				if g, ok := groupMap[groupID]; ok {
					g.Members = append(g.Members, GroupMember{
						UserID:   userID,
						Username: username,
					})
				}
			}
		}
	}

	// Fetch all available active users for the "Add Member" dropdown
	allUsersRows, err := h.DB.QueryContext(ctx, "SELECT id, username FROM users WHERE is_active = TRUE ORDER BY username ASC")
	var availableUsers []GroupMember
	if err == nil {
		defer allUsersRows.Close()
		for allUsersRows.Next() {
			var u GroupMember
			if err := allUsersRows.Scan(&u.UserID, &u.Username); err == nil {
				availableUsers = append(availableUsers, u)
			}
		}
	}

	data := map[string]interface{}{
		"CurrentUser":    currentUser,
		"CSRFToken":      currentSession.CSRFToken,
		"Groups":         groups,
		"AvailableUsers": availableUsers,
		"ActiveTab":      "groups",
		"SuccessMessage": r.URL.Query().Get("success"),
		"ErrorMessage":   r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "groups.html", data); err != nil {
		log.Printf("Render error: %v", err)
		http.Error(w, "Render error", http.StatusInternalServerError)
	}
}

// CreateGroup creates a new group.
func (h *GroupAdminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		http.Redirect(w, r, "/admin/groups?error=Nama+group+tidak+boleh+kosong", http.StatusSeeOther)
		return
	}

	groupID := uuid.New().String()
	_, err := h.DB.ExecContext(r.Context(), "INSERT INTO groups (id, name, created_at) VALUES ($1, $2, CURRENT_TIMESTAMP)", groupID, name)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			http.Redirect(w, r, "/admin/groups?error=Nama+group+sudah+digunakan", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/groups?error=Gagal+membuat+group", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/groups?success=Group+berhasil+dibuat", http.StatusSeeOther)
}

// EditGroup updates an existing group's name.
func (h *GroupAdminHandler) EditGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	name := strings.TrimSpace(r.PostFormValue("name"))

	if groupID == "" || name == "" {
		http.Redirect(w, r, "/admin/groups?error=Data+group+tidak+lengkap", http.StatusSeeOther)
		return
	}

	err := requireAffected(h.DB.ExecContext(r.Context(), "UPDATE groups SET name = $1 WHERE id = $2", name, groupID))
	if err != nil {
		http.Redirect(w, r, "/admin/groups?error=Gagal+memperbarui+nama+group", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/groups?success=Nama+group+berhasil+diperbarui", http.StatusSeeOther)
}

// DeleteGroup deletes a group and revokes sessions of affected members.
func (h *GroupAdminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		http.Redirect(w, r, "/admin/groups?error=ID+group+tidak+valid", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	err := withTransaction(ctx, h.DB, func(tx *sql.Tx) error {
		if err := lockGroup(ctx, tx, groupID); err != nil {
			return err
		}
		memberIDs, err := groupMemberIDs(ctx, tx, groupID)
		if err != nil {
			return err
		}
		for _, uid := range memberIDs {
			if err := auth.RevokeAllUserSessions(ctx, tx, uid); err != nil {
				return err
			}
		}
		// The delete cascades to memberships and grants only after revocation succeeds.
		return requireAffected(tx.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", groupID))
	})
	if err != nil {
		http.Redirect(w, r, "/admin/groups?error=Gagal+menghapus+group", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/groups?success=Group+berhasil+dihapus", http.StatusSeeOther)
}

// AddMember adds a user to a group (Privilege escalation: rotates session without logout).
func (h *GroupAdminHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	userID := strings.TrimSpace(r.PostFormValue("user_id"))

	if groupID == "" || userID == "" {
		http.Redirect(w, r, "/admin/groups?error=Data+keanggotaan+tidak+lengkap", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	query := `
		INSERT INTO user_groups (user_id, group_id)
		VALUES ($1, $2)
		ON CONFLICT (user_id, group_id) DO NOTHING
	`
	err := withTransaction(ctx, h.DB, func(tx *sql.Tx) error {
		if err := lockGroup(ctx, tx, groupID); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, query, userID, groupID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count == 0 {
			return err // An existing membership is an idempotent success.
		}
		return auth.RotateUserSessions(ctx, tx, userID)
	})
	if err != nil {
		http.Redirect(w, r, "/admin/groups?error=Gagal+menambahkan+anggota+ke+group", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/groups?success=Anggota+berhasil+ditambahkan+ke+group", http.StatusSeeOther)
}

// RemoveMember removes a user from a group (Access revocation: revokes sessions).
func (h *GroupAdminHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	userID := strings.TrimSpace(r.PostFormValue("user_id"))

	if groupID == "" || userID == "" {
		http.Redirect(w, r, "/admin/groups?error=Data+keanggotaan+tidak+lengkap", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	err := withTransaction(ctx, h.DB, func(tx *sql.Tx) error {
		if err := lockGroup(ctx, tx, groupID); err != nil {
			return err
		}
		if err := requireAffected(tx.ExecContext(ctx, "DELETE FROM user_groups WHERE user_id = $1 AND group_id = $2", userID, groupID)); err != nil {
			return err
		}
		return auth.RevokeAllUserSessions(ctx, tx, userID)
	})
	if err != nil {
		http.Redirect(w, r, "/admin/groups?error=Gagal+menghapus+anggota+dari+group", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/groups?success=Anggota+berhasil+dihapus+dari+group", http.StatusSeeOther)
}
