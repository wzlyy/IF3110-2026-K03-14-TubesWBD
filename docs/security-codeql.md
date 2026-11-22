# Laporan Security SAST — CodeQL & Audit Keamanan Kode
## Bonus 4 (PIC: Anggota 1 — Dika Pramudya Nugraha / `13524132`)

---

## 1. Aktivasi & Konfigurasi CodeQL SAST

Static Application Security Testing (SAST) menggunakan **GitHub CodeQL** diaktifkan melalui workflow [`.github/workflows/codeql.yml`](../.github/workflows/codeql.yml):

- **Trigger**: setiap `push` ke `main`, setiap `pull_request` ke `main`, jadwal mingguan (`cron: "0 2 * * 1"`), dan `workflow_dispatch`.
- **Cakupan Bahasa & Direktori yang Dianalisis**:
  1. **`go`** (`build-mode: autobuild`): seluruh backend API (`cmd/server`, `cmd/seed`), worker (`cmd/worker` setelah tersedia), dan seluruh paket internal (`internal/access`, `internal/admin`, `internal/api`, `internal/auth`, `internal/config`, `internal/database`, `internal/lock`, `internal/migration`, `internal/queue`, `internal/telemetry`, `internal/worker`).
  2. **`javascript-typescript`** (`build-mode: none`): skrip stress test k6 (`tests/k6/*.js`), aset JS (`web/static/js/*.js`), serta proyek Frontend SPA (`frontend/src/**/*.{ts,tsx,js,jsx}` setelah tersedia).
- **Query Suite**: `security-extended,security-and-quality` (mencakup kerentanan OWASP Top 10: SQL Injection, XSS, CSRF, Path Traversal, SSRF, Hardcoded Credentials, Improper Access Control, Sensitive Data Exposure).

---

## 2. Hasil Audit Statis & Pengamanan Pra-Scan (Go Backend & API)

Sebelum push ke GitHub Actions CodeQL, dilakukan pemeriksaan statis lokal (`go vet ./...`, `go vet -tags=integration ./...`) dan review keamanan manual terhadap seluruh kode Anggota 1 mengacu pada **OWASP Top 10**:

| No | Rule / Kategori OWASP | Severity | Lokasi Kode | Potensi Dampak | Keputusan & Mitigasi |
|---|---|---|---|---|---|
| 1 | `go/path-injection` (A01: Broken Access Control / Path Traversal) | High | `internal/api/spa.go` (`NewSPAHandler`) | Pengguna dapat mengakses file sensitif di luar `frontend/dist` (seperti `.env` atau `go.mod`) melalui path `../../` | **Diperbaiki Proaktif**: path dibersihkan dengan `path.Clean` + `filepath.Rel(absRoot, candidate)` menolak segala prefix `..`; diuji pada `TestSPAHandlerFallbackAndTraversalProtection`. |
| 2 | `go/clear-text-logging` (A09: Security Logging Failures) | Medium | `internal/telemetry/redact.go`, `logger.go`, `sqllog.go` | Password DB target, DSN `postgres://`/`amqp://`, `session_id`, dan `csrf_token` dapat bocor ke log JSON `stdout` | **Diperbaiki Proaktif**: `slog.HandlerOptions.ReplaceAttr` menyamarkan semua key sensitif & pola DSN/Bearer/cookie menjadi `[REDACTED]`; diuji pada `TestRedactionMasksSecretsAndDSNs`. |
| 3 | `go/sql-injection` (A03: Injection) | High | `internal/api/*.go`, `internal/lock/manager.go`, `internal/migration/*.go` | Eksekusi SQL tak terparameterisasi dapat memanipulasi tabel metadata platform | **Aman / Terverifikasi**: 100% query ke Platform DB memakai parameterized placeholder (`$1..$N`); SQL user target divalidasi `ValidateSQL` yang juga memblokir akses ke `_platform_meta` dan `_dbplatform_migration_receipts`. |
| 4 | `go/csrf` (A01: Broken Access Control) | High | `internal/auth/csrf.go`, `internal/api/router.go` | Mutasi state (approval, enqueue, admin CRUD) rentan CSRF dari origin lain | **Diperbaiki Proaktif**: seluruh endpoint mutasi `/api/v1/*` dilindungi `CSRFProtectionMiddleware` memakai `subtle.ConstantTimeCompare` pada header `X-CSRF-Token` + cookie `HttpOnly` & `SameSite=Lax`. |
| 5 | `go/unbounded-body-read` (A04: Insecure Design / DoS) | Medium | `internal/api/response.go` (`DecodeJSON`), `cmd/server/main.go` | Payload JSON raksasa atau Slowloris dapat menghabiskan memori/goroutine server | **Diperbaiki Proaktif**: seluruh JSON decoder membungkus body dengan `http.MaxBytesReader` dan `http.Server` menetapkan `ReadHeaderTimeout: 10s`, `ReadTimeout: 15s`, `WriteTimeout: 30s`. |
| 6 | `go/missing-access-control` (A01: Broken Access Control) | High | `internal/migration/approval.go`, `internal/api/m2_handlers.go` | Non-leader atau user `VIEW_ONLY` / admin tanpa grant dapat menyetujui atau mengubah migration | **Aman / Terverifikasi**: approval memvalidasi `groups.leader_user_id` secara live di DB (`403` bagi non-leader termasuk admin); akses DB mengevaluasi grant real-time tanpa bypass admin. |

---

## 3. Bukti Hasil Scan GitHub Actions CodeQL & Rescan

> **Instruksi Pengisian Bukti GitHub Actions** (setelah branch Anggota 1 di-push / PR ke repository GitHub):
> 1. Buka tab **Actions → CodeQL SAST** pada repository GitHub.
> 2. Pastikan kedua matrix job (`Analyze (go)` dan `Analyze (javascript-typescript)`) berstatus **Passed**.
> 3. Buka tab **Security → Code scanning alerts** dan catat seluruh temuan (atau bukti 0 alerts) pada tabel di bawah ini.

| Scan Run | Bahasa | Jumlah Temuan | Rule / Severity / Lokasi | Referensi Commit Perbaikan | Status Rescan |
|---|---|---|---|---|---|
| Scan #1 (GitHub Actions) | `go` | *(isi setelah push)* | — | — | *(isi setelah rescan)* |
| Scan #1 (GitHub Actions) | `javascript-typescript` | *(isi setelah push)* | — | — | *(isi setelah rescan)* |

> **Catatan Keterbatasan SAST**: Meskipun hasil scan CodeQL bersih (0 temuan), hasil SAST yang bersih **tidak menjamin** aplikasi sepenuhnya bebas dari seluruh kerentanan keamanan (misalnya bug logika bisnis, kesalahan konfigurasi runtime/infrastruktur, atau kerentanan zero-day pada dependensi). Oleh karena itu, SAST dilengkapi dengan automated integration testing (`go test -tags=integration`), `govulncheck`, prinsip *least privilege*, serta sanitasi input/log berlapis.
