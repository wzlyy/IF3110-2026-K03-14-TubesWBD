# IF3110-2026-K03-14-TubesWBD

Platform manajemen koneksi, viewer, analisis query plan, serta migration SQL PostgreSQL/MySQL berbasis Go `net/http` dan Vanilla CSS. Migration core menyediakan editor/preview, transaksi, history/detail, retry, dan rekonsiliasi PENDING.

---

## Cara Menjalankan Program

### 1. Prasyarat
- **Go 1.27** (sesuai `go.mod`)
- **Docker & Docker Compose** (atau PostgreSQL lokal)

### 2. Konfigurasi Environment
Salin file `.env.example` menjadi `.env`:
```powershell
cp .env.example .env
```
Isi `ENCRYPTION_KEY` dengan secret tepat 32 byte, `ADMIN_INITIAL_PASSWORD` dengan password privat untuk bootstrap pertama, serta port database yang sesuai. Bootstrap tanpa secret atau memakai password default publik lama ditolak. Instalasi yang sudah memiliki admin tidak membutuhkan secret bootstrap lagi.

### 3. Jalankan Database Platform
Jalankan service database PostgreSQL platform menggunakan Docker Compose:
```powershell
docker compose up -d platform-db
```
Skema tabel dimigrasi secara aditif saat server pertama kali berjalan. Akun admin awal dibuat dari environment yang Anda isi.

### 4. Jalankan Aplikasi
Jalankan server aplikasi Go:
```powershell
go run ./cmd/server
```

Aplikasi dapat diakses melalui browser pada:
👉 **[http://localhost:8080](http://localhost:8080)**

- **Username**: `admin`
- **Password**: nilai `ADMIN_INITIAL_PASSWORD` yang Anda tetapkan sendiri.

---

## Menjalankan Unit & Integration Test
Unit test tidak memerlukan database target:
```powershell
go test -p 1 ./...
```

Integration test memakai **database platform dan target yang khusus untuk pengujian**. Tes fondasi menghapus isi tabel platform pada database yang dikonfigurasi, jadi jangan arahkan perintah berikut ke database demo atau produksi. Sediakan PostgreSQL 16 dan MySQL 8.0 kosong, atur `PLATFORM_DB_*`, `TARGET_PG_*`, `TARGET_MYSQL_*`, serta `ENCRYPTION_KEY` pada environment pengujian, lalu jalankan:

```powershell
go test -p 1 -v -tags=integration ./...