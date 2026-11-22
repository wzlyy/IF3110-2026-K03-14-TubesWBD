# Stress Testing dengan k6 — Konfigurasi & Data Mentah Hasil Pengujian
## Bonus 3 Part A (PIC: Anggota 1 — Dika Pramudya Nugraha / `13524132`)

---

## 1. Desain Skenario (`tests/k6/stress_test.js`)

Skrip stress testing [`tests/k6/stress_test.js`](../tests/k6/stress_test.js) menguji sistem pada beban normal, beban meningkat di atas normal (*stress/peak*), hingga penurunan beban (*ramp-down/recovery*):

| Tahap (Stage) | Durasi | Target VUs | Tujuan Pengamatan |
|---|---|---|---|
| **Stage 1 (Baseline Normal)** | 30s | 10 VUs | Mengukur latency dasar API, throughput normal, dan queue wait rendah. |
| **Stage 2 (Ramp-Up 1)** | 30s | 30 VUs | Mulai menguji antrean serial pada DB sama & paralelisme pada DB berbeda. |
| **Stage 3 (High Stress)** | 30s | 60 VUs | Menguji tekanan connection pool PostgreSQL dan penumpukan queue. |
| **Stage 4 (Peak Stress)** | 30s | 100 VUs | Mengamati perilaku sistem di atas beban normal (p95, lock wait, error rate). |
| **Stage 5 (Ramp-Down Recovery)** | 30s | 10 VUs | Mengamati pemulihan latency API, pengosongan antrean, & kestabilan pool koneksi. |
| **Stage 6 (Cool-Down)** | 20s | 0 VUs | Memastikan seluruh in-flight job selesai tanpa lock tertinggal. |

### Skenario & Aturan Pengukuran
1. **Akses API Konkuren**: setiap VU memanggil `GET /api/v1/databases`, `GET /api/v1/databases/{id}/schema`, dan `GET /api/v1/databases/{id}/history?hide_rollback=false`.
2. **Pengiriman Job Migration Pre-Approved (DB Sama & DB Berbeda)**:
   - VU genap/ganjil memanggil `POST /api/v1/requests/{req_id}/enqueue` memakai daftar request yang **sudah di-approve** oleh seluruh leader grup (`tests/k6/setup_preapproved.ps1`) pada `target-postgres-1` (`scenario_type=same_db`) dan `target-postgres-2` (`scenario_type=diff_db`).
   - **Penting**: Respons HTTP `202 Accepted` dari endpoint `/enqueue` **tidak dihitung** sebagai keberhasilan eksekusi migration. Skrip melakukan polling `GET /api/v1/jobs/{job_id}` hingga status terminal (`success` atau `failed`/`partial`/`uncertain`) dan mencatat metrik kustom:
     - `migration_queue_wait_ms` (selisih `execution.started_at - enqueued_at`)
     - `migration_job_completion_duration_ms` (total waktu enqueue sampai terminal)
     - `migration_jobs_completed_total`, `migration_jobs_failed_total`, `migration_job_success_rate`
3. **Threshold k6**:
   - `http_req_duration`: `p(95) < 500ms`
   - `http_req_failed`: `rate < 0.02` (< 2%)
   - `migration_job_success_rate`: `rate > 0.95`

---

## 2. Panduan Eksekusi (Setelah Worker Bagian 4 & Observability Bagian 8 Aktif)

```powershell
# 1. Jalankan seluruh layanan (DB, RabbitMQ, API, Worker, dan Stack Observability)
docker compose -f compose.yaml -f compose.observability.yaml --profile app up -d --wait

# 2. Seed pengguna, grup + leader, dan target PostgreSQL 1 & 2
go run ./cmd/seed

# 3. Siapkan pre-approved migration requests pada target-postgres-1 dan target-postgres-2
.\tests\k6\setup_preapproved.ps1 -CountPerDB 15

# 4. Jalankan k6 menggunakan env vars yang dicetak oleh langkah 3
k6 run tests/k6/stress_test.js
```

Hasil ringkasan JSON otomatis tersimpan di `tests/k6/summary.json`.

---

## 3. Tabel Perekaman Data Mentah (Untuk Dianalisis di `README.md` pada Bagian 10)

| Tingkat Beban | API p50 (ms) | API p95 (ms) | Request Throughput (req/s) | Error Rate (%) | Queue Wait p50/p95 (ms) | Job Completion Duration p50/p95 (ms) | Job Throughput (jobs/min) | Catatan Pool & Recovery (Grafana) |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| Baseline (10 VUs) | *(isi hasil run)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* |
| Ramp-Up (30 VUs) | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* |
| High Stress (60 VUs) | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* |
| Peak Stress (100 VUs) | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* |
| Ramp-Down Recovery (10 → 0 VUs) | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* | *(isi)* |

### Evaluasi Threshold
- `http_req_duration p(95) < 500ms`: *(PASS / FAIL — isi dari `tests/k6/summary.json`)*
- `http_req_failed rate < 0.02`: *(PASS / FAIL — isi dari `tests/k6/summary.json`)*
- `migration_job_success_rate > 0.95`: *(PASS / FAIL — isi dari `tests/k6/summary.json`)*
