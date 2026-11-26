# Laporan Analisis Keamanan CodeQL (SAST)

Dokumen ini mencatat konfigurasi, temuan awal, perbaikan, hasil pemindaian ulang (*rescan*), serta batasan analisis statis (*Static Application Security Testing* / SAST) menggunakan **GitHub CodeQL** pada repositori ini.

## 1. Konfigurasi dan Cakupan Analisis

Workflow CodeQL didefinisikan pada [.github/workflows/codeql.yml](../.github/workflows/codeql.yml) menggunakan `github/codeql-action@v3` dengan paket query **`security-extended`** dan **`security-and-quality`**. Analisis berjalan otomatis pada setiap `push` dan `pull_request` ke branch `main`, serta terjadwal mingguan.

| Matriks Bahasa | Build Mode | Cakupan Berkas yang Dianalisis |
|---|---|---|
| `go` | `autobuild` | Seluruh kode sumber Go pada `cmd/server`, `cmd/worker`, `cmd/seed`, serta seluruh paket `internal/*` (`access`, `admin`, `api`, `auth`, `config`, `database`, `lock`, `migration`, `queue`, `telemetry`, `worker`). |
| `javascript-typescript` | `none` | Seluruh kode SPA React/TypeScript pada `frontend/src/*` serta skrip uji beban k6 pada `tests/k6/stress_test.js`. |

## 2. Temuan CodeQL dan Keputusan Perbaikan

Sepanjang pengembangan Milestone 2, GitHub CodeQL mendeteksi tiga temuan (satu saat implementasi awal backend, serta dua alert pada tab *Code scanning* di `main` pasca-merge PR #9 dan PR #11):

### 2.1 Temuan 1 — `go/log-injection` (Commit awal `64ef0d0`)

| Atribut | Detail Temuan |
|---|---|
| **Rule ID** | `go/log-injection` (*Log entries created from user input*, CWE-117) |
| **Severity** | High (Security Severity `7.8` pada query `security-extended`) |
| **Lokasi** | [`internal/telemetry/logger.go`](../internal/telemetry/logger.go) (`LogEvent`) dan [`internal/telemetry/redact.go`](../internal/telemetry/redact.go) (`SanitizeLogString`, `RedactParams`) |
| **Sumber & Aliran Taint** | Input permintaan HTTP (misalnya header `X-Correlation-ID`, metadata `actor`/`database`, dan atribut dinamis `extra ...slog.Attr`) diteruskan ke `Logger().LogAttrs(...)`. Sebelumnya, `SanitizeLogString` mengganti `\n` menjadi spasi tanpa menghapus karakter kontrol secara konsisten, cabang `default` pada `RedactParams` belum menyanitasi hasil `fmt.Sprint(v)`, dan objek `slog.Attr` diteruskan langsung tanpa rekonstruksi per tipe nilai. |
| **Dampak** | Jika baris log dikirim atau diproses oleh agregator berbasis teks baris tanpa escaping JSON, karakter CR/LF (`\r`, `\n`) dari input eksternal dapat menyuntikkan entri log palsu (*log forging/injection*) dan merusak keutuhan audit trail. |
| **Keputusan** | **Diperbaiki (*Fixed / Remediated*)** — tidak diabaikan dan tidak di-*dismiss*. |
| **Commit Perbaikan** | [`6ede7c2`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/commit/6ede7c20fa760f8595a3138260f70217164b722b) (*"fix(security): rekonstruksi atribut slog agar bebas taint log-injection CodeQL"*) |

Ringkasan perbaikan (`6ede7c2`):
1. **`internal/telemetry/redact.go`**: `SanitizeLogString` menghapus seluruh karakter `\n` dan `\r` dengan string kosong (`""`), dan `RedactParams` menyanitasi cabang `default` dengan `SanitizeLogString(RedactString(fmt.Sprint(v)))`.
2. **`internal/telemetry/logger.go`**: Seluruh field inti dan metadata job dibersihkan dari `\n` dan `\r`, serta setiap elemen `extra ...slog.Attr` **direkonstruksi ulang** berdasarkan `attr.Value.Kind()` dengan tetap menerapkan `IsSensitiveKey(key)` → `[REDACTED]`.
3. **Verifikasi regresi**: Unit test `TestRedactionMasksSecretsAndDSNs` pada [`internal/telemetry/telemetry_test.go`](../internal/telemetry/telemetry_test.go) memverifikasi penghapusan `\r`/`\n` dan penyamaran secret.

### 2.2 Temuan 2 — Alert #9 `go/sql-injection` (`7ea5343` / PR #9)

| Atribut | Detail Temuan |
|---|---|
| **Rule ID** | `go/sql-injection` (*Database query built from user-controlled sources*, CWE-89) |
| **Severity** | High / Error |
| **Lokasi** | [`internal/telemetry/sqllog.go:147`](../internal/telemetry/sqllog.go#L147) (`(*InstrumentedTx).ExecContext`, dipanggil dari [`internal/worker/executor_normal.go:20`](../internal/worker/executor_normal.go#L20)) |
| **Sumber & Aliran Taint** | CodeQL menelusuri input HTTP (`DecodeJSON` di [`internal/api/m2_handlers.go:140`](../internal/api/m2_handlers.go#L140) melalui `ValidateDefinitionInput`, serta `r.PostForm.Get` di [`internal/migration/editor_handlers.go:94,118`](../internal/migration/editor_handlers.go#L94)) ke `migration.ValidateSQL`. Di dalam `ValidateSQL`, closure bersarang `appendStatement` menangkap variabel `query` dan `statements` (*SSA implicit def*), sehingga menghilangkan *1-level call-context sensitivity* dan membuat kembalian `ValidateSQL` di [`internal/worker/executor_normal.go:10`](../internal/worker/executor_normal.go#L10) dianggap tercemar hingga ke `t.Tx.ExecContext` (`sqllog.go:147`). |
| **Dampak** | Secara fungsional worker hanya mengeksekusi *snapshot* SQL migrasi yang telah disetujui seluruh *group leader* dan bersifat *immutable* di tabel `migration_definitions` (dengan blokir DML/control transaksi dan skema internal `_platform_meta`). Namun jika suatu pemanggil tidak melalui validasi dan approval, eksekusi statement SQL mentah berisiko mengubah atau merusak data pada database target. |
| **Keputusan** | **Diperbaiki (*Fixed / Remediated*)** — menghilangkan kebocoran taint lintas pemanggil pada `ValidateSQL`. |
| **Perbaikan** | Menghapus closure `appendStatement` di [`internal/migration/sql.go`](../internal/migration/sql.go) dengan menggantinya menjadi lexer berbasis offset bilangan bulat `lexSQL` (mengembalikan indeks awal/akhir `[]int` dan konstanta kata kunci dari `canonicalSQLKind`), serta memisahkan entry point worker `SplitPostgresStatements` untuk [`internal/worker/executor_normal.go`](../internal/worker/executor_normal.go). |
| **Commit Perbaikan** | [`9fce00a`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/commit/9fce00a) (*"fix(codeql): fix issue #9 and # 10"*) |
| **Hasil Rescan** | [`45833937684`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45833937684) (`CodeQL SAST #14`) — **Success / Alert #9 tertutup (0 open alert)** |

### 2.3 Temuan 3 — Alert #10 `js/unused-local-variable` (`1311887` / PR #11)

| Atribut | Detail Temuan |
|---|---|
| **Rule ID** | `js/unused-local-variable` (*Unused variable, import, function or class*) |
| **Severity** | Note |
| **Lokasi** | [`frontend/src/pages/approvals/Review.tsx:44`](../frontend/src/pages/approvals/Review.tsx#L44) |
| **Sumber & Aliran** | Variabel destrukturisasi `error` pada `const { data: r, error, refresh } = useResource<Change>(path);` tertinggal tidak terpakai setelah `<Feedback>` dihapus pada konsolidasi hook di commit `8c595b4`. |
| **Dampak** | Tidak berdampak keamanan langsung, namun menurunkan kebersihan kode dan dapat menutupi kesalahan penanganan error lokal bila nanti ada refaktorisasi. |
| **Keputusan** | **Diperbaiki (*Fixed / Remediated*)** |
| **Perbaikan** | Menghapus binding `error` yang tidak dipakai menjadi `const { data: r, refresh } = useResource<Change>(path);` di [`frontend/src/pages/approvals/Review.tsx`](../frontend/src/pages/approvals/Review.tsx#L44) dan memverifikasi dengan `tsc --noUnusedLocals --noUnusedParameters`. |
| **Commit Perbaikan** | [`9fce00a`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/commit/9fce00a) (*"fix(codeql): fix issue #9 and # 10"*) |
| **Hasil Rescan** | [`45833937684`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45833937684) (`CodeQL SAST #14`) — **Success / Alert #10 tertutup (0 open alert)** |

## 3. Rekam Jejak Pemindaian (*Scan* & *Rescan*)

| Commit / Tahap | Konteks | Run ID | Hasil Scan |
|---|---|---|---|
| `6ede7c2` | Branch `fix/m2-codeql-log-sanitization` setelah perbaikan `go/log-injection` | [`45046275169`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45046275169) | **Success (0 alert)** |
| `8df59cd` | Merge PR #8 ke `main` (backend M2) | [`45047490984`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45047490984) | **Success (0 alert)** |
| `7ea5343` | Merge PR #9 ke `main` (menambah worker/SPA) | [`45200770227`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45200770227) | Workflow sukses; memunculkan **Alert #9** (`go/sql-injection`) |
| `1311887` | Merge PR #11 ke `main` (CodeQL SAST #13) | [`45824302384`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45824302384) | Workflow sukses; **Alert #9** (Go) & **Alert #10** (JS/TS) berstatus Open |
| [`9fce00a`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/commit/9fce00a) | Push perbaikan Alert #9 (`sql.go`, `executor_normal.go`) & Alert #10 (`Review.tsx`) ke `main` (`CodeQL SAST #14`) | [`45833937684`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45833937684) | **Success — 0 open alert; Alert #9 & #10 tertutup** (Go & JS/TS) |

Verifikasi CI tambahan pada `9fce00a`: workflow *Quality checks #32* (Go `vet`/test, TypeScript check, `vite build`, dan `govulncheck`) turut selesai dengan status **Success** pada [Run `45833937735`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45833937735).

## 4. Batasan Analisis Statis (SAST)

Hasil pemindaian CodeQL yang bersih (**0 alert** pada kode Go dan JavaScript/TypeScript) **tidak menjamin aplikasi bebas dari seluruh celah keamanan**. Keterbatasan mendasar SAST mencakup:

1. **Cacat logika bisnis dan otorisasi dinamis:** SAST tidak memverifikasi kebenaran semantik aturan kuorum persetujuan leader lintas grup, aturan linearitas forward/LIFO rollback, ataupun penolakan eksekusi ketika grant berubah setelah persetujuan. Kontrol ini diverifikasi melalui *integration test* pada [`internal/migration/m2_integration_test.go`](../internal/migration/m2_integration_test.go).
2. **Kondisi pacu (*race condition*) dan ketahanan *distributed lock*:** SAST statis tidak menyimulasikan interleaving antar worker, kedaluwarsa lease, maupun crash saat transaksi target. Perilaku tersebut diuji dengan skenario konkurensi dan pemulihan pada [`internal/worker/worker_integration_test.go`](../internal/worker/worker_integration_test.go).
3. **Kesalahan konfigurasi lingkungan (*deployment misconfiguration*):** Keamanan produksi tetap bergantung pada manajemen rahasia yang benar (`ENCRYPTION_KEY` 32-byte yang privat, `ADMIN_INITIAL_PASSWORD` yang kuat, `COOKIE_SECURE=true` di balik HTTPS, isolasi port database target/RabbitMQ, dan tidak membuka port Collector/Grafana/Prometheus secara publik).
4. **Kerentanan dependensi pihak ketiga dan perilaku runtime DBMS:** CodeQL berfokus pada aliran kode sumber aplikasi; kerentanan paket pihak ketiga dipantau terpisah oleh `govulncheck` di [`.github/workflows/quality.yml`](../.github/workflows/quality.yml), sedangkan validasi SQL tetap dibatasi oleh hak akses akun database target PostgreSQL.
