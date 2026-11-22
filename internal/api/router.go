package api

import (
	"database/sql"
	"net/http"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/telemetry"
)

// Dependencies menampung seluruh layanan backend untuk router REST JSON API.
type Dependencies struct {
	DB           *sql.DB
	Store        *database.ConnectionStore
	Manager      *database.TargetManager
	Locks        *lock.Manager
	Publisher    queue.Publisher
	SecureCookie bool
	Metrics      *telemetry.Metrics
}

// RegisterRoutes mendaftarkan seluruh endpoint /api/v1/* dan Prometheus /metrics ke mux.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	requireAuth := auth.RequireAuth(deps.DB, deps.SecureCookie)
	csrf := auth.CSRFProtectionMiddleware

	protectUser := func(fn http.HandlerFunc) http.Handler {
		return requireAuth(csrf(fn))
	}
	protectAdmin := func(fn http.HandlerFunc) http.Handler {
		return requireAuth(csrf(admin.RequireAdmin(fn)))
	}

	authAPI := &AuthAPIHandler{DB: deps.DB, SecureCookie: deps.SecureCookie}
	adminAPI := &AdminAPIHandler{DB: deps.DB, Store: deps.Store, Manager: deps.Manager}
	dbAPI := &DatabaseAPIHandler{DB: deps.DB, Store: deps.Store, Manager: deps.Manager, Locks: deps.Locks}
	m2API := &M2APIHandler{
		DB:      deps.DB,
		Store:   deps.Store,
		Manager: deps.Manager,
		Locks:   deps.Locks,
		Workflow: &migration.WorkflowService{
			DB:        deps.DB,
			Locks:     deps.Locks,
			Publisher: deps.Publisher,
		},
	}

	// Cek kesehatan dan metrik Prometheus (Bonus Observabilitas Bagian A)
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	metricsReg := deps.Metrics
	if metricsReg == nil {
		metricsReg = telemetry.GlobalMetrics()
	}
	mux.Handle("GET /metrics", metricsReg.Handler())

	// Autentikasi
	mux.HandleFunc("POST /api/v1/auth/login", authAPI.Login)
	mux.Handle("POST /api/v1/auth/logout", protectUser(authAPI.Logout))
	mux.Handle("GET /api/v1/auth/me", protectUser(authAPI.Me))

	// Admin: Pengguna
	mux.Handle("GET /api/v1/admin/users", protectAdmin(adminAPI.ListUsers))
	mux.Handle("POST /api/v1/admin/users", protectAdmin(adminAPI.CreateUser))
	mux.Handle("POST /api/v1/admin/users/{id}/toggle-active", protectAdmin(adminAPI.ToggleUserActive))
	mux.Handle("POST /api/v1/admin/users/{id}/toggle-admin", protectAdmin(adminAPI.ToggleUserAdmin))
	mux.Handle("POST /api/v1/admin/users/{id}/reset-password", protectAdmin(adminAPI.ResetUserPassword))

	// Admin: Grup dan Pemimpin Grup
	mux.Handle("GET /api/v1/admin/groups", protectAdmin(adminAPI.ListGroups))
	mux.Handle("POST /api/v1/admin/groups", protectAdmin(adminAPI.CreateGroup))
	mux.Handle("PUT /api/v1/admin/groups/{id}", protectAdmin(adminAPI.UpdateGroup))
	mux.Handle("DELETE /api/v1/admin/groups/{id}", protectAdmin(adminAPI.DeleteGroup))
	mux.Handle("POST /api/v1/admin/groups/{id}/members", protectAdmin(adminAPI.AddGroupMember))
	mux.Handle("DELETE /api/v1/admin/groups/{id}/members/{user_id}", protectAdmin(adminAPI.RemoveGroupMember))

	// Admin: Hak Akses Database
	mux.Handle("GET /api/v1/admin/grants", protectAdmin(adminAPI.ListGrants))
	mux.Handle("POST /api/v1/admin/grants", protectAdmin(adminAPI.AssignGrant))
	mux.Handle("DELETE /api/v1/admin/grants", protectAdmin(adminAPI.RevokeGrant))

	// Admin: Koneksi Database Target
	mux.Handle("GET /api/v1/admin/connections", protectAdmin(adminAPI.ListConnections))
	mux.Handle("POST /api/v1/admin/connections", protectAdmin(adminAPI.CreateConnection))
	mux.Handle("PUT /api/v1/admin/connections/{id}", protectAdmin(adminAPI.UpdateConnection))
	mux.Handle("DELETE /api/v1/admin/connections/{id}", protectAdmin(adminAPI.DeleteConnection))
	mux.Handle("POST /api/v1/admin/connections/{id}/test", protectAdmin(adminAPI.TestConnection))

	// Explorer Database (M1 dipindahkan ke JSON API)
	mux.Handle("GET /api/v1/databases", protectUser(dbAPI.ListDatabases))
	mux.Handle("GET /api/v1/databases/{database_id}/schema", protectUser(dbAPI.GetSchema))
	mux.Handle("GET /api/v1/databases/{database_id}/data", protectUser(dbAPI.GetData))
	mux.Handle("POST /api/v1/databases/{database_id}/explain", protectUser(dbAPI.Explain))
	mux.Handle("POST /api/v1/databases/{database_id}/diff", protectUser(dbAPI.Diff))

	// Definisi Migrasi M2
	mux.Handle("GET /api/v1/databases/{database_id}/migrations", protectUser(m2API.ListDefinitions))
	mux.Handle("POST /api/v1/databases/{database_id}/migrations", protectUser(m2API.CreateDefinition))
	mux.Handle("GET /api/v1/databases/{database_id}/migrations/{migration_id}", protectUser(m2API.GetDefinition))
	mux.Handle("PUT /api/v1/databases/{database_id}/migrations/{migration_id}", protectUser(m2API.UpdateDefinition))
	mux.Handle("POST /api/v1/databases/{database_id}/migrations/validate", protectUser(m2API.ValidateDefinition))

	// Change Request M2 dan Alur Persetujuan Leader Multi-Grup
	mux.Handle("GET /api/v1/requests", protectUser(m2API.ListRequests))
	mux.Handle("GET /api/v1/databases/{database_id}/requests", protectUser(m2API.ListRequests))
	mux.Handle("POST /api/v1/databases/{database_id}/requests", protectUser(m2API.CreateRequest))
	mux.Handle("GET /api/v1/requests/{request_id}", protectUser(m2API.GetRequest))
	mux.Handle("POST /api/v1/requests/{request_id}/submit", protectUser(m2API.SubmitRequest))
	mux.Handle("POST /api/v1/requests/{request_id}/approve", protectUser(m2API.ApproveRequest))
	mux.Handle("POST /api/v1/requests/{request_id}/reject", protectUser(m2API.RejectRequest))
	mux.Handle("POST /api/v1/requests/{request_id}/enqueue", protectUser(m2API.EnqueueRequest))

	// Riwayat Append-Only M2 (+ Hide rollback) dan Pemantauan Job Asinkron
	mux.Handle("GET /api/v1/databases/{database_id}/history", protectUser(m2API.History))
	mux.Handle("GET /api/v1/jobs/{job_id}", protectUser(m2API.GetJobStatus))
	mux.Handle("GET /api/v1/executions/{execution_id}", protectUser(m2API.GetExecutionDetail))
}
