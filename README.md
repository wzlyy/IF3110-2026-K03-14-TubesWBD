# IF3110-2026-K03-14-TubesWBD

Platform manajemen koneksi serta viewer database PostgreSQL/MySQL berbasis Go `net/http` dan Vanilla CSS. Modul editor serta runner migration SQL dikerjakan pada fase Anggota 3.

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
*(Pastikan `ENCRYPTION_KEY` terisi tepat 32 karakter dan port database sesuai).*

### 3. Jalankan Database Platform
Jalankan service database PostgreSQL platform menggunakan Docker Compose:
```powershell
docker compose up -d platform-db
```
*Catatan: Skema tabel dan akun admin awal (`admin` / `Admin@Secure123!`) akan otomatis diinisialisasi & dimigrasi saat server pertama kali berjalan.*

### 4. Jalankan Aplikasi
Jalankan server aplikasi Go:
```powershell
go run ./cmd/server
```

Aplikasi dapat diakses melalui browser pada:
👉 **[http://localhost:8080](http://localhost:8080)**

- **Username**: `admin`
- **Password**: `Admin@Secure123!`

---

## Menjalankan Unit & Integration Test
Unit test tidak memerlukan database target:
```powershell
go test -p 1 ./...
```

Integration test memakai **database platform dan target yang khusus untuk pengujian**. Tes fondasi menghapus isi tabel platform pada database yang dikonfigurasi, jadi jangan arahkan perintah berikut ke database demo atau produksi. Sediakan PostgreSQL 16 dan MySQL 8.0 kosong, atur `PLATFORM_DB_*`, `TARGET_PG_*`, `TARGET_MYSQL_*`, serta `ENCRYPTION_KEY` pada environment pengujian, lalu jalankan:

```powershell
go test -v ./internal/...
```