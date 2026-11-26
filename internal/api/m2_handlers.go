package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/access"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

// M2APIHandler melayani seluruh endpoint JSON Milestone 2:
// definisi migrasi, change request, persetujuan leader multi-grup, enqueue asinkron, dan riwayat.
type M2APIHandler struct {
	DB       *sql.DB
	Store    *database.ConnectionStore
	Manager  *database.TargetManager
	Locks    *lock.Manager
	Workflow *migration.WorkflowService
}

func (h *M2APIHandler) requireDBAccess(w http.ResponseWriter, r *http.Request, dbID string, required access.AccessLevel) (*auth.User, access.AccessLevel, context.Context, bool) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return nil, access.AccessNone, r.Context(), false
	}
	level, err := access.GetEffectiveAccess(r.Context(), h.DB, user.ID, dbID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Gagal mengevaluasi hak akses")
		return nil, access.AccessNone, r.Context(), false
	}
	if !access.IsHigherOrEqual(level, required) {
		telemetry.LogAudit(r.Context(), "access.check", "denied", slog.String("database_id", dbID))
		WriteError(w, http.StatusForbidden, "Akses database tidak mencukupi")
		return nil, access.AccessNone, r.Context(), false
	}
	ctx := r.Context()
	if conn, cerr := h.Store.Get(ctx, dbID); cerr == nil && conn != nil {
		ctx = telemetry.WithDatabase(ctx, conn.Name)
	}
	return user, level, ctx, true
}

func (h *M2APIHandler) validateBatchAgainstTarget(ctx context.Context, dbID string, in migration.DefinitionInput) error {
	if in.ExecutionMode != migration.ModeBatch || h.Manager == nil {
		return nil
	}
	conn, err := h.Store.Get(ctx, dbID)
	if err != nil {
		return err
	}
	if conn.DBMS != "postgres" {
		return migration.ErrUnsupportedTargetDBMS
	}
	vCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	target, dbms, err := h.Manager.Open(vCtx, conn.ID)
	if err != nil {
		return database.ErrTargetUnavailable
	}
	cat, err := database.LoadCatalog(vCtx, target, dbms, conn.DBName)
	if err != nil {
		return err
	}
	return migration.ValidateBatchTargetCatalog(cat, in.BatchTable, in.BatchCursorColumn)
}

// --- Definisi Migrasi ---

func (h *M2APIHandler) ListDefinitions(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	_, level, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewOnly)
	if !ok {
		return
	}
	defs, err := migration.ListDefinitions(ctx, h.DB, dbID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	execs, err := migration.ListAllExecutionsChronological(ctx, h.DB, dbID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	state := migration.ComputeActiveState(execs)

	var lockInfo *lock.LockInfo
	if h.Locks != nil {
		if li, lerr := h.Locks.GetLock(ctx, dbID); lerr == nil {
			lockInfo = li
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"database_id":            dbID,
		"access_level":           level,
		"definitions":            defs,
		"active_tip":             state.ActiveTip,
		"blocking_execution":     state.BlockingExecution,
		"applied_migration_ids":  state.AppliedMigrationIDs,
		"rolled_back_migrations": state.RolledBackMigrations,
		"failed_migrations":      state.FailedMigrations,
		"lock":                   lockInfo,
	})
}

func (h *M2APIHandler) GetDefinition(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	migID := r.PathValue("migration_id")
	_, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewOnly)
	if !ok {
		return
	}
	def, err := migration.GetDefinition(ctx, h.DB, dbID, migID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, def)
}

func (h *M2APIHandler) CreateDefinition(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	user, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewEdit)
	if !ok {
		return
	}
	var in migration.DefinitionInput
	if err := DecodeJSON(w, r, 64*1024, &in); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	clean, _, err := migration.ValidateDefinitionInput(in)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	if clean.ExecutionMode == migration.ModeBatch {
		if err := h.validateBatchAgainstTarget(ctx, dbID, clean); err != nil {
			HandleDomainError(w, err)
			return
		}
	}

	def, err := migration.CreateDefinition(ctx, h.DB, dbID, user.ID, clean)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	telemetry.LogAudit(ctx, "migration.definition.create", "ok",
		slog.String("database_id", dbID),
		slog.String("migration_id", def.ID),
		slog.Int("version", def.Version),
		slog.String("execution_mode", def.ExecutionMode),
	)
	WriteJSON(w, http.StatusCreated, def)
}

func (h *M2APIHandler) UpdateDefinition(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	migID := r.PathValue("migration_id")
	_, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewEdit)
	if !ok {
		return
	}
	var in migration.DefinitionInput
	if err := DecodeJSON(w, r, 64*1024, &in); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	clean, _, err := migration.ValidateDefinitionInput(in)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	if clean.ExecutionMode == migration.ModeBatch {
		if err := h.validateBatchAgainstTarget(ctx, dbID, clean); err != nil {
			HandleDomainError(w, err)
			return
		}
	}

	def, err := migration.UpdateDefinition(ctx, h.DB, dbID, migID, clean)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	telemetry.LogAudit(ctx, "migration.definition.update", "ok",
		slog.String("database_id", dbID),
		slog.String("migration_id", def.ID),
		slog.Int("revision", def.Revision),
	)
	WriteJSON(w, http.StatusOK, def)
}

func (h *M2APIHandler) ValidateDefinition(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	_, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewEdit)
	if !ok {
		return
	}
	var in migration.DefinitionInput
	if err := DecodeJSON(w, r, 64*1024, &in); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	clean, report, err := migration.ValidateDefinitionInput(in)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	if clean.ExecutionMode == migration.ModeBatch {
		if err := h.validateBatchAgainstTarget(ctx, dbID, clean); err != nil {
			HandleDomainError(w, err)
			return
		}
		report.BatchValidated = true
		report.Message = "Validasi SQL dan kolom cursor batch pada target PostgreSQL berhasil."
	}
	WriteJSON(w, http.StatusOK, report)
}

// --- Change Request dan Persetujuan Leader Multi-Grup ---

func (h *M2APIHandler) ListRequests(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	dbID := r.PathValue("database_id")
	if dbID == "" {
		dbID = strings.TrimSpace(r.URL.Query().Get("database_id"))
	}
	if dbID != "" {
		if _, _, _, ok := h.requireDBAccess(w, r, dbID, access.AccessViewOnly); !ok {
			return
		}
	}
	reqs, err := migration.ListRequestsForUser(r.Context(), h.DB, user.ID, dbID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"requests": reqs})
}

func (h *M2APIHandler) GetRequest(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("request_id")
	detail, err := migration.GetRequestDetail(r.Context(), h.DB, reqID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	if _, _, _, ok := h.requireDBAccess(w, r, detail.DatabaseID, access.AccessViewOnly); !ok {
		return
	}
	WriteJSON(w, http.StatusOK, detail)
}

func (h *M2APIHandler) CreateRequest(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	user, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewEdit)
	if !ok {
		return
	}
	var in migration.CreateRequestInput
	if err := DecodeJSON(w, r, 16*1024, &in); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, err := h.Workflow.CreateRequest(ctx, dbID, user.ID, in)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusCreated, detail)
}

func (h *M2APIHandler) SubmitRequest(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("request_id")
	existing, err := migration.GetRequestDetail(r.Context(), h.DB, reqID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	user, _, ctx, ok := h.requireDBAccess(w, r, existing.DatabaseID, access.AccessViewEdit)
	if !ok {
		return
	}
	detail, err := migration.SubmitRequestForReview(ctx, h.DB, reqID, user.ID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, detail)
}

func (h *M2APIHandler) ApproveRequest(w http.ResponseWriter, r *http.Request) {
	h.decideRequest(w, r, migration.DecisionApproved)
}

func (h *M2APIHandler) RejectRequest(w http.ResponseWriter, r *http.Request) {
	h.decideRequest(w, r, migration.DecisionRejected)
}

func (h *M2APIHandler) decideRequest(w http.ResponseWriter, r *http.Request, decision string) {
	reqID := r.PathValue("request_id")
	user := auth.GetUserFromContext(r.Context())
	if user == nil {
		WriteError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	var body struct {
		Comment string `json:"comment"`
	}
	if err := DecodeOptionalJSON(w, r, 8*1024, &body); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	existing, err := migration.GetRequestDetail(r.Context(), h.DB, reqID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	ctx := r.Context()
	if conn, cerr := h.Store.Get(ctx, existing.DatabaseID); cerr == nil && conn != nil {
		ctx = telemetry.WithDatabase(ctx, conn.Name)
	}

	detail, err := migration.DecideApproval(ctx, h.DB, reqID, user.ID, decision, body.Comment)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, detail)
}

func (h *M2APIHandler) EnqueueRequest(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("request_id")
	existing, err := migration.GetRequestDetail(r.Context(), h.DB, reqID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	user, _, ctx, ok := h.requireDBAccess(w, r, existing.DatabaseID, access.AccessViewEdit)
	if !ok {
		return
	}
	res, err := h.Workflow.EnqueueRequest(ctx, reqID, user.ID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusAccepted, res)
}

func (h *M2APIHandler) CancelRequest(w http.ResponseWriter, r *http.Request) {
	reqID := r.PathValue("request_id")
	existing, err := migration.GetRequestDetail(r.Context(), h.DB, reqID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	user, _, ctx, ok := h.requireDBAccess(w, r, existing.DatabaseID, access.AccessViewEdit)
	if !ok {
		return
	}
	detail, err := migration.CancelRequest(ctx, h.DB, reqID, user.ID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, detail)
}

// --- Riwayat (+ Hide rollback) dan Pemantauan Job/Eksekusi ---

func (h *M2APIHandler) History(w http.ResponseWriter, r *http.Request) {
	dbID := r.PathValue("database_id")
	_, _, ctx, ok := h.requireDBAccess(w, r, dbID, access.AccessViewOnly)
	if !ok {
		return
	}
	hideRollback, _ := strconv.ParseBool(r.URL.Query().Get("hide_rollback"))
	resp, err := migration.ListHistoryM2(ctx, h.DB, dbID, hideRollback)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (h *M2APIHandler) GetJobStatus(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	view, err := migration.GetJobStatus(r.Context(), h.DB, jobID)
	if err != nil {
		HandleDomainError(w, err)
		return
	}
	if _, _, _, ok := h.requireDBAccess(w, r, view.DatabaseID, access.AccessViewOnly); !ok {
		return
	}
	WriteJSON(w, http.StatusOK, view)
}

func (h *M2APIHandler) GetExecutionDetail(w http.ResponseWriter, r *http.Request) {
	execID := r.PathValue("execution_id")
	rec, batches, err := migration.GetExecutionDetail(r.Context(), h.DB, execID)
	if err != nil {
		if errors.Is(err, migration.ErrExecutionNotFound) {
			WriteError(w, http.StatusNotFound, err.Error())
			return
		}
		HandleDomainError(w, err)
		return
	}
	if _, _, _, ok := h.requireDBAccess(w, r, rec.DatabaseID, access.AccessViewOnly); !ok {
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"execution":      rec,
		"batch_progress": batches,
	})
}
