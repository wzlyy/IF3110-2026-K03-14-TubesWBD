package migration

import (
	"errors"
	"testing"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestValidateDefinitionInput(t *testing.T) {
	bs := 5000

	t.Run("valid reversible normal", func(t *testing.T) {
		in := DefinitionInput{
			Name:    "create_orders",
			SQLUp:   "CREATE TABLE orders (id BIGSERIAL PRIMARY KEY, status TEXT);",
			SQLDown: "DROP TABLE orders;",
		}
		clean, report, err := ValidateDefinitionInput(in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if clean.ExecutionMode != ModeNormal || report.UpStatementCount != 1 || report.DownStatementCount != 1 {
			t.Fatalf("unexpected validation result: clean=%#v report=%#v", clean, report)
		}
	})

	t.Run("missing sql_down when reversible fails", func(t *testing.T) {
		_, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:  "missing_down",
			SQLUp: "CREATE TABLE t (id INT);",
		})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("expected ErrInvalidDefinition, got %v", err)
		}
	})

	t.Run("irreversible requires reason", func(t *testing.T) {
		_, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:           "drop_old",
			SQLUp:          "DROP TABLE old_logs;",
			IsIrreversible: true,
		})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("expected ErrInvalidDefinition without reason, got %v", err)
		}

		clean, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:               "drop_old",
			SQLUp:              "DROP TABLE old_logs;",
			IsIrreversible:     true,
			IrreversibleReason: "Menghapus data arsip permanen sesuai retensi",
		})
		if err != nil || clean.SQLDown != "" {
			t.Fatalf("expected valid irreversible definition, err=%v clean=%#v", err, clean)
		}
	})

	t.Run("batch mode requires table, cursor column, and positive batch_size", func(t *testing.T) {
		_, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:          "batch_bad",
			SQLUp:         "UPDATE orders SET status = 'archived';",
			SQLDown:       "UPDATE orders SET status = 'active';",
			ExecutionMode: ModeBatch,
		})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("expected ErrInvalidDefinition for incomplete batch config, got %v", err)
		}

		clean, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:              "batch_good",
			SQLUp:             "UPDATE orders SET status = 'archived' WHERE id > $1 AND id <= $2;",
			SQLDown:           "UPDATE orders SET status = 'active' WHERE id > $1 AND id <= $2;",
			ExecutionMode:     ModeBatch,
			BatchTable:        "public.orders",
			BatchCursorColumn: "id",
			BatchSize:         &bs,
		})
		if err != nil || clean.BatchSize == nil || *clean.BatchSize != 5000 {
			t.Fatalf("expected valid batch definition, err=%v", err)
		}
	})

	t.Run("blocks touching internal _platform_meta schema", func(t *testing.T) {
		_, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:               "tamper_meta",
			SQLUp:              "DELETE FROM _platform_meta.execution_proofs;",
			IsIrreversible:     true,
			IrreversibleReason: "malicious",
		})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("expected _platform_meta reference to be rejected, got %v", err)
		}
	})
}

func TestValidateBatchTargetCatalog(t *testing.T) {
	cat := &database.Catalog{
		DBMS: "postgres",
		Name: "target_db",
		Schemas: []database.Schema{{
			Name: "public",
			Tables: []database.Table{{
				Schema: "public",
				Name:   "orders",
				Columns: []database.Column{
					{Name: "id", DataType: "bigint"},
					{Name: "code", DataType: "text"},
					{Name: "status", DataType: "text"},
				},
				Constraints: []database.Constraint{
					{Name: "orders_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
					{Name: "orders_code_key", Type: "UNIQUE", Columns: []string{"code"}},
				},
			}},
		}},
	}

	if err := ValidateBatchTargetCatalog(cat, "orders", "id"); err != nil {
		t.Fatalf("expected PK cursor column to pass: %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "code"); err != nil {
		t.Fatalf("expected UNIQUE cursor column to pass: %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "orders", "status"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected non-unique cursor column to fail, got %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "missing_table", "id"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected missing table to fail, got %v", err)
	}
}

func TestHideRollbackAndActiveTipSpecScenario(t *testing.T) {
	// Skenario sesuai spesifikasi Milestone 2:
	//   A [E1]
	//   B [E2]
	//   B (rollback E2) [E3]
	//   B (forward) [E4]
	//   C [E5]
	// Ditambah percobaan gagal (Revisi 2 & 4):
	//   D (up gagal) [E6]
	//   C (rollback E5 gagal) [E7]
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	records := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-A", MigrationVersion: 1, MigrationName: "A", ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base},
		{ExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2, MigrationName: "B", ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base.Add(1 * time.Minute)},
		{ExecutionID: "E3", MigrationID: "mig-B", MigrationVersion: 2, MigrationName: "B", ActionType: ActionRollback, RevertedExecutionID: "E2", Status: ExecStatusSuccess, StartedAt: base.Add(2 * time.Minute)},
		{ExecutionID: "E4", MigrationID: "mig-B", MigrationVersion: 2, MigrationName: "B", ActionType: ActionForward, Status: ExecStatusSuccess, StartedAt: base.Add(3 * time.Minute)},
		{ExecutionID: "E5", MigrationID: "mig-C", MigrationVersion: 3, MigrationName: "C", ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base.Add(4 * time.Minute)},
		{ExecutionID: "E6", MigrationID: "mig-D", MigrationVersion: 4, MigrationName: "D", ActionType: ActionUp, Status: ExecStatusFailed, StartedAt: base.Add(5 * time.Minute)},
		{ExecutionID: "E7", MigrationID: "mig-C", MigrationVersion: 3, MigrationName: "C", ActionType: ActionRollback, RevertedExecutionID: "E5", Status: ExecStatusFailed, StartedAt: base.Add(6 * time.Minute)},
	}

	// 1. Hide rollback = false menampilkan seluruh 7 percobaan.
	full, hiddenFull := FilterHistory(records, false)
	if len(full) != 7 || hiddenFull != 0 {
		t.Fatalf("hide_rollback=false want 7 items (0 hidden), got %d (%d hidden)", len(full), hiddenFull)
	}

	// 2. Hide rollback = true hanya menyembunyikan E2 dan E3; E1, E4, E5, E6, dan E7 tetap tampil.
	filtered, hiddenCount := FilterHistory(records, true)
	if hiddenCount != 2 {
		t.Fatalf("expected 2 hidden records (E2 & E3), got %d", hiddenCount)
	}
	gotIDs := make([]string, len(filtered))
	for i, r := range filtered {
		gotIDs[i] = r.ExecutionID
	}
	wantIDs := []string{"E1", "E4", "E5", "E6", "E7"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("filtered IDs = %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Fatalf("filtered[%d] = %s, want %s (full=%v)", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}

	// 3. Rollback gagal E7 tidak boleh mengubah Active Tip (tetap E5 / C).
	state := ComputeActiveState(records)
	if state.ActiveTip == nil || state.ActiveTip.ExecutionID != "E5" || state.ActiveTip.MigrationID != "mig-C" {
		t.Fatalf("expected ActiveTip E5 (mig-C), got %#v", state.ActiveTip)
	}

	// 4. Percobaan gagal D (E6) tetap dapat di-forward ulang (Revisi 2 & 4).
	defs := []Definition{
		{ID: "mig-A", Version: 1, Name: "A"},
		{ID: "mig-B", Version: 2, Name: "B"},
		{ID: "mig-C", Version: 3, Name: "C"},
		{ID: "mig-D", Version: 4, Name: "D"},
	}
	if err := ValidateLinearForward(defs[3], defs, state); err != nil {
		t.Fatalf("expected failed attempt D to be eligible for forward, got %v", err)
	}
}

func TestLinearForwardEnforcesOrderAfterMultiRollback(t *testing.T) {
	// A [E1] -> B [E2] -> C [E3] -> C rollback E3 [E4] -> B rollback E2 [E5]
	// Active Tip kini A (v1). Forward C (v3) sebelum B (v2) harus gagal; forward B (v2) harus lolos.
	base := time.Date(2026, 11, 22, 13, 0, 0, 0, time.UTC)
	records := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-A", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base},
		{ExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base.Add(1 * time.Minute)},
		{ExecutionID: "E3", MigrationID: "mig-C", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base.Add(2 * time.Minute)},
		{ExecutionID: "E4", MigrationID: "mig-C", MigrationVersion: 3, ActionType: ActionRollback, RevertedExecutionID: "E3", Status: ExecStatusSuccess, StartedAt: base.Add(3 * time.Minute)},
		{ExecutionID: "E5", MigrationID: "mig-B", MigrationVersion: 2, ActionType: ActionRollback, RevertedExecutionID: "E2", Status: ExecStatusSuccess, StartedAt: base.Add(4 * time.Minute)},
	}
	defs := []Definition{
		{ID: "mig-A", Version: 1, Name: "A"},
		{ID: "mig-B", Version: 2, Name: "B"},
		{ID: "mig-C", Version: 3, Name: "C"},
	}
	state := ComputeActiveState(records)
	if state.ActiveTip == nil || state.ActiveTip.ExecutionID != "E1" {
		t.Fatalf("expected ActiveTip E1, got %#v", state.ActiveTip)
	}

	if err := ValidateLinearForward(defs[2], defs, state); !errors.Is(err, ErrLinearOrderViolation) {
		t.Fatalf("expected ErrLinearOrderViolation forwarding C before B, got %v", err)
	}
	if err := ValidateLinearForward(defs[1], defs, state); err != nil {
		t.Fatalf("expected forwarding B (next linear) to succeed, got %v", err)
	}
}

func TestPartialBatchBlockingAndResolutionStates(t *testing.T) {
	base := time.Date(2026, 11, 22, 14, 0, 0, 0, time.UTC)

	// 1. Eksekusi partial yang belum selesai menetapkan BlockingExecution.
	partialRecords := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-A", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base},
		{ExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2, ActionType: ActionUp, ExecutionMode: ModeBatch, Status: ExecStatusPartial, StartedAt: base.Add(time.Minute)},
	}
	st1 := ComputeActiveState(partialRecords)
	if st1.BlockingExecution == nil || st1.BlockingExecution.ExecutionID != "E2" {
		t.Fatalf("expected E2 to block database, got %#v", st1.BlockingExecution)
	}

	// 2. resume_batch sukses dengan ParentExecutionID=E2 melepas blokir dan memajukan Active Tip ke E3.
	resumedRecords := append(partialRecords, ExecutionRecord{
		ExecutionID: "E3", ParentExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2,
		ActionType: ActionResumeBatch, ExecutionMode: ModeBatch, Status: ExecStatusSuccess, StartedAt: base.Add(2 * time.Minute),
	})
	st2 := ComputeActiveState(resumedRecords)
	if st2.BlockingExecution != nil {
		t.Fatalf("expected blocking cleared after resume_batch success, got %#v", st2.BlockingExecution)
	}
	if st2.ActiveTip == nil || st2.ActiveTip.ExecutionID != "E3" {
		t.Fatalf("expected ActiveTip E3 after resume_batch success, got %#v", st2.ActiveTip)
	}

	// 3. Sebagai alternatif, revert_batch sukses pada E2 melepas blokir dan menjaga Active Tip di E1.
	revertedRecords := append(partialRecords, ExecutionRecord{
		ExecutionID: "E4", RevertedExecutionID: "E2", ParentExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2,
		ActionType: ActionRevertBatch, ExecutionMode: ModeBatch, Status: ExecStatusSuccess, StartedAt: base.Add(2 * time.Minute),
	})
	st3 := ComputeActiveState(revertedRecords)
	if st3.BlockingExecution != nil {
		t.Fatalf("expected blocking cleared after revert_batch success, got %#v", st3.BlockingExecution)
	}
	if st3.ActiveTip == nil || st3.ActiveTip.ExecutionID != "E1" {
		t.Fatalf("expected ActiveTip E1 after revert_batch success, got %#v", st3.ActiveTip)
	}
}

func TestFailedAttemptDoesNotBlockAnotherMigrationForward(t *testing.T) {
	// Revisi 2 & 4: attempt gagal boleh di-forward lagi ATAU dilanjutkan dengan migration lain.
	base := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	records := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-A", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base},
		{ExecutionID: "E2", MigrationID: "mig-B", MigrationVersion: 2, ActionType: ActionUp, Status: ExecStatusFailed, StartedAt: base.Add(time.Minute)},
		{ExecutionID: "E3", MigrationID: "mig-C", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusFailed, StartedAt: base.Add(2 * time.Minute)},
	}
	defs := []Definition{
		{ID: "mig-A", Version: 1, Name: "A"},
		{ID: "mig-B", Version: 2, Name: "B"},
		{ID: "mig-C", Version: 3, Name: "C"},
	}
	state := ComputeActiveState(records)
	if err := ValidateLinearForward(defs[1], defs, state); err != nil {
		t.Fatalf("failed mig-B should be forwardable: %v", err)
	}
	if err := ValidateLinearForward(defs[2], defs, state); err != nil {
		t.Fatalf("failed mig-C should not be blocked by failed mig-B: %v", err)
	}
}
func TestPartialRollbackContinuationHistory(t *testing.T) {
	base := []ExecutionRecord{
		{ExecutionID: "applied", MigrationID: "M", ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "rollback", MigrationID: "M", ActionType: ActionRollback, RevertedExecutionID: "applied", Status: ExecStatusPartial},
		{ExecutionID: "retry", MigrationID: "M", ActionType: ActionResumeBatch, ParentExecutionID: "rollback", Status: ExecStatusPartial},
	}
	for _, action := range []string{ActionResumeBatch, ActionRevertBatch} {
		t.Run(action, func(t *testing.T) {
			done := ExecutionRecord{ExecutionID: "done", MigrationID: "M", ActionType: action, ParentExecutionID: "retry", Status: ExecStatusSuccess}
			if action == ActionRevertBatch {
				done.RevertedExecutionID = "retry"
			}
			history := append(base, done)
			state := ComputeActiveState(history)
			reverted := action == ActionRevertBatch
			if state.BlockingExecution != nil || state.AppliedMigrationIDs["M"] != reverted || state.RolledBackMigrations["M"] == reverted {
				t.Fatalf("unexpected state: %+v", state)
			}
			if reverted && (state.ActiveTip == nil || state.ActiveTip.ExecutionID != "applied") || !reverted && state.ActiveTip != nil {
				t.Fatalf("unexpected active tip: %+v", state.ActiveTip)
			}
			visible, hidden := FilterHistory(history, true)
			if hidden != 2 || len(visible) != 2 || (visible[0].ExecutionID == "applied") != reverted {
				t.Fatalf("unexpected filtered history: %+v", visible)
			}
		})
	}
}
