package admin

import (
	"database/sql"
	"log"
	"net/http"
	"strings"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

// GrantViewModel represents a database grant assigned to a group.
type GrantViewModel struct {
	GroupID      string
	GroupName    string
	DatabaseID   string
	DatabaseName string
	DBMS         string
	AccessLevel  access.AccessLevel
}

// DatabaseOption represents a registered database target for selection.
type DatabaseOption struct {
	ID   string
	Name string
	DBMS string
}

// GroupOption represents a group for selection.
type GroupOption struct {
	ID   string
	Name string
}

// GrantAdminHandler handles administration operations on group database permissions.
type GrantAdminHandler struct {
	DB        *sql.DB
	Templates auth.TemplateRenderer
}

// ListGrants renders the grant management page.
func (h *GrantAdminHandler) ListGrants(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	currentUser := auth.GetUserFromContext(ctx)
	currentSession := auth.GetSessionFromContext(ctx)

	// Fetch all existing grants
	rows, err := h.DB.QueryContext(ctx, `
		SELECT g.id AS group_id, g.name AS group_name,
		       dc.id AS db_id, dc.name AS db_name, dc.dbms_type,
		       gdg.access_level
		FROM group_database_grants gdg
		JOIN groups g ON gdg.group_id = g.id
		JOIN database_connections dc ON gdg.database_id = dc.id
		ORDER BY g.name ASC, dc.name ASC
	`)
	if err != nil {
		log.Printf("Failed to load grants: %v", err)
		http.Error(w, "Failed to load database grants", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var grants []GrantViewModel
	for rows.Next() {
		var gr GrantViewModel
		var rawLevel string
		if err := rows.Scan(&gr.GroupID, &gr.GroupName, &gr.DatabaseID, &gr.DatabaseName, &gr.DBMS, &rawLevel); err == nil {
			gr.AccessLevel = access.ParseAccessLevel(rawLevel)
			grants = append(grants, gr)
		}
	}

	// Fetch groups for dropdown
	gRows, err := h.DB.QueryContext(ctx, "SELECT id, name FROM groups ORDER BY name ASC")
	var groups []GroupOption
	if err == nil {
		defer gRows.Close()
		for gRows.Next() {
			var opt GroupOption
			if err := gRows.Scan(&opt.ID, &opt.Name); err == nil {
				groups = append(groups, opt)
			}
		}
	}

	// Fetch database connections for dropdown
	dRows, err := h.DB.QueryContext(ctx, "SELECT id, name, dbms_type FROM database_connections ORDER BY name ASC")
	var databases []DatabaseOption
	if err == nil {
		defer dRows.Close()
		for dRows.Next() {
			var opt DatabaseOption
			if err := dRows.Scan(&opt.ID, &opt.Name, &opt.DBMS); err == nil {
				databases = append(databases, opt)
			}
		}
	}

	data := map[string]interface{}{
		"CurrentUser":    currentUser,
		"CSRFToken":      currentSession.CSRFToken,
		"Grants":         grants,
		"Groups":         groups,
		"Databases":      databases,
		"ActiveTab":      "grants",
		"SuccessMessage": r.URL.Query().Get("success"),
		"ErrorMessage":   r.URL.Query().Get("error"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.Templates.ExecuteTemplate(w, "grants.html", data); err != nil {
		log.Printf("Render error: %v", err)
		http.Error(w, "Render error", http.StatusInternalServerError)
	}
}

// AssignGrant assigns or updates a database grant for a group.
// Escalations rotate active sessions for group members; demotions revoke active sessions.
func (h *GrantAdminHandler) AssignGrant(w http.ResponseWriter, r *http.Request) {
	groupID := strings.TrimSpace(r.PostFormValue("group_id"))
	databaseID := strings.TrimSpace(r.PostFormValue("database_id"))
	rawLevel := strings.TrimSpace(r.PostFormValue("access_level"))

	targetLevel := access.ParseAccessLevel(rawLevel)
	if groupID == "" || databaseID == "" || targetLevel == access.AccessNone {
		http.Redirect(w, r, "/admin/grants?error=Pilihan+group,+database,+atau+hak+akses+tidak+valid", http.StatusSeeOther)
		return
	}

	ctx := r.Context()

	// Check previous grant level if any
	var previousRawLevel string
	var hasPrevious bool
	err := h.DB.QueryRowContext(ctx, `
		SELECT access_level FROM group_database_grants
		WHERE group_id = $1 AND database_id = $2
	`, groupID, databaseID).Scan(&previousRawLevel)
	if err == nil {
		hasPrevious = true
	}

	query := `
		INSERT INTO group_database_grants (group_id, database_id, access_level)
		VALUES ($1, $2, $3)
		ON CONFLICT (group_id, database_id)
		DO UPDATE SET access_level = EXCLUDED.access_level
	`
	_, err = h.DB.ExecContext(ctx, query, groupID, databaseID, string(targetLevel))
	if err != nil {
		log.Printf("Failed to assign grant: %v", err)
		http.Redirect(w, r, "/admin/grants?error=Gagal+menyimpan+hak+akses+database", http.StatusSeeOther)
		return
	}

	// Fetch all members of this group
	mRows, err := h.DB.QueryContext(ctx, "SELECT user_id FROM user_groups WHERE group_id = $1", groupID)
	var memberIDs []string
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var uid string
			if err := mRows.Scan(&uid); err == nil {
				memberIDs = append(memberIDs, uid)
			}
		}
	}

	if hasPrevious {
		prevLevel := access.ParseAccessLevel(previousRawLevel)
		if access.IsHigherOrEqual(targetLevel, prevLevel) {
			// Privilege escalation: rotate sessions smoothly
			for _, uid := range memberIDs {
				_ = auth.RotateUserSessions(ctx, h.DB, uid)
			}
		} else {
			// Privilege demotion: revoke sessions to force re-login
			for _, uid := range memberIDs {
				_ = auth.RevokeAllUserSessions(ctx, h.DB, uid)
			}
		}
	} else {
		// New grant given (escalation)
		for _, uid := range memberIDs {
			_ = auth.RotateUserSessions(ctx, h.DB, uid)
		}
	}

	http.Redirect(w, r, "/admin/grants?success=Hak+akses+database+berhasil+disimpan", http.StatusSeeOther)
}

// RevokeGrant removes a database grant from a group and revokes affected sessions.
func (h *GrantAdminHandler) RevokeGrant(w http.ResponseWriter, r *http.Request) {
	groupID := strings.TrimSpace(r.PostFormValue("group_id"))
	databaseID := strings.TrimSpace(r.PostFormValue("database_id"))

	if groupID == "" || databaseID == "" {
		http.Redirect(w, r, "/admin/grants?error=Data+hak+akses+tidak+lengkap", http.StatusSeeOther)
		return
	}

	ctx := r.Context()
	// Fetch group members before revoking
	mRows, err := h.DB.QueryContext(ctx, "SELECT user_id FROM user_groups WHERE group_id = $1", groupID)
	var memberIDs []string
	if err == nil {
		defer mRows.Close()
		for mRows.Next() {
			var uid string
			if err := mRows.Scan(&uid); err == nil {
				memberIDs = append(memberIDs, uid)
			}
		}
	}

	_, err = h.DB.ExecContext(ctx, "DELETE FROM group_database_grants WHERE group_id = $1 AND database_id = $2", groupID, databaseID)
	if err != nil {
		http.Redirect(w, r, "/admin/grants?error=Gagal+mencabut+hak+akses+database", http.StatusSeeOther)
		return
	}

	// Access revocation: revoke all sessions of affected members
	for _, uid := range memberIDs {
		_ = auth.RevokeAllUserSessions(ctx, h.DB, uid)
	}

	http.Redirect(w, r, "/admin/grants?success=Hak+akses+database+berhasil+dicabut", http.StatusSeeOther)
}
