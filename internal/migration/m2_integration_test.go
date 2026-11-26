//go:build integration

package migration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/api"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/config"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/lock"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/migration"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/queue"
)

type spyPublisher struct {
	mu       sync.Mutex
	messages []queue.JobMessage
}

func (s *spyPublisher) PublishJob(_ context.Context, msg queue.JobMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, msg)
	return nil
}

func (s *spyPublisher) Close() error { return nil }

type m2TestEnv struct {
	db             *sql.DB
	mux            *http.ServeMux
	store          *database.ConnectionStore
	manager        *database.TargetManager
	locks          *lock.Manager
	pub            *spyPublisher
	adminSess      *auth.Session
	editorSess     *auth.Session
	viewerSess     *auth.Session
	leaderCoreSess *auth.Session
	leaderViewSess *auth.Session
	editorUserID   string
	leaderCoreID   string
	leaderViewID   string
	db1ID          string
}

func setupM2IntegrationEnv(t *testing.T) *m2TestEnv {
	t.Helper()
	if os.Getenv("WBD_TEST_DATABASES") != "1" {
		t.Skip("use scripts/test-integration.ps1 with dedicated test databases")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config load: %v", err)
	}
	db, err := database.NewPlatformDB(cfg)
	if err != nil {
		t.Fatalf("platform db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := database.MigratePlatformDB(ctx, db); err != nil {
		t.Fatalf("migrate platform db: %v", err)
	}
	_, err = db.ExecContext(ctx, `TRUNCATE execution_batch_progress, database_locks, migration_executions, migration_approvals, migration_requests, migration_definitions, session_grace_tokens, sessions, user_groups, group_database_grants, migrations, database_connections, groups, users CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}

	store, err := database.NewConnectionStore(db, cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	mgr := database.NewTargetManager(store)
	t.Cleanup(func() { _ = mgr.Close() })
	locks := lock.NewManager(db)
	pub := &spyPublisher{}

	mux := http.NewServeMux()
	api.RegisterRoutes(mux, api.Dependencies{
		DB:           db,
		Store:        store,
		Manager:      mgr,
		Locks:        locks,
		Publisher:    pub,
		SecureCookie: false,
	})

	createUser := func(username string, isAdmin bool) (string, *auth.Session) {
		id := uuid.NewString()
		hash, err := auth.HashPassword("TestPass!2026")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO users(id, username, password_hash, is_platform_admin, is_active) VALUES($1, $2, $3, $4, TRUE)`, id, username, hash, isAdmin); err != nil {
			t.Fatal(err)
		}
		sess, err := auth.CreateSession(ctx, db, id)
		if err != nil {
			t.Fatal(err)
		}
		return id, sess
	}

	_, adminSess := createUser("m2_admin", true)
	editorID, editorSess := createUser("m2_editor", false)
	viewerID, viewerSess := createUser("m2_viewer", false)
	leaderCoreID, leaderCoreSess := createUser("m2_leader_core", false)
	leaderViewID, leaderViewSess := createUser("m2_leader_view", false)

	pgPort, _ := strconv.Atoi(os.Getenv("TARGET_PG_PORT"))
	if pgPort == 0 {
		pgPort = 15433
	}
	conn, err := store.Create(ctx, database.Connection{
		Name:     "target-pg-test",
		DBMS:     "postgres",
		Host:     os.Getenv("TARGET_PG_HOST"),
		Port:     pgPort,
		DBName:   os.Getenv("TARGET_PG_NAME"),
		Username: os.Getenv("TARGET_PG_USER"),
	}, os.Getenv("TARGET_PG_PASSWORD"))
	if err != nil {
		t.Fatalf("create test pg connection: %v", err)
	}

	groupCoreID := uuid.NewString()
	groupViewID := uuid.NewString()
	mustExec := func(q string, args ...any) {
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`INSERT INTO groups(id, name, leader_user_id) VALUES($1, 'core-team', $2)`, groupCoreID, leaderCoreID)
	mustExec(`INSERT INTO groups(id, name, leader_user_id) VALUES($1, 'analytics-team', $2)`, groupViewID, leaderViewID)
	mustExec(`INSERT INTO user_groups(user_id, group_id) VALUES($1, $2), ($3, $2)`, leaderCoreID, groupCoreID, editorID)
	mustExec(`INSERT INTO user_groups(user_id, group_id) VALUES($1, $2), ($3, $2)`, leaderViewID, groupViewID, viewerID)
	mustExec(`INSERT INTO group_database_grants(group_id, database_id, access_level) VALUES($1, $2, 'VIEW_EDIT'), ($3, $2, 'VIEW_ONLY')`, groupCoreID, conn.ID, groupViewID)

	return &m2TestEnv{
		db:             db,
		mux:            mux,
		store:          store,
		manager:        mgr,
		locks:          locks,
		pub:            pub,
		adminSess:      adminSess,
		editorSess:     editorSess,
		viewerSess:     viewerSess,
		leaderCoreSess: leaderCoreSess,
		leaderViewSess: leaderViewSess,
		editorUserID:   editorID,
		leaderCoreID:   leaderCoreID,
		leaderViewID:   leaderViewID,
		db1ID:          conn.ID,
	}
}

func (e *m2TestEnv) doJSON(t *testing.T, method, path string, sess *auth.Session, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if sess != nil {
		req.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: sess.ID})
		req.Header.Set("X-CSRF-Token", sess.CSRFToken)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestM2EndToEndWorkflowApprovalsHistoryAndAccessDenial(t *testing.T) {
	env := setupM2IntegrationEnv(t)
	ctx := context.Background()

	// --- PEMERIKSAAN PENOLAKAN AKSES ---
	// 1. Request tanpa autentikasi mengembalikan JSON 401.
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/databases", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /databases = %d, want 401", rec.Code)
	}
	// 2. Pengguna VIEW_ONLY tidak boleh membuat definisi migrasi (403).
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.viewerSess, map[string]any{
		"name": "viewer_attempt", "sql_up": "CREATE TABLE x(id INT);", "sql_down": "DROP TABLE x;",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("VIEW_ONLY create migration = %d, want 403", rec.Code)
	}
	// 3. Platform Admin tanpa grant DB tidak dapat mengakses migrasi DB (403).
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/databases/"+env.db1ID+"/migrations", env.adminSess, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("admin without grant GET migrations = %d, want 403", rec.Code)
	}

	// --- ALUR SUKSES: DRAF -> EDIT -> SUBMIT -> PERSETUJUAN LEADER MULTI-GRUP -> ENQUEUE ---
	// Buat migrasi A (v1, reversible) dan B (v2, irreversible).
	recA := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":     "create_items",
		"sql_up":   "CREATE TABLE items (id BIGSERIAL PRIMARY KEY, name TEXT);",
		"sql_down": "DROP TABLE items;",
	})
	if recA.Code != http.StatusCreated {
		t.Fatalf("create mig A = %d: %s", recA.Code, recA.Body.String())
	}
	var migA migration.Definition
	_ = json.Unmarshal(recA.Body.Bytes(), &migA)

	// Edit draf sebelum eksekusi menaikkan nomor revisi.
	recEdit := env.doJSON(t, http.MethodPut, "/api/v1/databases/"+env.db1ID+"/migrations/"+migA.ID, env.editorSess, map[string]any{
		"name":     "create_items_v2",
		"sql_up":   "CREATE TABLE items (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);",
		"sql_down": "DROP TABLE items;",
	})
	if recEdit.Code != http.StatusOK {
		t.Fatalf("edit draft mig A = %d: %s", recEdit.Code, recEdit.Body.String())
	}
	_ = json.Unmarshal(recEdit.Body.Bytes(), &migA)
	if migA.Revision != 2 {
		t.Fatalf("expected revision 2 after draft edit, got %d", migA.Revision)
	}

	// Buat change request (up) untuk migrasi A.
	recReq := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id": migA.ID,
		"action_type":  "up",
	})
	if recReq.Code != http.StatusCreated {
		t.Fatalf("create request = %d: %s", recReq.Code, recReq.Body.String())
	}
	var reqA migration.RequestDetail
	_ = json.Unmarshal(recReq.Body.Bytes(), &reqA)

	// Submit untuk review menghasilkan 2 persetujuan wajib (core-team VIEW_EDIT & analytics-team VIEW_ONLY).
	recSub := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/submit", env.editorSess, nil)
	if recSub.Code != http.StatusOK {
		t.Fatalf("submit request = %d: %s", recSub.Code, recSub.Body.String())
	}
	_ = json.Unmarshal(recSub.Body.Bytes(), &reqA)
	if reqA.Status != migration.ReqStatusPendingReview || len(reqA.Approvals) != 2 {
		t.Fatalf("expected pending_review with 2 group approvals, got status=%s len=%d", reqA.Status, len(reqA.Approvals))
	}

	// Percobaan approve oleh non-leader ditolak dengan 403 Forbidden.
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/approve", env.editorSess, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-leader approve = %d, want 403", rec.Code)
	}

	// Leader 1 (grup VIEW_EDIT) menyetujui, status tetap pending_review dan belum bisa di-enqueue.
	recApp1 := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/approve", env.leaderCoreSess, map[string]string{"comment": "Core LGTM"})
	if recApp1.Code != http.StatusOK {
		t.Fatalf("leader 1 approve = %d: %s", recApp1.Code, recApp1.Body.String())
	}
	_ = json.Unmarshal(recApp1.Body.Bytes(), &reqA)
	if reqA.Status != migration.ReqStatusPendingReview {
		t.Fatalf("expected still pending_review after 1/2 approvals, got %s", reqA.Status)
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/enqueue", env.editorSess, nil); rec.Code != http.StatusConflict {
		t.Fatalf("enqueue before all approvals = %d, want 409", rec.Code)
	}

	// Leader 2 (grup VIEW_ONLY) menyetujui sehingga status berubah menjadi approved.
	recApp2 := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/approve", env.leaderViewSess, map[string]string{"comment": "Analytics compatible"})
	if recApp2.Code != http.StatusOK {
		t.Fatalf("leader 2 approve = %d: %s", recApp2.Code, recApp2.Body.String())
	}
	_ = json.Unmarshal(recApp2.Body.Bytes(), &reqA)
	if reqA.Status != migration.ReqStatusApproved {
		t.Fatalf("expected approved after 2/2 leader approvals, got %s", reqA.Status)
	}

	// Enqueue langsung mengembalikan 202 Accepted dengan job_id dan mengirim pesan ke queue.
	recEnq := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqA.ID+"/enqueue", env.editorSess, nil)
	if recEnq.Code != http.StatusAccepted {
		t.Fatalf("enqueue = %d: %s", recEnq.Code, recEnq.Body.String())
	}
	var enqRes migration.EnqueueResult
	_ = json.Unmarshal(recEnq.Body.Bytes(), &enqRes)
	if enqRes.JobID == "" || enqRes.ExecutionID == "" || len(env.pub.messages) != 1 {
		t.Fatalf("unexpected enqueue result %#v (published=%d)", enqRes, len(env.pub.messages))
	}

	// Simulasikan worker menandai immutable dan mencatat eksekusi sukses E1.
	if err := migration.MarkDefinitionImmutable(ctx, env.db, migA.ID); err != nil {
		t.Fatal(err)
	}
	_, err := env.db.ExecContext(ctx, `
		INSERT INTO migration_executions (
			execution_id, request_id, job_id, database_id, migration_id,
			migration_version, migration_revision, migration_name, action_type,
			executed_by, executed_sql_snapshot, execution_mode, status, started_at, finished_at
		) VALUES ($1, $2, $3, $4, $5, 1, 2, $6, 'up', $7, $8, 'normal', 'success', $9, $9)
	`, enqRes.ExecutionID, reqA.ID, enqRes.JobID, env.db1ID, migA.ID, migA.Name, env.editorUserID, migA.SQLUp, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	// Mengubah migrasi immutable harus gagal dengan 409 Conflict.
	if rec := env.doJSON(t, http.MethodPut, "/api/v1/databases/"+env.db1ID+"/migrations/"+migA.ID, env.editorSess, map[string]any{
		"name": "tamper", "sql_up": "SELECT 2;", "sql_down": "SELECT 1;",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("edit immutable migration = %d, want 409", rec.Code)
	}

	// --- VERIFIKASI KEGAGALAN DAN HIDE ROLLBACK ---
	// Catat rollback E2 (sukses), percobaan gagal E3 (Revisi 2 & 4), dan forward E4 (sukses).
	e2ID, e3ID, e4ID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	insertExec := func(id, action, reverted, status, sqlSnap string, offset time.Duration) {
		var rev any
		if reverted != "" {
			rev = reverted
		}
		_, err := env.db.ExecContext(ctx, `
			INSERT INTO migration_executions (
				execution_id, request_id, job_id, database_id, migration_id,
				migration_version, migration_revision, migration_name, action_type,
				reverted_execution_id, executed_by, executed_sql_snapshot, execution_mode,
				status, started_at, finished_at
			) VALUES ($1, $2, $3, $4, $5, 1, 2, $6, $7, $8, $9, $10, 'normal', $11, $12, $12)
		`, id, reqA.ID, uuid.NewString(), env.db1ID, migA.ID, migA.Name, action, rev, env.editorUserID, sqlSnap, status, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
	}
	insertExec(e2ID, "rollback", enqRes.ExecutionID, "success", migA.SQLDown, 1*time.Second)
	insertExec(e3ID, "forward", "", "failed", migA.SQLUp, 2*time.Second)
	insertExec(e4ID, "forward", "", "success", migA.SQLUp, 3*time.Second)

	// Riwayat penuh menampilkan seluruh 4 percobaan.
	recHistAll := env.doJSON(t, http.MethodGet, "/api/v1/databases/"+env.db1ID+"/history?hide_rollback=false", env.viewerSess, nil)
	if recHistAll.Code != http.StatusOK {
		t.Fatalf("history full = %d: %s", recHistAll.Code, recHistAll.Body.String())
	}
	var histAll migration.HistoryResponse
	_ = json.Unmarshal(recHistAll.Body.Bytes(), &histAll)
	if len(histAll.Executions) != 4 {
		t.Fatalf("expected 4 total attempts, got %d", len(histAll.Executions))
	}

	// Riwayat dengan hide_rollback=true menyembunyikan (E1, E2) serta mempertahankan E3 gagal dan E4 forward.
	recHistHide := env.doJSON(t, http.MethodGet, "/api/v1/databases/"+env.db1ID+"/history?hide_rollback=true", env.viewerSess, nil)
	var histHide migration.HistoryResponse
	_ = json.Unmarshal(recHistHide.Body.Bytes(), &histHide)
	if len(histHide.Executions) != 2 || histHide.HiddenCount != 2 {
		t.Fatalf("hide_rollback=true expected 2 visible (2 hidden), got %d (%d hidden)", len(histHide.Executions), histHide.HiddenCount)
	}
	if histHide.Executions[0].ExecutionID != e3ID || histHide.Executions[1].ExecutionID != e4ID {
		t.Fatalf("expected [E3(failed), E4(forward)], got [%s, %s]", histHide.Executions[0].ExecutionID, histHide.Executions[1].ExecutionID)
	}

	// Migrasi irreversible tidak dapat diajukan rollback (409).
	recIrrev := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":                "drop_legacy",
		"sql_up":              "DROP TABLE IF EXISTS legacy_data;",
		"is_irreversible":     true,
		"irreversible_reason": "Data legacy sudah diarsipkan",
	})
	var migIrrev migration.Definition
	_ = json.Unmarshal(recIrrev.Body.Bytes(), &migIrrev)
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id": migIrrev.ID,
		"action_type":  "rollback",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("rollback irreversible = %d, want 409", rec.Code)
	}

	// Endpoint status job mengembalikan 200 beserta detail eksekusi.
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/jobs/"+enqRes.JobID, env.viewerSess, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /jobs/{job_id} = %d: %s", rec.Code, rec.Body.String())
	}

	// A second continuation must preview the root attempt, even when the
	// immediate parent stored a different snapshot.
	root, retry := uuid.NewString(), uuid.NewString()
	insertExec(root, "rollback", e4ID, "partial", "SELECT 42", 4*time.Second)
	insertExec(retry, "resume_batch", "", "partial", "SELECT 99", 5*time.Second)
	if _, err := env.db.ExecContext(ctx, "UPDATE migration_executions SET parent_execution_id=$1 WHERE execution_id=$2", root, retry); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{"resume_batch": "SELECT 42", "revert_batch": migA.SQLUp} {
		id := uuid.NewString()
		if _, err := env.db.ExecContext(ctx, `INSERT INTO migration_requests
			(id,database_id,migration_id,action_type,target_execution_id) VALUES ($1,$2,$3,$4,$5)`,
			id, env.db1ID, migA.ID, action, retry); err != nil {
			t.Fatal(err)
		}
		detail, err := migration.GetRequestDetail(ctx, env.db, id)
		if err != nil {
			t.Fatal(err)
		}
		if detail.SQLPreview != want {
			t.Fatalf("%s preview = %q, want %q", action, detail.SQLPreview, want)
		}
	}

}

func TestM2AuthAdminExplorerBatchValidationAndRejectionIntegration(t *testing.T) {
	env := setupM2IntegrationEnv(t)
	ctx := context.Background()

	// 1. Alur autentikasi JSON: Login, Me, dan Logout.
	recLogin := env.doJSON(t, http.MethodPost, "/api/v1/auth/login", nil, map[string]string{
		"username": "m2_leader_core",
		"password": "TestPass!2026",
	})
	if recLogin.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/login = %d: %s", recLogin.Code, recLogin.Body.String())
	}
	var loginBody struct {
		User struct {
			ID             string   `json:"id"`
			Username       string   `json:"username"`
			LeaderGroupIDs []string `json:"leader_group_ids"`
		} `json:"user"`
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.Unmarshal(recLogin.Body.Bytes(), &loginBody)
	if loginBody.CSRFToken == "" || len(loginBody.User.LeaderGroupIDs) != 1 {
		t.Fatalf("unexpected login payload: %+v", loginBody)
	}

	// 2. Pembuatan grup admin mewajibkan leader_user_id (422 tanpa leader, 201 dengan leader aktif).
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/admin/groups", env.adminSess, map[string]string{
		"name": "no-leader-group",
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create group without leader = %d, want 422", rec.Code)
	}
	recGrp := env.doJSON(t, http.MethodPost, "/api/v1/admin/groups", env.adminSess, map[string]string{
		"name":           "ops-team",
		"leader_user_id": env.leaderCoreID,
	})
	if recGrp.Code != http.StatusCreated {
		t.Fatalf("create group with leader = %d: %s", recGrp.Code, recGrp.Body.String())
	}

	// 3. Siapkan tabel nyata di Target PostgreSQL lalu uji API Schema, Data, Explain, Diff, dan validasi batch.
	targetDB, _, err := env.manager.Open(ctx, env.db1ID)
	if err != nil {
		t.Fatalf("open target pg: %v", err)
	}
	if _, err := targetDB.ExecContext(ctx, `
		DROP TABLE IF EXISTS public.m2_batch_orders;
		CREATE TABLE public.m2_batch_orders (
			id BIGSERIAL PRIMARY KEY,
			partial_key TEXT NOT NULL DEFAULT 'k1',
			status TEXT NOT NULL DEFAULT 'pending'
		);
		CREATE UNIQUE INDEX m2_batch_orders_partial_uidx ON public.m2_batch_orders (partial_key) WHERE status = 'pending';
		INSERT INTO public.m2_batch_orders (partial_key, status) VALUES ('k1', 'pending'), ('k2', 'pending');
	`); err != nil {
		t.Fatalf("seed target table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = targetDB.ExecContext(context.Background(), `DROP TABLE IF EXISTS public.m2_batch_orders;`)
	})

	if rec := env.doJSON(t, http.MethodGet, "/api/v1/databases", env.viewerSess, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/databases = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/databases/"+env.db1ID+"/schema", env.viewerSess, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /schema = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodGet, "/api/v1/databases/"+env.db1ID+"/data?schema=public&table=m2_batch_orders&page=1", env.viewerSess, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /data = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/explain", env.viewerSess, map[string]any{
		"sql_query": "SELECT * FROM public.m2_batch_orders WHERE id = 1",
		"analyze":   false,
	}); rec.Code != http.StatusOK {
		t.Fatalf("POST /explain = %d: %s", rec.Code, rec.Body.String())
	}

	// 4. Validasi definisi batch: kolom cursor non-unik -> 422; WHERE non-BETWEEN -> 422; kolom cursor PK + BETWEEN -> 201.
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":                "bad_batch_cursor",
		"sql_up":              "UPDATE public.m2_batch_orders SET status = 'done' WHERE status BETWEEN $1 AND $2;",
		"sql_down":            "UPDATE public.m2_batch_orders SET status = 'pending' WHERE status BETWEEN $1 AND $2;",
		"execution_mode":      "batch",
		"batch_table":         "public.m2_batch_orders",
		"batch_cursor_column": "status",
		"batch_size":          100,
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("batch with non-unique cursor = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":                "bad_batch_partial_uidx",
		"sql_up":              "UPDATE public.m2_batch_orders SET status = 'done' WHERE partial_key BETWEEN $1 AND $2;",
		"sql_down":            "UPDATE public.m2_batch_orders SET status = 'pending' WHERE partial_key BETWEEN $1 AND $2;",
		"execution_mode":      "batch",
		"batch_table":         "public.m2_batch_orders",
		"batch_cursor_column": "partial_key",
		"batch_size":          100,
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("batch with partial unique index cursor = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":                "bad_batch_where",
		"sql_up":              "UPDATE public.m2_batch_orders SET status = 'done' WHERE id > $1 AND id <= $2;",
		"sql_down":            "UPDATE public.m2_batch_orders SET status = 'pending' WHERE id BETWEEN $1 AND $2;",
		"execution_mode":      "batch",
		"batch_table":         "public.m2_batch_orders",
		"batch_cursor_column": "id",
		"batch_size":          100,
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("batch with non-BETWEEN WHERE = %d, want 422 (%s)", rec.Code, rec.Body.String())
	}

	recBatch := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/migrations", env.editorSess, map[string]any{
		"name":                "good_batch_orders",
		"sql_up":              "UPDATE public.m2_batch_orders SET status = 'done' WHERE id BETWEEN $1 AND $2;",
		"sql_down":            "UPDATE public.m2_batch_orders SET status = 'pending' WHERE id BETWEEN $1 AND $2;",
		"execution_mode":      "batch",
		"batch_table":         "public.m2_batch_orders",
		"batch_cursor_column": "id",
		"batch_size":          100,
	})
	if recBatch.Code != http.StatusCreated {
		t.Fatalf("batch with PK cursor = %d: %s", recBatch.Code, recBatch.Body.String())
	}
	var migBatch migration.Definition
	_ = json.Unmarshal(recBatch.Body.Bytes(), &migBatch)

	// 5. Alur penolakan: leader menolak -> status menjadi rejected dan tidak bisa di-enqueue (409).
	recReq := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id": migBatch.ID,
		"action_type":  "up",
	})
	var reqDetail migration.RequestDetail
	_ = json.Unmarshal(recReq.Body.Bytes(), &reqDetail)

	if rec := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqDetail.ID+"/submit", env.editorSess, nil); rec.Code != http.StatusOK {
		t.Fatalf("submit batch request = %d: %s", rec.Code, rec.Body.String())
	}
	recRej := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqDetail.ID+"/reject", env.leaderViewSess, map[string]string{
		"comment": "Wait until analytics ETL completes",
	})
	if recRej.Code != http.StatusOK {
		t.Fatalf("reject request = %d: %s", recRej.Code, recRej.Body.String())
	}
	_ = json.Unmarshal(recRej.Body.Bytes(), &reqDetail)
	if reqDetail.Status != migration.ReqStatusRejected {
		t.Fatalf("expected status 'rejected', got %q", reqDetail.Status)
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/requests/"+reqDetail.ID+"/enqueue", env.editorSess, nil); rec.Code != http.StatusConflict {
		t.Fatalf("enqueue rejected request = %d, want 409", rec.Code)
	}

	// 6. Eksekusi batch partial memblokir request lain (409) sambil mengizinkan resume_batch dan revert_batch.
	partialExecID := uuid.NewString()
	_, err = env.db.ExecContext(ctx, `
		INSERT INTO migration_executions (
			execution_id, request_id, job_id, database_id, migration_id,
			migration_version, migration_revision, migration_name, action_type,
			executed_by, executed_sql_snapshot, execution_mode, status,
			total_batches_committed, total_rows_affected
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'up', $9, $10, 'batch', 'partial', 3, 300)
	`, partialExecID, reqDetail.ID, uuid.NewString(), env.db1ID, migBatch.ID, migBatch.Version, migBatch.Revision, migBatch.Name, env.editorUserID, migBatch.SQLUp)
	if err != nil {
		t.Fatal(err)
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id": migBatch.ID,
		"action_type":  "up",
	}); rec.Code != http.StatusConflict {
		t.Fatalf("unrelated 'up' while partial exists = %d, want 409", rec.Code)
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id":        migBatch.ID,
		"action_type":         "resume_batch",
		"target_execution_id": partialExecID,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("resume_batch targeting partial execution = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := env.doJSON(t, http.MethodPost, "/api/v1/databases/"+env.db1ID+"/requests", env.editorSess, map[string]any{
		"migration_id":        migBatch.ID,
		"action_type":         "revert_batch",
		"target_execution_id": partialExecID,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("revert_batch targeting partial execution = %d: %s", rec.Code, rec.Body.String())
	}

	// 7. GET /api/v1/requests memuat persetujuan secara batch (mengisi 2 approval untuk submitted dan [] untuk draft).
	recReqs := env.doJSON(t, http.MethodGet, "/api/v1/requests", env.viewerSess, nil)
	if recReqs.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/requests = %d: %s", recReqs.Code, recReqs.Body.String())
	}
	var listResp struct {
		Requests []migration.RequestDetail `json:"requests"`
	}
	if err := json.Unmarshal(recReqs.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode GET /requests: %v", err)
	}
	if len(listResp.Requests) < 3 {
		t.Fatalf("expected at least 3 requests, got %d", len(listResp.Requests))
	}
	var sawSubmitted, sawDraft bool
	for _, r := range listResp.Requests {
		if r.Approvals == nil {
			t.Fatalf("request %s returned nil approvals instead of empty slice", r.ID)
		}
		if r.ID == reqDetail.ID && len(r.Approvals) == 2 {
			sawSubmitted = true
		}
		if r.Status == migration.ReqStatusDraft && len(r.Approvals) == 0 {
			sawDraft = true
		}
	}
	if !sawSubmitted || !sawDraft {
		t.Fatalf("GET /requests did not hydrate approvals as expected: sawSubmitted=%v sawDraft=%v", sawSubmitted, sawDraft)
	}
}
