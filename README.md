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

Setelah seed, jalankan `.\tests\k6\setup_preapproved.ps1`. Terapkan nilai variabel lingkungan yang dicetak oleh skrip, lalu jalankan `k6 run --out json=k6-points.json tests/k6/stress_test.js`. Jika menggunakan password seed khusus, teruskan `-Password` ke skrip persiapan dan set `K6_PASSWORD` sebelum k6. Gunakan database pengujian karena request yang disiapkan akan benar-benar dieksekusi.

Skenario baca menaikkan beban sampai 100 virtual user (VU), lalu menurunkannya. Skenario kedua mengirim 40 job pra-persetujuan ke dua database dan melakukan polling hingga status terminal. Threshold global p95 HTTP <500 ms dan error rate <2% tercapai pada run ini (p95 **71,39 ms**, **0%** gagal). Seluruh 40 job mencapai success, dengan p95 antre **4,175 detik** dan p95 enqueue hingga hasil teramati **4,308 detik**.

| Tahap yang dijadwalkan | Hasil dan interpretasi per jendela pengamatan |
|---|---|
| Baseline menuju 10 VU | 45,30 req/s, p95 22,36 ms. Seluruh job selesai pada jendela ini, setara 1,33 job/s. Ada selisih besar antara median antre 28 ms dan p95 4.175 ms. |
| Naik menuju 30 VU | 147,10 req/s dengan p95 23,68 ms. Trafik baca naik dan latensi ekor dekat dengan baseline. |
| Naik menuju 60 VU | 320,33 req/s dengan p95 22,44 ms. Throughput terus naik tanpa kegagalan HTTP. |
| Puncak menuju 100 VU | 371,00 req/s, p95 98,46 ms. Tambahan throughput lebih kecil, sementara p95 mencapai lebih dari empat kali jendela sebelumnya. |
| Turun menuju 10 VU | 13,07 req/s, p95 **29,26 detik**. Fase ini melampaui ambang 500 ms meskipun threshold agregat lulus. P95 waktu kirim k6 juga melonjak ke 27,80 detik. Data ini belum mengisolasi penyebab di aplikasi, jaringan atau mesin uji. |
| Pemulihan menuju 0 VU | Pada sampel akhir p95 turun ke 17,82 ms, dengan 105 respons dan 0 kegagalan. Job baru seluruhnya dikirim di awal, sehingga pemulihan antrean pada beban puncak belum terukur. |

[Rekap sampel k6 dan cara membagi jendela](docs/k6-raw-results.md)

### Observabilitas dan CodeQL

Log JSON membawa actor, database, attempt, trace ID dan span ID. Log SQL membedakan awal, hasil, dan akhir transaksi. Saat uji batch, trace yang sama memuat span enqueue API, publikasi RabbitMQ, konsumsi worker, lock dan transaksi PostgreSQL. [Contoh log, trace dan metrik Grafana](docs/observability.md)

Workflow CodeQL menganalisis Go dan JavaScript/TypeScript dengan query security-extended dan security-and-quality. Run pada commit dasar `8df59cd` berhasil. Perubahan lokal dalam workspace ini belum memperoleh hasil CodeQL GitHub tersendiri. [Run CodeQL pada commit dasar](https://github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/actions/runs/45047490984)
