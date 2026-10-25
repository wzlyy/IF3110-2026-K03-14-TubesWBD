# Database Management Platform
### IF3110 Pengembangan Aplikasi Web | Milestone 1
**Kelompok K03-14 | Semester Ganjil 2026/2027**

---

## 1. Deskripsi Proyek

**Database Management Platform** adalah aplikasi berbasis web yang dirancang untuk mengelola akses database secara terpusat, menjelajahi skema dan data tabel, menganalisis rencana eksekusi query (*execution plan*), serta menyusun dan mengeksekusi migrasi skema SQL secara terkontrol dan transaksional.

Aplikasi dibangun menggunakan bahasa pemrograman **Go** dengan pustaka standar `net/http` dan Server-Side Rendering (SSR) via `html/template` serta Vanilla CSS modern tanpa dependensi framework eksternal yang berlebihan (*no over-engineering*). Platform mendukung database metadata **PostgreSQL** serta dua target database: **PostgreSQL** (target utama) dan **MySQL** (target tambahan/bonus).

---

## 2. Anggota Kelompok & Pembagian Tugas

| NIM | Nama | Pembagian Tanggung Jawab / Fitur |
|---|---|---|
| **13524132** | Dika Pramudya Nugraha | Fondasi arsitektur, sistem autentikasi dan manajemen sesi aman (bcrypt, cookie flags, rotasi sesi, grace period, CSRF), engine *access control* dan evaluasi hak akses efektif multi-group (`VIEW_ONLY` & `VIEW_EDIT`), panel Platform Admin (CRUD User, Group, Grants), fitur **Bonus 2: Schema Diff & DDL Generation** (pembanding skema terstruktur, generator kandidat DDL lintas dialek, integrasi preview ke editor), pengujian integrasi terpadu, dan dokumentasi. |
| **13524054** | Wildan Abdurrahman Ghazali | Manajemen koneksi database target (PostgreSQL & MySQL), enkripsi simetris kredensial AES-256-GCM, antarmuka penjelajah skema (*Schema Viewer*) dan penjelajah data (*Data Viewer* paginasi 50 baris), sistem migrasi inti (*Migration Query Editor & Preview*, otorisasi, transactional runner 5 langkah, penanganan rollback DDL PostgreSQL & implicit commit MySQL, versi linear, dan rekonsiliasi PENDING), fitur **Bonus 1: DBMS Tambahan (MySQL 8.0)**, serta fitur **Bonus 3: Query Execution Plan Analysis** (`EXPLAIN` & `EXPLAIN ANALYZE`). |
| **13522132** | Hafizh Hananta Akbari | - |

---

## 3. Cakupan Fitur (Fitur Wajib & 3 Fitur Bonus)

### Fitur Wajib
1. **Autentikasi & Keamanan Sesi:**
   - Penyimpanan password menggunakan hashing `bcrypt`.
   - Sesi berbasis *opaque token* 32-byte acak kriptografis (`crypto/rand`) tersimpan di backend.
   - Atribut cookie aman: `HttpOnly`, `SameSite=Lax`, dan `Secure` otomatis pada environment production.
   - Kebijakan sesi: *idle timeout* (30 menit), *absolute expiration* (8 jam), rotasi token periodik (15 menit) dan saat peningkatan hak akses dengan *grace period* 30 detik.
   - Proteksi CSRF berbasis token unik per sesi pada seluruh mutasi HTTP `POST`.
2. **Access Control & Platform Admin:**
   - Hak akses berbasis group terhadap target database: `VIEW_ONLY` (hanya membaca) dan `VIEW_EDIT` (membaca, menyusun, dan mengeksekusi migrasi).
   - Peran *Platform Admin* terpisah dari grant database target (*separation of duties*).
   - Evaluasi hak akses real-time di backend; pencabutan hak akses mematikan sesi pengguna seketika.
3. **Manajemen Target Database & Kredensial:**
   - Penyimpanan koneksi target dengan enkripsi password AES-256-GCM berbasis environment variable `ENCRYPTION_KEY`.
   - Fitur *Test Connection* dengan sanitasi pesan error agar kredensial tidak bocor.
4. **Schema & Data Viewer:**
   - Menampilkan struktur tabel, kolom, nullability, default value, constraint (PK, FK, Unique, Check), dan index.
   - Penjelajahan data tabel dengan paginasi tepat 50 baris, label visual `NULL`, dan identifier quoting aman dari SQL Injection.
5. **Migration Core & Transactional Runner:**
   - Editor penyusunan SQL dengan pratinjau (*preview*) sebelum eksekusi dan pembatasan buffer 16 KiB.
   - Siklus eksekusi 5 langkah: pencatatan `PENDING` $\to$ eksekusi transaksi target $\to$ `COMMIT`/`ROLLBACK` $\to$ pembaruan status `SUCCESS`/`FAILED` $\to$ rekonsiliasi berbasis *receipt verification*.
   - Transaksional Rollback DDL 100% pada PostgreSQL.
   - Penomoran versi linear (hanya bertambah saat `SUCCESS`) dan percobaan ulang (*retry*) menggunakan nomor versi kandidat yang sama.

### 3 Fitur Bonus
- **Bonus 1 — DBMS Tambahan (MySQL 8.0):** Pengelolaan database MySQL meliputi koneksi target, penjelajah skema & data, penegakan prinsip least-privilege (menolak akun root aplikasi), serta penanganan batasan *implicit commit* pada DDL MySQL.
- **Bonus 2 — Schema Diff & Migration Generation:** Pembanding skema terstruktur antara katalog aktual target dengan definisi JSON yang diunggah/diinput. Menghasilkan kandidat DDL PostgreSQL dan MySQL dalam urutan dependensi aman, mendeteksi aksi destruktif, dan mengalirkan SQL ke Editor (*human-in-the-loop*).
- **Bonus 3 — Query Execution Plan Analysis:** Analisis query melalui mode default aman `EXPLAIN` (scan type, index usage, join, estimated cost/rows) dan mode lanjutan `EXPLAIN ANALYZE` (terproteksi rollback transaksi otomatis untuk DML pada PostgreSQL serta catatan sequence).

---

## 4. Teknologi & Prasyarat Sistem

- **Bahasa Pemrograman:** Go versi 1.22+ (sesuai spesifikasi; kompatibel dengan Go 1.27 toolchain).
- **Driver Database:**
  - `github.com/jackc/pgx/v5` (PostgreSQL)
  - `github.com/go-sql-driver/mysql` (MySQL)
- **Containerization:** Docker & Docker Compose.
- **Database Engine:**
  - PostgreSQL 16 (Metadata platform pada port `5435`/`5432` dan target pada port `5433`).
  - MySQL 8.0 (Target pada port `3306`).

---

## 5. Panduan Instalasi & Menjalankan Aplikasi

### Langkah 1: Kloning Repositori & Persiapan Environment
Salin berkas template environment:
```powershell
Copy-Item .env.example .env
```

Buka dan sesuaikan nilai konfigurasi pada berkas `.env` mengacu pada berkas `.env.example`:
- **`ENCRYPTION_KEY`**: Kunci enkripsi AES-256-GCM (wajib tepat 32 byte rahasia).
- **`ADMIN_INITIAL_PASSWORD`**: Password privat untuk inisialisasi akun admin pertama saat sistem booting.
- **`PLATFORM_DB_*`**: Konfigurasi koneksi ke database metadata platform PostgreSQL (host, port, user, password, dan nama database).

> [!CAUTION]
> Jangan pernah mencantumkan nilai rahasia (`ENCRYPTION_KEY` atau password asli) ke dalam repositori publik. Seluruh secret wajib disimpan hanya pada berkas `.env` lokal.


### Langkah 2: Jalankan Layanan Database (Docker Compose)
Jalankan seluruh layanan database (metadata platform, target PostgreSQL, dan target MySQL):
```powershell
docker compose up -d --wait platform-db target-postgres target-mysql
```

> [!NOTE]
> Database metadata platform (`platform-db`) terisolasi dari database target migrasi. Jangan pernah mendaftarkan database metadata platform sebagai target migrasi.

### Langkah 3: Jalankan Server Aplikasi
Jalankan server aplikasi Go:
```powershell
go run ./cmd/server
```

Akses aplikasi melalui browser:
👉 **[http://localhost:8080](http://localhost:8080)**

- **Username Awal:** `admin`
- **Password Awal:** nilai `ADMIN_INITIAL_PASSWORD` yang Anda tetapkan di `.env`.

Untuk menghentikan kontainer database:
```powershell
docker compose stop
```

---

## 6. Konfigurasi Khusus Target Database

### Target PostgreSQL
- **Host:** `127.0.0.1` | **Port:** `5433` | **Database:** `target_db`
- **Username:** `postgres` | **Password:** `postgres`

### Target MySQL (Least-Privilege Provisioning)
Aplikasi menerapkan validasi keamanan *least-privilege*: **akun `root` ditolak untuk koneksi aplikasi**. Gunakan akun khusus berskala satu database target (`target_db`):
```sql
CREATE DATABASE IF NOT EXISTS target_db;
CREATE USER IF NOT EXISTS 'wbd_migrator'@'%' IDENTIFIED BY 'GANTI_DENGAN_PASSWORD_ANDA';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, DROP, REFERENCES, INDEX, ALTER,
      CREATE VIEW, SHOW VIEW, TRIGGER
ON `target\_db`.* TO 'wbd_migrator'@'%';
```
*(Atau gunakan akun uji otomatis `target_user` dengan password `test-password` jika container diinisialisasi melalui script fixture).*

---

## 7. Arsitektur Repositori

```text
.
├── cmd/
│   └── server/
│       └── main.go               # Entrypoint HTTP, konfigurasi middleware, dan routing
├── internal/
│   ├── access/                   # Engine otorisasi, policy, dan evaluasi effective access
│   ├── admin/                    # Handler administrasi pengguna, group, dan database grants
│   ├── auth/                     # Autentikasi bcrypt, manajemen session lifecycle, dan CSRF
│   ├── config/                   # Loader konfigurasi environment (.env) dan validasi kunci
│   ├── database/                 # Koneksi target, AES-256 cipher, katalog PG/MySQL, viewer, EXPLAIN
│   └── migration/                # Schema diff, DDL generator, validation, runner 5 langkah, history
├── migrations/
│   └── platform/                 # DDL inisialisasi metadata platform PostgreSQL
├── scripts/
│   ├── mysql-test-init.sql       # Skrip provisioning database MySQL pengujian
│   └── test-integration.ps1      # Skrip automasi integration testing terisolasi
├── web/
│   ├── static/                   # Aset statis: CSS modern responsif dan JavaScript pembantu
│   └── templates/                # Template server-side rendering (Go html/template)
│       ├── admin/                # Antarmuka manajemen user, group, grants, connections
│       ├── auth/                 # Antarmuka login
│       ├── database/             # Antarmuka daftar database, schema viewer, data viewer, explain
│       ├── migration/            # Antarmuka editor, preview, history, detail, dan schema diff
│       └── layout.html           # Layout induk aplikasi (navbar, status sesi, flash messages)
├── compose.yaml                  # Konfigurasi Docker Compose database aplikasi
├── compose.test.yaml             # Konfigurasi Docker Compose database terisolasi untuk tes
├── PANDUAN_TESTING_MANUAL.md     # Panduan komprehensif skenario pengujian manual
└── README.md                     # Dokumentasi resmi proyek
```

---

## 8. Panduan Pengujian

### Pengujian Otomatis (Automated Testing)
1. **Unit Testing & Verifikasi Kode:**
   ```powershell
   go test -p 1 -count=1 ./...
   go vet ./...
   go build -o server.exe ./cmd/server
   ```
2. **Integration Testing Terisolasi:**
   Jalankan container pengujian khusus dan jalankan script pengujian integrasi (tidak mengganggu data demo):
   ```powershell
   docker compose -p wbd-test -f compose.test.yaml up -d --wait
   .\scripts\test-integration.ps1
   docker compose -p wbd-test -f compose.test.yaml stop
   ```
