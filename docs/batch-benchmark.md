# Benchmark batch PostgreSQL

Pengujian pada 23 November 2026 menggunakan PostgreSQL 16 di Docker Desktop. Kedua pengukuran memakai tabel `public.benchmark_accounts` yang sama pada koneksi `target-postgres-1`. Tabel berisi 500.000 akun dengan primary key `id` dan saldo awal 1.000.

| Pengukuran | Hasil |
|---|---:|
| Satu perintah UPDATE atas seluruh tabel, waktu dari psql | 6,155 detik |
| Eksekusi worker batch, dari started_at sampai finished_at | 16,804 detik |
| Ukuran batch | 10.000 baris |
| Checkpoint committed | 50 |
| Baris yang diperbarui | 500.000 |
| Interval checkpoint, minimum / median / p95 / maksimum | 169 / 284 / 576 / 1.180 ms |
| Saldo minimum / maksimum setelah batch | 1.001 / 1.001 |

Pengukuran pertama menjalankan `UPDATE benchmark_accounts SET balance = balance + 1` dalam transaksi, lalu melakukan rollback agar batch mulai dari saldo yang sama. Angka 6,155 detik mengukur perintah UPDATE tanpa waktu rollback. Interval batch dihitung dari timestamp progres di platform, dengan `started_at` sebagai awal interval pertama. Durasi worker mencakup pencatatan snapshot pembatalan, checkpoint dan commit tiap batch. Waktu antre sebelum worker mulai dikecualikan. Angka ini berasal dari satu putaran pada mesin pengujian.

## Mengulang

1. Jalankan stack dan seed akun/grup menurut README. Pada target pengujian kosong, jalankan dari root repo di PowerShell.

   ```powershell
   Get-Content -Raw scripts/seed_large_dataset.sql | docker compose exec -T target-postgres psql -X -v ON_ERROR_STOP=1 -U postgres -d target_db
   ```

   Untuk koneksi PostgreSQL lokal, alternatifnya jalankan `go run ./scripts/seed_large_dataset.go` dengan `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` dan `PGPASSWORD` yang sesuai. Seeder berhenti jika nama tabel sudah dipakai.

2. Ukur perintah berikut dengan psql pada koneksi yang sama.

   ```sql
   \timing on
   BEGIN;
   UPDATE public.benchmark_accounts SET balance = balance + 1;
   ROLLBACK;
   ```

3. Di SPA buat migrasi reversible, mode Batch, tabel `public.benchmark_accounts`, cursor `id`, ukuran 10000. Gunakan pasangan SQL berikut.

   ```sql
   -- SQL up
   UPDATE public.benchmark_accounts SET balance = balance + 1 WHERE id BETWEEN $1 AND $2;
   -- SQL down
   UPDATE public.benchmark_accounts SET balance = balance - 1 WHERE id BETWEEN $1 AND $2;
   ```

4. Ajukan permintaan `up`, minta persetujuan kedua leader, lalu enqueue. Buka monitor job dan riwayat untuk memeriksa hasil. Pada target, cocokkan `SELECT count(*), min(balance), max(balance) FROM public.benchmark_accounts` dan jumlah checkpoint untuk execution ID tersebut di `_platform_meta.batch_checkpoints`.
