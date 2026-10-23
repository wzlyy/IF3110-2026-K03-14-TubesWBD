package database

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateExplainSQL(t *testing.T) {
	for _, tc := range []struct {
		name, dbms, sql string
		analyze         bool
		wantKind        string
		wantErr         error
	}{
		{"select", "postgres", " SELECT * FROM things WHERE label='a;b' ", false, "SELECT", nil},
		{"insert", "postgres", "INSERT INTO things(label) VALUES('x')", true, "INSERT", nil},
		{"with", "postgres", "WITH x AS (SELECT 1) SELECT * FROM x", false, "WITH", nil},
		{"mysql-with", "mysql", "WITH x AS (SELECT 1) SELECT * FROM x", false, "WITH", nil},
		{"mysql-analyze-dml", "mysql", "UPDATE things SET label='x'", true, "", ErrExplainMySQLAnalyzeDML},
		{"mysql-analyze-with", "mysql", "WITH x AS (SELECT 1) SELECT * FROM x", true, "", ErrExplainMySQLAnalyzeDML},
		{"ddl", "postgres", "CREATE TABLE x(id int)", false, "", ErrExplainDDL},
		{"ddl-comment", "postgres", "/* hi */ DROP TABLE x", false, "", ErrExplainDDL},
		{"transaction", "postgres", "BEGIN", false, "", ErrExplainStatement},
		{"stacked", "postgres", "SELECT 1; DROP TABLE things", false, "", ErrExplainStatement},
		{"mysql-hash-comment", "mysql", "SELECT 1 # trailing", false, "", ErrExplainStatement},
		{"unterminated", "postgres", "SELECT 'x", false, "", ErrExplainStatement},
		{"empty", "postgres", "   ", false, "", ErrExplainStatement},
		{"oversize", "postgres", "SELECT '" + strings.Repeat("x", 17*1024) + "'", false, "", ErrExplainTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind, err := ValidateExplainSQL(tc.dbms, tc.sql, tc.analyze)
			if kind != tc.wantKind || !errors.Is(err, tc.wantErr) {
				t.Fatalf("kind=%q err=%v, want %q %v", kind, err, tc.wantKind, tc.wantErr)
			}
		})
	}
}

func TestParsePostgresExplainPlan(t *testing.T) {
	raw := `[{"Plan":{"Node Type":"Hash Join","Join Type":"Inner","Startup Cost":1.1,"Total Cost":2.2,"Plan Rows":4,"Actual Total Time":0.9,"Actual Rows":3,"Plans":[{"Node Type":"Index Scan","Relation Name":"tasks","Index Name":"tasks_pkey","Total Cost":1.7,"Plan Rows":4}]},"Planning Time":0.11,"Execution Time":0.91}]`
	plan, err := ParseExplainPlan("postgres", raw, true)
	if err != nil || plan.Root.Label != "Hash Join" || plan.Root.Join != "Inner" || plan.Root.Cost == nil || *plan.Root.Cost != 2.2 || plan.Root.ActualTime == nil || *plan.Root.ActualTime != .9 || len(plan.Root.Children) != 1 || plan.Root.Children[0].Index != "tasks_pkey" || plan.ExecutionTime == nil {
		t.Fatalf("PostgreSQL plan lost metrics: %+v %v", plan, err)
	}
	without, err := ParseExplainPlan("postgres", `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"t","Total Cost":2,"Plan Rows":1}}]`, false)
	if err != nil || without.Root.ActualTime != nil || without.Root.ActualRows != nil {
		t.Fatalf("unavailable timings not distinct from zero: %+v %v", without, err)
	}
}

func TestParseMySQLExplainPlan(t *testing.T) {
	raw := `{"query_block":{"cost_info":{"query_cost":"2.50"},"nested_loop":[{"table":{"table_name":"users","access_type":"ALL","rows_examined_per_scan":9,"cost_info":{"read_cost":"1.25"}}},{"table":{"table_name":"tasks","access_type":"ref","key":"tasks_owner","rows_produced_per_join":3,"cost_info":{"prefix_cost":"2.50"}}}]}}`
	plan, err := ParseExplainPlan("mysql", raw, false)
	if err != nil || plan.Root == nil || len(plan.Root.Children) != 2 || plan.Root.Children[1].Index != "tasks_owner" || plan.Root.Children[1].Scan != "ref" || plan.Root.Children[1].Cost == nil || *plan.Root.Children[1].Cost != 2.5 || plan.Root.Children[0].ActualTime != nil {
		t.Fatalf("MySQL JSON plan lost scan/index/cost: %+v %v", plan, err)
	}
	tree := "-> Nested loop inner join  (cost=3.5 rows=2) (actual time=0.020..0.032 rows=2 loops=1)\n    -> Table scan on users  (cost=1 rows=2) (actual time=0.011..0.012 rows=2 loops=1)\n    -> Index lookup on tasks using tasks_owner  (cost=1.2 rows=1) (actual time=0.008..0.009 rows=1 loops=2)"
	analyzed, err := ParseExplainPlan("mysql", tree, true)
	if err != nil || analyzed.Root == nil || len(analyzed.Root.Children) != 2 || analyzed.Root.Children[1].Index != "tasks_owner" || analyzed.Root.Children[1].ActualTime == nil || *analyzed.Root.Children[1].ActualTime != .009 || analyzed.Root.Children[1].ActualRows == nil || *analyzed.Root.Children[1].ActualRows != 1 {
		t.Fatalf("MySQL TREE actual metrics lost: %+v %v", analyzed, err)
	}
}

func TestParseExplainPlanRejectsMalformedAndExcessiveOutput(t *testing.T) {
	for _, raw := range []string{"not json", `[{"Plan":{}}]`, `[{"Plan":{"Node Type":"Seq Scan","Plans":[null]}}]`, strings.Repeat("x", 1024*1024+1)} {
		if _, err := ParseExplainPlan("postgres", raw, false); err == nil {
			t.Fatalf("accepted invalid PostgreSQL output length=%d", len(raw))
		}
	}
	deep := `{"Node Type":"Result"}`
	for range 35 {
		deep = `{"Node Type":"Result","Plans":[` + deep + `]}`
	}
	if _, err := ParseExplainPlan("postgres", `[{"Plan":`+deep+`}]`, false); err == nil {
		t.Fatal("accepted excessive plan depth")
	}
}
