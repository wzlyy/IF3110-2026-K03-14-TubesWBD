# Kontrak REST JSON API & Integrasi Worker — Milestone 2
## Handoff Bagian 3 (Anggota 1 → Anggota 2 untuk Worker & SPA Tahap 2)

---

## 1. REST JSON API Milestone 2

### 1.1 Migration Definitions
- `GET /api/v1/databases/{database_id}/migrations` *(min `VIEW_ONLY`)*
  - Response `200 OK`:
    ```json
    {
      "database_id": "uuid",
      "access_level": "VIEW_EDIT",
      "definitions": [ { "id": "...", "version": 1, "revision": 1, "name": "...", "sql_up": "...", "sql_down": "...", "is_irreversible": false, "execution_mode": "normal", "is_immutable": false } ],
      "active_tip": { "execution_id": "...", "migration_id": "...", "migration_version": 1 },
      "blocking_execution": null,
      "applied_migration_ids": { "mig-id": true },
      "rolled_back_migrations": {},
      "failed_migrations": {},
      "lock": null
    }
    ```
- `POST /api/v1/databases/{database_id}/migrations` *(`VIEW_EDIT` + CSRF)*
  - Request Body:
    ```json
    {
      "name": "backfill_order_status",
      "description": "Update status pesanan dalam mode batch",
      "sql_up": "UPDATE orders SET status = 'archived' WHERE id > $1 AND id <= $2;",
      "sql_down": "UPDATE orders SET status = 'active' WHERE id > $1 AND id <= $2;",
      "is_irreversible": false,
      "irreversible_reason": "",
      "execution_mode": "batch",
      "batch_table": "public.orders",
      "batch_cursor_column": "id",
      "batch_size": 5000
    }
    ```
  - Validasi otomatis: `sql_up` wajib valid; jika `is_irreversible=false` wajib `sql_down`; jika `is_irreversible=true` wajib `irreversible_reason`; jika `execution_mode="batch"`, tabel & kolom cursor divalidasi harus ada & unik/PK pada target PostgreSQL; SQL tidak boleh menyentuh `_platform_meta` atau `_dbplatform_migration_receipts`.
- `GET /api/v1/databases/{database_id}/migrations/{migration_id}` *(min `VIEW_ONLY`)*
- `PUT /api/v1/databases/{database_id}/migrations/{migration_id}` *(`VIEW_EDIT` + CSRF)*
  - Ditolak dengan `409 Conflict` jika `is_immutable = true` (pernah dieksekusi).
- `POST /api/v1/databases/{database_id}/migrations/validate` *(`VIEW_EDIT` + CSRF)*
  - Dry-run validasi SQL & katalog batch tanpa menyimpan draft.

### 1.2 Change Requests & Multi-Group Leader Approval
- `GET /api/v1/requests` atau `GET /api/v1/databases/{database_id}/requests` *(anggota/leader grup dengan akses DB)*
- `GET /api/v1/requests/{request_id}`
- `POST /api/v1/databases/{database_id}/requests` *(`VIEW_EDIT` + CSRF)*
  - Body:
    ```json
    {
      "migration_id": "uuid",
      "action_type": "up | rollback | forward | resume_batch | revert_batch",
      "target_execution_id": "uuid (wajib untuk resume_batch/revert_batch; opsional untuk rollback)",
      "batch_size_override": 5000
    }
    ```
  - Aturan validasi:
    - DB dengan status `partial`/`partially_reverted`/`uncertain` menolak request selain `resume_batch`/`revert_batch` untuk eksekusi tersebut (`409`).
    - `rollback` hanya boleh pada **Active Tip** terakhir (`409` jika bukan tip) dan non-irreversible (`409` jika irreversible).
    - `forward` mengikuti urutan linear setelah Active Tip; *attempt* `failed` boleh di-forward lagi atau dilanjutkan dengan migration lain.
- `POST /api/v1/requests/{request_id}/submit` *(`VIEW_EDIT` + CSRF)*
  - Mengubah `draft → pending_review` dan otomatis membuat baris `migration_approvals` untuk **semua** grup (`VIEW_ONLY` & `VIEW_EDIT`) yang memiliki grant pada `database_id`.
- `POST /api/v1/requests/{request_id}/approve` *(Khusus `leader_user_id` grup terkait + CSRF)*
  - Body opsional: `{"comment": "Kompatibel dengan service analytics"}`.
  - Non-leader (termasuk admin) ditolak dengan `403 Forbidden`. Saat semua grup `approved`, status berubah ke `approved`.
- `POST /api/v1/requests/{request_id}/reject` *(Khusus `leader_user_id` grup terkait + CSRF)*
  - Body: `{"comment": "Kolom X masih dipakai service Y"}` → status request menjadi `rejected`.
- `POST /api/v1/requests/{request_id}/enqueue` *(`VIEW_EDIT` + CSRF)*
  - Memverifikasi status `approved` + `VerifyCurrentApprovals`, mengirim pesan persistent ke RabbitMQ beserta OTel `traceparent`, dan **langsung merespons `202 Accepted`**:
    ```json
    {
      "job_id": "uuid",
      "execution_id": "uuid",
      "request_id": "uuid",
      "status": "pending_queue"
    }
    ```

### 1.3 Migration History (`Hide rollback`) & Job/Execution Monitor
- `GET /api/v1/databases/{database_id}/history?hide_rollback=true|false` *(min `VIEW_ONLY`)*
  - Menampilkan **semua attempt** (`success`, `failed`, `partial`, `partially_reverted`, `uncertain`) beserta salinan SQL aktual (`executed_sql_snapshot`), `migration_id`, `execution_id`, `migration_version`, `migration_revision`, `executed_by_username`, `started_at`, `reverted_execution_id`.
  - Jika `hide_rollback=true`, pasangan `(reverted_execution_id, rollback_success_execution_id)` disembunyikan; attempt `failed` dan `forward` baru tetap tampil.
- `GET /api/v1/jobs/{job_id}` *(min `VIEW_ONLY`)*
  - Mengembalikan `request_status`, objek `execution` (termasuk `total_batches_committed`, `total_rows_affected`, `status`), `batch_progress[]`, `can_resume_batch`, `can_revert_batch`.
- `GET /api/v1/executions/{execution_id}` *(min `VIEW_ONLY`)*
  - Mengembalikan detail eksekusi & array `batch_progress`.

---

## 2. Kontrak Integrasi Go Worker (`cmd/worker` & `internal/worker` — untuk Anggota 2)

### 2.1 Topology & Payload JSON RabbitMQ (`internal/queue`)
- Exchange: `migration.exchange` (`direct`, durable)
- Main Queue: `migration.jobs` (routing key `migration.job`, durable)
- Retry Queue: `migration.jobs.retry` (routing key `migration.job.retry`, DLX kembali ke `migration.jobs` setelah per-message TTL)
- Dead Letter Queue: `migration.jobs.dlq` (routing key `migration.job.dlq`)
- Payload JSON (`queue.JobMessage`):
  ```json
  {
    "job_id": "uuid",
    "execution_id": "uuid",
    "request_id": "uuid",
    "database_id": "uuid",
    "migration_id": "uuid",
    "action_type": "up | rollback | forward | resume_batch | revert_batch",
    "target_execution_id": "uuid-optional",
    "execution_mode": "normal | batch",
    "batch_size_override": 5000,
    "requested_by": "user-uuid",
    "attempt_number": 1,
    "retry_count": 0,
    "enqueued_at": "2026-11-22T14:00:00Z"
  }
  ```
- **Propagasi Trace OpenTelemetry**:
  - Ekstrak trace context di worker: `ctx := queue.ExtractTraceContext(baseCtx, delivery.Headers)`
  - Re-publish retry dengan backoff terbatas: `rmq.PublishRetry(ctx, msg, queue.RetryBackoff(msg.RetryCount))` (maks `queue.MaxTransientRetries = 3`).

### 2.2 Paket Go yang Siap Dipakai Worker (Sudah Tersedia dari Anggota 1)
1. **Distributed Lock (`internal/lock`)**:
   - `lockMgr.Acquire(ctx, dbID, workerID, execID, lock.DefaultLeaseTTL)` *(aman dipanggil SEBELUM insert ke `migration_executions` karena `database_locks.execution_id` adalah logical UUID tanpa FK)*
   - `lockMgr.AcquireForRecoveryOrResume(ctx, dbID, workerID, newExecID, targetExecID, lock.DefaultLeaseTTL)`
   - `stopHB := lockMgr.StartHeartbeat(ctx, lease, lock.DefaultLeaseTTL, lock.DefaultHeartbeatPeriod)`
   - `lockMgr.Hold(ctx, dbID, lease.LockToken, lock.StateHeldPartial)` / `lock.StateHeldUncertain`
   - `lockMgr.RecoverExpiredLock(ctx, dbID, workerID, lock.DefaultLeaseTTL)` *(jika `rec.NoExecutionStarted == true`, worker lama crash sebelum eksekusi dimulai dan lock sudah otomatis dibersihkan)*
   - `lockMgr.Release(ctx, dbID, lease.LockToken)`
2. **Revalidation & Immutability (`internal/migration`)**:
   - `migration.VerifyCurrentApprovals(ctx, db, msg.RequestID, msg.DatabaseID)`
   - `migration.MarkDefinitionImmutable(ctx, db, msg.MigrationID)`
   - `migration.ComputeActiveState(allExecs)` & `migration.ValidateLinearForward(...)`
3. **Telemetry, Instrumented SQL, & Prometheus Metrics (`internal/telemetry`)**:
   - `telemetry.InitLogger(os.Stdout)` & `telemetry.InitTracer(ctx, "db-platform-worker", cfg.OTelOTLPEndpoint, os.Stdout)`
   - `idb := telemetry.WrapDB(targetDB, conn.Name)` → seluruh SQL batch/normal otomatis menghasilkan span OTel dan 3 log JSON (`sql.start`, `sql.result`, `sql.tx_end`).
   - `metrics := telemetry.GlobalMetrics()`; register pool `metrics.RegisterDBPool("platform", db.Stats)`, `metrics.RegisterDBPool("target_postgres", targetMgr.AggregatePoolStats)`, dan `metrics.RegisterJobInflightDB(db)`; jalankan `telemetry.StartMetricsServer(ctx, ":"+cfg.MetricsPort, metrics)` pada `:9091/metrics`.
   - Panggil `metrics.RecordJobStarted(...)`, `metrics.RecordBatchProcessed(...)`, `metrics.RecordJobRetry(...)`, `metrics.RecordJobFinished(...)`.
