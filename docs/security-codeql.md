# Laporan Analisis Keamanan CodeQL (SAST)

Dokumen ini mencatat konfigurasi, temuan awal, perbaikan, hasil pemindaian ulang (*rescan*), serta batasan analisis statis (*Static Application Security Testing* / SAST) menggunakan **GitHub CodeQL** pada repositori ini.

## 1. Konfigurasi dan Cakupan Analisis

Workflow CodeQL didefinisikan pada [.github/workflows/codeql.yml](../.github/workflows/codeql.yml) menggunakan `github/codeql-action@v3` dengan paket query **`security-extended`** dan **`security-and-quality`**. Analisis berjalan otomatis pada setiap `push` dan `pull_request` ke branch `main`, serta terjadwal mingguan.

| Matriks Bahasa | Build Mode | Cakupan Berkas yang Dianalisis |
|---|---|---|
| `go` | `autobuild` | Seluruh kode sumber Go pada `cmd/server`, `cmd/worker`, `cmd/seed`, serta seluruh paket `internal/*` (`access`, `admin`, `api`, `auth`, `config`, `database`, `lock`, `migration`, `queue`, `telemetry`, `worker`). |
| `javascript-typescript` | `none` | Seluruh kode SPA React/TypeScript pada `frontend/src/*` serta skrip uji beban k6 pada `tests/k6/stress_test.js`. |

## 2. Temuan Awal dan Keputusan Perbaikan

Pada pemindaian awal implementasi backend Milestone 2 (commit `64ef0d0`), CodeQL mendeteksi satu temuan keamanan pada pipeline pencatatan log terstruktur Go:

| Atribut | Detail Temuan |
|---|---|
| **Rule ID** | `go/log-injection` (*Log entries created from user input*, CWE-117) |
| **Severity** | High (Security Severity `7.8` pada query `security-extended`) |
| **Lokasi** | [`internal/telemetry/logger.go`](../internal/telemetry/logger.go) (`LogEvent`) dan [`internal/telemetry/redact.go`](../internal/telemetry/redact.go) (`SanitizeLogString`, `RedactParams`) |
| **Sumber & Aliran Taint** | Input permintaan HTTP (misalnya header `X-Correlation-ID`, metadata `actor`/`database`, dan atribut dinamis `extra ...slog.Attr`) diteruskan ke `Logger().LogAttrs(...)`. Sebelumnya, `SanitizeLogString` mengganti `\n` menjadi spasi tanpa menghapus karakter kontrol secara konsisten, cabang `default` pada `RedactParams` belum menyanitasi hasil `fmt.Sprint(v)`, dan objek `slog.Attr` diteruskan langsung tanpa rekonstruksi per tipe nilai. |
| **Dampak** | Jika baris log dikirim atau diproses oleh agregator berbasis teks baris tanpa escaping JSON, karakter CR/LF (`\r`, `\n`) dari input eksternal dapat menyuntikkan entri log palsu (*log forging/injection*) dan merusak keutuhan audit trail. |
| **Keputusan** | **Diperbaiki (*Fixed / Remediated*)** — tidak diabaikan dan tidak di-*dismiss*. |
| **Commit Perbaikan** | [`6ede7c2`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/commit/6ede7c20fa760f8595a3138260f70217164b722b) (*"fix(security): rekonstruksi atribut slog agar bebas taint log-injection CodeQL"*) |

### Ringkasan Perubahan Perbaikan (`6ede7c2`)

1. **`internal/telemetry/redact.go`**:
   - `SanitizeLogString` menghapus seluruh karakter `\n` dan `\r` dengan string kosong (`""`).
   - `RedactParams` menyanitasi tipe pada cabang `default` dengan `SanitizeLogString(RedactString(fmt.Sprint(v)))`.
2. **`internal/telemetry/logger.go`**:
   - Seluruh field inti (`event`, `actor`, `database`, `status`, `correlation_id`, `trace_id`, `span_id`) dan metadata job (`migration_id`, `job_id`, `execution_id`) dibersihkan dari `\n` dan `\r`.
   - Setiap elemen `extra ...slog.Attr` **direkonstruksi ulang** berdasarkan `attr.Value.Kind()` (`KindString`, `KindInt64`, `KindUint64`, `KindFloat64`, `KindBool`, `KindDuration`, `KindTime`, atau fallback tersanitasi) serta tetap menerapkan `IsSensitiveKey(key)` → `[REDACTED]`, sehingga tidak ada `slog.Attr` mentah yang membawa taint ke `Logger().LogAttrs`.
3. **Verifikasi regresi**: Unit test `TestRedactionMasksSecretsAndDSNs` pada [`internal/telemetry/telemetry_test.go`](../internal/telemetry/telemetry_test.go) memverifikasi penghapusan `\r`/`\n` dan penyamaran secret DSN/Bearer/cookie/parameter.

## 3. Rekam Jejak Pemindaian Ulang (*Rescan*) Bersih

| Commit | Konteks | Run ID | Status CodeQL (Go + JS/TS) |
|---|---|---|---|
| `6ede7c2` | Branch `fix/m2-codeql-log-sanitization` setelah perbaikan log injection | [`45046275169`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45046275169) | **Success (0 alert)** |
| `8df59cd` | Merge PR #8 ke `main` (backend M2) | [`45047490984`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45047490984) | **Success (0 alert)** |
| `7ea5343` | Commit terbaru `main` (PR #9: backend + worker + SPA React/TS + observabilitas) | [`45200770227`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45200770227) | **Success (0 alert)** |

> **Catatan workspace lokal:** Hasil bersih pada run `45200770227` berlaku untuk commit `7ea5343` di `main`. Perubahan lokal setelah commit tersebut perlu di-push ke GitHub agar memperoleh status rescan GitHub Actions tersendiri.

## 4. Batasan Analisis Statis (SAST)

Hasil pemindaian CodeQL yang bersih (**0 alert** pada kode Go dan JavaScript/TypeScript) **tidak menjamin aplikasi bebas dari seluruh celah keamanan**. Keterbatasan mendasar SAST mencakup:

1. **Cacat logika bisnis dan otorisasi dinamis:** SAST tidak memverifikasi kebenaran semantik aturan kuorum persetujuan leader lintas grup, aturan linearitas forward/LIFO rollback, ataupun penolakan eksekusi ketika grant berubah setelah persetujuan. Kontrol ini diverifikasi melalui *integration test* pada [`internal/migration/m2_integration_test.go`](../internal/migration/m2_integration_test.go).
2. **Kondisi pacu (*race condition*) dan ketahanan *distributed lock*:** SAST statis tidak menyimulasikan interleaving antar worker, kedaluwarsa lease, maupun crash saat transaksi target. Perilaku tersebut diuji dengan skenario konkurensi dan pemulihan pada [`internal/worker/worker_integration_test.go`](../internal/worker/worker_integration_test.go).
3. **Kesalahan konfigurasi lingkungan (*deployment misconfiguration*):** Keamanan produksi tetap bergantung pada manajemen rahasia yang benar (`ENCRYPTION_KEY` 32-byte yang privat, `ADMIN_INITIAL_PASSWORD` yang kuat, `COOKIE_SECURE=true` di balik HTTPS, isolasi port database target/RabbitMQ, dan tidak membuka port Collector/Grafana/Prometheus secara publik).
4. **Kerentanan dependensi pihak ketiga dan perilaku runtime DBMS:** CodeQL berfokus pada aliran kode sumber aplikasi; kerentanan paket pihak ketiga dipantau terpisah oleh `govulncheck` di [`.github/workflows/quality.yml`](../.github/workflows/quality.yml), sedangkan validasi SQL tetap dibatasi oleh hak akses akun database target PostgreSQL.
