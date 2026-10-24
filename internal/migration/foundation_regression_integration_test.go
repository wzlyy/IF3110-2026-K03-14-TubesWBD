//go:build integration

package migration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/admin"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/auth"
	"github.com/wzlyy/IF3110-2026-K03-14-TubesWBD/internal/database"
)

func denySessionMutation(t *testing.T, f *fixture, event string) {
	t.Helper()
	_, err := f.db.Exec(`CREATE FUNCTION deny_session_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test-only session persistence failure'; END $$;
		CREATE TRIGGER deny_session_mutation BEFORE ` + event + ` ON sessions FOR EACH ROW EXECUTE FUNCTION deny_session_mutation()`)
	if err != nil {
		t.Fatal(err)
	}
}

// Call the business handler directly; authentication/CSRF are exercised by route tests.
func callAdminMutation(fn http.HandlerFunc, id string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/admin/test", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	fn(w, r)
	return w
}

func requireMutationFailure(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if strings.Contains(w.Header().Get("Location"), "success=") {
		t.Fatalf("session mutation failed but handler reported success: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w.Code < 300 {
		t.Fatalf("failed mutation unexpectedly accepted: %d", w.Code)
	}
}

func TestPasswordResetRollsBackWhenSessionRevocationFails(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	oldHash, err := auth.HashPassword("OldPassword!2026")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec("UPDATE users SET password_hash=$1 WHERE id=$2", oldHash, f.user); err != nil {
		t.Fatal(err)
	}
	denySessionMutation(t, f.fixture, "DELETE")
	h := &admin.UserAdminHandler{DB: f.db}
	w := callAdminMutation(h.ResetPassword, f.user, url.Values{"new_password": {"NewPassword!2026"}})
	requireMutationFailure(t, w)
	var actualHash string
	if err := f.db.QueryRow("SELECT password_hash FROM users WHERE id=$1", f.user).Scan(&actualHash); err != nil {
		t.Fatal(err)
	}
	if actualHash != oldHash {
		t.Fatal("password changed despite failed session revocation")
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil {
		t.Fatalf("rollback must preserve the original session: %v", err)
	}
}

func TestConnectionDeletionRevokesOnlyAffectedSessions(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	unrelated := uuid.NewString()
	if _, err := f.db.Exec("INSERT INTO users(id,username,password_hash) VALUES($1,$1,'test-only')", unrelated); err != nil {
		t.Fatal(err)
	}
	otherSession, err := auth.CreateSession(context.Background(), f.db, unrelated)
	if err != nil {
		t.Fatal(err)
	}
	h := &database.ConnectionAdminHandler{Store: f.store, Manager: f.manager}
	w := callAdminMutation(h.DeleteConnection, f.connection.ID, nil)
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "success=") {
		t.Fatalf("deletion failed: %d %s", w.Code, w.Header().Get("Location"))
	}
	if _, err := f.store.Get(context.Background(), f.connection.ID); !errors.Is(err, database.ErrConnectionNotFound) {
		t.Fatalf("connection remains: %v", err)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("grant holder session remains valid: %v", err)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, otherSession.ID); err != nil {
		t.Fatalf("unrelated session was revoked: %v", err)
	}
}

func TestConnectionDeletionRollsBackWhenSessionRevocationFails(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	denySessionMutation(t, f.fixture, "DELETE")
	h := &database.ConnectionAdminHandler{Store: f.store, Manager: f.manager}
	w := callAdminMutation(h.DeleteConnection, f.connection.ID, nil)
	requireMutationFailure(t, w)
	if _, err := f.store.Get(context.Background(), f.connection.ID); err != nil {
		t.Fatalf("connection deleted despite revoke failure: %v", err)
	}
	var grants int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM group_database_grants WHERE database_id=$1", f.connection.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Fatalf("connection grants changed despite rollback: %d", grants)
	}
	if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil {
		t.Fatalf("original session not preserved: %v", err)
	}
	if err := f.manager.Test(context.Background(), f.connection.ID); err != nil {
		t.Fatalf("unchanged target must remain usable: %v", err)
	}
}

func TestGrantChangesRollBackWhenSessionMutationFails(t *testing.T) {
	for _, tc := range []struct{ name, before, after, event string }{
		{"demotion_revoke", "VIEW_EDIT", "VIEW_ONLY", "DELETE"},
		{"escalation_rotate", "VIEW_ONLY", "VIEW_EDIT", "UPDATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := migrationHTTPFixture(t, "postgres", tc.before)
			var group string
			if err := f.db.QueryRow("SELECT group_id FROM user_groups WHERE user_id=$1", f.user).Scan(&group); err != nil {
				t.Fatal(err)
			}
			denySessionMutation(t, f.fixture, tc.event)
			h := &admin.GrantAdminHandler{DB: f.db}
			w := callAdminMutation(h.AssignGrant, "", url.Values{"group_id": {group}, "database_id": {f.connection.ID}, "access_level": {tc.after}})
			requireMutationFailure(t, w)
			var level string
			if err := f.db.QueryRow("SELECT access_level FROM group_database_grants WHERE group_id=$1 AND database_id=$2", group, f.connection.ID).Scan(&level); err != nil {
				t.Fatal(err)
			}
			if level != tc.before {
				t.Fatalf("grant changed despite session mutation failure: got %s, want %s", level, tc.before)
			}
			if _, _, err := auth.GetSession(context.Background(), f.db, f.session.ID); err != nil {
				t.Fatalf("original session not preserved: %v", err)
			}
		})
	}
}

func TestGrantRevocationRollsBackWhenSessionRevocationFails(t *testing.T) {
	f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
	var group string
	if err := f.db.QueryRow("SELECT group_id FROM user_groups WHERE user_id=$1", f.user).Scan(&group); err != nil {
		t.Fatal(err)
	}
	denySessionMutation(t, f.fixture, "DELETE")
	h := &admin.GrantAdminHandler{DB: f.db}
	w := callAdminMutation(h.RevokeGrant, "", url.Values{"group_id": {group}, "database_id": {f.connection.ID}})
	requireMutationFailure(t, w)
	var grants int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM group_database_grants WHERE group_id=$1 AND database_id=$2", group, f.connection.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 1 {
		t.Fatal("grant deleted despite failed session revocation")
	}
}

func TestMembershipChangesRollBackWhenSessionMutationFails(t *testing.T) {
	for _, adding := range []bool{false, true} {
		name := "remove_revoke"
		if adding {
			name = "add_rotate"
		}
		t.Run(name, func(t *testing.T) {
			f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
			var group string
			if err := f.db.QueryRow("SELECT group_id FROM user_groups WHERE user_id=$1", f.user).Scan(&group); err != nil {
				t.Fatal(err)
			}
			h := &admin.GroupAdminHandler{DB: f.db}
			operation, event, want := h.RemoveMember, "DELETE", 1
			if adding {
				group = uuid.NewString()
				if _, err := f.db.Exec("INSERT INTO groups(id,name) VALUES($1,$1)", group); err != nil {
					t.Fatal(err)
				}
				operation, event, want = h.AddMember, "UPDATE", 0
			}
			denySessionMutation(t, f.fixture, event)
			w := callAdminMutation(operation, group, url.Values{"user_id": {f.user}})
			requireMutationFailure(t, w)
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM user_groups WHERE user_id=$1 AND group_id=$2", f.user, group).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != want {
				t.Fatalf("membership changed despite session mutation failure: got %d, want %d", count, want)
			}
		})
	}
}

func TestRoleChangesRollBackWhenSessionMutationFails(t *testing.T) {
	for _, before := range []bool{true, false} {
		name, event := "promotion_rotate", "UPDATE"
		if before {
			name, event = "demotion_revoke", "DELETE"
		}
		t.Run(name, func(t *testing.T) {
			f := migrationHTTPFixture(t, "postgres", "VIEW_EDIT")
			if _, err := f.db.Exec("UPDATE users SET is_platform_admin=$1 WHERE id=$2", before, f.user); err != nil {
				t.Fatal(err)
			}
			// Another admin keeps a last-admin safeguard from masking the failure path.
			other := uuid.NewString()
			if _, err := f.db.Exec("INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$1,'test-only',true)", other); err != nil {
				t.Fatal(err)
			}
			denySessionMutation(t, f.fixture, event)
			h := &admin.UserAdminHandler{DB: f.db}
			w := callAdminMutation(h.ToggleAdmin, f.user, nil)
			requireMutationFailure(t, w)
			var actual bool
			if err := f.db.QueryRow("SELECT is_platform_admin FROM users WHERE id=$1", f.user).Scan(&actual); err != nil {
				t.Fatal(err)
			}
			if actual != before {
				t.Fatal("admin role changed despite session mutation failure")
			}
		})
	}
}
