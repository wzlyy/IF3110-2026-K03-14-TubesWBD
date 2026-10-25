# Database Management Platform

Platform web untuk mengelola akses database, menjelajahi skema dan data, serta menjalankan migrasi SQL dengan pratinjau dan riwayat. Aplikasi menggunakan Go dan HTML yang dirender di server, dengan dukungan target PostgreSQL dan MySQL.

## Fitur

- Autentikasi, pengelolaan sesi, serta administrasi pengguna dan group.
- Hak akses database melalui group: `VIEW_ONLY` dan `VIEW_EDIT`.
- Pengelolaan koneksi dengan kredensial terenkripsi.
- Penjelajahan tabel, kolom, constraint, index, dan data dengan paginasi.
- Perbandingan skema JSON dan pembuatan kandidat SQL migrasi.
- Editor SQL, pratinjau, eksekusi migrasi, dan riwayat perubahan.
- Analisis query melalui `EXPLAIN` dan mode lanjutan `EXPLAIN ANALYZE`.

## Teknologi dan prasyarat

- **Go 1.27.3 atau lebih baru**, sesuai `go.mod`. Dengan `GOTOOLCHAIN=auto`, Go dapat mengunduh toolchain yang dibutuhkan.
- **Docker Compose** untuk database lokal dan database pengujian.
- **PostgreSQL 16** untuk metadata platform dan target utama.
- **MySQL 8.0** untuk target tambahan.

Backend menggunakan `net/http` dan `html/template`. Antarmuka memakai HTML, CSS, dan JavaScript tanpa proses build frontend.

## Menjalankan aplikasi

### 1. Siapkan konfigurasi

Jalankan perintah berikut dari direktori utama proyek:

```powershell
Copy-Item .env.example .env
```

Isi `.env` sebelum menjalankan aplikasi:

| Variabel | Kegunaan |
|---|---|
| `PORT` | Port HTTP aplikasi; default `8080`. |
| `APP_ENV` | `development` untuk lokal, `production` untuk deployment HTTPS. |
| `ENCRYPTION_KEY` | Kunci AES-256-GCM; wajib tepat 32 byte. Gunakan secret acak dan simpan dengan aman. |
| `ADMIN_INITIAL_USERNAME` | Nama akun admin pertama; default `admin`. |
| `ADMIN_INITIAL_PASSWORD` | Password privat untuk bootstrap pertama. |
| `PLATFORM_DB_HOST`, `PLATFORM_DB_PORT` | Alamat metadata PostgreSQL; contoh lokal `127.0.0.1:5432`. |
| `PLATFORM_DB_USER`, `PLATFORM_DB_PASSWORD`, `PLATFORM_DB_NAME` | Kredensial dan nama database metadata. |
| `PLATFORM_DB_SSLMODE` | Mode SSL PostgreSQL; `disable` untuk konfigurasi lokal contoh. |
| `TARGET_PG_*` | Konfigurasi container PostgreSQL target lokal. |
| `TARGET_MYSQL_*` | Konfigurasi container dan akun MySQL target lokal. Password root hanya untuk provisioning. |

Jangan gunakan contoh kunci enkripsi atau password publik untuk deployment. Bootstrap menolak password kosong/default publik. Instalasi yang sudah mempunyai admin tidak memerlukan password bootstrap lagi. Pertahankan kunci enkripsi agar kredensial koneksi yang tersimpan tetap dapat dibaca.

### 2. Jalankan database

Metadata platform dan target migrasi harus memakai database terpisah. `compose.yaml` menyediakan ketiganya:

```powershell
docker compose up -d --wait platform-db target-postgres target-mysql
```

Dengan konfigurasi bawaan, metadata PostgreSQL memakai port `5432` dan database `platform_db`; target PostgreSQL memakai `5433`/`target_db`; target MySQL memakai `3306`/`target_db`. Sesuaikan environment jika port sudah digunakan. Akun MySQL aplikasi perlu dibuat sebelum koneksi didaftarkan; lihat [Konfigurasi target MySQL](#konfigurasi-target-mysql).

### 3. Jalankan server

```powershell
go run ./cmd/server
```

Buka [http://localhost:8080](http://localhost:8080), lalu masuk dengan akun bootstrap. Struktur metadata diinisialisasi secara aditif saat startup. Gunakan `APP_ENV=production` pada deployment HTTPS agar cookie `Secure` aktif.

Untuk menghentikan container lokal:

```powershell
docker compose stop
```

## Alur penggunaan

1. Masuk sebagai Platform Admin, lalu daftarkan koneksi target. Simpan sebelum menekan **Uji Koneksi**.
2. Buat pengguna dan group, tambahkan keanggotaan, lalu berikan grant database pada group.
3. Masuk sebagai pengguna ber-grant, pilih database, lalu periksa skema dan data.
4. Susun SQL pada editor atau gunakan Schema Diff untuk menghasilkan kandidat SQL.
5. Tinjau dan edit SQL, jalankan **Pratinjau SQL**, lalu **Eksekusi SQL yang ditinjau**.
6. Periksa status, versi, SQL, dan pelaksana pada riwayat migrasi. Jika gagal, periksa target sebelum memperbaiki SQL dan mencoba lagi.

Platform Admin juga memerlukan grant untuk membaca atau mengubah target. Jangan mendaftarkan database metadata platform sebagai target migrasi.

## Struktur proyek

```text
cmd/server/           Entrypoint dan konfigurasi server HTTP
internal/access/      Kebijakan dan pemeriksaan hak akses
internal/admin/       Pengelolaan pengguna, group, dan grant
internal/auth/        Login, password, sesi, dan CSRF
internal/config/      Pembacaan konfigurasi environment
internal/database/    Koneksi, katalog, viewer, dan query plan
internal/migration/   Editor, diff, generator, eksekusi, dan history
migrations/platform/  Skema metadata PostgreSQL
scripts/              Pengujian integrasi dan fixture MySQL
web/                  Template HTML dan aset statis
```

Tes Go berada di dekat modul yang diuji. Tes integrasi memakai build tag `integration` dan database khusus pengujian.

## Akses dan sesi

- Password disimpan sebagai hash bcrypt. Kredensial target dienkripsi dengan AES-256-GCM sebelum disimpan pada metadata.
- Cookie `session_id` berisi 32 byte acak atau 64 digit hex, dengan atribut `HttpOnly`, `SameSite=Lax`, dan `Secure` pada production. Sesi berakhir setelah tidak aktif selama 30 menit atau mencapai umur 8 jam. Token dirotasi setiap 15 menit; token lama berlaku selama masa tenggang 30 detik. Logout mencabut sesi.
- Hak akses efektif dihitung dari seluruh group pengguna; `VIEW_EDIT` mengungguli `VIEW_ONLY`. Pengguna tanpa grant tidak dapat membuka target. Platform Admin mengelola pengguna, group, koneksi, dan grant, tetapi tetap membutuhkan grant untuk mengakses target.
- Penonaktifan pengguna, reset password, dan pencabutan akses mencabut sesi terkait. Peningkatan hak akses merotasi token. Form yang mengubah data dilindungi CSRF. Login menggunakan POST sehingga password tidak dikirim melalui URL.

Password koneksi tidak ditampilkan kembali. Viewer menampilkan 50 baris per halaman; nama tabel dan skema dipilih dari katalog.

## Konfigurasi target MySQL

`PLATFORM_DB_*` hanya mengatur metadata platform. Form koneksi memakai alamat, port, database, dan akun target yang akan dikelola.

Gunakan akun khusus yang hanya mempunyai akses ke satu database target. Root, grant global, wildcard database yang tidak di-escape, `GRANT OPTION`, role, dan privilege di luar daftar yang didukung ditolak. Akun root hanya untuk provisioning, tidak didaftarkan pada aplikasi.

Contoh provisioning oleh administrator MySQL (ganti password):

```sql
CREATE DATABASE IF NOT EXISTS target_db;
CREATE USER 'wbd_migrator'@'%' IDENTIFIED BY 'GANTI_PASSWORD_PRIVAT';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER,
      CREATE VIEW, SHOW VIEW, TRIGGER
ON `target\_db`.* TO 'wbd_migrator'@'%';
```

Underscore pada pola GRANT di-escape agar bukan wildcard. Untuk fixture otomatis, `scripts/mysql-test-init.sql` menyediakan `target_user` khusus `target_test`.

## Perbandingan skema dan generator SQL

Masukkan skema tujuan melalui textarea **atau** file JSON. Pilih tepat satu sumber; server menolak input kosong maupun dua sumber yang terisi bersamaan. Format dapat berupa daftar `tables` atau daftar `schemas` yang berisi tabel. Field `dbms` bersifat opsional; jika diisi, nilainya harus `postgres` atau `mysql` dan cocok dengan koneksi target. Skema bawaan PostgreSQL adalah `public`; MySQL menggunakan nama database koneksi. Properti JSON yang tidak didukung akan ditolak.

```json
{
  "dbms": "postgres",
  "tables": [{
    "name": "produk_toko",
    "columns": [
      {"name": "id", "type": "integer", "nullable": false},
      {"name": "nama", "type": "varchar(200)", "nullable": false},
      {"name": "harga", "type": "numeric(12,2)", "nullable": false, "default": "0"}
    ],
    "primary_key": ["id"],
    "constraints": [{"name": "harga_positif", "type": "CHECK", "columns": ["harga"], "definition": "CHECK (harga >= 0)"}],
    "indexes": [{"name": "produk_nama", "columns": ["nama"], "unique": false}]
  }]
}
```

`schemas`: `{"schemas":[{"name":"public","tables":[...]}]}`. Definisikan **seluruh tabel aplikasi yang ingin dipertahankan**: objek yang tidak ada pada tujuan dianggap dihapus. Tabel `_dbplatform_migration_receipts` dikecualikan dari diff dan tidak boleh dimasukkan sebagai tujuan. Identifier JSON adalah nama katalog, tanpa pembungkus quote SQL; generator mengutip nama tersebut. Case PostgreSQL dipertahankan. Schema/tabel MySQL mengikuti `@@lower_case_table_names`: 0 peka case, 1/2 tidak; nama kolom/index/constraint MySQL tidak peka case.

Model mendukung `PRIMARY KEY`, `UNIQUE`, `FOREIGN KEY`, `CHECK`, serta `EXCLUDE` khusus PostgreSQL. Urutan kolom pada `columns` dipertahankan. FK, CHECK, dan EXCLUDE memerlukan `definition` SQL lengkap, misalnya `FOREIGN KEY (owner_id) REFERENCES public.owners(id)`. FK mendukung daftar identifier kolom dan nama tabel dengan skema opsional. Kolom lokal dan referensi harus tersedia pada skema tujuan; bentuk referensi yang tidak didukung akan ditolak.

Gunakan `primary_key` **atau** constraint PK eksplisit. Perbedaan nama PK saja tidak menghasilkan penggantian key. Index biasa menggunakan `name`, daftar `columns` berurutan, dan `unique`. Jangan mendefinisikan ulang index otomatis milik PK atau UNIQUE sebagai index mandiri. Nama yang duplikat atau berkonflik dan referensi kolom yang tidak tersedia akan ditolak.

Field `default` yang **tidak dicantumkan** mempertahankan default kolom lama. `"default": null` menghapus default; string berisi ekspresi SQL menetapkan default. Perubahan constraint/index bernama sama menghasilkan penggantian. Generator melepas dependensi terkait sebelum perubahan struktur, membuat seluruh tabel/key/index sebelum FK, lalu memasang FK. Perubahan destruktif diberi peringatan. Tipe, default, dan definition adalah fragmen SQL yang wajib ditinjau pengguna; generator bukan parser SQL atau alat pembuktian ekuivalensi semantik. Expression/partial/include indexes, generated/identity columns, schema baru, custom casts, dan ekstensi memerlukan SQL manual yang ditinjau; format sederhana tidak menyatakan atribut tersebut.

SQL kandidat **tidak otomatis dieksekusi**. Tekan **Bawa SQL ke Editor Migration**, periksa/edit SQL, **Pratinjau SQL**, lalu **Eksekusi SQL yang ditinjau**. Semua eksekusi memakai runner/history yang sama. JSON dibandingkan hanya terhadap target yang dipilih.

## Analisis query plan

EXPLAIN standar tidak mengeksekusi DML. Satu terminator `;` terakhir diperbolehkan; statement berantai ditolak. DDL menerima pesan informatif karena tidak mempunyai plan tabular. Plan menampilkan scan/index/join/cost/rows serta metrik aktual bila tersedia.

ANALYZE membutuhkan `VIEW_EDIT`, checkbox **ANALYZE** dan **konfirmasi eksekusi fisik**. PostgreSQL mengeksekusi query dalam transaksi yang selalu di-rollback. Sequence dan efek mesin di luar transaksi tidak dapat dipulihkan. MySQL ANALYZE hanya menerima **SELECT**; DML ditolak karena engine nontransaksional tidak menjamin rollback. SQL maksimum 16 KiB; waktu analisis dibatasi 5 detik dan hasil plan 1 MiB. Script migration multi-statement perlu dianalisis per query data.

## Eksekusi migrasi dan pemulihan

Eksekusi migrasi mengambil lock untuk target, mengalokasikan versi berikutnya berdasarkan migrasi SUCCESS, lalu menyimpan percobaan berstatus PENDING pada metadata. Setelah transaksi target selesai, hasil eksekusi dicatat pada riwayat.

PostgreSQL membatalkan seluruh DDL/DML dalam transaksi jika salah satu statement gagal. DDL MySQL dapat melakukan implicit commit; status FAILED dapat disertai perubahan parsial (`partial_changes=true`). Periksa katalog target sebelum memperbaiki SQL dan mencoba lagi. Percobaan ulang menghasilkan entri riwayat baru; percobaan FAILED tetap tersimpan dan tidak menghabiskan nomor versi sukses berikutnya.

PENDING menandakan hasil yang belum dapat dipastikan, misalnya karena koneksi terputus saat commit. Migrasi berikutnya diblokir sampai hasil dapat diverifikasi. Jalankan **Rekonsiliasi** dari halaman detail melalui form yang dilindungi CSRF. Endpoint-nya adalah `POST /databases/{id}/migration/{attempt_id}/reconcile`.

Rekonsiliasi menetapkan SUCCESS hanya jika receipt cocok dengan percobaan, database, versi, hash SQL, dan waktu selesai. Receipt yang tidak ditemukan atau bukti yang ambigu membuat status tetap PENDING. Periksa log dan kondisi target sebelum intervensi administrator; jangan mengubah metadata atau menghapus receipt untuk memaksakan status.

## Pengujian

Jalankan tes unit dan pemeriksaan kode dari direktori utama proyek:

```powershell
go test -p 1 -count=1 ./...
go vet ./...
go build ./cmd/server
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

Tes tanpa database tidak menggantikan tes integrasi. Tes database memerlukan `WBD_TEST_DATABASES=1`, yang diatur oleh script berikut. **Suite integrasi mengubah dan menghapus data fixture**; gunakan `compose.test.yaml` yang terpisah dari data aplikasi. Port `15432`, `15433`, dan `13306` harus tersedia.

```powershell
docker compose -p wbd-test -f compose.test.yaml up -d --wait
.\scripts\test-integration.ps1
# Jalankan satu paket atau satu alur:
.\scripts\test-integration.ps1 -Package ./internal/migration -Run TestGeneratedSQLToEditorFlow
docker compose -p wbd-test -f compose.test.yaml stop
```

Jika kebijakan PowerShell lokal menolak script, gunakan `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-integration.ps1`; pengaturan hanya berlaku pada proses tersebut. Bila port uji telah dipakai project Compose lain, periksa pemiliknya sebelum menjalankan suite. Jangan menghentikan atau menghapus database yang belum diidentifikasi.

Script menetapkan environment database uji dan menjalankan paket secara serial (`-p 1`) agar fixture tidak saling mengganggu. Periksa hasil dan pastikan tes database tidak berstatus SKIP. Workflow CI menjalankan vet, build, audit kerentanan, dan suite integrasi dengan konfigurasi pengujian yang sama.
