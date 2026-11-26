package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

const (
	DecisionPending  = "pending"
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

var (
	ErrNoDatabaseGroups    = errors.New("tidak ada group yang memiliki akses ke database ini; tetapkan grant dan leader terlebih dahulu")
	ErrMissingGroupLeader  = errors.New("terdapat group pengakses database yang belum memiliki leader aktif")
	ErrNotGroupLeader      = errors.New("hanya leader group yang ditugaskan yang dapat menyetujui atau menolak perubahan ini")
	ErrInvalidDecision     = errors.New("keputusan approval harus 'approved' atau 'rejected'")
	ErrApprovalStale       = errors.New("approval tidak lagi valid karena perubahan keanggotaan grant atau leader group; ajukan ulang review")
	ErrInvalidStateChange  = errors.New("transisi status change request tidak valid untuk status saat ini")
)

// ApprovalRecord merepresentasikan keputusan persetujuan satu leader grup untuk sebuah change request.
type ApprovalRecord struct {
	ID                  string     `json:"id"`
	RequestID           string     `json:"request_id"`
	GroupID             string     `json:"group_id"`
	GroupName           string     `json:"group_name"`
	LeaderUserID        string     `json:"leader_user_id"`
	LeaderUsername      string     `json:"leader_username"`
	RequiredAccessLevel string     `json:"required_access_level"`
	Decision            string     `json:"decision"`
	Comment             string     `json:"comment,omitempty"`
	DecidedAt           *time.Time `json:"decided_at,omitempty"`
}

// SubmitRequestForReview mengubah draf request menjadi pending_review dan membuat daftar persetujuan
// untuk setiap grup yang memiliki akses VIEW_ONLY atau VIEW_EDIT ke database_id.
func SubmitRequestForReview(ctx context.Context, db *sql.DB, requestID, actorUserID string) (*RequestDetail, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var databaseID, status string
	err = tx.QueryRowContext(ctx, `
		SELECT database_id, status
		FROM migration_requests
		WHERE id = $1
		FOR UPDATE
	`, requestID).Scan(&databaseID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	if status != ReqStatusDraft {
		return nil, fmt.Errorf("%w: hanya request berstatus 'draft' yang dapat diajukan ke review", ErrInvalidStateChange)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.name, COALESCE(g.leader_user_id, ''), COALESCE(u.is_active, FALSE), gdg.access_level
		FROM group_database_grants gdg
		JOIN groups g ON g.id = gdg.group_id
		LEFT JOIN users u ON u.id = g.leader_user_id
		WHERE gdg.database_id = $1 AND gdg.access_level IN ('VIEW_ONLY', 'VIEW_EDIT')
		ORDER BY g.name ASC
	`, databaseID)
	if err != nil {
		return nil, err
	}

	type requiredGroup struct {
		groupID      string
		groupName    string
		leaderUserID string
		leaderActive bool
		accessLevel  string
	}
	var groups []requiredGroup
	for rows.Next() {
		var rg requiredGroup
		if err := rows.Scan(&rg.groupID, &rg.groupName, &rg.leaderUserID, &rg.leaderActive, &rg.accessLevel); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, rg)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(groups) == 0 {
		return nil, ErrNoDatabaseGroups
	}
	for _, g := range groups {
		if g.leaderUserID == "" || !g.leaderActive {
			return nil, fmt.Errorf("%w: group %q belum memiliki leader aktif", ErrMissingGroupLeader, g.groupName)
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM migration_approvals WHERE request_id = $1`, requestID); err != nil {
		return nil, err
	}

	for _, g := range groups {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO migration_approvals (
				id, request_id, group_id, leader_user_id, required_access_level, decision, comment
			) VALUES ($1, $2, $3, $4, $5, 'pending', '')
		`, uuid.NewString(), requestID, g.groupID, g.leaderUserID, g.accessLevel); err != nil {
			return nil, fmt.Errorf("create approval requirement for group %s: %w", g.groupName, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE migration_requests
		SET status = 'pending_review',
		    submitted_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1
	`, requestID); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	telemetry.LogAudit(ctx, "migration.request.submit", "pending_review",
		slog.String("request_id", requestID),
		slog.String("database_id", databaseID),
		slog.String("submitted_by", actorUserID),
		slog.Int("required_group_approvals", len(groups)),
	)

	return GetRequestDetail(ctx, db, requestID)
}

// DecideApproval mencatat keputusan approve atau reject dari leader grup.
// Memastikan actorUserID adalah leader aktif dan memproses semua grupnya secara atomik.
func DecideApproval(ctx context.Context, db *sql.DB, requestID, actorUserID, decision, comment string) (*RequestDetail, error) {
	decision = strings.ToLower(strings.TrimSpace(decision))
	comment = strings.TrimSpace(comment)
	if decision != DecisionApproved && decision != DecisionRejected {
		return nil, ErrInvalidDecision
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var databaseID, reqStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT database_id, status
		FROM migration_requests
		WHERE id = $1
		FOR UPDATE
	`, requestID).Scan(&databaseID, &reqStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRequestNotFound
		}
		return nil, err
	}
	if reqStatus != ReqStatusPendingReview {
		return nil, fmt.Errorf("%w: approval hanya dapat diberikan pada status 'pending_review'", ErrInvalidStateChange)
	}

	res, err := tx.ExecContext(ctx, `
		UPDATE migration_approvals ma
		SET decision = $1,
		    comment = $2,
		    decided_at = CURRENT_TIMESTAMP
		FROM groups g
		WHERE ma.request_id = $3
		  AND ma.group_id = g.id
		  AND ma.leader_user_id = $4
		  AND g.leader_user_id = $4
		  AND ma.decision = 'pending'
		  AND EXISTS (
		      SELECT 1 FROM group_database_grants gdg
		      WHERE gdg.group_id = ma.group_id
		        AND gdg.database_id = $5
		        AND gdg.access_level IN ('VIEW_ONLY', 'VIEW_EDIT')
		  )
	`, decision, comment, requestID, actorUserID, databaseID)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, ErrNotGroupLeader
	}

	newStatus := ReqStatusPendingReview
	if decision == DecisionRejected {
		newStatus = ReqStatusRejected
		if _, err := tx.ExecContext(ctx, `
			UPDATE migration_requests
			SET status = 'rejected', updated_at = CURRENT_TIMESTAMP
			WHERE id = $1
		`, requestID); err != nil {
			return nil, err
		}
	} else {
		var pendingOrRejected int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM migration_approvals
			WHERE request_id = $1 AND decision <> 'approved'
		`, requestID).Scan(&pendingOrRejected); err != nil {
			return nil, err
		}
		if pendingOrRejected == 0 {
			newStatus = ReqStatusApproved
			if _, err := tx.ExecContext(ctx, `
				UPDATE migration_requests
				SET status = 'approved',
				    approved_at = CURRENT_TIMESTAMP,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = $1
			`, requestID); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	telemetry.LogAudit(ctx, "migration.approval."+decision, newStatus,
		slog.String("request_id", requestID),
		slog.String("database_id", databaseID),
		slog.String("leader_user_id", actorUserID),
		slog.Int64("groups_decided", affected),
	)

	return GetRequestDetail(ctx, db, requestID)
}

// DBQuerier allows approval queries to share an active *sql.Tx or use *sql.DB.
type DBQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// VerifyCurrentApprovals memastikan setiap grup berakses VIEW_ONLY/VIEW_EDIT pada databaseID
// memiliki leader aktif dan baris persetujuan 'approved' dari leader tersebut (untuk API dan Worker).
func VerifyCurrentApprovals(ctx context.Context, db DBQuerier, requestID, databaseID string) error {
	rows, err := db.QueryContext(ctx, `
		SELECT g.id, g.name, COALESCE(g.leader_user_id, ''), COALESCE(u.is_active, FALSE),
		       COALESCE(ma.decision, ''), COALESCE(ma.leader_user_id, '')
		FROM group_database_grants gdg
		JOIN groups g ON g.id = gdg.group_id
		LEFT JOIN users u ON u.id = g.leader_user_id
		LEFT JOIN migration_approvals ma ON ma.request_id = $1 AND ma.group_id = g.id
		WHERE gdg.database_id = $2 AND gdg.access_level IN ('VIEW_ONLY', 'VIEW_EDIT')
		ORDER BY g.name ASC
	`, requestID, databaseID)
	if err != nil {
		return err
	}
	defer rows.Close()

	groupCount := 0
	for rows.Next() {
		groupCount++
		var gid, gname, curLeader, decision, approvedLeader string
		var leaderActive bool
		if err := rows.Scan(&gid, &gname, &curLeader, &leaderActive, &decision, &approvedLeader); err != nil {
			return err
		}
		if curLeader == "" || !leaderActive {
			return fmt.Errorf("%w: group %q tidak memiliki leader aktif", ErrApprovalStale, gname)
		}
		if decision != DecisionApproved || approvedLeader != curLeader {
			return fmt.Errorf("%w: approval leader saat ini untuk group %q belum terpenuhi", ErrApprovalStale, gname)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if groupCount == 0 {
		return ErrNoDatabaseGroups
	}
	return nil
}

// ListApprovalsForRequest mengambil semua catatan persetujuan grup untuk satu request.
func ListApprovalsForRequest(ctx context.Context, db DBQuerier, requestID string) ([]ApprovalRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT ma.id, ma.request_id, ma.group_id, g.name,
		       ma.leader_user_id, COALESCE(u.username, ''),
		       ma.required_access_level, ma.decision, ma.comment, ma.decided_at
		FROM migration_approvals ma
		JOIN groups g ON g.id = ma.group_id
		LEFT JOIN users u ON u.id = ma.leader_user_id
		WHERE ma.request_id = $1
		ORDER BY g.name ASC
	`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ApprovalRecord, 0)
	for rows.Next() {
		var a ApprovalRecord
		if err := rows.Scan(
			&a.ID, &a.RequestID, &a.GroupID, &a.GroupName,
			&a.LeaderUserID, &a.LeaderUsername,
			&a.RequiredAccessLevel, &a.Decision, &a.Comment, &a.DecidedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListApprovalsForRequests memuat catatan persetujuan untuk banyak request dalam satu query.
func ListApprovalsForRequests(ctx context.Context, db DBQuerier, requestIDs []string) (map[string][]ApprovalRecord, error) {
	out := make(map[string][]ApprovalRecord, len(requestIDs))
	if len(requestIDs) == 0 {
		return out, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT ma.id, ma.request_id, ma.group_id, g.name,
		       ma.leader_user_id, COALESCE(u.username, ''),
		       ma.required_access_level, ma.decision, ma.comment, ma.decided_at
		FROM migration_approvals ma
		JOIN groups g ON g.id = ma.group_id
		LEFT JOIN users u ON u.id = ma.leader_user_id
		WHERE ma.request_id = ANY($1::text[])
		ORDER BY ma.request_id ASC, g.name ASC
	`, requestIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var a ApprovalRecord
		if err := rows.Scan(
			&a.ID, &a.RequestID, &a.GroupID, &a.GroupName,
			&a.LeaderUserID, &a.LeaderUsername,
			&a.RequiredAccessLevel, &a.Decision, &a.Comment, &a.DecidedAt,
		); err != nil {
			return nil, err
		}
		out[a.RequestID] = append(out[a.RequestID], a)
	}
	return out, rows.Err()
}
