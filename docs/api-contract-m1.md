# Kontrak REST JSON API — Milestone 1 (Migrasi SPA)
## Handoff Bagian 1 (Anggota 1 → Anggota 2 untuk SPA Tahap 1)

Seluruh endpoint di bawah disajikan pada origin yang sama dengan Frontend SPA (`frontend/dist` atau proxy Vite `/api` ke `http://127.0.0.1:8080`).

### Konvensi Autentikasi, Sesi, & CSRF
- **Sesi**: Cookie `session_id` (`HttpOnly`, `SameSite=Lax`, `Secure` pada production). Frontend SPA wajib memakai `credentials: 'include'`.
- **CSRF**: Setiap request mutasi (`POST`, `PUT`, `PATCH`, `DELETE`) selain `POST /api/v1/auth/login` wajib menyertakan header `X-CSRF-Token: <csrf_token>` (diperoleh dari respons `POST /api/v1/auth/login` atau `GET /api/v1/auth/me`).
- **Error Format**:
  ```json
  { "error": "Pesan error yang aman dan tersanitasi" }
  ```
- **HTTP Status**: `401 Unauthorized` (belum login / sesi kedaluwarsa), `403 Forbidden` (CSRF tidak valid / bukan admin / hak akses DB kurang), `404 Not Found`, `409 Conflict`, `422 Unprocessable Entity`, `503 Service Unavailable`.

---

### 1. Auth Endpoints

#### `POST /api/v1/auth/login`
- **Request**:
  ```json
  { "username": "admin", "password": "..." }
  ```
- **Response (`200 OK`)** + `Set-Cookie: session_id=...`:
  ```json
  {
    "user": {
      "id": "uuid",
      "username": "admin",
      "is_platform_admin": true,
      "is_active": true,
      "leader_group_ids": ["group-uuid"]
    },
    "csrf_token": "64-hex-token"
  }
  ```

#### `GET /api/v1/auth/me`
- **Response (`200 OK`)**: Struktur sama dengan respons `login`.

#### `POST /api/v1/auth/logout` *(Header `X-CSRF-Token` wajib)*
- **Response (`200 OK`)**: `{"status": "logged_out"}` + clear cookie `session_id`.

---

### 2. Platform Admin Endpoints *(Khusus `is_platform_admin = true`)*

#### Users
| Method & Path | Request Body | Response |
|---|---|---|
| `GET /api/v1/admin/users` | — | `{"users": [{"id","username","is_platform_admin","is_active","created_at","groups":[]}]}` |
| `POST /api/v1/admin/users` | `{"username","password","is_platform_admin"}` | `201 Created` user object |
| `POST /api/v1/admin/users/{id}/toggle-active` | — | `{"id","is_active"}` *(nonaktif langsung revoke session)* |
| `POST /api/v1/admin/users/{id}/toggle-admin` | — | `{"id","is_platform_admin"}` *(promosi rotate, demosi revoke)* |
| `POST /api/v1/admin/users/{id}/reset-password` | `{"new_password"}` | `{"status":"password_updated"}` *(revoke session)* |

#### Groups *(Wajib Penetapan `leader_user_id` untuk Milestone 2)*
| Method & Path | Request Body | Response |
|---|---|---|
| `GET /api/v1/admin/groups` | — | `{"groups": [{"id","name","leader_user_id","leader_username","created_at","members":[{"user_id","username"}]}]}` |
| `POST /api/v1/admin/groups` | `{"name", "leader_user_id"}` | `201 Created` *(leader otomatis masuk `user_groups`)* |
| `PUT /api/v1/admin/groups/{id}` | `{"name", "leader_user_id"}` | `200 OK` |
| `DELETE /api/v1/admin/groups/{id}` | — | `{"status":"deleted"}` *(revoke session anggota)* |
| `POST /api/v1/admin/groups/{id}/members` | `{"user_id"}` | `{"status":"added"}` *(rotate session)* |
| `DELETE /api/v1/admin/groups/{id}/members/{user_id}` | — | `{"status":"removed"}` *(ditolak `409` jika user masih leader grup)* |

#### Database Grants
| Method & Path | Request Body | Response |
|---|---|---|
| `GET /api/v1/admin/grants` | — | `{"grants": [{"group_id","group_name","database_id","database_name","dbms","access_level"}]}` |
| `POST /api/v1/admin/grants` | `{"group_id","database_id","access_level":"VIEW_ONLY\|VIEW_EDIT"}` | `200 OK` |
| `DELETE /api/v1/admin/grants?group_id=&database_id=` | *(atau JSON body)* | `{"status":"revoked"}` |

#### Target Database Connections *(PostgreSQL)*
| Method & Path | Request Body | Response |
|---|---|---|
| `GET /api/v1/admin/connections` | — | `{"connections": [{"id","name","dbms","host","port","db_name","username","created_at","updated_at"}]}` *(password tidak pernah dikembalikan)* |
| `POST /api/v1/admin/connections` | `{"name","dbms":"postgres","host","port":5432,"db_name","username","password"}` | `201 Created` |
| `PUT /api/v1/admin/connections/{id}` | payload sama (`password` opsional untuk mempertahankan yang lama) | `200 OK` |
| `DELETE /api/v1/admin/connections/{id}` | — | `{"status":"deleted"}` |
| `POST /api/v1/admin/connections/{id}/test` | — | `{"status":"ok","message":"..."}` |

---

### 3. Database Explorer Endpoints *(Memerlukan Grant Grup — Admin Tanpa Grant Ditolak `403`)*

| Method & Path | Akses Minimum | Keterangan |
|---|---|---|
| `GET /api/v1/databases` | Auth | `{"databases": [{...connection, "access_level", "lock_state"}]}` |
| `GET /api/v1/databases/{database_id}/schema` | `VIEW_ONLY` | `{"database","access_level","catalog":{"DBMS","Name","Schemas":[...]}}` |
| `GET /api/v1/databases/{database_id}/data?schema=public&table=t&page=1` | `VIEW_ONLY` | Paginasi 50 baris, tiap sel `{"Text","Null"}` |
| `POST /api/v1/databases/{database_id}/explain` | `VIEW_ONLY` (`analyze:true` butuh `VIEW_EDIT`) | Body `{"sql_query","analyze":false,"confirm_analyze":false}` → `{"plan":{...}}` |
| `POST /api/v1/databases/{database_id}/diff` | `VIEW_ONLY` | Body `{"target_schema_json":"{...}"}` → `{"diff_result","generated_sql","has_destructive"}` |

### 4. Kredensial Seed Pengembangan (`go run ./cmd/seed`)
| Username | Password Default | Peran |
|---|---|---|
| `admin` | `ADMIN_INITIAL_PASSWORD` di `.env` | Platform Admin |
| `leader_core` | `DevPass!2026` | Leader `team-core` (`VIEW_EDIT` pada DB1 & DB2) |
| `editor_core` | `DevPass!2026` | Member `team-core` (`VIEW_EDIT` pada DB1 & DB2) |
| `leader_analytics` | `DevPass!2026` | Leader `team-analytics` (`VIEW_ONLY` pada DB1) |
| `viewer_analytics` | `DevPass!2026` | Member `team-analytics` (`VIEW_ONLY` pada DB1) |
