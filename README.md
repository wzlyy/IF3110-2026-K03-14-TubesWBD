# IF3110-2026-K03-14-TubesWBD

Platform manajemen database, multi-DBMS target viewer, dan runner migration SQL berbasis Go murni (standar web library `net/http` + Vanilla CSS).

---

## Cara Menjalankan Program

### 1. Prasyarat
- **Go** (versi 1.22 atau lebih baru)
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
Untuk memastikan seluruh modul fondasi, autentikasi, sesi, dan access control berjalan dengan baik:
```powershell
go test -v ./internal/...
```