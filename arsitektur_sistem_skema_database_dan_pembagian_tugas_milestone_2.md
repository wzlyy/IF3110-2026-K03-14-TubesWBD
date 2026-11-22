# Rancangan Arsitektur Sistem, Skema Database, & Pembagian Tugas Milestone 2
## IF3110 Pengembangan Aplikasi Web 2026 — Kelompok K03-14

Dokumen ini adalah *blueprint* teknis lengkap untuk mengembangkan aplikasi Milestone 1 pada repositori ini menjadi platform Milestone 2 yang memenuhi seluruh **Kebutuhan Wajib** (berdasarkan Spesifikasi Tugas Besar Milestone 2 Versi 4 — 16 November 2026) serta **4 Fitur Bonus** (Pengumpulan Super Cepat, Observability, Stress Testing k6, dan Security SAST CodeQL).

Seluruh perancangan di dokumen ini disesuaikan langsung dengan struktur kode Milestone 1 yang sudah ada (`cmd/server`, `internal/auth`, `internal/access`, `internal/admin`, `internal/database`, `internal/migration`, `migrations/platform/001..004`, `compose.yaml`, `compose.test.yaml`) serta menerapkan aturan pembagian kerja **1 Bagian = 1 Anggota Tunggal, Berurutan (Tidak Dikerjakan Bersamaan)**.

---

## Ringkasan Tim, Aturan Kerja, & Target Waktu

### Daftar Anggota Aktif
| Peran | Nama & NIM | Basis Kepemilikan dari Milestone 1 |
|---|---|---|
| **Anggota 1** | Dika Pramudya Nugraha (`13524132`) | Fondasi arsitektur, `internal/auth`, `internal/access`, `internal/admin`, `internal/migration/diff*` & `generator*`, integration testing |
| **Anggota 2** | Wildan Abdurrahman Ghazali (`13524054`) | `internal/database` (`connections`, `target_manager`, `catalog`, `data_viewer`, `explain`), `internal/migration` (`runner`, `history`, `sql`, `editor_handlers`) |

### Aturan Pembagian Tugas (Wajib Dipatuhi)
1. **Satu Bagian = Satu Anggota Saja**: Tidak ada satu pun bagian, sub-tugas, maupun item checklist verifikasi yang dikerjakan berdua (`A1 & A2` dilarang).
2. **Tidak Dikerjakan Bersamaan (Sekuensial Ketat)**: Pekerjaan dibagi menjadi **10 Bagian berurutan (`Bagian 1` s.d. `Bagian 10`)** dengan pola bergantian (`Anggota 1 → Anggota 2 → Anggota 1 → Anggota 2 → ...`). Hanya **1 Bagian** yang aktif pada satu waktu. Anggota berikutnya baru memulai bagiannya setelah bagian sebelumnya selesai diuji dan di-merge ke `main`.
3. **Lingkup File Eksklusif**: Setiap bagian mencantumkan daftar direktori/berkas spesifik yang menjadi tanggung jawab tunggal PIC bagian tersebut.

### Target Waktu
- **Bonus Pengumpulan Super Cepat**: selambat-lambatnya **23 November 2026, 13:10 WIB** (4 hari sebelum deadline awal).
- **Deadline Pengumpulan Milestone 2**: **27 November 2026, 13:10 WIB**.

---

## Bagian 0: Matriks Kepatuhan Spesifikasi Milestone 2 (Versi 4)

| Ketentuan Spesifikasi | Keputusan Implementasi di Repositori Ini |
|---|---|
| **Kompatibilitas Milestone 1** | Auth (`bcrypt`, opaque 32-byte session token, idle 30m, absolute 8h, rotasi 15m + grace 30s, CSRF), Access Control real-time (`VIEW_ONLY` & `VIEW_EDIT`, tanpa bypass admin), Manajemen Koneksi AES-256-GCM, Schema/Data Viewer (50 baris/halaman), Explain, Schema Diff, dan Linear Migration tetap berfungsi melalui REST JSON API + SPA. |
| **Backend & Worker Wajib Go** | Backend API (`cmd/server`) dan Worker (`cmd/worker`) 100% bahasa Go (`go.mod` modul `github.com/wzlyy/IF3110-2026-K03-14-TubesWBD`). |
| **Frontend Wajib SPA & Responsif** | Seluruh `web/templates/*` (Go HTML templates, termasuk Login & Admin) digantikan oleh SPA di `frontend/` (React + TypeScript + Vite + TailwindCSS), responsif Desktop (`>=1280px`) & Mobile (`375px`) dengan internal scroll untuk tabel lebar dan editor SQL. |
| **Target DBMS PostgreSQL** | Target DB Milestone 2 dibatasi pada PostgreSQL (`dbms_type = 'postgres'`). Sediakan `target-postgres-1` dan `target-postgres-2` di `compose.yaml` untuk menguji eksekusi paralel lintas database. |
| **Automated Testing** | Unit test (dengan mock/stub) dan Integration test (memanggil handler/service dengan database uji nyata di `compose.test.yaml` tanpa wajib menyalakan HTTP server) mencakup alur **sukses**, **gagal**, dan **penolakan akses**. |
| **History Semua Attempt (Revisi 2 & 4)** | **Semua** attempt (`up`, `rollback`, `forward`, `resume_batch`, `revert_batch`, retry, kegagalan SQL maupun kegagalan revalidation) dicatat *append-only* di `migration_executions` beserta salinan SQL aktual (`executed_sql_snapshot`) dan dikeluarkan juga di `log/slog`. |

---

## Bagian 1: Arsitektur Sistem

### 1.1 Diagram Arsitektur High-Level

```text
+-----------------------------------------------------------------------------------+
|                                  CLIENT (Browser)                                 |
|   Frontend SPA (`frontend/`: React + TypeScript + Vite + TailwindCSS)             |
|   Responsif Desktop (>=1280px) & Mobile (375px), Polling Status Job               |
+-----------------------------------------+-----------------------------------------+
                                          | Same-Origin HTTPS/HTTP REST JSON API
                                          | Cookie `session_id` (HttpOnly, SameSite=Lax)
                                          | Header `X-CSRF-Token`
                                          v
+-----------------------------------------------------------------------------------+
|                        GO BACKEND API SERVICE (`cmd/server`)                      |
|  - Serve SPA Static Bundle (`frontend/dist`) + Fallback `index.html`              |
|  - `/api/v1/auth/*` (Login, Logout, Me/CSRF) & Session/RBAC Middleware            |
|  - `/api/v1/admin/*` (Users, Groups + Leader, Grants, Connections AES-256-GCM)    |
|  - `/api/v1/databases/*` (Schema/Data Explorer, Explain, Schema Diff -> PG)       |
|  - `/api/v1/databases/*/migrations` (Draft Definition, Immutability Guard)        |
|  - `/api/v1/requests/*` (Submit, Multi-Group Leader Approval, Enqueue -> job_id)  |
|  - `/api/v1/jobs/*` & `/api/v1/executions/*` + History (`?hide_rollback=`)        |
|  - Instrumented SQL Wrapper (Read-Only) + `log/slog` JSON + OTel + `/metrics`     |
+------------------+----------------------------------+-----------------------------+
                   |                                  |
       Publish Job | (Durable + OTel `traceparent`)   | Read / Write Metadata
                   v                                  v
+--------------------------------------+   +----------------------------------------+
|       MESSAGE QUEUE (RabbitMQ)       |   |       PLATFORM DB (PostgreSQL)         |
|  - Exchange: `migration.exchange`    |   |  - M1: `users`, `sessions`,            |
|  - Durable Queue: `migration.jobs`   |   |    `session_grace_tokens`, `groups`    |
|  - Retry Queue (TTL/Backoff)         |   |    (+`leader_user_id`), `user_groups`, |
|  - Dead Letter Queue (`*.dlq`)       |   |    `database_connections`,             |
|  - Persistent Delivery Mode (2)      |   |    `group_database_grants`             |
+------------------+-------------------+   |  - M2: `migration_definitions`,        |
                   | Consume Concurrent    |    `migration_requests`,               |
                   | (Parallel across DBs) |    `migration_approvals`,              |
                   v                       |    `migration_executions`,             |
+--------------------------------------+   |    `database_locks`,                   |
|     GO WORKER SERVICE (`cmd/worker`) |   |    `execution_batch_progress`          |
|  - OTel Trace Context Extractor      |   +-------------------+--------------------+
|  - Distributed Lock Lease/Heartbeat  |                       |
|  - Revalidation Guard (Approval/Tip) | <---------------------+
|  - Normal & Batch Streaming Executor |   Acquire/Renew Lock, Revalidate,
|  - Crash/Uncertain Commit Verifier   |   Record Attempt History & Batch Mirror
|  - `log/slog` JSON + `/metrics`      |
+------------------+-------------------+
                   | Dynamic Connection Pool (`TargetManager`) per Target DB
                   v
+-----------------------------------------------------------------------------------+
|                    TARGET DATABASES (PostgreSQL 1..M)                             |
|  - Schema & Data Aplikasi Target                                                  |
|  - Schema Internal `_platform_meta`:                                              |
|      * `execution_proofs`  (bukti eksekusi normal & cegah double commit)          |
|      * `batch_checkpoints` (committed atomik dalam TX yang sama dengan batch)     |
|      * `batch_undo_logs`   (cakupan row & snapshot before_data untuk revert)      |
+-----------------------------------------------------------------------------------+

================ OBSERVABILITY STACK (WAJIB OTel + BONUS TERPUSAT) ==================
Go API & Go Worker --(stdout JSON `log/slog` + OTLP)--> OpenTelemetry Collector
                                                            |-- Traces --> Tempo
                                                            |-- Logs   --> Loki
Go API (:8080/metrics) & Go Worker (:9091/metrics) -------> Prometheus
Tempo + Loki + Prometheus --------------------------------> Grafana (Provisioned)
```

### 1.2 Pemetaan Alur Minimum Spesifikasi ke Komponen

| Langkah Alur Minimum | Implementasi Teknis |
|---|---|
| `Login` | `POST /api/v1/auth/login` memverifikasi `bcrypt`, membuat session opaque 32-byte + `csrf_token`, mengembalikan cookie `HttpOnly` + JSON profil user. |
| `Access Control` | Middleware `RequireAuthAPI` (`401` JSON) + `RequireAdminAPI` / `access.GetEffectiveAccess` (`403` JSON) mengevaluasi grant grup real-time tanpa bypass admin. |
| `Select Database` | `GET /api/v1/databases` hanya mengembalikan koneksi PostgreSQL yang memiliki grant minimal `VIEW_ONLY` untuk grup user. |
| `View Schema/Data` | `GET /api/v1/databases/{id}/schema` & `/data` menjalankan query *read-only* melalui **Instrumented SQL Wrapper** (mencatat log JSON start/result/commit + OTel span). |
| `Create Migration` | `POST /api/v1/databases/{id}/migrations` (khusus `VIEW_EDIT`) membuat definisi `sql_up` + `sql_down` (atau `is_irreversible` + `irreversible_reason`), mode `normal` atau `batch`. |
| `Validation & Review` | Validasi statis SQL (`ValidateSQL`, menolak kontrol transaksi & akses ke `_platform_meta`), validasi kolom/tabel batch via katalog target, preview/EXPLAIN/Schema Diff sebelum submit ke `pending_review`. |
| `Approval` | Saat submit, sistem membuat record `migration_approvals` untuk **semua** grup yang memiliki grant (`VIEW_ONLY` dan `VIEW_EDIT`) ke database target. Hanya `leader_user_id` masing-masing grup yang boleh memanggil `POST /api/v1/requests/{id}/approve` atau `reject`. |
| `Queue` | Setelah semua grup `approved`, `POST /api/v1/requests/{id}/enqueue` menghasilkan `job_id`, mengubah status request ke `pending_queue`, menyisipkan OTel `traceparent` ke AMQP headers, publish pesan persistent ke RabbitMQ, dan langsung mengembalikan `202 Accepted` (`{job_id}`). |
| `Worker` | `cmd/worker` mengonsumsi pesan dari `migration.jobs` secara konkuren (worker pool), mengekstrak OTel trace context sebagai parent span worker. |
| `Lock` | Worker mengakuisisi lease di `database_locks` (`hold_state = 'running'`, TTL 30s, heartbeat 10s). Jika DB sedang dikunci oleh worker lain atau berstatus `held_partial`/`held_uncertain`, pesan di-requeue dengan backoff atau ditahan. |
| `Revalidation` | Setelah lock didapat, worker memvalidasi ulang: (1) seluruh grup yang saat ini memiliki grant (`VIEW_ONLY` & `VIEW_EDIT`) masih ter-approve oleh `leader_user_id` aktif, (2) tidak ada `partial`/`uncertain` lain yang memblokir, (3) aturan linear/LIFO masih terpenuhi. |
| `Execute` | Tandai `is_immutable = true`, buat baris attempt baru di `migration_executions` (`status = 'running'`, salinan `executed_sql_snapshot`), lalu eksekusi mode `normal` atau `batch` melalui Instrumented SQL Wrapper. |
| `Commit/Rollback` | Mode normal: SQL + insert `_platform_meta.execution_proofs` dalam 1 TX Target DB (`COMMIT`, atau `ROLLBACK` jika gagal). Mode batch: loop keyset cursor per batch dalam TX terpisah (`COMMIT` batch SQL + `batch_undo_logs` + `batch_checkpoints`). |
| `Update Migration History` | Update `migration_executions` menjadi `success`, `failed`, `partial`, `partially_reverted`, atau `uncertain` lengkap dengan `error_sqlstate`, `total_batches_committed`, `total_rows_affected`. Baris lama tidak pernah ditimpa oleh attempt berikutnya. |
| `Audit Log` | Seluruh event penting (`auth.*`, `access.*`, `migration.*`, `approval.*`, `queue.*`, `worker.*`, `lock.*`, dan `sql.start`/`sql.result`/`sql.tx_end`) ditulis sebagai JSON terstruktur via `log/slog` ke `stdout`/`stderr` (tidak disimpan ke tabel DB platform). |

### 1.3 Keputusan Arsitektur Kunci

1. **Same-Origin SPA & Proteksi CSRF**:
   - Backend Go (`cmd/server`) menyajikan berkas statis hasil build Vite (`frontend/dist`) dan me-route seluruh path non-`/api/*` & non-`/metrics` ke `index.html`.
   - Middleware `CSRFProtectionMiddleware` memeriksa header `X-CSRF-Token` terlebih dahulu sebelum fallback ke form field agar body JSON pada request SPA tidak terganggu.
   - Middleware API (`/api/v1/*`) wajib mengembalikan respons JSON terstruktur (`401`, `403`, `409`, `422`), bukan redirect HTML `303 /login`.

2. **Ketahanan Queue & Anti-Double-Commit**:
   - RabbitMQ dideklarasikan dengan `exchange` durable, queue utama `migration.jobs` (`durable=true`), retry queue `migration.jobs.retry` (dengan `x-message-ttl` eksponensial bertingkat + dead-letter ke `migration.jobs`), dan `migration.jobs.dlq`.
   - Setiap pesan dikirim dengan `DeliveryMode: amqp.Persistent` agar selamat saat broker restart.
   - Duplikasi pesan (*at-least-once delivery*) dicegah dari *double commit* melalui pengecekan ganda: status terminal `migration_executions` untuk `job_id` di Platform DB **dan** keberadaan `execution_id` di `_platform_meta.execution_proofs` (normal) atau `(execution_id, batch_number)` di `_platform_meta.batch_checkpoints` (batch).

3. **Mode Eksekusi Batch Streaming (Tanpa Memuat Dataset ke Memori)**:
   - Sebelum batch ke-1 dimulai, worker menentukan cakupan target dari Target DB: `SELECT MIN(col), MAX(col) FROM schema.table`.
   - Setiap batch memakai **keyset cursor**:
     `WHERE col > $last_cursor AND col <= $max_cursor ORDER BY col ASC LIMIT $batch_size`.
   - Worker tidak pernah memanggil `SELECT *` seluruh tabel ke memori Go.
   - Dalam **1 transaksi PostgreSQL tunggal** pada Target DB untuk batch ke-$k$:
     1. Jika `is_irreversible = false`, tangkap primary key & snapshot nilai kolom sebelum diubah (`before_data` JSONB) ke `_platform_meta.batch_undo_logs` untuk rentang batch tersebut (mis. menggunakan `UPDATE ... RETURNING` atau `SELECT ... FOR UPDATE` hanya pada `batch_size` baris batch tersebut).
     2. Jalankan SQL batch pada rentang cursor batch ke-$k$.
     3. Insert/update checkpoint ke `_platform_meta.batch_checkpoints` (`execution_id`, `batch_number`, `cursor_start`, `cursor_end`, `target_max_cursor`, `rows_affected`).
     4. `COMMIT` transaksi Target DB.
   - Setelah `COMMIT` sukses, worker menyinkronkan progres ke `execution_batch_progress` dan `migration_executions` di Platform DB.
   - Jika batch ke-$k$ gagal, hanya transaksi batch ke-$k$ yang di-rollback; batch $1..(k-1)$ tetap committed. Status eksekusi menjadi `partial`, lock berubah menjadi `hold_state = 'held_partial'`.

4. **Resume Batch & Kompensasi Revert Batch Terbalik**:
   - **`resume_batch`**: membaca checkpoint committed terakhir (`MAX(batch_number)`, `cursor_end`) dari `_platform_meta.batch_checkpoints` lalu melanjutkan tepat dari `cursor > last_committed_cursor` tanpa mengulang batch $1..(k-1)$.
   - **`revert_batch`**: hanya diizinkan bila migrasi **reversible** (`is_irreversible = false`) dan data cakupan batch/undo snapshot tersedia. Jika tidak aman (misal `irreversible` atau missing undo/cursor scope), request ditolak (`409 Conflict` / `400 Bad Request`) dan pengguna wajib membuat migration perbaikan baru.
   - Eksekusi `revert_batch` mengiterasi `_platform_meta.batch_checkpoints` yang `is_reverted = FALSE` secara **urutan terbalik (`ORDER BY batch_number DESC`)**. Tiap batch dijalankan dalam transaksi tersendiri (menjalankan kompensasi `sql_down` berbasis cakupan batch / memulihkan `batch_undo_logs` + set `is_reverted = TRUE` dalam TX yang sama). Jika gagal di tengah, batch tersisa ditandai `partially_reverted` dan lock tetap `held_partial` agar dapat dilanjutkan dengan request `revert_batch` baru.
   - Baik `resume_batch` maupun `revert_batch` tetap melalui alur penuh: `Request → Approval Leader Semua Grup → Queue → Worker → Lock → Revalidation → Execute`.

5. **Distributed Lock Lease, Crash Recovery, & Uncertain Commit Verification**:
   - Tabel `database_locks` memakai pola **Lease + Heartbeat** (TTL lease 30 detik, diperbarui setiap 10 detik oleh goroutine worker selama eksekusi).
   - **`hold_state`**:
     - `running`: terkunci oleh worker aktif. Jika worker mati/crash, heartbeat berhenti hingga `expires_at < NOW()`.
     - `held_partial`: dikunci secara permanen sampai ada `resume_batch` atau `revert_batch` yang sukses memulihkan kondisi database.
     - `held_uncertain`: dikunci sementara hasil commit belum dapat dipastikan.
   - **Protokol Pemulihan Crash Worker**:
     1. Saat worker lain mengambil job pada `database_id` yang sama dan menemukan lock `hold_state = 'running'` dengan `expires_at < NOW()`, worker mengambil alih lock secara atomik (`UPDATE database_locks ... WHERE expires_at < NOW()`) dan memindahkan eksekusi lama ke `uncertain` (`hold_state = 'held_uncertain'`).
     2. Worker pemulih membuka koneksi ke Target DB dan memeriksa `_platform_meta.execution_proofs` (untuk normal) atau `_platform_meta.batch_checkpoints` (untuk batch):
        - Jika normal & `execution_proofs` ada → eksekusi lama **benar-benar ter-commit** sebelum crash → tandai `migration_executions.status = 'success'`, lepaskan lock, lanjutkan job baru.
        - Jika normal & `execution_proofs` tidak ada (dan Target DB dapat dihubungi) → transaksi PostgreSQL target otomatis ter-rollback saat koneksi worker lama putus → tandai `migration_executions.status = 'failed'` (error: worker crash before commit), lepaskan lock.
        - Jika batch & ada `batch_checkpoints` untuk `execution_id` lama → sinkronkan progres batch ke Platform DB, tandai eksekusi lama `partial`, ubah lock ke `held_partial`, dan **tahan/tolak** job baru yang bukan `resume_batch`/`revert_batch` untuk eksekusi tersebut.
     3. Selama Target DB tidak dapat dihubungi untuk verifikasi bukti, status tetap `uncertain` dan lock tetap `held_uncertain` sehingga tidak pernah terjadi eksekusi paralel atau *double commit*.

6. **Structured Logging (`log/slog`) & OpenTelemetry Tracing**:
   - Output `log/slog` JSON hanya ke `stdout`/`stderr`, **tidak** disimpan ke DB platform dan **tidak** wajib ditampilkan di SPA.
   - Atribut wajib pada setiap log: `time`, `level`, `event`, `actor`, `database`, `status`, `correlation_id`, `trace_id`, `span_id`.
   - Atribut tambahan pada eksekusi SQL (termasuk *read-only* viewer/explain/catalog dan batch): `query`, `params` (tersamarkan), `duration_ms`, `rows_affected`, `error`, `sqlstate`, `migration_id`, `job_id`, `execution_id`, `attempt`, `batch_number`.
   - Setiap unit SQL mencatat 3 event log terpisah: `sql.start`, `sql.result`, dan `sql.tx_end` (`commit` / `rollback`).
   - Semua secret (`password`, `password_encrypted`, `ENCRYPTION_KEY`, `session_id`, `csrf_token`, DSN URL) wajib disamarkan (`[REDACTED]`).

### 1.4 State Machine, Aturan LIFO, & Logika `Hide rollback`

1. **Siklus State Request (`migration_requests.status`)**:
   - `draft` → (submit) → `pending_review`
   - `pending_review` → (ada 1 leader grup reject) → `rejected` *(terminal)*
   - `pending_review` → (semua leader grup dengan grant `VIEW_ONLY` & `VIEW_EDIT` approve) → `approved`
   - `approved` → (enqueue) → `pending_queue` → (worker mulai) → `running`
   - `running` → `success` | `failed` | `partial` | `partially_reverted` | `uncertain`

2. **Immutability Definisi Migration**:
   - Ketika attempt eksekusi pertama dimulai di worker (`status = 'running'`), kolom `migration_definitions.is_immutable` diset `TRUE`.
   - Segala permintaan `PUT/PATCH` terhadap `sql_up`, `sql_down`, `is_irreversible`, maupun konfigurasi batch ditolak dengan `409 Conflict`. Jika SQL perlu diubah (misalnya setelah gagal), pengguna harus membuat **definisi migration baru**.

3. **Active Migration Tip (LIFO), Rollback, Forward, & Kegagalan (Revisi 2 & 4)**:
   - **Active Execution Set**: semua eksekusi dengan `action_type IN ('up', 'forward', 'resume_batch')` dan `status = 'success'` yang `execution_id`-nya **belum** muncul sebagai `reverted_execution_id` pada eksekusi `rollback`/`revert_batch` yang `status = 'success'`.
   - **Active Migration Tip**: eksekusi aktif paling akhir (berdasarkan urutan kronologis penerapan / `migration_version` tertinggi yang aktif).
   - **Rollback (`action_type = 'rollback'`)**:
     - Hanya boleh diajukan pada **Active Migration Tip** dan hanya jika `is_irreversible = FALSE`.
     - Untuk urutan `A → B → C`, rollback `C` wajib `success` sebelum `B` boleh di-rollback.
     - Jika rollback gagal (`status = 'failed'`), posisi Active Tip **tidak berubah**; attempt gagal tetap tercatat di `migration_executions`.
   - **Forward (`action_type = 'forward'`)**:
     - Menjalankan ulang `sql_up` sebagai **execution_id baru** pada migration yang pernah di-rollback (atau pada migration yang attempt sebelumnya `failed`).
     - Mengikuti urutan linear setelah Active Tip dan **hanya** mengeksekusi migration tersebut (tidak otomatis mengeksekusi migration berikutnya).
     - Sesuai Revisi 2 & 4: *attempt* yang `failed` tetap tercatat di riwayat, **tetap boleh di-forward lagi**, atau **boleh dilanjutkan dengan migration baru lainnya** selama tidak ada status `partial`/`partially_reverted`/`uncertain` yang menahan lock DB.

4. **Algoritma `Hide rollback`**:
   - Cari seluruh baris di `migration_executions` dengan `action_type IN ('rollback', 'revert_batch') AND status = 'success' AND reverted_execution_id IS NOT NULL`.
   - Kumpulkan himpunan `HiddenIDs` = gabungan seluruh `execution_id` rollback sukses tersebut **dan** seluruh `reverted_execution_id` pasangannya.
   - Saat parameter `?hide_rollback=true`: kembalikan baris yang `execution_id NOT IN HiddenIDs`.
   - Verifikasi contoh spesifikasi:
     - Riwayat lengkap: `A [E1]`, `B [E2]`, `B (rollback E2) [E3]`, `B (forward) [E4]`, `C [E5]`.
     - `HiddenIDs = {E2, E3}`.
     - Saat `Hide rollback` dicentang: tampil `A [E1]`, `B [E4]`, `C [E5]`.
     - Attempt `failed` tidak pernah masuk `HiddenIDs`, sehingga selalu tetap tampil. Filter tidak menghapus data di tabel maupun audit log.

### 1.5 Kontrak REST JSON API (`/api/v1/*`)

| Kategori | Method & Endpoint | Akses / Role | Keterangan |
|---|---|---|---|
| **Auth** | `POST /api/v1/auth/login` | Publik | Login JSON `{username, password}` → set cookie `session_id` + return user & `csrf_token`. |
| | `POST /api/v1/auth/logout` | Auth + CSRF | Revoke session & clear cookie. |
| | `GET /api/v1/auth/me` | Auth | Profil user aktif + `csrf_token` + daftar grup yang dipimpin (`leader_group_ids`). |
| **Admin** | `GET/POST /api/v1/admin/users` | Platform Admin | List & create user. |
| | `POST /api/v1/admin/users/{id}/toggle-active` | Admin + CSRF | Aktif/nonaktifkan user (+ revoke session). |
| | `POST /api/v1/admin/users/{id}/toggle-admin` | Admin + CSRF | Promosi/demosi admin (+ rotate/revoke session). |
| | `POST /api/v1/admin/users/{id}/reset-password` | Admin + CSRF | Reset password (+ revoke session). |
| | `GET/POST /api/v1/admin/groups` | Admin + CSRF | List & create group (**wajib `leader_user_id`**). |
| | `PUT/DELETE /api/v1/admin/groups/{id}` | Admin + CSRF | Edit nama/`leader_user_id` & hapus group. |
| | `POST/DELETE /api/v1/admin/groups/{id}/members` | Admin + CSRF | Tambah/hapus anggota grup (+ rotate/revoke session). |
| | `GET/POST/DELETE /api/v1/admin/grants` | Admin + CSRF | List, assign (`VIEW_ONLY`/`VIEW_EDIT`), & revoke grant. |
| | `GET/POST /api/v1/admin/connections` | Admin + CSRF | List & create koneksi target PostgreSQL. |
| | `PUT/DELETE /api/v1/admin/connections/{id}` | Admin + CSRF | Update, hapus koneksi. |
| | `POST /api/v1/admin/connections/{id}/test` | Admin + CSRF | Uji koneksi target (error disanitasi). |
| **Explorer** | `GET /api/v1/databases` | Auth | Daftar DB PostgreSQL sesuai grant user (memuat `access_level` & status lock saat ini). |
| | `GET /api/v1/databases/{id}/schema` | `VIEW_ONLY`+ | Katalog tabel, kolom, constraint, index (via Instrumented SQL Wrapper). |
| | `GET /api/v1/databases/{id}/data` | `VIEW_ONLY`+ | Data tabel paginasi 50 baris + `NULL` indicator. |
| | `POST /api/v1/databases/{id}/explain` | `VIEW_ONLY`+ (`ANALYZE` butuh `VIEW_EDIT`) | EXPLAIN / EXPLAIN ANALYZE + CSRF. |
| | `POST /api/v1/databases/{id}/diff` | `VIEW_ONLY`+ | Schema diff JSON vs katalog live + generator kandidat DDL. |
| **Migration Def** | `GET /api/v1/databases/{id}/migrations` | `VIEW_ONLY`+ | Daftar definisi migration, status `is_immutable`, versi aktif (Active Tip), kandidat `rollback`/`forward`. |
| | `POST /api/v1/databases/{id}/migrations` | `VIEW_EDIT` + CSRF | Buat draft definisi migration (Normal/Batch, Reversible/Irreversible). |
| | `PUT /api/v1/databases/{id}/migrations/{mid}` | `VIEW_EDIT` + CSRF | Edit draft (ditolak `409 Conflict` bila `is_immutable = true`). |
| | `POST /api/v1/databases/{id}/migrations/validate` | `VIEW_EDIT` + CSRF | Dry-run validasi SQL, statement count, & verifikasi tabel/kolom cursor batch. |
| **Workflow & Approval** | `GET /api/v1/requests` (atau per DB) | Auth (anggota grup terkait) | Daftar change request + status approval tiap grup. |
| | `POST /api/v1/databases/{id}/requests` | `VIEW_EDIT` + CSRF | Buat request untuk `up`, `rollback`, `forward`, `resume_batch`, atau `revert_batch`. |
| | `POST /api/v1/requests/{rid}/submit` | `VIEW_EDIT` + CSRF | Ubah `draft` → `pending_review` & generate baris `migration_approvals` untuk semua grup (`VIEW_ONLY` & `VIEW_EDIT`). |
| | `POST /api/v1/requests/{rid}/approve` | **Group Leader terkait** + CSRF | Approve untuk grup yang dipimpin user (`403` jika bukan leader grup yang diperlukan). |
| | `POST /api/v1/requests/{rid}/reject` | **Group Leader terkait** + CSRF | Reject dengan komentar alasan (`403` jika bukan leader grup). |
| | `POST /api/v1/requests/{rid}/enqueue` | `VIEW_EDIT` + CSRF | Jika `approved`, publish ke RabbitMQ & kembalikan `202 Accepted` `{job_id, execution_id, status: "pending_queue"}`. |
| **Monitor & History** | `GET /api/v1/jobs/{job_id}` | `VIEW_ONLY`+ | Status real-time job, state machine, progres batch/row, status `partial`/`partially_reverted`/`uncertain`. |
| | `GET /api/v1/executions/{execution_id}` | `VIEW_ONLY`+ | Detail eksekusi, SQL aktual snapshot, dan rincian `execution_batch_progress`. |
| | `GET /api/v1/databases/{id}/history?hide_rollback=true\|false` | `VIEW_ONLY`+ | Riwayat seluruh attempt (`success`, `failed`, `partial`, dll.) + filter `Hide rollback` by `execution_id`. |
| **Observability** | `GET /metrics` | Internal / Scraper | Metrik Prometheus (API pada `:8080/metrics`, Worker pada `:9091/metrics`). |

---

## Bagian 2: Skema Database (Evolusi Aditif dari Milestone 1)

### 2.1 Platform Database (PostgreSQL — Lanjutan dari `001`–`004`)

Seluruh tabel Milestone 1 (`users`, `sessions`, `session_grace_tokens`, `groups`, `user_groups`, `database_connections`, `group_database_grants`) **tetap dipertahankan** menggunakan primary key `VARCHAR(36)` UUID agar seluruh mekanisme auth, session grace period, enkripsi koneksi, dan access control M1 tetap kompatibel 100%.

Berkas migrasi baru `migrations/platform/005_m2_schema.sql` menambahkan ekstensi kolom dan tabel berikut:

```sql
-- ============================================================================
-- 1. EKSTENSI GRUP: PENETAPAN GROUP LEADER (WAJIB M2)
-- ============================================================================
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS leader_user_id VARCHAR(36) REFERENCES users(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_groups_leader ON groups(leader_user_id);

-- ============================================================================
-- 2. DEFINISI MIGRATION (IMMUTABLE SETELAH PERNAH DIEKSEKUSI)
-- ============================================================================
CREATE TABLE IF NOT EXISTS migration_definitions (
    id                   VARCHAR(36) PRIMARY KEY,
    database_id          VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    version              INT NOT NULL,               -- urutan linear per database (1, 2, 3, ...)
    revision             INT NOT NULL DEFAULT 1,
    name                 VARCHAR(200) NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    sql_up               TEXT NOT NULL,
    sql_down             TEXT,                       -- NULL jika irreversible
    is_irreversible      BOOLEAN NOT NULL DEFAULT FALSE,
    irreversible_reason  TEXT,
    execution_mode       VARCHAR(16) NOT NULL DEFAULT 'normal'
                         CHECK (execution_mode IN ('normal', 'batch')),
    batch_table          VARCHAR(128),               -- nama tabel target untuk validasi & undo log
    batch_cursor_column  VARCHAR(128),               -- kolom unik & immutable, misal 'id'
    batch_size           INT CHECK (batch_size IS NULL OR batch_size > 0),
    is_immutable         BOOLEAN NOT NULL DEFAULT FALSE, -- otomatis TRUE saat attempt pertama dimulai
    created_by           VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (database_id, version),
    CONSTRAINT chk_reversible_rule CHECK (
        (is_irreversible = FALSE AND sql_down IS NOT NULL AND length(trim(sql_down)) > 0)
        OR
        (is_irreversible = TRUE AND irreversible_reason IS NOT NULL AND length(trim(irreversible_reason)) > 0)
    ),
    CONSTRAINT chk_batch_config CHECK (
        execution_mode = 'normal'
        OR (
            batch_table IS NOT NULL AND length(trim(batch_table)) > 0 AND
            batch_cursor_column IS NOT NULL AND length(trim(batch_cursor_column)) > 0 AND
            batch_size IS NOT NULL AND batch_size > 0
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_mig_def_db_version ON migration_definitions(database_id, version ASC);

-- ============================================================================
-- 3. WORKFLOW CHANGE REQUEST & MULTI-GROUP LEADER APPROVAL
-- ============================================================================
CREATE TABLE IF NOT EXISTS migration_requests (
    id                   VARCHAR(36) PRIMARY KEY,
    database_id          VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    migration_id         VARCHAR(36) NOT NULL REFERENCES migration_definitions(id) ON DELETE CASCADE,
    action_type          VARCHAR(24) NOT NULL
                         CHECK (action_type IN ('up', 'rollback', 'forward', 'resume_batch', 'revert_batch')),
    target_execution_id  VARCHAR(36),                -- sasaran untuk rollback / resume_batch / revert_batch
    status               VARCHAR(32) NOT NULL DEFAULT 'draft'
                         CHECK (status IN (
                             'draft', 'pending_review', 'approved', 'rejected',
                             'pending_queue', 'running', 'success', 'failed',
                             'partial', 'partially_reverted', 'uncertain'
                         )),
    requested_by         VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    batch_size_override  INT CHECK (batch_size_override IS NULL OR batch_size_override > 0),
    job_id               VARCHAR(36),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    submitted_at         TIMESTAMPTZ,
    approved_at          TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_mig_req_db_status ON migration_requests(database_id, status);
CREATE INDEX IF NOT EXISTS idx_mig_req_job_id ON migration_requests(job_id);

CREATE TABLE IF NOT EXISTS migration_approvals (
    id                    VARCHAR(36) PRIMARY KEY,
    request_id            VARCHAR(36) NOT NULL REFERENCES migration_requests(id) ON DELETE CASCADE,
    group_id              VARCHAR(36) NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    leader_user_id        VARCHAR(36) NOT NULL REFERENCES users(id),
    required_access_level VARCHAR(20) NOT NULL CHECK (required_access_level IN ('VIEW_ONLY', 'VIEW_EDIT')),
    decision              VARCHAR(16) NOT NULL DEFAULT 'pending'
                          CHECK (decision IN ('pending', 'approved', 'rejected')),
    comment               TEXT NOT NULL DEFAULT '',
    decided_at            TIMESTAMPTZ,
    UNIQUE (request_id, group_id)
);

CREATE INDEX IF NOT EXISTS idx_mig_app_leader ON migration_approvals(leader_user_id, decision);

-- ============================================================================
-- 4. MIGRATION HISTORY (APPEND-ONLY, SEMUA ATTEMPT SUKSES & GAGAL)
--    Sesuai Revisi 2 & 4: catat semua attempt + salinan SQL aktual
-- ============================================================================
CREATE TABLE IF NOT EXISTS migration_executions (
    execution_id            VARCHAR(36) PRIMARY KEY,
    request_id              VARCHAR(36) NOT NULL REFERENCES migration_requests(id) ON DELETE CASCADE,
    job_id                  VARCHAR(36) NOT NULL,
    database_id             VARCHAR(36) NOT NULL REFERENCES database_connections(id) ON DELETE CASCADE,
    migration_id            VARCHAR(36) NOT NULL REFERENCES migration_definitions(id) ON DELETE CASCADE,
    migration_version       INT NOT NULL,
    migration_revision      INT NOT NULL,
    migration_name          VARCHAR(200) NOT NULL,
    action_type             VARCHAR(24) NOT NULL
                            CHECK (action_type IN ('up', 'rollback', 'forward', 'resume_batch', 'revert_batch')),
    reverted_execution_id   VARCHAR(36) REFERENCES migration_executions(execution_id),
    parent_execution_id     VARCHAR(36) REFERENCES migration_executions(execution_id),
    attempt_number          INT NOT NULL DEFAULT 1,
    executed_by             VARCHAR(36) REFERENCES users(id) ON DELETE SET NULL,
    executed_sql_snapshot   TEXT NOT NULL,
    execution_mode          VARCHAR(16) NOT NULL CHECK (execution_mode IN ('normal', 'batch')),
    status                  VARCHAR(32) NOT NULL
                            CHECK (status IN ('running', 'success', 'failed', 'partial', 'partially_reverted', 'uncertain')),
    total_batches_committed INT NOT NULL DEFAULT 0,
    total_rows_affected     BIGINT NOT NULL DEFAULT 0,
    error_message           TEXT,
    error_sqlstate          VARCHAR(16),
    trace_id                VARCHAR(64),
    started_at              TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at             TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_exec_db_started ON migration_executions(database_id, started_at ASC, execution_id ASC);
CREATE INDEX IF NOT EXISTS idx_exec_reverted   ON migration_executions(reverted_execution_id);
CREATE INDEX IF NOT EXISTS idx_exec_job_id     ON migration_executions(job_id);

-- ============================================================================
-- 5. DISTRIBUTED LOCK & BATCH PROGRESS MIRROR
-- ============================================================================
CREATE TABLE IF NOT EXISTS database_locks (
    database_id    VARCHAR(36) PRIMARY KEY REFERENCES database_connections(id) ON DELETE CASCADE,
    lock_token     VARCHAR(36) NOT NULL,
    worker_id      VARCHAR(128) NOT NULL,
    execution_id   VARCHAR(36) NOT NULL,
    hold_state     VARCHAR(24) NOT NULL CHECK (hold_state IN ('running', 'held_partial', 'held_uncertain')),
    acquired_at    TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    heartbeat_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS execution_batch_progress (
    id                VARCHAR(36) PRIMARY KEY,
    execution_id      VARCHAR(36) NOT NULL REFERENCES migration_executions(execution_id) ON DELETE CASCADE,
    batch_number      INT NOT NULL,
    cursor_start      TEXT,
    cursor_end        TEXT,
    target_max_cursor TEXT,
    rows_affected     BIGINT NOT NULL DEFAULT 0,
    direction         VARCHAR(16) NOT NULL CHECK (direction IN ('forward', 'revert')),
    status            VARCHAR(16) NOT NULL CHECK (status IN ('committed', 'reverted', 'failed')),
    committed_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (execution_id, batch_number, direction)
);
```

> **Catatan Migrasi Tabel M1 `migrations`**: Tabel `migrations` dari `001_init_platform.sql` dan `002_migration_core.sql` tetap dipertahankan agar test regresi M1 tetap valid; seluruh alur M2 membaca dan menulis ke `migration_definitions`, `migration_requests`, `migration_approvals`, dan `migration_executions`.

### 2.2 Metadata Schema di Target Database (`_platform_meta`)

Dibuat otomatis secara idempoten oleh worker (`CREATE SCHEMA IF NOT EXISTS _platform_meta`) di masing-masing Target PostgreSQL DB. `ValidateSQL` wajib memblokir SQL migrasi user yang menyentuh schema `_platform_meta` (selain blokir `_dbplatform_migration_receipts` dari M1).

```sql
CREATE SCHEMA IF NOT EXISTS _platform_meta;

-- Bukti eksekusi normal untuk verifikasi uncertain commit & cegah double commit
CREATE TABLE IF NOT EXISTS _platform_meta.execution_proofs (
    execution_id   VARCHAR(36) PRIMARY KEY,
    migration_id   VARCHAR(36) NOT NULL,
    action_type    VARCHAR(24) NOT NULL,
    committed_at   TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Checkpoint batch yang di-commit dalam TX yang sama dengan perubahan batch
CREATE TABLE IF NOT EXISTS _platform_meta.batch_checkpoints (
    execution_id      VARCHAR(36) NOT NULL,
    batch_number      INT NOT NULL,
    cursor_start      TEXT,
    cursor_end        TEXT NOT NULL,
    target_max_cursor TEXT NOT NULL,
    rows_affected     BIGINT NOT NULL,
    is_reverted       BOOLEAN NOT NULL DEFAULT FALSE,
    committed_at      TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reverted_at       TIMESTAMPTZ,
    PRIMARY KEY (execution_id, batch_number)
);

-- Menyimpan cakupan row & snapshot nilai lama (JSONB) bila batch perlu di-revert terbalik
CREATE TABLE IF NOT EXISTS _platform_meta.batch_undo_logs (
    execution_id   VARCHAR(36) NOT NULL,
    batch_number   INT NOT NULL,
    row_key        TEXT NOT NULL,
    before_data    JSONB NOT NULL,
    PRIMARY KEY (execution_id, batch_number, row_key)
);
```

---

## Bagian 3: Pembagian Tugas Terperinci, Eksklusif, & Sekuensial

### Peta Alur Pengerjaan (1 Bagian = 1 PIC Tunggal, Berurutan 1 → 10)

```text
[Bagian 1: A1] Infra, Skema M2, Telemetry/Instrumented SQL, & REST JSON API M1
      │ (merge ke main + docs/api-contract-m1.md)
      ▼
[Bagian 2: A2] Frontend SPA Tahap 1 (Migrasi Halaman M1 + Layout Responsif)
      │ (merge ke main)
      ▼
[Bagian 3: A1] Backend M2 Core (Migrations, Approval Workflow, History, Lock, RabbitMQ Publisher)
      │ (merge ke main + docs/api-contract-m2.md)
      ▼
[Bagian 4: A2] Go Worker & Batch Engine + Dataset Uji >1 Detik & Benchmark
      │ (merge ke main + docs/batch-benchmark.md)
      ▼
[Bagian 5: A1] Automated Tests (Unit & Integration M1+M2) + Sample Log & Trace OTel
      │ (merge ke main + docs/sample-logs-and-traces.md)
      ▼
[Bagian 6: A2] Frontend SPA Tahap 2 (Fitur M2 Penuh + Audit Responsif + Bundle SPA)
      │ (merge ke main — SELURUH FITUR WAJIB MILESTONE 2 SELESAI)
      ▼
[Bagian 7: A1] Bonus Observability Part A (Prometheus /metrics + OTLP) & Bonus CodeQL SAST
      │ (merge ke main + docs/security-codeql.md)
      ▼
[Bagian 8: A2] Bonus Observability Part B (OTel Collector, Tempo, Loki, Prometheus, Grafana)
      │ (merge ke main + docs/observability.md)
      ▼
[Bagian 9: A1] Bonus Stress Testing k6 Part A (Script, Eksekusi Beban Bertahap, Raw Data)
      │ (merge ke main + docs/k6-raw-results.md)
      ▼
[Bagian 10: A2] Bonus Stress Testing k6 Part B (Analisis) + README Final + Verifikasi + Pengumpulan
```

### Tabel Ringkas Kepemilikan Bagian

| Bagian | PIC Tunggal | Prasyarat | Lingkup Direktori/File Eksklusif | Target Fitur |
|---|---|---|---|---|
| **Bagian 1** | **Anggota 1** (Dika) | Repo M1 `main` | `compose.yaml`, `compose.test.yaml`, `.env.example`, `migrations/platform/005_m2_schema.sql`, `scripts/seed_m2.sql`, `internal/config/`, `internal/telemetry/`, `internal/database/instrumented.go`, `internal/api/auth_admin_db.go`, `cmd/server/`, `docs/api-contract-m1.md` | Infrastruktur, DB Schema M2, `log/slog` + OTel + Instrumented SQL, REST JSON API M1 |
| **Bagian 2** | **Anggota 2** (Wildan) | Bagian 1 merged | `frontend/` (app shell, auth, admin + group leader, DB viewer/explain/diff) | Frontend SPA M1 + Responsif Desktop & Mobile |
| **Bagian 3** | **Anggota 1** (Dika) | Bagian 2 merged | `internal/lock/`, `internal/queue/`, `internal/migration/definition.go`, `internal/migration/workflow.go`, `internal/migration/approval.go`, `internal/migration/history_m2.go`, `internal/api/m2_handlers.go`, `docs/api-contract-m2.md` | Workflow Approval, Immutability, LIFO/Linear/Hide Rollback, RabbitMQ Publisher, Distributed Lock |
| **Bagian 4** | **Anggota 2** (Wildan) | Bagian 3 merged | `cmd/worker/`, `internal/worker/`, `scripts/seed_large_dataset.sql`, `scripts/seed_large_dataset.go`, `docs/batch-benchmark.md` | Go Worker, Normal & Batch Streaming, Resume/Revert, Crash/Uncertain Recovery, Dataset Uji >1s |
| **Bagian 5** | **Anggota 1** (Dika) | Bagian 4 merged | `internal/**/*_test.go` (unit & integration M2), `scripts/test-integration.ps1`, `docs/sample-logs-and-traces.md` | Unit Test (Mock/Stub), Integration Test (Sukses/Gagal/Tolak Akses), Contoh Log & Span |
| **Bagian 6** | **Anggota 2** (Wildan) | Bagian 5 merged | `frontend/src/pages/migrations/*`, `frontend/src/pages/approvals/*`, `frontend/src/pages/jobs/*`, `frontend/dist/` | Frontend SPA Fitur M2 + Hide Rollback + Job Monitor + Audit Responsivitas + Hapus Legacy Templates |
| **Bagian 7** | **Anggota 1** (Dika) | Bagian 6 merged | `internal/telemetry/metrics.go`, wiring `/metrics` di `cmd/server` & `cmd/worker`, `.github/workflows/codeql.yml`, perbaikan SAST, `docs/security-codeql.md` | Bonus Observability (Instrumentasi Metrics + OTLP) & Bonus Security CodeQL SAST |
| **Bagian 8** | **Anggota 2** (Wildan) | Bagian 7 merged | `compose.observability.yaml`, `observability/` (`otel-collector`, `prometheus`, `tempo`, `loki`, `grafana/provisioning`, dashboard JSON), `docs/observability.md` | Bonus Observability (Collector + Tempo + Loki + Prometheus + Grafana Dashboard + Korelasi) |
| **Bagian 9** | **Anggota 1** (Dika) | Bagian 8 merged | `tests/k6/stress_test.js`, `tests/k6/seed_k6.sql`, `docs/k6-raw-results.md` | Bonus Stress Testing k6 (Script, Pre-approved Jobs, Eksekusi Ramp-Up & Ramp-Down) |
| **Bagian 10** | **Anggota 2** (Wildan) | Bagian 9 merged | `README.md`, tag rilis final & submission | Bonus Analisis k6 + Dokumentasi README Final + Clean-Env Dry Run + Pengumpulan Resmi |

---

### Rincian Eksekusi Setiap Bagian

#### BAGIAN 1 — Infrastruktur Core, Skema Database M2, Telemetry/Instrumented SQL, & REST JSON API M1
* **PIC Tunggal**: **Anggota 1** (Dika Pramudya Nugraha — `13524132`)
* **Prasyarat**: Branch `main` Milestone 1 bersih dan seluruh test M1 lulus.
* **Lingkup File Eksklusif**:
  - `compose.yaml`, `compose.test.yaml`, `.env.example`
  - `migrations/platform/005_m2_schema.sql`, `internal/database/platform.go`, `scripts/seed_m2.sql`
  - `internal/config/config.go`
  - `internal/telemetry/` (`logger.go`, `redact.go`, `otel.go`, `sqllog.go`)
  - `internal/database/` (integrasi Instrumented SQL Wrapper ke `catalog_postgres.go`, `data_viewer.go`, `explain.go`)
  - `internal/auth/csrf.go`, `internal/auth/middleware.go`, `internal/admin/`, `internal/api/` (handler JSON M1)
  - `cmd/server/main.go`, `docs/api-contract-m1.md`
* **Rincian Tugas Teknis**:
  1. **Infrastruktur Docker & Konfigurasi**:
     - Perbarui `compose.yaml`: pertahankan `platform-db`, `target-postgres` (`target-postgres-1`), tambahkan `target-postgres-2` (port `5434`, untuk uji paralel lintas database), tambahkan `rabbitmq` (`rabbitmq:3-management-alpine`, durable volume, healthcheck), serta service `backend-api` dan `worker`.
     - Perbarui `compose.test.yaml`: tambahkan service `rabbitmq` (port `15672`/`15673` terisolasi) agar integration test antrean berjalan tanpa mengganggu data demo.
     - Tambah environment config di `internal/config/config.go` & `.env.example` untuk `RABBITMQ_URL`, `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT` (kosong = console exporter), `SPA_DIST_DIR`, dan `WORKER_ID`.
  2. **Skema DB M2 & Seeder**:
     - Implementasikan `migrations/platform/005_m2_schema.sql` sesuai Bagian 2.1 dan pastikan `database.MigratePlatformDB` mengeksekusinya secara idempoten.
     - Siapkan `scripts/seed_m2.sql` (atau bootstrap CLI helper) berisi 1 admin, 2+ group dengan masing-masing `leader_user_id` terisi, minimal 2 user member, serta grant kombinasi `VIEW_ONLY` dan `VIEW_EDIT` ke `target-postgres-1` dan `target-postgres-2`.
  3. **Paket Telemetry & Instrumented SQL Wrapper (`internal/telemetry`)**:
     - Setup `log/slog` JSON handler ke `stdout`/`stderr` dengan atribut wajib (`time`, `level`, `event`, `actor`, `database`, `status`, `correlation_id`, `trace_id`, `span_id`).
     - Buat fungsi `Redact(key, value)` untuk menyamarkan password, token session, `csrf_token`, `ENCRYPTION_KEY`, dan DSN URL secara konsisten.
     - Setup OpenTelemetry `TracerProvider` (default `stdouttrace`, mendukung OTLP bila env endpoint tersedia).
     - Bangun **Instrumented SQL Wrapper** (`ExecContext`, `QueryContext`, `QueryRowContext`, `BeginTx`, `Commit`, `Rollback`) yang membuat child OTel span dan mencetak 3 log JSON terpisah (`sql.start`, `sql.result` + `query`, sanitized `params`, `duration_ms`, `rows_affected`, `error`, `sqlstate`, dan `sql.tx_end`). Terapkan pada query *read-only* di `internal/database`.
  4. **REST JSON API M1 & SPA Hook**:
     - Buat middleware `RequireAuthAPI` (`401` JSON) & perbarui `CSRFProtectionMiddleware` agar mengecek header `X-CSRF-Token` sebelum `PostFormValue`.
     - Bangun endpoint JSON `/api/v1/auth/*`, `/api/v1/admin/*` (termasuk input/edit `leader_user_id` pada grup, validasi grup wajib punya leader aktif), dan `/api/v1/databases/*` (list DB, schema, data 50 rows, explain, schema diff).
     - Tambah static SPA file handler di `cmd/server/main.go` (jika `frontend/dist` ada) dengan tetap menjaga kompatibilitas test paket internal M1.
     - Tulis kontrak lengkap di `docs/api-contract-m1.md`.
* **Kriteria Selesai (DoD)**:
  - `docker compose up -d --wait platform-db target-postgres target-postgres-2 rabbitmq` berjalan sehat.
  - Migrasi `005_m2_schema.sql` sukses berjalan idempoten; `go test ./internal/auth/... ./internal/access/... ./internal/admin/... ./internal/database/...` lulus.
  - Uji via `curl` menunjukkan seluruh endpoint `/api/v1/*` M1 bekerja dan query *read-only* ke target PostgreSQL mencetak 3 log JSON terstruktur (`sql.start`, `sql.result`, `sql.tx_end`) beserta `trace_id`/`span_id` tanpa membocorkan kredensial.
* **Artefak Handoff**: Merge branch `bagian-1-infra-telemetry-api-m1` ke `main` + `docs/api-contract-m1.md`.

---

#### BAGIAN 2 — Frontend SPA Tahap 1 (Migrasi Halaman Milestone 1 & Layout Responsif)
* **PIC Tunggal**: **Anggota 2** (Wildan Abdurrahman Ghazali — `13524054`)
* **Prasyarat**: Bagian 1 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - Seluruh direktori `frontend/` (`package.json`, `vite.config.ts`, `tsconfig.json`, `src/api/client.ts`, `src/context/AuthContext.tsx`, `src/components/Layout.tsx`, `src/pages/LoginPage.tsx`, `src/pages/admin/*`, `src/pages/databases/*`)
* **Rincian Tugas Teknis**:
  1. Inisialisasi proyek SPA (`React + TypeScript + Vite + TailwindCSS` + React Router) di `frontend/` dengan proxy dev `/api` ke backend Go (`:8080`).
  2. Bangun API client terpusat (`src/api/client.ts`) yang otomatis mengirim cookie same-origin (`credentials: 'include'`), menyisipkan header `X-CSRF-Token` pada `POST`/`PUT`/`PATCH`/`DELETE`, menangani `401` redirect ke `/login`, dan menyediakan state loading/error global.
  3. Implementasikan seluruh halaman Milestone 1 di SPA tanpa reload penuh:
     - **Login & Logout** (`/login`) beserta pesan error status akun nonaktif / kredensial salah.
     - **Admin Dashboard** (khusus `is_platform_admin`):
       - Users (`/admin/users`): create user, toggle active, toggle admin, reset password.
       - Groups (`/admin/groups`): create/edit/delete group, **dropdown pemilihan `leader_user_id`** (wajib diisi), kelola anggota grup.
       - Grants (`/admin/grants`): assign/revoke akses `VIEW_ONLY` & `VIEW_EDIT`.
       - Connections (`/admin/connections`): CRUD koneksi target PostgreSQL + tombol *Test Connection*.
     - **Database Explorer** (`/databases`):
       - Pilih database yang dapat diakses (menampilkan badge akses `VIEW_ONLY` / `VIEW_EDIT`).
       - Schema Viewer (`/databases/:id/schema`): daftar tabel, kolom, nullability, default, PK/FK/Unique/Check, dan index dengan loading skeleton.
       - Data Viewer (`/databases/:id/data`): paginasi 50 baris, badge visual `NULL`, dan pemilih tabel.
       - Explain & Schema Diff (`/databases/:id/explain`, `/databases/:id/diff`): pertahankan fungsionalitas bonus M1 di SPA.
  4. Terapkan desain responsif menyeluruh untuk **Desktop (`>=1280px`)** dan **Mobile (`375px`)**:
     - Sidebar navigasi berubah menjadi *collapsible drawer* di mobile.
     - Form, modal, dan tombol aksi mudah dijangkau tanpa terpotong.
     - Tabel lebar di Data Viewer & Schema Viewer menggunakan container `overflow-x-auto` (internal scroll) agar `body` tidak pernah overflow secara horizontal.
* **Kriteria Selesai (DoD)**:
  - `npm run build` di `frontend/` sukses menghasilkan `frontend/dist` tanpa error TypeScript.
  - Seluruh alur M1 (`Login → Admin Group+Leader → Grants → Select DB → Schema/Data/Explain/Diff`) berjalan mulus di SPA pada viewport `1440px` dan `375px` tanpa horizontal overflow halaman.
* **Artefak Handoff**: Merge branch `bagian-2-spa-m1` ke `main`.

---

#### BAGIAN 3 — Backend Milestone 2 Core (Workflow Approval, Immutability, History, Queue Publisher, & Distributed Lock)
* **PIC Tunggal**: **Anggota 1** (Dika Pramudya Nugraha — `13524132`)
* **Prasyarat**: Bagian 2 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `internal/lock/` (`manager.go`)
  - `internal/queue/` (`rabbitmq.go`, `carrier.go`)
  - `internal/migration/` (`definition.go`, `workflow.go`, `approval.go`, `history_m2.go`, update `sql.go` untuk memblokir `_platform_meta`)
  - `internal/api/` (`m2_migration_handlers.go`, `m2_request_handlers.go`, `m2_history_handlers.go`, `m2_job_handlers.go`)
  - `cmd/server/main.go` (registrasi route M2), `docs/api-contract-m2.md`
* **Rincian Tugas Teknis**:
  1. **Migration Definition & Validation API**:
     - Update `internal/migration/sql.go` agar menolak SQL yang menyentuh `_platform_meta` atau `_dbplatform_migration_receipts`.
     - Implementasikan CRUD Draft `migration_definitions`: wajib `sql_up` + `sql_down` (atau `is_irreversible = true` + `irreversible_reason`); mendukung mode `normal` dan `batch` (`batch_table`, `batch_cursor_column`, `batch_size`).
     - Validasi mode batch: cek terhadap katalog Target DB bahwa `batch_table` & `batch_cursor_column` ada dan bertipe unik/primary key.
     - **Immutability Guard**: tolak `PUT/PATCH` dengan `409 Conflict` jika `is_immutable = true`.
  2. **Change Request & Multi-Group Leader Approval Engine**:
     - Buat `POST /api/v1/databases/{id}/requests` (`VIEW_EDIT` only) untuk `up`, `rollback`, `forward`, `resume_batch`, dan `revert_batch`.
     - Validasi pengajuan sesuai spesifikasi:
       - Tolak jika DB sedang terkunci status `partial`/`partially_reverted`/`uncertain` (kecuali aksinya adalah `resume_batch`/`revert_batch` terhadap eksekusi tersebut).
       - `rollback`: validasi bahwa target adalah **Active Migration Tip** saat ini (LIFO) dan `is_irreversible = false`.
       - `forward`: validasi urutan linear terhadap Active Tip saat ini (boleh pada migration yang pernah di-rollback atau pernah `failed`).
       - `revert_batch`: tolak jika `is_irreversible = true`.
     - Saat `submit` ke `pending_review`: query semua grup pada `group_database_grants` (`VIEW_ONLY` dan `VIEW_EDIT`) untuk `database_id` terkait (tolak jika ada grup yang belum memiliki `leader_user_id`), lalu buat baris `migration_approvals` untuk setiap grup tersebut.
     - Endpoint `approve` & `reject`: hanya boleh diakses oleh `leader_user_id` grup terkait (`403 Forbidden` jika user lain, walau admin!). Jika semua grup `approved`, request berubah ke `approved`.
     - Endpoint `enqueue`: hanya jika status `approved`. Generate `job_id` & `execution_id` kandidat, ubah status request ke `pending_queue`, publish pesan ke RabbitMQ dengan OTel `traceparent`, catat audit log, dan langsung return `202 Accepted` `{job_id}` (< 200ms).
  3. **RabbitMQ Publisher (`internal/queue`)**:
     - Deklarasi topology durable (`migration.exchange`, `migration.jobs`, `migration.jobs.retry`, `migration.jobs.dlq`) + `OTel TextMapCarrier` untuk AMQP headers.
  4. **Distributed Lock Manager (`internal/lock`)**:
     - Implementasikan `Acquire(ctx, dbID, workerID, execID, ttl)`, `RenewHeartbeat(ctx, dbID, lockToken, ttl)`, `Release(ctx, dbID, lockToken)`, `HoldState(ctx, dbID, lockToken, holdState)`, dan `RecoverExpiredLock(ctx, dbID, newWorkerID, newExecID)` berbasis tabel `database_locks`.
  5. **Migration History (`Hide rollback`) & Job Status API**:
     - Implementasikan `GET /api/v1/databases/{id}/history?hide_rollback=true|false`: tampilkan seluruh attempt (`success`, `failed`, `partial`, `partially_reverted`, `uncertain`) beserta `executed_sql_snapshot`, `reverted_execution_id`, serta algoritma `Hide rollback` berbasis himpunan `HiddenIDs` (`execution_id` + `reverted_execution_id`).
     - Implementasikan `GET /api/v1/jobs/{job_id}` & `GET /api/v1/executions/{execution_id}` (mengembalikan status, jumlah batch & row committed, serta list `execution_batch_progress`).
     - Dokumentasikan seluruh kontrak baru di `docs/api-contract-m2.md`.
* **Kriteria Selesai (DoD)**:
  - Submit request otomatis menghasilkan daftar approval untuk semua grup `VIEW_ONLY` dan `VIEW_EDIT`; non-leader ditolak `403`.
  - Edit migration yang `is_immutable = true` ditolak `409`.
  - Pengajuan rollback non-tip atau irreversible ditolak `400/409`.
  - `POST /enqueue` langsung mengembalikan `202 Accepted` + `job_id` dan pesan masuk ke RabbitMQ dengan header `traceparent`.
* **Artefak Handoff**: Merge branch `bagian-3-backend-m2-core` ke `main` + `docs/api-contract-m2.md`.

---

#### BAGIAN 4 — Go Worker Service, Batch Engine, Crash/Uncertain Recovery, & Dataset Uji > 1 Detik
* **PIC Tunggal**: **Anggota 2** (Wildan Abdurrahman Ghazali — `13524054`)
* **Prasyarat**: Bagian 3 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `cmd/worker/main.go`
  - `internal/worker/` (`consumer.go`, `revalidator.go`, `executor_normal.go`, `executor_batch.go`, `recovery.go`, `target_meta.go`, `retry.go`)
  - `scripts/seed_large_dataset.sql`, `scripts/seed_large_dataset.go`, `docs/batch-benchmark.md`
* **Rincian Tugas Teknis**:
  1. **Go Worker Pool & Trace Context**:
     - Bangun `cmd/worker/main.go` yang mengonsumsi `migration.jobs` secara konkuren (configurable worker concurrency) dan melakukan graceful shutdown saat `SIGTERM`.
     - Ekstrak OTel trace context dari header AMQP agar span worker tersambung ke trace API → Queue → Worker → Lock → DB.
  2. **Lock Acquisition, Heartbeat, & Revalidation**:
     - Gunakan `internal/lock` untuk mengunci `database_id` target. Jika DB sedang dikunci oleh worker lain (`running`), kembalikan/requeue job dengan jeda backoff agar database lain tetap berjalan paralel sementara DB yang sama tetap serial. Jika lock berstatus `held_partial`/`held_uncertain` (dan job ini bukan `resume_batch`/`revert_batch` pasangannya), tandai request/attempt `failed` & ack.
     - Jalankan goroutine heartbeat perpanjangan lease tiap `10s` (TTL `30s`).
     - Jalankan **Revalidation** setelah lock didapat: cek idempotensi (`job_id` sudah terminal atau `_platform_meta.execution_proofs`), cek kelengkapan approval leader grup saat ini (`VIEW_ONLY` & `VIEW_EDIT`), cek precondition linear/LIFO. Jika gagal revalidasi, **tetap catat attempt `failed`** di `migration_executions`, tulis `log/slog`, lepas lock, dan ack pesan.
  3. **Mode Eksekusi Normal**:
     - Set `migration_definitions.is_immutable = TRUE` sebelum eksekusi.
     - Insert row `migration_executions` (`status = 'running'`, salinan `executed_sql_snapshot` `sql_up` atau `sql_down`).
     - Pastikan schema `_platform_meta` ada di Target DB, jalankan SQL + `INSERT INTO _platform_meta.execution_proofs` dalam 1 TX via **Instrumented SQL Wrapper**.
     - Update `migration_executions` & `migration_requests` (`success` / `failed` + `error_sqlstate`), lalu lepas lock.
  4. **Mode Eksekusi Batch Streaming, Resume, & Revert Terbalik**:
     - Tangkap batas awal (`MIN(cursor_col), MAX(cursor_col)`), lalu iterasi dengan keyset cursor (`WHERE col > $last AND col <= $max ORDER BY col ASC LIMIT $batch_size`) tanpa memuat seluruh dataset ke memori.
     - Tiap batch berjalan dalam 1 TX Target DB: simpan *before snapshot* ke `_platform_meta.batch_undo_logs` (jika reversible), jalankan SQL batch, simpan checkpoint ke `_platform_meta.batch_checkpoints`, lalu `COMMIT`. Sinkronkan progres ke `execution_batch_progress` & `migration_executions`.
     - Jika batch ke-$k$ gagal: rollback hanya TX batch ke-$k$; batch $1..(k-1)$ tetap committed; set status `partial` dan ubah lock menjadi `held_partial`.
     - Implementasikan **`resume_batch`** (lanjut dari `last_committed_cursor`) dan **`revert_batch`** (iterasi `_platform_meta.batch_checkpoints` dengan `ORDER BY batch_number DESC` dalam TX terpisah per batch menggunakan `batch_undo_logs` / `sql_down` immutable; jika gagal tengah jalan, set status `partially_reverted` dan pertahankan `held_partial`).
  5. **Retry Terbatas, Crash Recovery, & Uncertain Commit Verification**:
     - Retry error koneksi/transient dibatasi (misal max 3 kali via `migration.jobs.retry` dengan TTL eksponensial).
     - Jika koneksi putus saat `COMMIT` (`uncertain commit`) atau mendeteksi lock `running` kedaluwarsa dari worker yang crash: ubah ke `uncertain` / `held_uncertain` lalu verifikasi bukti di `_platform_meta.execution_proofs` atau `_platform_meta.batch_checkpoints` sebelum menentukan apakah `success`, `partial`, atau `failed`.
  6. **Dataset Pengujian Besar (> 1 Detik) & Benchmark**:
     - Buat `scripts/seed_large_dataset.go` / `.sql` untuk mengisi tabel uji di `target-postgres-1` hingga satu query `UPDATE` penuh tanpa batching membutuhkan waktu **> 1 detik** di lingkungan uji.
     - Uji dataset yang sama menggunakan mode per-batch dan dokumentasikan jumlah row, durasi non-batch (> 1s), durasi per-batch, serta jumlah batch/checkpoint di `docs/batch-benchmark.md`.
* **Kriteria Selesai (DoD)**:
  - Eksekusi job ke `target-postgres-1` dan `target-postgres-2` berjalan paralel, sementara 2 job ke `target-postgres-1` berjalan bergantian secara ketat.
  - Simulasi kegagalan batch ke-$k$ menghasilkan status `partial`, lock `held_partial`, dan sukses dilanjutkan oleh `resume_batch` maupun dibatalkan terbalik oleh `revert_batch`.
  - Restart container RabbitMQ/Worker di tengah eksekusi tidak menghilangkan job maupun checkpoint; skenario crash lock recovery & duplicate message idempotency terverifikasi.
  - `docs/batch-benchmark.md` terdokumentasi lengkap dengan angka dataset > 1 detik.
* **Artefak Handoff**: Merge branch `bagian-4-worker-batch-engine` ke `main` + `docs/batch-benchmark.md`.

---

#### BAGIAN 5 — Automated Backend Testing (Unit & Integration) + Sample Logs & Traces
* **PIC Tunggal**: **Anggota 1** (Dika Pramudya Nugraha — `13524132`)
* **Prasyarat**: Bagian 4 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `internal/lock/manager_test.go`, `internal/telemetry/redact_test.go`, `internal/migration/m2_unit_test.go`, `internal/worker/worker_unit_test.go`
  - `internal/migration/m2_integration_test.go`, `internal/worker/worker_integration_test.go`
  - `scripts/test-integration.ps1` (update agar menjalankan suite M1 + M2 dengan `compose.test.yaml`)
  - `docs/sample-logs-and-traces.md`
* **Rincian Tugas Teknis**:
  1. **Unit Tests (Mock/Stub)**:
     - Validasi definisi migration (`reversible` vs `irreversible` + alasan, `is_immutable` guard, validasi config batch).
     - State machine request & approval lintas grup (`VIEW_ONLY` & `VIEW_EDIT`).
     - Logika LIFO rollback, linear forward, attempt gagal tetap bisa di-forward/dilanjutkan, dan algoritma `Hide rollback` berbasis `execution_id` (kasus `E1..E5`).
     - Fungsi redaksi data sensitif (`log/slog` sanitizer) & logika backoff/lease expiration.
  2. **Integration Tests (Menggunakan DB Uji Nyata `compose.test.yaml` & RabbitMQ Uji — memanggil handler/service langsung)**:
     - **Alur Sukses**: create draft → submit → multi-group leader (`VIEW_ONLY` & `VIEW_EDIT`) approve → enqueue (`202` + `job_id`) → worker execute → history tercatat → rollback LIFO → forward linear → filter `Hide rollback` → paralelisme lintas 2 target DB vs serial 1 DB.
     - **Alur Kegagalan**: SQL syntax error (attempt `failed` tetap tercatat di `migration_executions`!), kegagalan batch tengah → `partial` → `resume_batch` → `success`, kegagalan `revert_batch` → `partially_reverted` → lanjut revert, lock crash recovery (`expires_at < NOW()`), dan duplicate delivery tidak menyebabkan *double commit*.
     - **Alur Penolakan Akses**: user grup `VIEW_ONLY` mencoba membuat migration (`403`), non-leader mencoba approve/reject (`403`), user tanpa akses mencoba melihat history/job (`403`), admin tanpa grant DB mencoba mengakses target DB (`403`), mencoba edit SQL migration `is_immutable = true` (`409`), mencoba rollback bukan Active Tip (`409`).
  3. **Dokumentasi Sample Output Log JSON & Span OTel**:
     - Jalankan skenario lengkap `API → RabbitMQ → Worker → Lock → Batch/SQL Target DB`, tangkap output aktual `stdout` dari API dan Worker, verifikasi keterkaitan `trace_id`/`span_id`, verifikasi pemisahan log `sql.start` / `sql.result` / `sql.tx_end` (termasuk query read-only), serta verifikasi penyamaran password/session/DSN, lalu simpan contohnya ke `docs/sample-logs-and-traces.md`.
* **Kriteria Selesai (DoD)**:
  - `go test -p 1 -count=1 ./...` dan `./scripts/test-integration.ps1` **PASS 100%** (unit + integration M1 & M2: sukses, gagal, penolakan akses).
  - `docs/sample-logs-and-traces.md` memuat bukti output log JSON `log/slog` dan trace/span OTel konsisten end-to-end.
* **Artefak Handoff**: Merge branch `bagian-5-automated-tests-and-samples` ke `main` + `docs/sample-logs-and-traces.md`.

---

#### BAGIAN 6 — Frontend SPA Tahap 2 (Seluruh Fitur Milestone 2 + Audit Responsif + Retire Legacy Templates)
* **PIC Tunggal**: **Anggota 2** (Wildan Abdurrahman Ghazali — `13524054`)
* **Prasyarat**: Bagian 5 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `frontend/src/pages/migrations/*`, `frontend/src/pages/approvals/*`, `frontend/src/pages/jobs/*`, `frontend/src/components/*`, `frontend/dist/`
  - Pembersihan route template lama di `web/templates/` (atau menonaktifkan rendering template HTML dari runtime server) dan update `cmd/server/main_test.go` agar memverifikasi SPA static serving menggantikan template M1.
* **Rincian Tugas Teknis**:
  1. **Halaman Migration Definitions (Create & Edit Draft)**:
     - Form `name`, `description`, `sql_up`, `sql_down` (editor SQL dengan internal scroll `overflow-x-auto` & `overflow-y-auto`), toggle `Irreversible` + input wajib `irreversible_reason`.
     - Pilihan mode `Normal` / `Batch` + input `batch_table`, `batch_cursor_column`, `batch_size`.
     - Tombol **Validate SQL / Dry-Run** dan indikator badge **Immutable (Terkunci)** bila sudah pernah dieksekusi (`is_immutable = true`).
  2. **Halaman Approval & Review Queue**:
     - Daftar change request (`up`, `rollback`, `forward`, `resume_batch`, `revert_batch`) beserta preview SQL aktual, target execution ID, dan tabel status approval per grup (`VIEW_ONLY` & `VIEW_EDIT`) beserta nama leader masing-masing.
     - Tombol `Submit Review`, `Approve`, `Reject` (+ komentar) yang aktif sesuai peran leader grup user yang login, serta tombol `Enqueue Execution` saat status `approved`.
  3. **Komponen Live Job & Batch Progress Monitor**:
     - Polling otomatis ke `GET /api/v1/jobs/:job_id` saat status `pending_queue` / `running`.
     - Visualisasi state machine (`Draft → Pending Review → Approved → Queue → Running → Terminal`), progress bar jumlah batch & row committed, indikator status `partial` / `partially_reverted` / `uncertain`, serta tombol cepat untuk mengajukan request `Resume Batch` atau `Revert Batch`.
  4. **Halaman Migration History**:
     - Tabel riwayat menampilkan seluruh attempt (sukses maupun gagal) lengkap dengan `migration_id`, `execution_id`, `version`/`revision`, `action_type`, pengguna, timestamp, status, jumlah batch/row, serta modal/drawer **Salinan SQL Aktual yang Dieksekusi** (`executed_sql_snapshot`).
     - Checkbox **`Hide rollback`** yang memanggil `?hide_rollback=true|false` dan menyembunyikan pasangan eksekusi dibatalkan + rollback suksesnya berdasarkan `execution_id` sambil tetap menampilkan attempt `failed`.
     - Tombol ajukan `Rollback` (aktif hanya pada Active Tip yang reversible) dan `Forward` (aktif pada kandidat linear berikutnya / attempt gagal).
  5. **Audit Responsivitas & Finalisasi SPA**:
     - Uji seluruh halaman (M1 + M2) di Desktop (`1440px`) dan Mobile (`375px`): pastikan tidak ada horizontal overflow di halaman, tombol/dialog terjangkau, dan seluruh alur minimum end-to-end berjalan 100% di SPA.
     - Build `frontend/dist` dan hapus ketergantungan Go HTML templates (`web/templates`).
* **Kriteria Selesai (DoD)**:
  - Alur minimum `Login → Access Control → Select DB → View Schema/Data → Create Migration → Validation & Review → Approval → Queue → Worker → Execute → History` dapat dioperasikan 100% di SPA tanpa page reload pada viewport desktop & mobile.
  - Kasus contoh spesifikasi `A[E1], B[E2], B(rollback E2)[E3], B(forward)[E4], C[E5]` + attempt `failed` terverifikasi akurat di UI saat checkbox `Hide rollback` dicentang/tidak dicentang.
  - **Seluruh Kebutuhan Wajib Milestone 2 Selesai 100%**.
* **Artefak Handoff**: Merge branch `bagian-6-spa-m2-complete` ke `main`.

---

#### BAGIAN 7 — Bonus Observability Part A (Prometheus Metrics & OTLP) + Bonus Security SAST (CodeQL)
* **PIC Tunggal**: **Anggota 1** (Dika Pramudya Nugraha — `13524132`)
* **Prasyarat**: Bagian 6 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `internal/telemetry/metrics.go`, `go.mod` / `go.sum` (dependensi `prometheus/client_golang`)
  - Wiring endpoint `/metrics` & OTLP exporter pada `cmd/server/main.go` dan `cmd/worker/main.go` (serta `internal/worker/`, `internal/api/`)
  - `.github/workflows/codeql.yml`, perbaikan kode atas temuan CodeQL (jika ada), `docs/security-codeql.md`
* **Rincian Tugas Teknis**:
  1. **Instrumentasi Prometheus Metrics (Label Ber-Kardinalitas Rendah)**:
     - **DILARANG** memakai label berkardinalitas tinggi (`user_id`, `job_id`, `execution_id`, atau query SQL). Hanya gunakan label terbatas seperti `status`, `action_type`, `mode`, `database`, `db_role`, `state`.
     - Metrik Queue/Migration:
       - `migration_jobs_total{status, action_type, mode}` (Counter)
       - `migration_jobs_inflight{status}` (Gauge: `waiting`, `running`)
       - `migration_job_retries_total{action_type}` (Counter)
       - `migration_queue_wait_seconds` (Histogram)
       - `migration_lock_wait_seconds` (Histogram)
       - `migration_execution_duration_seconds{mode, status}` (Histogram)
       - `migration_batches_processed_total{status}` & `migration_rows_processed_total` (Counter)
     - Metrik Database Connection Pool (`sql.DBStats()` periodik untuk Platform DB & Target DB pools):
       - `db_pool_connections{db_role, state}` (`in_use`/`active` & `idle`)
       - `db_pool_wait_count_total{db_role}` & `db_pool_wait_duration_seconds_total{db_role}`
     - Ekspos `GET /metrics` di Go API (`:8080/metrics`) dan Go Worker (`:9091/metrics`), serta pastikan `OTEL_EXPORTER_OTLP_ENDPOINT` aktif mengirim trace/log ke Collector.
  2. **Security SAST dengan CodeQL**:
     - Buat workflow `.github/workflows/codeql.yml` untuk memindai bahasa **Go** (API + Worker) dan **JavaScript/TypeScript** (Frontend SPA).
     - Jalankan CodeQL di GitHub Actions, kumpulkan bukti hasil scan (screenshot/export SARIF summary), lalu tulis laporan di `docs/security-codeql.md`:
       - Dokumentasikan **setiap** temuan: `rule`, `severity`, `lokasi kode`, `potensi dampak`, dan `keputusan` (`perbaiki` / `false positive` / `accepted risk`) beserta alasannya.
       - Perbaiki seluruh temuan yang perlu diperbaiki, catat **referensi hash commit perbaikan**, jalankan scan ulang, dan lampirkan bukti hasil rescan.
       - Jika 0 temuan, dokumentasikan cakupan direktori & bahasa yang dianalisis serta penjelasan bahwa hasil bersih SAST tidak menjamin aplikasi bebas dari seluruh kerentanan.
* **Kriteria Selesai (DoD)**:
  - `curl localhost:8080/metrics` dan `curl localhost:9091/metrics` menampilkan seluruh metrik queue/migration & DB pool dengan label rendah kardinalitas.
  - GitHub Actions CodeQL selesai bersih/tertangani dan `docs/security-codeql.md` lengkap (aturan, severity, lokasi, dampak, keputusan, commit perbaikan + rescan).
* **Artefak Handoff**: Merge branch `bagian-7-metrics-and-codeql` ke `main` + `docs/security-codeql.md`.

---

#### BAGIAN 8 — Bonus Observability Part B (OTel Collector, Backend Log/Trace, Prometheus, & Dashboard Grafana)
* **PIC Tunggal**: **Anggota 2** (Wildan Abdurrahman Ghazali — `13524054`)
* **Prasyarat**: Bagian 7 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `compose.observability.yaml` (atau tambahan profile di `compose.yaml` agar berjalan dengan 1 perintah)
  - `observability/` (`otel-collector-config.yaml`, `prometheus.yml`, `tempo.yaml`, `loki-config.yaml`, `grafana/provisioning/datasources/*.yaml`, `grafana/provisioning/dashboards/*.yaml`, `observability/grafana/dashboards/migration-platform.json`)
  - `docs/observability.md`
* **Rincian Tugas Teknis**:
  1. Konfigurasi stack observability agar dapat dijalankan ulang secara deterministik:
     - **OpenTelemetry Collector** menerima OTLP traces & logs dari Go API & Go Worker (dan log container/stdout via collector pipeline) lalu meneruskannya ke **Grafana Tempo** (traces) dan **Grafana Loki** (logs).
     - **Prometheus** melakukan scrape `/metrics` dari `backend-api:8080` dan `worker:9091`.
     - **Grafana** dengan **auto-provisioning** datasource (Prometheus, Tempo, Loki) dan **JSON Dashboard** (`migration-platform.json`).
  2. Susun panel Dashboard Grafana sesuai spesifikasi:
     - Jumlah job per status (`waiting`, `running`, `success`, `failed`, `partial`), total retry.
     - Waktu tunggu queue (`p50`/`p95`), waktu tunggu lock, durasi eksekusi migration (`p50`/`p95`), serta laju batch & row yang diproses.
     - Database connection pool: koneksi aktif (`in_use`) vs `idle` dan waktu tunggu koneksi (`wait_duration`).
  3. Konfigurasikan **Trace-to-Logs & Logs-to-Trace correlation** di datasource Grafana dan dokumentasikan contoh hasil korelasi (screenshot/JSON trace+log+metric untuk 1 eksekusi end-to-end) di `docs/observability.md`.
* **Kriteria Selesai (DoD)**:
  - Menjalankan `docker compose -f compose.yaml -f compose.observability.yaml up -d` langsung memunculkan Grafana beserta datasource & dashboard terisi data metrik, log, dan trace tanpa konfigurasi manual.
  - Korelasi `trace_id` antara Loki, Tempo, dan Prometheus terdokumentasi di `docs/observability.md`.
* **Artefak Handoff**: Merge branch `bagian-8-observability-stack` ke `main` + `docs/observability.md`.

---

#### BAGIAN 9 — Bonus Stress Testing dengan k6 Part A (Pembuatan Script & Eksekusi Pengujian)
* **PIC Tunggal**: **Anggota 1** (Dika Pramudya Nugraha — `13524132`)
* **Prasyarat**: Bagian 8 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `tests/k6/stress_test.js`, `tests/k6/setup_preapproved.sh` (atau `.ps1`), `docs/k6-raw-results.md`
* **Rincian Tugas Teknis**:
  1. Siapkan skrip `tests/k6/stress_test.js` dan fixture *pre-approved migrations* pada:
     - **Database yang sama** (`target-postgres-1`) untuk menguji antrean serial & lock wait time.
     - **Database yang berbeda** (`target-postgres-1` dan `target-postgres-2`) untuk menguji paralelisme lintas worker.
  2. Konfigurasikan tahapan beban bertahap (*stages*):
     - *Baseline / Normal* (misal 10 VUs selama 30s)
     - *Ramp-up di atas beban normal* (misal 30 VUs → 60 VUs → 100 VUs)
     - *Ramp-down / Recovery* (turun kembali ke 10 VUs → 0 VUs) untuk mengamati pemulihan sistem, pengosongan queue, dan stabilisasi connection pool.
  3. Dalam setiap iterasi VU:
     - Akses API read (`GET /databases`, `GET /schema`, `GET /history`) **dan** submit job migration yang sudah `approved` (`POST /enqueue`).
     - **Wajib polling `GET /api/v1/jobs/{job_id}` sampai status terminal** (`success`/`failed`); respons `202 Accepted` dari enqueue **TIDAK** dihitung sebagai keberhasilan eksekusi migration.
     - Catat custom metrics k6: `queue_wait_ms`, `job_completion_duration_ms`, `jobs_completed_total`, serta API `p50`/`p95`, throughput request, dan error rate.
  4. Tetapkan `thresholds` eksplisit di script k6 untuk `http_req_duration` (`p(95)`) dan `http_req_failed` (error rate).
  5. Jalankan stress test dan rekam seluruh hasil mentah per tingkat beban (summary k6 + pembacaan Prometheus/Grafana) ke `docs/k6-raw-results.md`.
* **Kriteria Selesai (DoD)**:
  - Skrip `k6 run tests/k6/stress_test.js` berjalan lengkap dari baseline → peak → recovery, menguji DB sama & berbeda, menunggu job selesai, dan menyimpan metrik lengkap per level beban di `docs/k6-raw-results.md`.
* **Artefak Handoff**: Merge branch `bagian-9-k6-execution` ke `main` + `docs/k6-raw-results.md`.

---

#### BAGIAN 10 — Bonus Analisis k6, Dokumentasi Final `README.md`, Verifikasi Bersih, & Pengumpulan
* **PIC Tunggal**: **Anggota 2** (Wildan Abdurrahman Ghazali — `13524054`)
* **Prasyarat**: Bagian 9 sudah di-merge ke `main`.
* **Lingkup File Eksklusif**:
  - `README.md`, tag rilis final Git, & pengumpulan ke form resmi asisten
* **Rincian Tugas Teknis**:
  1. Susun **README.md** utama yang komprehensif dan terstruktur, mencakup seluruh poin wajib dokumentasi Milestone 2:
     - Arsitektur sistem, skema database, dan penjelasan detail **mekanisme pemulihan lock saat worker gagal/crash** serta penanganan *uncertain commit*.
     - Panduan instalasi & menjalankan aplikasi (`docker compose up`), menjalankan `go test` / `test-integration.ps1`, dan membuka Dashboard Grafana.
     - Ringkasan & tautan hasil pengujian **Dataset Besar Batch** (> 1 detik non-batch vs per-batch dari `docs/batch-benchmark.md`).
     - Penjelasan singkat skrip k6, threshold `p95` & error rate, serta **tabel hasil + analisis mendalam di setiap tingkat beban** (baseline, puncak beban, dan fase pemulihan/ramp-down) berdasarkan `docs/k6-raw-results.md`.
     - Ringkasan bukti **Log JSON & Span OTel** (`docs/sample-logs-and-traces.md`), **Observability** (`docs/observability.md`), serta hasil **CodeQL SAST** (`docs/security-codeql.md`).
  2. Lakukan **Clean-Environment Dry Run** dari kondisi kosong (`docker compose down -v && docker compose -f compose.yaml -f compose.observability.yaml up -d --build`) dan pastikan seluruh test (`go test -p 1 -count=1 ./...` & `test-integration.ps1`) lulus.
  3. Audit akhir repo: pastikan `.env` tidak ter-commit, tidak ada kredensial/secret hardcode, lalu lakukan submit melalui form pengumpulan resmi asisten (**target Bonus Super Cepat <= 23 November 2026, 13:10 WIB**, atau maksimal **27 November 2026, 13:10 WIB**).
* **Kriteria Selesai (DoD)**:
  - `README.md` memuat 100% poin yang diminta spesifikasi Milestone 2.
  - Aplikasi + observability stack menyala bersih dari nol (`docker compose up`) dan seluruh alur wajib + 4 bonus terverifikasi.
  - Form pengumpulan resmi telah diisi dan dikonfirmasi sebelum batas waktu.

---

## Bagian 4: Matriks Checklist Verifikasi Akhir (1 Item = 1 Pemilik Tunggal)

> **Aturan**: Setiap item di bawah ini hanya dimiliki dan dipertanggungjawabkan oleh **1 orang PIC** (tidak ada item `A1 & A2`).

| Kategori | Item Verifikasi Wajib & Bonus | PIC Tunggal | Status |
|---|---|---|---|
| **Umum (API & DB)** | Fitur M1 (Auth, Session, Access Control, DB Mgmt, Linear) berjalan via REST JSON API; Backend & Worker 100% Go; Target DB PostgreSQL | **Anggota 1** | [ ] |
| **SPA & Responsif** | Seluruh halaman M1 (termasuk Login & Admin) & fitur baru M2 100% di SPA (`frontend/`), responsif Desktop & Mobile (internal scroll tabel/editor), tanpa Go HTML templates | **Anggota 2** | [ ] |
| **Testing** | Unit test (mock/stub) & Integration test (alur sukses, gagal, penolakan akses pada DB nyata) semua PASS 100% | **Anggota 1** | [ ] |
| **History & Rollback** | Definisi `sql_up`/`sql_down`/`irreversible` + `is_immutable`, aturan LIFO rollback & linear forward, serta seluruh attempt (sukses & gagal) + salinan SQL aktual tercatat append-only di `migration_executions` | **Anggota 1** | [ ] |
| **History (UI)** | Checkbox `Hide rollback` di SPA menyembunyikan pasangan eksekusi dibatalkan + rollback sukses berdasar `execution_id`, sambil tetap menampilkan attempt `failed` | **Anggota 2** | [ ] |
| **Approval Workflow** | State `Draft → Pending Review → Approved → Queue → Running → Terminal`; semua aksi (`up`, `rollback`, `forward`, `resume_batch`, `revert_batch`) wajib di-approve leader **semua** grup (`VIEW_ONLY` & `VIEW_EDIT`) | **Anggota 1** | [ ] |
| **Async Queue & Lock** | Setiap eksekusi di-publish ke RabbitMQ durable (`202` + `job_id`), dilindungi lease lock per `database_id`, tahan restart, dan tidak double commit pada pesan duplikat | **Anggota 1** | [ ] |
| **Worker & Batch Engine** | Worker pool paralel lintas DB, serial per DB, tahap Revalidation, mode normal (`execution_proofs`) & batch streaming cursor + checkpoint atomik, `partial`/`resume_batch`/`revert_batch` terbalik (`partially_reverted`), crash lock & uncertain recovery | **Anggota 2** | [ ] |
| **Dataset Uji > 1 Detik** | Dataset uji disiapkan hingga 1 query `UPDATE` non-batch > 1 detik, terdokumentasi jumlah row & perbandingan durasi batch vs non-batch (`docs/batch-benchmark.md`) | **Anggota 2** | [ ] |
| **Log & Tracing** | `log/slog` JSON ke stdout/stderr (termasuk SQL read-only; start/result/commit-rollback terpisah), data sensitif disamarkan, OTel trace context `API → Queue → Worker → Lock → DB`, plus contoh output (`docs/sample-logs-and-traces.md`) | **Anggota 1** | [ ] |
| **Bonus 2 (A): Metrics & OTLP** | Endpoint `/metrics` Prometheus di Go API & Go Worker (metrik queue/migration + DB pool) dengan label kardinalitas rendah & OTLP exporter aktif | **Anggota 1** | [ ] |
| **Bonus 2 (B): Observability Stack** | OTel Collector + Tempo + Loki + Prometheus + Grafana auto-provisioned JSON dashboard & bukti korelasi log/trace/metrics (`docs/observability.md`) | **Anggota 2** | [ ] |
| **Bonus 3 (A): k6 Execution** | Skrip k6 menguji beban naik & turun, DB sama & berbeda, pakai migration approved, polling hingga job selesai, threshold `p95` & error rate (`tests/k6/stress_test.js`) | **Anggota 1** | [ ] |
| **Bonus 3 (B) & README** | Analisis hasil k6 di setiap tingkat beban, penjelasan pemulihan lock saat worker gagal, dan dokumentasi lengkap di `README.md` | **Anggota 2** | [ ] |
| **Bonus 4: CodeQL SAST** | CodeQL workflow aktif (Go & JS/TS) + bukti scan + analisis tiap temuan + commit perbaikan + hasil rescan (`docs/security-codeql.md`) | **Anggota 1** | [ ] |
| **Bonus 1: Pengumpulan** | Clean-environment dry run & pengumpulan resmi ke form asisten sebelum **23 November 2026, 13:10 WIB** (maksimal 27 Nov 2026, 13:10 WIB) | **Anggota 2** | [ ] |
