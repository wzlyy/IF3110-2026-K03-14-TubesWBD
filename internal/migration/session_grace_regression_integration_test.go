//go:build integration

package migration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func TestSessionGraceSurvivesRepeatedRotations(t *testing.T) {
	f := migrationFixture(t, "postgres")
	ctx := context.Background()
	original, err := auth.CreateSession(ctx, f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	first, err := auth.RotateSession(ctx, f.db, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := auth.RotateSession(ctx, f.db, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{original.ID, first.ID} {
		s, previous, err := auth.GetSession(ctx, f.db, token)
		if err != nil || !previous || s.ID != latest.ID {
			t.Fatalf("earlier token must resolve to latest rotation during its grace: previous=%v err=%v", previous, err)
		}
		if !s.CreatedAt.Truncate(time.Microsecond).Equal(original.CreatedAt.Truncate(time.Microsecond)) || !s.ExpiresAt.Truncate(time.Microsecond).Equal(original.ExpiresAt.Truncate(time.Microsecond)) {
			t.Fatal("repeated rotation changed the session's absolute lifetime")
		}
	}
	var originalDeadline time.Time
	if err := f.db.QueryRow("SELECT expires_at FROM session_grace_tokens WHERE token=$1", original.ID).Scan(&originalDeadline); err != nil {
		t.Fatal(err)
	}
	if !originalDeadline.Equal(first.GraceUntil.Time) {
		t.Fatal("later rotation extended the original token's grace deadline")
	}
	// A delayed response still carrying the first token must receive the latest cookie.
	h := auth.RequireAuth(f.db, false)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	r := httptest.NewRequest("GET", "/protected", nil)
	r.AddCookie(&http.Cookie{Name: auth.CookieSessionName, Value: original.ID})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("earlier grace token logged client out: %d", w.Code)
	}
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieSessionName {
			cookie = c.Value
		}
	}
	if cookie != latest.ID {
		t.Fatal("earlier grace token did not refresh to latest cookie")
	}
	if err := auth.RevokeSession(ctx, f.db, original.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := f.db.QueryRow("SELECT (SELECT COUNT(*) FROM sessions WHERE user_id=$1)+(SELECT COUNT(*) FROM session_grace_tokens)", f.user).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("logout through earlier token left canonical session or grace tokens: %d %v", remaining, err)
	}
}

func TestSessionGraceDeadlinesRemainIndependent(t *testing.T) {
	f := migrationFixture(t, "postgres")
	ctx := context.Background()
	original, err := auth.CreateSession(ctx, f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	first, err := auth.RotateSession(ctx, f.db, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Advance only the old token's deadline; a later rotation must not renew it.
	deadline := time.Now().UTC().Add(-time.Second)
	if _, err := f.db.Exec("UPDATE sessions SET grace_until=$1 WHERE id=$2", deadline, first.ID); err != nil {
		t.Fatal(err)
	}
	latest, err := auth.RotateSession(ctx, f.db, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, original.ID); err == nil {
		t.Fatal("expired earlier token became valid after another rotation")
	}
	if s, previous, err := auth.GetSession(ctx, f.db, first.ID); err != nil || !previous || s.ID != latest.ID {
		t.Fatalf("earlier deadline expired the newest grace token: %v", err)
	}
	var stale int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM session_grace_tokens WHERE token=$1", original.ID).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("expired grace tokens were not cleaned up: %d %v", stale, err)
	}
	// The legacy immediate-previous deadline remains authoritative.
	if _, err := f.db.Exec("UPDATE sessions SET grace_until=$1 WHERE id=$2", deadline, latest.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, first.ID); err == nil {
		t.Fatal("alias bypassed expired immediate-previous deadline")
	}
	// Expiry must also be enforced when the presented token is already older
	// than previous_id and the canonical session remains active.
	if _, err := auth.RotateSession(ctx, f.db, latest.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, first.ID); err == nil {
		t.Fatal("another rotation renewed an expired grace token")
	}
}

func TestSessionGraceRapidPrivilegeEscalations(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_ONLY")
	h := &admin.GroupAdminHandler{DB: f.db}
	for i := 0; i < 2; i++ {
		group := uuid.NewString()
		if _, err := f.db.Exec("INSERT INTO groups(id,name) VALUES($1,$1)", group); err != nil {
			t.Fatal(err)
		}
		w := callAdminMutation(h.AddMember, group, url.Values{"user_id": {f.user}})
		if w.Code != http.StatusSeeOther {
			t.Fatalf("membership escalation failed: %d", w.Code)
		}
	}
	var latest string
	if err := f.db.QueryRow("SELECT id FROM sessions WHERE user_id=$1", f.user).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if s, previous, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil || !previous || s.ID != latest {
		t.Fatalf("rapid privilege escalations invalidated original grace token: %v", err)
	}
}

func TestSessionGraceExpiredOlderAliasIsDenied(t *testing.T) {
	f := migrationFixture(t, "postgres")
	ctx := context.Background()
	s, err := auth.CreateSession(ctx, f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	first, err := auth.RotateSession(ctx, f.db, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := auth.RotateSession(ctx, f.db, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE session_grace_tokens SET expires_at=$1 WHERE token=$2", time.Now().UTC().Add(-time.Second), s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, s.ID); err == nil {
		t.Fatal("expired older alias authenticated an active canonical session")
	}
	if _, _, err := auth.GetSession(ctx, f.db, latest.ID); err != nil {
		t.Fatalf("expired alias invalidated the current token: %v", err)
	}
	var aliases int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM session_grace_tokens WHERE token=$1", s.ID).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("expired alias was not cleaned up on demand: %d %v", aliases, err)
	}
}

func TestSessionGraceClearedLegacyDeadlineStaysInvalid(t *testing.T) {
	f := migrationFixture(t, "postgres")
	ctx := context.Background()
	s, err := auth.CreateSession(ctx, f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	first, err := auth.RotateSession(ctx, f.db, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE sessions SET grace_until=NULL WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, s.ID); err == nil {
		t.Fatal("cleared legacy deadline accepted the immediate previous token")
	}
	if _, err := auth.RotateSession(ctx, f.db, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := auth.GetSession(ctx, f.db, s.ID); err == nil {
		t.Fatal("later rotation revived a grace token with a cleared deadline")
	}
}

func TestSessionGraceMigrationBackfillsLegacyPreviousToken(t *testing.T) {
	f := migrationFixture(t, "postgres")
	ctx := context.Background()
	s, err := auth.CreateSession(ctx, f.db, f.user)
	if err != nil {
		t.Fatal(err)
	}
	legacy := "legacy-previous-token"
	deadline := time.Now().UTC().Add(20 * time.Second).Truncate(time.Microsecond)
	if _, err := f.db.Exec("UPDATE sessions SET previous_id=$1,grace_until=$2 WHERE id=$3", legacy, deadline, s.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := database.MigratePlatformDB(ctx, f.db); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := auth.RotateSession(ctx, f.db, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, previous, err := auth.GetSession(ctx, f.db, legacy); err != nil || !previous {
		t.Fatalf("legacy previous token lost grace after first upgraded rotation: %v", err)
	}
	var actual time.Time
	if err := f.db.QueryRow("SELECT expires_at FROM session_grace_tokens WHERE token=$1", legacy).Scan(&actual); err != nil || !actual.Equal(deadline) {
		t.Fatalf("legacy token deadline changed during migration or rotation: %v", err)
	}
}

func TestSessionGraceRetentionFailureRollsBackRotation(t *testing.T) {
	for _, callerTransaction := range []bool{false, true} {
		name := "standalone"
		if callerTransaction {
			name = "membership_transaction"
		}
		t.Run(name, func(t *testing.T) {
			f := migrationHTTPFixture(t, "postgres", "VIEW_ONLY")
			if _, err := f.db.Exec(`CREATE FUNCTION deny_grace_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test-only grace persistence failure'; END $$;
				CREATE TRIGGER deny_grace_insert BEFORE INSERT ON session_grace_tokens FOR EACH ROW EXECUTE FUNCTION deny_grace_insert()`); err != nil {
				t.Fatal(err)
			}
			if callerTransaction {
				group := uuid.NewString()
				if _, err := f.db.Exec("INSERT INTO groups(id,name) VALUES($1,$1)", group); err != nil {
					t.Fatal(err)
				}
				h := &admin.GroupAdminHandler{DB: f.db}
				w := callAdminMutation(h.AddMember, group, url.Values{"user_id": {f.user}})
				requireMutationFailure(t, w)
				var members int
				if err := f.db.QueryRow("SELECT COUNT(*) FROM user_groups WHERE group_id=$1", group).Scan(&members); err != nil || members != 0 {
					t.Fatalf("membership committed despite grace persistence failure: %d %v", members, err)
				}
			} else if _, err := auth.RotateSession(context.Background(), f.db, f.session.ID); err == nil {
				t.Fatal("rotation reported success despite grace persistence failure")
			}
			if s, previous, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil || previous || s.ID != f.session.ID {
				t.Fatalf("failed grace persistence changed the canonical session: previous=%v err=%v", previous, err)
			}
			var aliases int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM session_grace_tokens").Scan(&aliases); err != nil || aliases != 0 {
				t.Fatalf("failed rotation left grace tokens: %d %v", aliases, err)
			}
		})
	}
}
