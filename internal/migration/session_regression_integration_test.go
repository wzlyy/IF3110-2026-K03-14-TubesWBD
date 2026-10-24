//go:build integration

package migration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
)

func TestPeriodicRotationThroughMiddleware(t *testing.T) {
	f := migrationFixture(t, "postgres")
	s, err := auth.CreateSession(context.Background(), f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET last_active_at=$1 WHERE id=$2", time.Now().UTC().Add(-16*time.Minute), s.ID); err != nil {
		t.Fatal(err)
	}
	// Existing rows predate the independent rotation clock; use an old creation
	// time too so the upgrade/backfill and middleware contract are both covered.
	if _, err := f.db.Exec("UPDATE sessions SET created_at=$1,last_rotated_at=$1,last_active_at=$3 WHERE id=$2", time.Now().UTC().Add(-16*time.Minute), s.ID, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	h := auth.RequireAuth(f.db, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	r := httptest.NewRequest("GET", "/protected", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: s.ID})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var id string
	if err := f.db.QueryRow("SELECT id FROM sessions WHERE user_id=$1", f.user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == s.ID {
		t.Fatal("middleware failed to rotate a session older than rotation interval")
	}
	previous, wasPrevious, err := auth.GetSession(context.Background(), f.db, s.ID)
	if err != nil || !wasPrevious || previous.ID != id {
		t.Fatalf("old token must survive grace: %v", err)
	}
	if !previous.ExpiresAt.Truncate(time.Microsecond).Equal(s.ExpiresAt.Truncate(time.Microsecond)) {
		t.Fatal("rotation extended absolute expiration")
	}
}

func TestLogoutRouteRequiresCSRF(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	h := &auth.AuthHandler{DB: f.db}
	h.RegisterRoutes(f.mux, auth.RequireAuth(f.db, false))
	for _, token := range []string{"", "invalid"} {
		r := httptest.NewRequest("POST", "/logout", strings.NewReader("csrf_token="+token))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: f.session.ID})
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("invalid/missing CSRF accepted: %d", w.Code)
		}
		if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil {
			t.Fatal("rejected logout changed session")
		}
	}
	w := f.request("POST", "/logout", nil, true)
	if w.Code != 303 {
		t.Fatalf("valid logout: %d", w.Code)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err == nil {
		t.Fatal("logout left session valid")
	}
}

func TestLogoutPersistenceFailureDoesNotReportSuccess(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	denySessionMutation(t, f.fixture, "DELETE")
	h := &auth.AuthHandler{DB: f.db}
	h.RegisterRoutes(f.mux, auth.RequireAuth(f.db, false))
	w := f.request("POST", "/logout", nil, true)
	if w.Code < 500 {
		t.Fatalf("logout reported success while session deletion failed: %d", w.Code)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil {
		t.Fatal("failed logout must preserve session for retry")
	}
}

func TestPeriodicRotationConcurrentRequests(t *testing.T) {
	f := migrationFixture(t, "postgres")
	s, err := auth.CreateSession(context.Background(), f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET last_rotated_at=$1 WHERE id=$2", time.Now().UTC().Add(-16*time.Minute), s.ID); err != nil {
		t.Fatal(err)
	}
	h := auth.RequireAuth(f.db, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	var wg sync.WaitGroup
	statuses := make(chan int, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("GET", "/protected", nil)
			r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: s.ID})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != 200 {
			t.Errorf("parallel request lost rotation grace: %d", status)
		}
	}
	var count int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE user_id=$1", f.user).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rotation forked sessions: %d %v", count, err)
	}
	rotated, wasPrevious, err := auth.GetSession(context.Background(), f.db, s.ID)
	if err != nil || !wasPrevious || rotated.ID == s.ID {
		t.Fatalf("grace failed: %v", err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET grace_until=$1 WHERE id=$2", time.Now().UTC().Add(-time.Second), rotated.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, s.ID); err == nil {
		t.Fatal("old token survived grace deadline")
	}
}

func TestPeriodicRotationFailureIsFailClosed(t *testing.T) {
	f := migrationFixture(t, "postgres")
	s, err := auth.CreateSession(context.Background(), f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET last_rotated_at=$1 WHERE id=$2", time.Now().UTC().Add(-16*time.Minute), s.ID); err != nil {
		t.Fatal(err)
	}
	denySessionMutation(t, f, "UPDATE")
	h := auth.RequireAuth(f.db, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("protected action ran despite rotation failure") }))
	r := httptest.NewRequest("GET", "/protected", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: s.ID})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("rotation failure not reported: %d", w.Code)
	}
}
