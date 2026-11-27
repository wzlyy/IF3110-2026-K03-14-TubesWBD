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
			SQLUp:             "-- SQL up\nUPDATE orders SET status = 'archived' WHERE id BETWEEN $1 AND $2;",
			SQLDown:           "/* revert */ UPDATE orders SET status = 'active' WHERE id BETWEEN $1 AND $2;",
			ExecutionMode:     ModeBatch,
			BatchTable:        "public.orders",
			BatchCursorColumn: "id",
			BatchSize:         &bs,
		})
		if err != nil || clean.BatchSize == nil || *clean.BatchSize != 5000 {
			t.Fatalf("expected valid batch definition, err=%v", err)
		}

		// UPDATE tanpa WHERE pada mode batch juga valid dan akan ditambahkan WHERE <cursor> BETWEEN $1 AND $2 oleh worker.
		if _, _, err := ValidateDefinitionInput(DefinitionInput{
			Name:              "batch_no_where",
			SQLUp:             "UPDATE public.orders SET status = 'archived';",
			SQLDown:           "UPDATE public.orders SET status = 'active';",
			ExecutionMode:     ModeBatch,
			BatchTable:        "public.orders",
			BatchCursorColumn: "id",
			BatchSize:         &bs,
		}); err != nil {
			t.Fatalf("expected batch UPDATE without WHERE to pass, got %v", err)
		}
	})

	t.Run("batch mode allows keywords and comment tokens inside string literals", func(t *testing.T) {
		cases := []struct {
			name string
			sql  string
			want string
		}{
			{
				name: "from and where inside single quotes",
				sql:  "UPDATE orders SET note = 'migrated from v1 where id = 1' WHERE id BETWEEN $1 AND $2;",
				want: `UPDATE "orders" SET note = 'migrated from v1 where id = 1' WHERE "id" BETWEEN $1 AND $2`,
			},
			{
				name: "returning and comment tokens inside escaped quotes",
				sql:  "UPDATE orders SET note = E'it\\'s returning -- /* not a comment' WHERE id BETWEEN $1 AND $2;",
				want: `UPDATE "orders" SET note = E'it\'s returning -- /* not a comment' WHERE "id" BETWEEN $1 AND $2`,
			},
			{
				name: "doubled quotes and cursor text inside literal",
				sql:  "UPDATE orders SET note = 'a'', id = 99, from x';",
				want: `UPDATE "orders" SET note = 'a'', id = 99, from x' WHERE "id" BETWEEN $1 AND $2`,
			},
			{
				name: "dollar-quoted string literal",
				sql:  "UPDATE orders SET note = $tag$from a where b returning c -- /*$tag$ WHERE id BETWEEN $1 AND $2;",
				want: `UPDATE "orders" SET note = $tag$from a where b returning c -- /*$tag$ WHERE "id" BETWEEN $1 AND $2`,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, _, built, err := BuildBatchUpdateSQL(tc.sql, "orders", "id")
				if err != nil {
					t.Fatalf("BuildBatchUpdateSQL(%q) unexpected error: %v", tc.sql, err)
				}
				if built != tc.want {
					t.Fatalf("BuildBatchUpdateSQL(%q) = %q, want %q", tc.sql, built, tc.want)
				}
				if _, _, err := ValidateDefinitionInput(DefinitionInput{
					Name:              tc.name,
					SQLUp:             tc.sql,
					SQLDown:           "UPDATE orders SET note = 'reverted' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				}); err != nil {
					t.Fatalf("ValidateDefinitionInput(%q) unexpected error: %v", tc.sql, err)
				}
			})
		}
	})

	t.Run("batch mode rejects incompatible SQL clauses matching worker batchSpec", func(t *testing.T) {
		badCases := []struct {
			name string
			in   DefinitionInput
		}{
			{
				name: "legacy range predicates in WHERE",
				in: DefinitionInput{
					Name:              "batch_range_gt_lte",
					SQLUp:             "UPDATE public.orders SET status = 'archived' WHERE id > $1 AND id <= $2;",
					SQLDown:           "UPDATE public.orders SET status = 'active' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
			{
				name: "multiple statements",
				in: DefinitionInput{
					Name:              "batch_multi",
					SQLUp:             "UPDATE public.orders SET status = 'a' WHERE id BETWEEN $1 AND $2; UPDATE public.orders SET status = 'b' WHERE id BETWEEN $1 AND $2;",
					SQLDown:           "UPDATE public.orders SET status = 'c' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
			{
				name: "different target table",
				in: DefinitionInput{
					Name:              "batch_wrong_table",
					SQLUp:             "UPDATE public.other_orders SET status = 'archived' WHERE id BETWEEN $1 AND $2;",
					SQLDown:           "UPDATE public.orders SET status = 'active' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
			{
				name: "mutating cursor column",
				in: DefinitionInput{
					Name:              "batch_mutate_cursor",
					SQLUp:             "UPDATE public.orders SET id = id + 1 WHERE id BETWEEN $1 AND $2;",
					SQLDown:           "UPDATE public.orders SET status = 'active' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
			{
				name: "FROM or RETURNING clause",
				in: DefinitionInput{
					Name:              "batch_returning",
					SQLUp:             "UPDATE public.orders SET status = 'archived' RETURNING id;",
					SQLDown:           "UPDATE public.orders SET status = 'active' WHERE id BETWEEN $1 AND $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
			{
				name: "invalid sql_down in reversible batch",
				in: DefinitionInput{
					Name:              "batch_bad_down",
					SQLUp:             "UPDATE public.orders SET status = 'archived' WHERE id BETWEEN $1 AND $2;",
					SQLDown:           "UPDATE public.orders SET status = 'active' WHERE id > $1 AND id <= $2;",
					ExecutionMode:     ModeBatch,
					BatchTable:        "public.orders",
					BatchCursorColumn: "id",
					BatchSize:         &bs,
				},
			},
		}
		for _, tc := range badCases {
			t.Run(tc.name, func(t *testing.T) {
				_, _, err := ValidateDefinitionInput(tc.in)
				if !errors.Is(err, ErrInvalidDefinition) {
					t.Fatalf("%s: expected ErrInvalidDefinition, got %v", tc.name, err)
				}
			})
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
					{Name: "opt_code", DataType: "text", Nullable: true},
					{Name: "ext_id", DataType: "text"},
					{Name: "partial_code", DataType: "text"},
					{Name: "partial_flagged", DataType: "text"},
					{Name: "invalid_code", DataType: "text"},
					{Name: "status", DataType: "text"},
				},
				Constraints: []database.Constraint{
					{Name: "orders_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
					{Name: "orders_code_key", Type: "UNIQUE", Columns: []string{"code"}},
					{Name: "orders_opt_code_key", Type: "UNIQUE", Columns: []string{"opt_code"}},
				},
				Indexes: []database.Index{
					{
						Name:       "orders_ext_id_uidx",
						Columns:    []string{"ext_id"},
						Unique:     true,
						Definition: "CREATE UNIQUE INDEX orders_ext_id_uidx ON public.orders USING btree (ext_id)",
					},
					{
						Name:       "orders_partial_code_uidx",
						Columns:    []string{"partial_code"},
						Unique:     true,
						Definition: "CREATE UNIQUE INDEX orders_partial_code_uidx ON public.orders USING btree (partial_code) WHERE (status = 'active'::text)",
					},
					{
						Name:       "orders_partial_flagged_uidx",
						Columns:    []string{"partial_flagged"},
						Unique:     true,
						Partial:    true,
						Definition: "CREATE UNIQUE INDEX orders_partial_flagged_uidx ON public.orders USING btree (partial_flagged)",
					},
					{
						Name:       "orders_invalid_code_uidx",
						Columns:    []string{"invalid_code"},
						Unique:     true,
						Invalid:    true,
						Definition: "CREATE UNIQUE INDEX orders_invalid_code_uidx ON public.orders USING btree (invalid_code)",
					},
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
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "ext_id"); err != nil {
		t.Fatalf("expected full valid UNIQUE index cursor column to pass: %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "partial_code"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected partial UNIQUE index (WHERE predicate) to fail, got %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "partial_flagged"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected partial UNIQUE index (Partial=true) to fail, got %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "invalid_code"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected invalid UNIQUE index to fail, got %v", err)
	}
	if err := ValidateBatchTargetCatalog(cat, "public.orders", "opt_code"); !errors.Is(err, ErrBatchColumnInvalid) {
		t.Fatalf("expected nullable UNIQUE cursor column to fail, got %v", err)
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
	defM := Definition{ID: "M", Version: 1}

	t.Run("partially_reverted_rollback_completed_by_revert_batch", func(t *testing.T) {
		history := []ExecutionRecord{
			{ExecutionID: "applied", MigrationID: "M", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
			{ExecutionID: "rollback", MigrationID: "M", MigrationVersion: 1, ActionType: ActionRollback, RevertedExecutionID: "applied", Status: ExecStatusPartiallyReverted},
			{ExecutionID: "done", MigrationID: "M", MigrationVersion: 1, ActionType: ActionRevertBatch, ParentExecutionID: "rollback", RevertedExecutionID: "rollback", Status: ExecStatusSuccess},
		}
		state := ComputeActiveState(history)
		if state.BlockingExecution != nil || state.AppliedMigrationIDs["M"] || !state.RolledBackMigrations["M"] || state.ActiveTip != nil {
			t.Fatalf("unexpected state: %+v", state)
		}
		visible, hidden := FilterHistory(history, true)
		if hidden != 2 || len(visible) != 1 || visible[0].ExecutionID != "rollback" {
			t.Fatalf("unexpected filtered history: hidden=%d visible=%+v", hidden, visible)
		}
		if err := ValidateLinearForward(defM, []Definition{defM}, state); err != nil {
			t.Fatalf("ValidateLinearForward returned unexpected error: %v", err)
		}
	})

	t.Run("partial_up_completed_by_multi_step_revert_batch", func(t *testing.T) {
		history := []ExecutionRecord{
			{ExecutionID: "up", MigrationID: "M", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusPartial},
			{ExecutionID: "retry", MigrationID: "M", MigrationVersion: 1, ActionType: ActionResumeBatch, ParentExecutionID: "up", Status: ExecStatusPartial},
			{ExecutionID: "done", MigrationID: "M", MigrationVersion: 1, ActionType: ActionRevertBatch, ParentExecutionID: "retry", RevertedExecutionID: "retry", Status: ExecStatusSuccess},
		}
		state := ComputeActiveState(history)
		if state.BlockingExecution != nil || state.AppliedMigrationIDs["M"] || !state.RolledBackMigrations["M"] || state.ActiveTip != nil {
			t.Fatalf("unexpected state: %+v", state)
		}
		visible, hidden := FilterHistory(history, true)
		if hidden != 2 || len(visible) != 1 || visible[0].ExecutionID != "up" {
			t.Fatalf("unexpected filtered history: hidden=%d visible=%+v", hidden, visible)
		}
		if err := ValidateLinearForward(defM, []Definition{defM}, state); err != nil {
			t.Fatalf("ValidateLinearForward returned unexpected error: %v", err)
		}
	})
}

func TestParseBatchUpdateSQLQuotedCommentsSelectiveWhereAndRejections(t *testing.T) {
	p1, err := ParseBatchUpdateSQL(`UPDATE "public"."orders" /* lead */ SET status = 'archived' WHERE "id" BETWEEN $1 AND $2 -- end`, "orders", "id")
	if err != nil {
		t.Fatalf("quoted+comments parse failed: %v", err)
	}
	if p1.ExtraWhere != "" || p1.Statement != `UPDATE "public"."orders" SET status = 'archived' WHERE "id" BETWEEN $1 AND $2` && p1.Statement != `UPDATE "orders" SET status = 'archived' WHERE "id" BETWEEN $1 AND $2` {
		t.Fatalf("unexpected quoted plan: %+v", p1)
	}

	p2, err := ParseBatchUpdateSQL(`UPDATE orders SET status = 'archived' WHERE status = 'pending' OR retry = true -- trailing`, "orders", "id")
	if err != nil {
		t.Fatalf("selective WHERE parse failed: %v", err)
	}
	wantStmt := `UPDATE "orders" SET status = 'archived' WHERE ("id" BETWEEN $1 AND $2) AND (status = 'pending' OR retry = true)`
	if p2.ExtraWhere != `status = 'pending' OR retry = true` || p2.Statement != wantStmt {
		t.Fatalf("unexpected selective plan: %+v, want statement %q", p2, wantStmt)
	}

	// Idempotent when parsing the rewritten execution snapshot (e.g. during resume_batch).
	p2Again, err := ParseBatchUpdateSQL(p2.Statement, "orders", "id")
	if err != nil {
		t.Fatalf("re-parse rewritten snapshot failed: %v", err)
	}
	if p2Again.ExtraWhere != p2.ExtraWhere || p2Again.Statement != p2.Statement {
		t.Fatalf("non-idempotent re-parse: %+v vs %+v", p2Again, p2)
	}

	p3, err := ParseBatchUpdateSQL(`UPDATE orders SET status = 'archived' WHERE orders.tag = 'target'`, "orders", "id")
	if err != nil || p3.ExtraWhere != `orders.tag = 'target'` {
		t.Fatalf("table-qualified selective WHERE failed: err=%v plan=%+v", err, p3)
	}

	for _, badSQL := range []string{
		`UPDATE orders SET note = $1`,
		`UPDATE orders SET status = 'x' WHERE tenant_id = $3`,
		`UPDATE orders SET status = 'x' WHERE active = true RETURNING id`,
		`UPDATE orders SET status = 'x' WHERE id BETWEEN $1 AND $2 AND a = 1 OR b = 2`,
		`UPDATE orders SET status = 'x' WHERE ("id" BETWEEN $1 AND $2) AND a = 1 OR b = 2`,
	} {
		if _, err := ParseBatchUpdateSQL(badSQL, "orders", "id"); err == nil {
			t.Fatalf("expected error for invalid batch SQL %q", badSQL)
		}
	}
}

func TestValidateLinearNextBlocksDraftSkippingAndOutOfOrder(t *testing.T) {
	defs := []Definition{
		{ID: "mig-1", Version: 1, Name: "v1"},
		{ID: "mig-2", Version: 2, Name: "v2"},
		{ID: "mig-3", Version: 3, Name: "v3"},
	}
	stV1 := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
	})
	if err := ValidateLinearNext(defs[2], defs, stV1); !errors.Is(err, ErrLinearOrderViolation) {
		t.Fatalf("skipping unexecuted draft v2 should fail with ErrLinearOrderViolation, got %v", err)
	}
	if err := ValidateLinearNext(defs[1], defs, stV1); err != nil {
		t.Fatalf("next draft v2 should succeed, got %v", err)
	}

	// Failed v2 must not block draft v3, but once v3 succeeds, forwarding v2 out-of-order is rejected.
	stV1FailV2 := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, Status: ExecStatusFailed, StartedAt: time.Now()},
	})
	if err := ValidateLinearNext(defs[2], defs, stV1FailV2); err != nil {
		t.Fatalf("failed v2 should not block v3 draft, got %v", err)
	}
	stV3Active := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, Status: ExecStatusFailed},
		{ExecutionID: "E3", MigrationID: "mig-3", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusSuccess},
	})
	if err := ValidateLinearForward(defs[1], defs, stV3Active); !errors.Is(err, ErrLinearOrderViolation) {
		t.Fatalf("forwarding v2 below active tip v3 should fail with ErrLinearOrderViolation, got %v", err)
	}
}

func TestFailedForwardAfterRollbackDoesNotBlockNextMigration(t *testing.T) {
	defs := []Definition{
		{ID: "mig-1", Version: 1, Name: "v1"},
		{ID: "mig-2", Version: 2, Name: "v2"},
	}
	base := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)

	// v1 rolled back after a prior failure should still block v2 until a new forward attempt fails.
	stRolledBack := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E0", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusFailed, StartedAt: base},
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base.Add(time.Minute)},
		{ExecutionID: "E2", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionRollback, RevertedExecutionID: "E1", Status: ExecStatusSuccess, StartedAt: base.Add(2 * time.Minute)},
	})
	if err := ValidateLinearNext(defs[1], defs, stRolledBack); !errors.Is(err, ErrLinearOrderViolation) {
		t.Fatalf("rolled-back v1 without failed forward should block v2, got %v", err)
	}

	// Once forward on v1 fails, v1 can be retried OR v2 can proceed.
	stFailedForward := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess, StartedAt: base},
		{ExecutionID: "E2", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionRollback, RevertedExecutionID: "E1", Status: ExecStatusSuccess, StartedAt: base.Add(time.Minute)},
		{ExecutionID: "E3", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionForward, Status: ExecStatusFailed, StartedAt: base.Add(2 * time.Minute)},
	})
	if err := ValidateLinearForward(defs[0], defs, stFailedForward); err != nil {
		t.Fatalf("expected failed forward v1 to remain forwardable, got %v", err)
	}
	if err := ValidateLinearNext(defs[1], defs, stFailedForward); err != nil {
		t.Fatalf("expected failed forward v1 not to block v2, got %v", err)
	}
}

func TestPartiallyRevertedAllowsRepairMigrationUpAndResolvesBlocker(t *testing.T) {
	defs := []Definition{
		{ID: "mig-1", Version: 1, Name: "v1"},
		{ID: "mig-2", Version: 2, Name: "v2"},
		{ID: "mig-3", Version: 3, Name: "v3-repair"},
	}
	records := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, ExecutionMode: ModeBatch, Status: ExecStatusPartial},
		{ExecutionID: "E3", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionRevertBatch, ParentExecutionID: "E2", ExecutionMode: ModeBatch, Status: ExecStatusPartiallyReverted},
	}
	stBlocked := ComputeActiveState(records)
	if stBlocked.BlockingExecution == nil || stBlocked.BlockingExecution.ExecutionID != "E3" {
		t.Fatalf("expected E3 to be blocking execution, got %#v", stBlocked.BlockingExecution)
	}
	if err := ValidateLinearNext(defs[2], defs, stBlocked); err != nil {
		t.Fatalf("expected repair migration v3 to pass ValidateLinearNext after partially_reverted v2, got %v", err)
	}

	stRepaired := ComputeActiveState(append(records, ExecutionRecord{
		ExecutionID: "E4", MigrationID: "mig-3", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusSuccess,
	}))
	if stRepaired.BlockingExecution != nil {
		t.Fatalf("expected successful repair migration to clear BlockingExecution, got %#v", stRepaired.BlockingExecution)
	}
	if stRepaired.ActiveTip == nil || stRepaired.ActiveTip.ExecutionID != "E4" {
		t.Fatalf("expected ActiveTip E4, got %#v", stRepaired.ActiveTip)
	}
}

func TestParseWorkerBatchUpdateSQLMatchesHTTPParser(t *testing.T) {
	q := `UPDATE "public"."orders" SET status = 'archived' WHERE status = 'pending'`
	httpPlan, err := ParseBatchUpdateSQL(q, "orders", "id")
	if err != nil {
		t.Fatalf("ParseBatchUpdateSQL failed: %v", err)
	}
	workerPlan, err := ParseWorkerBatchUpdateSQL(q, "orders", "id")
	if err != nil {
		t.Fatalf("ParseWorkerBatchUpdateSQL failed: %v", err)
	}
	if httpPlan != workerPlan {
		t.Fatalf("mismatch between HTTP and worker batch plans: %#v vs %#v", httpPlan, workerPlan)
	}
}

func TestPartialIrreversibleAndFailedRevertAllowRepairMigration(t *testing.T) {
	defs := []Definition{
		{ID: "mig-1", Version: 1, Name: "v1"},
		{ID: "mig-2", Version: 2, Name: "v2"},
		{ID: "mig-3", Version: 3, Name: "v3-repair"},
	}

	// Reversible partial without failed revert must keep blocking repair migration v3.
	revPartial := ComputeActiveState([]ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, ExecutionMode: ModeBatch, IsIrreversible: false, Status: ExecStatusPartial},
	})
	if revPartial.BlockingAllowsRepair {
		t.Fatalf("expected reversible partial without failed revert to disallow repair")
	}
	if err := ValidateLinearNext(defs[2], defs, revPartial); !errors.Is(err, ErrLinearOrderViolation) {
		t.Fatalf("expected ErrLinearOrderViolation for v3 over reversible partial v2, got %v", err)
	}

	// Irreversible partial allows repair migration v3 and clears blocker once v3 succeeds.
	irrevRecords := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, ExecutionMode: ModeBatch, IsIrreversible: true, Status: ExecStatusPartial},
	}
	irrevState := ComputeActiveState(irrevRecords)
	if !irrevState.BlockingAllowsRepair {
		t.Fatalf("expected irreversible partial to allow repair")
	}
	if err := ValidateLinearNext(defs[2], defs, irrevState); err != nil {
		t.Fatalf("expected repair v3 to pass ValidateLinearNext after irreversible partial, got %v", err)
	}
	irrevRepaired := ComputeActiveState(append(irrevRecords, ExecutionRecord{
		ExecutionID: "E3", MigrationID: "mig-3", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusSuccess,
	}))
	if irrevRepaired.BlockingExecution != nil || irrevRepaired.ActiveTip == nil || irrevRepaired.ActiveTip.ExecutionID != "E3" {
		t.Fatalf("expected successful repair v3 to clear blocker and become ActiveTip E3, got blocker=%#v tip=%#v", irrevRepaired.BlockingExecution, irrevRepaired.ActiveTip)
	}

	// Reversible partial with failed revert_batch allows repair migration v3 and clears blocker once v3 succeeds.
	failedRevertRecords := []ExecutionRecord{
		{ExecutionID: "E1", MigrationID: "mig-1", MigrationVersion: 1, ActionType: ActionUp, Status: ExecStatusSuccess},
		{ExecutionID: "E2", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionUp, ExecutionMode: ModeBatch, IsIrreversible: false, Status: ExecStatusPartial},
		{ExecutionID: "E3", MigrationID: "mig-2", MigrationVersion: 2, ActionType: ActionRevertBatch, RevertedExecutionID: "E2", ParentExecutionID: "E2", ExecutionMode: ModeBatch, Status: ExecStatusFailed},
	}
	failedRevState := ComputeActiveState(failedRevertRecords)
	if !failedRevState.BlockingAllowsRepair {
		t.Fatalf("expected partial with failed revert_batch to allow repair")
	}
	if err := ValidateLinearNext(defs[2], defs, failedRevState); err != nil {
		t.Fatalf("expected repair v3 to pass ValidateLinearNext after failed revert_batch, got %v", err)
	}
	failedRevRepaired := ComputeActiveState(append(failedRevertRecords, ExecutionRecord{
		ExecutionID: "E4", MigrationID: "mig-3", MigrationVersion: 3, ActionType: ActionUp, Status: ExecStatusSuccess,
	}))
	if failedRevRepaired.BlockingExecution != nil || failedRevRepaired.ActiveTip == nil || failedRevRepaired.ActiveTip.ExecutionID != "E4" {
		t.Fatalf("expected successful repair v3 to clear blocker and become ActiveTip E4, got blocker=%#v tip=%#v", failedRevRepaired.BlockingExecution, failedRevRepaired.ActiveTip)
	}
}
