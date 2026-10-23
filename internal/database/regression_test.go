package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeConnectionError(t *testing.T) {
	if got := sanitizeConnectionError(nil); got != nil {
		t.Fatalf("successful connection: %v", got)
	}
	if got := sanitizeConnectionError(fmt.Errorf("lookup: %w", ErrConnectionNotFound)); !errors.Is(got, ErrConnectionNotFound) {
		t.Fatalf("missing connection must remain identifiable: %v", got)
	}
	for _, input := range []error{
		errors.New("postgres://alice:PrivatePassword@secret-host/db SQLSTATE 28P01"),
		errors.New("mysql access denied: password=PrivatePassword"),
		context.Canceled, context.DeadlineExceeded,
	} {
		got := sanitizeConnectionError(input)
		if got == nil || got.Error() != "connection failed; check host, port, database, credentials and network access" {
			t.Fatalf("expected safe, useful connection error, got %v", got)
		}
		for _, secret := range []string{"PrivatePassword", "alice", "secret-host", "SQLSTATE", "postgres://"} {
			if strings.Contains(got.Error(), secret) {
				t.Fatalf("connection error exposed %q", secret)
			}
		}
	}
}

func TestMySQLJSONJoinStrategies(t *testing.T) {
	for _, strategy := range []string{"hash join", "Block Nested Loop", "Batched Key Access", ""} {
		t.Run(strategy, func(t *testing.T) {
			raw := fmt.Sprintf(`{"query_block":{"nested_loop":[{"table":{"table_name":"parent","access_type":"ALL"}},{"table":{"table_name":"child","access_type":"ALL","using_join_buffer":%q}}]}}`, strategy)
			plan, err := ParseExplainPlan("mysql", raw, false)
			if err != nil {
				t.Fatal(err)
			}
			want := strategy
			if want == "" {
				want = "Nested loop"
			}
			if plan.Root.Children[1].Join != want {
				t.Fatalf("join strategy = %q, want %q", plan.Root.Children[1].Join, want)
			}
			if plan.Root.Join != "" || plan.Root.Children[0].Join != "" {
				t.Fatal("query container or first table incorrectly labeled as a join")
			}
		})
	}
}
