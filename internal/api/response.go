package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
)

const DefaultMaxJSONBytes = 128 * 1024

// WriteJSON menulis payload JSON dengan header cache no-store.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

// WriteError menulis pesan error JSON yang sudah disanitasi.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// DecodeJSON membaca body request JSON dengan batas ukuran ke dst.
func DecodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxJSONBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body JSON tidak boleh kosong")
		}
		return errors.New("format JSON request tidak valid")
	}
	return nil
}

// DecodeOptionalJSON membaca body JSON opsional; body kosong diperbolehkan, sedangkan JSON rusak/kebesaran ditolak.
func DecodeOptionalJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxJSONBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return errors.New("format JSON request tidak valid")
	}
	return nil
}

// HandleDomainError memetakan error domain ke kode status HTTP REST yang konsisten.
func HandleDomainError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, migration.ErrDefinitionNotFound),
		errors.Is(err, migration.ErrRequestNotFound),
		errors.Is(err, migration.ErrExecutionNotFound),
		errors.Is(err, migration.ErrJobNotFound),
		errors.Is(err, database.ErrConnectionNotFound),
		errors.Is(err, database.ErrTableNotFound):
		WriteError(w, http.StatusNotFound, err.Error())

	case errors.Is(err, migration.ErrNotGroupLeader):
		WriteError(w, http.StatusForbidden, err.Error())

	case errors.Is(err, migration.ErrImmutableDefinition),
		errors.Is(err, migration.ErrDefinitionHasActiveReq),
		errors.Is(err, migration.ErrDatabaseBlocked),
		errors.Is(err, migration.ErrRollbackNotActiveTip),
		errors.Is(err, migration.ErrIrreversibleMigration),
		errors.Is(err, migration.ErrMigrationAlreadyActive),
		errors.Is(err, migration.ErrForwardNotEligible),
		errors.Is(err, migration.ErrLinearOrderViolation),
		errors.Is(err, migration.ErrBatchActionInvalid),
		errors.Is(err, migration.ErrInvalidStateChange),
		errors.Is(err, migration.ErrApprovalStale),
		errors.Is(err, lock.ErrLockBusy),
		errors.Is(err, lock.ErrLockHeldPartial),
		errors.Is(err, lock.ErrLockHeldUncertain):
		WriteError(w, http.StatusConflict, err.Error())

	case errors.Is(err, migration.ErrInvalidDefinition),
		errors.Is(err, migration.ErrBatchColumnInvalid),
		errors.Is(err, migration.ErrInvalidActionType),
		errors.Is(err, migration.ErrInvalidDecision),
		errors.Is(err, migration.ErrInvalidSQL),
		errors.Is(err, migration.ErrNoDatabaseGroups),
		errors.Is(err, migration.ErrMissingGroupLeader),
		errors.Is(err, migration.ErrUnsupportedTargetDBMS),
		errors.Is(err, database.ErrInvalidConnection),
		errors.Is(err, database.ErrInvalidPage):
		WriteError(w, http.StatusUnprocessableEntity, err.Error())

	case errors.Is(err, queue.ErrQueueUnavailable),
		errors.Is(err, migration.ErrPublisherNotConfigured),
		errors.Is(err, database.ErrTargetUnavailable):
		WriteError(w, http.StatusServiceUnavailable, err.Error())

	default:
		WriteError(w, http.StatusInternalServerError, "Terjadi kesalahan internal saat memproses permintaan.")
	}
}
