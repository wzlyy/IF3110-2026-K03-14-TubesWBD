# Database Management Platform

**IF3110 Pengembangan Aplikasi Web · K03-14 · Milestone 2**

Platform ini memberi grup akses ke database PostgreSQL dan menyediakan alur untuk meninjau, menyetujui, mengeksekusi, serta membatalkan migrasi. Login, administrasi, schema/data viewer, EXPLAIN, schema diff, editor migrasi, approval, monitor job dan riwayat tersedia di satu SPA.

| Anggota | Tanggung jawab M2 |
|---|---|
| Dika Pramudya Nugraha (13524132) | API Go, skema platform, workflow dan approval, lock, publisher, instrumentasi, CodeQL dan skenario k6 |
| Wildan Abdurrahman Ghazali (13524054) | SPA responsif, worker dan recovery, dataset batch, stack observabilitas, benchmark dan analisis |
| Hafizh Hananta Akbari (13522132) | - |

## Menjalankan aplikasi

Gunakan Docker Compose v2. Pada PowerShell, salin konfigurasi jika belum ada.

```powershell
if (!(Test-Path .env)) { Copy-Item .env.example .env }
```

Isi `ADMIN_INITIAL_PASSWORD` dan `ENCRYPTION_KEY` di `.env`. Kunci harus tepat 32 byte, misalnya 32 karakter ASCII acak. Simpan kunci yang sama selama volume platform masih digunakan karena password koneksi target dienkripsi dengan kunci tersebut. Port platform bawaan 5432 dapat diganti melalui `PLATFORM_DB_PORT` jika sudah dipakai PostgreSQL lokal. Gunakan password privat untuk layanan yang akan dibuka di luar mesin pengembangan.

```powershell
docker compose up -d --build --wait
docker compose exec backend-api /app/seed
```

Buka [aplikasi lokal](http://localhost:8080). Seed opsional membuat dua koneksi target dengan host internal Compose, dua grup, serta empat pengguna demo. Login admin memakai password dari `.env`. Untuk menetapkan password demo sendiri pada seed pertama, jalankan `docker compose exec -e SEED_USER_PASSWORD=<password-demo> backend-api /app/seed`. Seed bawaan memakai `DevPass!2026` dan tidak mengganti password pengguna yang sudah ada.

| Pengguna demo | Akses |
|---|---|
| editor_core | VIEW_EDIT pada kedua target |
| leader_core | Leader team-core pada kedua target |
| leader_analytics | Leader team-analytics dengan VIEW_ONLY pada target pertama |
| viewer_analytics | VIEW_ONLY pada target pertama |

Untuk mengaktifkan Grafana, jalankan `docker compose -f compose.yaml -f compose.observability.yaml up -d --build`. Dashboard tersedia di [Grafana lokal](http://localhost:3000/d/migration-platform). Hentikan stack dengan `docker compose -f compose.yaml -f compose.observability.yaml stop` untuk mempertahankan data.

## Alur penggunaan

1. Login sebagai admin untuk mengatur pengguna, anggota dan leader grup, koneksi, serta grant. Hak admin dan hak akses target diberikan terpisah.
2. Login sebagai editor, pilih database, dan tinjau skema/data. Editor dapat memeriksa SQL dengan EXPLAIN atau menggunakan DDL hasil schema diff sebagai awal draft.
3. Buat definisi migrasi dengan SQL up dan down, atau isi alasan irreversible. Tombol validasi memeriksa aturan SQL dan, untuk batch, katalog cursor di target. Ajukan request lalu submit untuk ditinjau.
4. Setiap leader grup yang memiliki grant pada target menyetujui SQL melalui antrean review. Target pertama membutuhkan kedua leader demo, target kedua cukup leader_core. Enqueue mengembalikan job ID. Monitor menampilkan hasil worker dan progres commit.
5. Buka riwayat untuk melihat SQL, versi, percobaan gagal dan eksekusi sukses. **Hide rollback** menyembunyikan pasangan eksekusi dan rollback berdasarkan execution ID. Rollback berlaku pada active tip yang reversible. Forward, resume dan revert memerlukan persetujuan baru.

## Arsitektur dan penyimpanan

```text
Browser → React / TypeScript / Vite → Go REST /api/v1 → PostgreSQL platform
                                           ↓
                                     RabbitMQ durable
                                           ↓
                                Go worker pool + DB lease
                                           ↓
                                  PostgreSQL target 1 / 2

API + worker → OTel Collector → Tempo (traces) / Loki (logs)
Prometheus → scrape API dan worker → Grafana
```

| Lokasi | Data dan fungsi |
|---|---|
| Platform PostgreSQL | `users`, `groups`, `user_groups`, sesi, koneksi target terenkripsi serta grant VIEW_ONLY/VIEW_EDIT |
| Definisi dan review | `migration_definitions` menyimpan revisi SQL dan pengaturan batch. `migration_requests` mereferensikan definisi dan eksekusi sasaran. `migration_approvals` merekam keputusan per grup |
| Eksekusi di platform | `migration_executions` menyimpan satu attempt dengan salinan SQL. `execution_batch_progress` menyimpan progres yang dibaca SPA. `database_locks` membatasi satu migrasi aktif per database |
| PostgreSQL target | Skema `_platform_meta` menyimpan execution proof, checkpoint dan snapshot pembatalan dalam transaksi yang sama dengan perubahan data |

Skema platform lengkap tersedia di [migrasi SQL](migrations/platform/005_m2_schema.sql). API memeriksa sesi, CSRF dan grant pada server. Worker memeriksa kembali approval grup, urutan linear dan active tip setelah memperoleh lock. `WORKER_CONCURRENCY` mengatur paralelisme worker (bawaan 4).

### Bila worker terhenti

Worker memperbarui lease 30 detik setiap 10 detik. Pesan persisten memakai acknowledgment manual. Pada redelivery, worker memeriksa status attempt dan lock. Jika lease kedaluwarsa sebelum attempt tercatat, lock dapat diambil kembali. Jika transaksi sudah mungkin berjalan, worker menahan database dalam `held_uncertain`, mengambil advisory lock di target dan memeriksa bukti commit sebelum mengambil keputusan. Token lease mencegah worker lama meneruskan transaksi setelah kepemilikan berpindah. Ketika target belum dapat dihubungi, pesan verifikasi dijadwalkan kembali. Retry eksekusi SQL sementara dibatasi hingga tiga kali.

Migrasi normal menulis proof bersama SQL dalam satu transaksi. Pada batch, worker menangkap batas awal cursor, membaca rentang keyset terurut, lalu melakukan commit per rentang. Gunakan satu UPDATE pada tabel yang dikonfigurasi, dengan cursor unik, NOT NULL, dan stabil. SQL dapat memakai `WHERE id BETWEEN $1 AND $2`. Batch reversible menyimpan keadaan baris sebelum dan sesudah pembaruan. Jika gagal setelah sebagian commit, `held_partial` melindungi database. Resume memulai dari checkpoint terakhir. Revert menjalankan SQL kompensasi yang disetujui atas key yang tercatat dalam urutan batch terbalik dan menolak baris yang berubah dari snapshot. Bila kompensasi terputus, progres yang sudah direvert tersimpan.

Dalam pengujian restart, worker dimatikan setelah monitor melaporkan tiga checkpoint. Sesudah RabbitMQ dan worker hidup kembali, target membuktikan empat batch telah committed. Request resume menyelesaikan 96 sisanya. Pada akhir uji, seluruh 500.000 baris memiliki saldo 1.003 sesuai satu kali pembaruan per baris.

## Pengujian

```powershell
go test -p 1 -count=1 ./...
go vet ./...
docker compose -p wbd-test -f compose.test.yaml up -d --wait
.\scripts\test-integration.ps1
docker compose -p wbd-test -f compose.test.yaml stop
npm --prefix frontend ci
npm --prefix frontend run build
```

Go mengikuti versi di `go.mod`. Untuk pengembangan SPA jalankan `npm --prefix frontend run dev` di samping API lokal pada port 8080. Vite mem-proxy `/api` ke Go. Image produksi menyalin hasil build ke `/app/frontend/dist`. Kontainer pengujian memakai database dan port khusus. Skenario worker mencakup serialisasi per target, eksekusi paralel dua target, duplicate delivery, partial/resume, kegagalan revert dan pemulihan commit setelah lease kedaluwarsa.

Pada 500.000 baris, satu UPDATE membutuhkan **6,155 detik**. Worker batch reversible menyelesaikan **50 × 10.000 baris dalam 16,804 detik**. Pengukuran mencakup satu putaran lokal. [Metode, SQL dan hasil benchmark](docs/batch-benchmark.md)

### Hasil k6

Setelah seed, jalankan `.\tests\k6\setup_preapproved.ps1` (kuota bawaan kini 30 job per DB / 60 job total; dapat diubah lewat `-CountPerDB`). Terapkan nilai variabel lingkungan yang dicetak oleh skrip, lalu jalankan `k6 run --out json=k6-points.json tests/k6/stress_test.js`. Jika menggunakan password seed khusus, teruskan `-Password` ke skrip persiapan dan set `K6_PASSWORD` sebelum k6. Gunakan database pengujian karena request yang disiapkan akan benar-benar dieksekusi. Skrip `tests/k6/stress_test.js` kini menyebarkan pengiriman job ke lima tahap beban (`baseline_10vu`, `ramp_30vu`, `ramp_60vu`, `peak_100vu`, `recovery_10vu`) beserta tag `load_stage` (atau `K6_JOB_MODE=burst` untuk pengiriman serentak di awal).

Pada run pengukuran 25 November 2026 menggunakan mode bertahap (`K6_JOB_MODE=staged`, 60 job pra-persetujuan tersebar ke lima tahap beban pada dua target PostgreSQL), skenario baca menaikkan beban dari `0 → 10 → 30 → 60 → 100 VU` lalu menurunkannya kembali ke `10 → 0 VU`, sementara skenario kedua mengirim `30` job `same_db` (`target-postgres-1`) dan `30` job `diff_db` (`target-postgres-2`) lalu mem-polling status hingga terminal. Seluruh threshold k6 lulus: **`p95` HTTP 28,08 ms** (`< 500 ms`), **error rate HTTP 0,00%** (`0 / 35.455`, `< 2%`), throughput agregat **207,95 req/s**, dan **60 / 60 job sukses (`100%`)** dengan `queue_wait` `p50 = 18 ms` / `p95 = 34,25 ms` serta durasi penyelesaian job `p50 = 279,5 ms` / `p95 = 304,2 ms`.

| Jendela waktu (tahap beban) | HTTP selesai (req/s) | HTTP p50 / p95 (ms) | Error | Job selesai (job/s) | Queue wait p50 / p95 (ms) | Durasi job p50 / p95 (ms) |
|---|---:|---:|---:|---:|---:|---:|
| `0–30s` (`0 → 10 VU` / `baseline_10vu`) | 999 (33,30) | 6,53 / 20,47 | 0% | 10 (0,333) | 19,0 / 30,65 | 277,0 / 285,55 |
| `30–60s` (`10 → 30 VU` / `ramp_30vu`) | 3.738 (124,60) | 6,12 / 26,10 | 0% | 12 (0,400) | 18,0 / 30,20 | 282,0 / 305,80 |
| `60–90s` (`30 → 60 VU` / `ramp_60vu`) | 7.949 (264,97) | 5,24 / 39,36 | 0% | 15 (0,500) | 17,0 / 36,50 | 280,0 / 303,90 |
| `90–120s` (`60 → 100 VU` / `peak_100vu`) | 14.165 (472,17) | 5,24 / 27,70 | 0% | 15 (0,500) | 18,0 / 34,05 | 282,0 / 301,50 |
| `120–150s` (`100 → 10 VU` / `recovery_10vu`) | 8.131 (271,03) | 4,49 / 22,31 | 0% | 8 (0,267) | 18,5 / 23,30 | 277,0 / 287,30 |
| `150s–akhir` (17,44s, `10 → 0 VU`) | 472 (27,07) | 3,83 / 14,45 | 0% | 0 (0,000) | — | — |

**Analisis perilaku tiap tingkat beban & pemulihan:**
1. **Baseline & Ramp-up Awal (`0–60s`, `10 → 30 VU`):** Latensi HTTP sangat stabil (`p50 ~6,1–6,5 ms`, `p95 ~20,5–26,1 ms`) seiring peningkatan throughput hingga `124,60 req/s`, dengan waktu tunggu antrean job `~18–19 ms`.
2. **Beban Menengah–Puncak (`60–120s`, `30 → 100 VU`):** Pada transisi `60–90s`, terjadi lonjakan maksimum sesaat (`654,25 ms`, `p95 = 39,36 ms`) akibat antrean sementara pada pool target `SetMaxOpenConns(5)` ([`internal/database/target_manager.go`](internal/database/target_manager.go#L139)) ketika `GET /schema` menahan transaksi katalog `REPEATABLE READ` bersamaan dengan eksekusi migrasi. Pada puncak `90–120s`, throughput mencapai **`472,17 req/s`** (`14.165` request) dengan `p95` HTTP `27,70 ms`, `0%` error, dan `15` job tetap selesai lancar.
3. **Pemulihan (`120s–akhir`, `100 → 0 VU`) & Perbandingan DB:** Sistem langsung pulih tanpa *tail latency* tertahan: `p95` HTTP turun ke `22,31 ms` (`120–150s`) lalu `14,45 ms` (`150s–akhir`), dengan seluruh 8 job sisa tuntas dan gauge inflight kembali ke `0`. Di sisi antrean, job `same_db` mencatat ekor `queue_wait` lebih tinggi (`p95 = 41,75 ms`, `max = 49 ms`) dibanding `diff_db` (`p95 = 24,10 ms`, `max = 26 ms`) karena serialisasi distributed lock pada database target yang sama.

[Rekap lengkap metrik, per tahap `load_stage`, perbandingan `same_db` vs `diff_db`, dan analisis](docs/k6-raw-results.md)

### Observabilitas dan CodeQL

Log JSON `log/slog` membedakan transaksi (`sql.tx_start`, `sql.tx_end`) dan eksekusi pernyataan (`sql.start`, `sql.result`), menyertakan `actor`, `database`, `correlation_id`, `trace_id`, `span_id`, metadata migrasi/batch, `sqlstate` saat gagal, serta menyamarkan kredensial/parameter sensitif dengan `[REDACTED]`. Saat alur migrasi berjalan, trace OpenTelemetry berantai menghubungkan span `HTTP POST enqueue → api.migration.enqueue → queue.publish → worker.consume → lock.acquire → db.tx → db.sql.tx_exec`, dikirim ke Tempo/Loki melalui OTel Collector, serta dilengkapi metrik Prometheus `/metrics` berkardinalitas rendah dengan label pool `db_role` seragam (`platform` dan `target_postgres`). [Contoh nyata log SQL JSON, span OTel/Tempo, dan sampel `/metrics`](docs/observability.md)

Workflow CodeQL (`.github/workflows/codeql.yml`) menganalisis kode **Go** (`autobuild`: `cmd/*` dan `internal/*`) serta **JavaScript/TypeScript** (`frontend/src/*` dan `tests/k6/stress_test.js`) menggunakan query `security-extended` dan `security-and-quality`. Pada pemindaian awal backend M2 (`64ef0d0`), CodeQL menemukan kerentanan `go/log-injection` (*High*, CWE-117) pada `internal/telemetry/logger.go` (`LogEvent`) dan `internal/telemetry/redact.go` (`SanitizeLogString`, `RedactParams`) karena karakter CR/LF dan `slog.Attr` mentah masih berpotensi menyuntikkan entri log palsu. Temuan tersebut **diperbaiki** pada commit `6ede7c2` dengan menghapus `\n`/`\r` dan merekonstruksi setiap `slog.Attr` per `slog.Kind`. Hasil rescan penuh CodeQL pada `main` hingga commit `1eabb21` (mencakup pembaruan Go, SPA React/TS, dan skrip k6) bersih (**0 alert** Go maupun JS/TS) pada [Run CodeQL `45609367648`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45609367648); untuk commit inkremental pasca-`1eabb21` (`3970bce`–`7cc9c00`), *Quality checks* + `govulncheck` ([Run `45650226033`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45650226033)) dan matriks CodeQL JS/TS ([Run `45650226130`](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45650226130)) telah sukses, sedangkan matriks Go pada run tersebut terdampak gangguan API GitHub sementara (*`Error creating policy`*) dan akan tervalidasi ulang saat *re-run*/push berikutnya. **Catatan:** hasil bersih SAST tidak menjamin aplikasi bebas dari seluruh celah keamanan (misalnya cacat logika bisnis, kondisi pacu *distributed lock*, maupun salah konfigurasi lingkungan). [Laporan lengkap analisis keamanan CodeQL](docs/security-codeql.md)
