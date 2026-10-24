package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxExplainSQLBytes  = 16 * 1024
	maxExplainPlanBytes = 1024 * 1024
	maxPlanDepth        = 32
	maxPlanNodes        = 512
)

const ExplainDDLMessage = "Pernyataan DDL tidak memiliki execution plan tabular. Analisis EXPLAIN hanya berlaku untuk query data (SELECT atau DML)."
const ExplainAnalyzeWarning = "Peringatan: Mode ANALYZE mengeksekusi query secara fisik di target. Data tabel dilindungi dengan rollback transaksi otomatis, namun efek samping tingkat mesin seperti urutan sequence ID (nextval) tidak dapat di-rollback."

var (
	ErrExplainDDL             = errors.New(ExplainDDLMessage)
	ErrExplainStatement       = errors.New("Masukkan tepat satu query data SELECT, INSERT, UPDATE, DELETE, atau WITH yang valid.")
	ErrExplainTooLarge        = errors.New("Query atau hasil plan terlalu besar untuk dianalisis.")
	ErrExplainMySQLAnalyzeDML = errors.New("MySQL ANALYZE hanya mendukung SELECT. DML ditolak karena tabel nontransaksional tidak dapat dijamin oleh rollback.")
	ErrExplainUnavailable     = errors.New("Rencana query tidak dapat dibuat. Periksa query dan izin akun database target.")
	ErrExplainMalformedPlan   = errors.New("Format rencana query dari database target tidak dapat dibaca.")
)

// PlanNode is a DBMS-neutral view of one plan step. Nil metrics mean that the
// target did not report the value; zero is a real measured zero.
type PlanNode struct {
	Label, Relation, Index, Scan, Join string
	Cost, Rows, ActualTime, ActualRows *float64
	Children                           []*PlanNode
}

func planMetric(value *float64) string {
	if value == nil {
		return "Tidak tersedia"
	}
	return strconv.FormatFloat(*value, 'f', 3, 64)
}

func (n *PlanNode) CostText() string       { return planMetric(n.Cost) }
func (n *PlanNode) RowsText() string       { return planMetric(n.Rows) }
func (n *PlanNode) ActualTimeText() string { return planMetric(n.ActualTime) }
func (n *PlanNode) ActualRowsText() string { return planMetric(n.ActualRows) }

type ExplainPlan struct {
	DBMS, Format, Raw           string
	Analyzed                    bool
	Root                        *PlanNode
	PlanningTime, ExecutionTime *float64
}

// ValidateExplainSQL is deliberately a statement boundary, not a SQL parser.
// The server prepares the composed EXPLAIN as a second single-statement check.
func ValidateExplainSQL(dbms, query string, analyze bool) (string, error) {
	if len(query) > maxExplainSQLBytes {
		return "", ErrExplainTooLarge
	}
	if dbms != "postgres" && dbms != "mysql" {
		return "", ErrUnsupportedDBMS
	}
	var cleaned strings.Builder
	state := byte(0)
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch state {
		case '\'':
			if c == '\\' && dbms == "mysql" && i+1 < len(query) {
				i++
				continue
			}
			if c == '\'' {
				if i+1 < len(query) && query[i+1] == '\'' {
					i++
					continue
				}
				state = 0
			}
			continue
		case '"', '`':
			if c == state {
				if i+1 < len(query) && query[i+1] == state {
					i++
					continue
				}
				state = 0
			}
			continue
		case '-':
			if c == '\n' {
				state = 0
				cleaned.WriteByte(' ')
			}
			continue
		case '*':
			if c == '*' && i+1 < len(query) && query[i+1] == '/' {
				i++
				state = 0
				cleaned.WriteByte(' ')
			}
			continue
		}
		if c == '\'' || c == '"' || (c == '`' && dbms == "mysql") {
			state = c
			cleaned.WriteByte(' ')
			continue
		}
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			i++
			state = '-'
			cleaned.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			i++
			state = '*'
			cleaned.WriteByte(' ')
			continue
		}
		if c == '#' && dbms == "mysql" {
			return "", ErrExplainStatement
		}
		if c == ';' {
			return "", ErrExplainStatement
		}
		cleaned.WriteByte(c)
	}
	if state != 0 && state != '-' {
		return "", ErrExplainStatement
	}
	fields := strings.Fields(cleaned.String())
	if len(fields) == 0 {
		return "", ErrExplainStatement
	}
	kind := strings.ToUpper(fields[0])
	switch kind {
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "COMMENT", "GRANT", "REVOKE":
		return "", ErrExplainDDL
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH":
		if analyze && dbms == "mysql" && kind != "SELECT" {
			return "", ErrExplainMySQLAnalyzeDML
		}
		return kind, nil
	default:
		return "", ErrExplainStatement
	}
}

// RunExplain bounds work, asks the driver to prepare exactly one EXPLAIN, and
// always rolls back. PostgreSQL ANALYZE intentionally uses a writable tx; the
// caller must have VIEW_EDIT even for SELECT.
func RunExplain(ctx context.Context, target *sql.DB, dbms, query string, analyze bool) (*ExplainPlan, error) {
	kind, err := ValidateExplainSQL(dbms, query, analyze)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if dbms == "mysql" {
		var name string
		if err := target.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&name); err != nil {
			return nil, ErrExplainUnavailable
		}
		if err := ValidateMySQLScope(ctx, target, name); err != nil {
			return nil, err
		}
	}
	statement := ""
	switch dbms {
	case "postgres":
		if analyze {
			statement = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + query
		} else {
			statement = "EXPLAIN (FORMAT JSON) " + query
		}
	case "mysql":
		if analyze {
			statement = "EXPLAIN ANALYZE FORMAT=TREE " + query
		} else {
			statement = "EXPLAIN FORMAT=JSON " + query
		}
	}
	readOnly := !analyze
	if dbms == "mysql" {
		// MySQL rejects even nonexecuting EXPLAIN DML in READ ONLY
		// transactions (error 1792). The prepared EXPLAIN prefix and
		// unconditional rollback remain in effect; ANALYZE is SELECT-only.
		readOnly = analyze || kind == "SELECT"
	}
	tx, err := target.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return nil, ErrExplainUnavailable
	}
	defer tx.Rollback()
	if dbms == "postgres" {
		if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '5s'"); err != nil {
			return nil, ErrExplainUnavailable
		}
	}
	stmt, err := tx.PrepareContext(ctx, statement)
	if err != nil {
		return nil, ErrExplainUnavailable
	}
	defer stmt.Close()
	var raw string
	if err = stmt.QueryRowContext(ctx).Scan(&raw); err != nil {
		return nil, ErrExplainUnavailable
	}
	return ParseExplainPlan(dbms, raw, analyze)
}

// ParseExplainPlan accepts PostgreSQL JSON, MySQL JSON, and MySQL 8 TREE.
func ParseExplainPlan(dbms, raw string, analyze bool) (*ExplainPlan, error) {
	if len(raw) == 0 || len(raw) > maxExplainPlanBytes {
		return nil, ErrExplainTooLarge
	}
	p := &ExplainPlan{DBMS: dbms, Raw: raw, Analyzed: analyze}
	budget := maxPlanNodes
	switch {
	case dbms == "postgres":
		p.Format = "JSON"
		var documents []struct {
			Plan          *pgPlanNode `json:"Plan"`
			PlanningTime  *float64    `json:"Planning Time"`
			ExecutionTime *float64    `json:"Execution Time"`
		}
		if json.Unmarshal([]byte(raw), &documents) != nil || len(documents) != 1 || documents[0].Plan == nil {
			return nil, ErrExplainMalformedPlan
		}
		var err error
		p.Root, err = fromPGPlan(documents[0].Plan, 0, &budget)
		if err != nil {
			return nil, err
		}
		p.PlanningTime, p.ExecutionTime = documents[0].PlanningTime, documents[0].ExecutionTime
	case dbms == "mysql" && !analyze:
		p.Format = "JSON"
		var document map[string]any
		if json.Unmarshal([]byte(raw), &document) != nil {
			return nil, ErrExplainMalformedPlan
		}
		qb, ok := document["query_block"].(map[string]any)
		if !ok {
			return nil, ErrExplainMalformedPlan
		}
		var err error
		p.Root, err = fromMySQLJSON("Query block", qb, 0, &budget)
		if err != nil {
			return nil, err
		}
	case dbms == "mysql":
		p.Format = "TREE"
		var err error
		p.Root, err = fromMySQLTree(raw, &budget)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrUnsupportedDBMS
	}
	return p, nil
}

type pgPlanNode struct {
	NodeType        string        `json:"Node Type"`
	Relation        string        `json:"Relation Name"`
	Index           string        `json:"Index Name"`
	Join            string        `json:"Join Type"`
	StartupCost     *float64      `json:"Startup Cost"`
	TotalCost       *float64      `json:"Total Cost"`
	PlanRows        *float64      `json:"Plan Rows"`
	ActualTotalTime *float64      `json:"Actual Total Time"`
	ActualRows      *float64      `json:"Actual Rows"`
	Plans           []*pgPlanNode `json:"Plans"`
}

func fromPGPlan(n *pgPlanNode, depth int, budget *int) (*PlanNode, error) {
	if n == nil || depth > maxPlanDepth || *budget <= 0 || n.NodeType == "" {
		return nil, ErrExplainMalformedPlan
	}
	*budget--
	result := &PlanNode{Label: n.NodeType, Relation: n.Relation, Index: n.Index, Join: n.Join, Cost: n.TotalCost, Rows: n.PlanRows, ActualTime: n.ActualTotalTime, ActualRows: n.ActualRows}
	if strings.Contains(n.NodeType, "Scan") {
		result.Scan = n.NodeType
	}
	for _, child := range n.Plans {
		parsed, err := fromPGPlan(child, depth+1, budget)
		if err != nil {
			return nil, err
		}
		result.Children = append(result.Children, parsed)
	}
	return result, nil
}

func asMetric(v any) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func fromMySQLJSON(label string, value map[string]any, depth int, budget *int) (*PlanNode, error) {
	if depth > maxPlanDepth || *budget <= 0 {
		return nil, ErrExplainMalformedPlan
	}
	*budget--
	n := &PlanNode{Label: label}
	if label == "table" {
		if s, ok := value["table_name"].(string); ok {
			n.Relation = s
			n.Label = "Table " + s
		}
		if s, ok := value["access_type"].(string); ok {
			n.Scan = s
		}
		if s, ok := value["key"].(string); ok {
			n.Index = s
		}
		if strategy, ok := value["using_join_buffer"].(string); ok {
			n.Join = strategy
		}
		n.Rows = asMetric(value["rows_produced_per_join"])
		if n.Rows == nil {
			n.Rows = asMetric(value["rows_examined_per_scan"])
		}
	}
	if cost, ok := value["cost_info"].(map[string]any); ok {
		for _, key := range []string{"prefix_cost", "query_cost", "read_cost"} {
			if n.Cost = asMetric(cost[key]); n.Cost != nil {
				break
			}
		}
	}
	if loop, ok := value["nested_loop"].([]any); ok {
		for i, entry := range loop {
			m, ok := entry.(map[string]any)
			if !ok {
				return nil, ErrExplainMalformedPlan
			}
			child, err := mysqlJSONObject(m, depth+1, budget)
			if err != nil {
				return nil, err
			}
			// nested_loop describes the join order; a buffered step may use
			// hash join or Batched Key Access instead of a nested-loop join.
			if i > 0 && child.Join == "" {
				child.Join = "Nested loop"
			}
			n.Children = append(n.Children, child)
		}
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		if key != "nested_loop" && key != "cost_info" && key != "table_name" && key != "access_type" && key != "key" && key != "used_key_parts" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		child, ok := value[key].(map[string]any)
		if !ok {
			continue
		}
		if key == "materialized_from_subquery" || key == "attached_subqueries" {
			continue
		}
		parsed, err := fromMySQLJSON(key, child, depth+1, budget)
		if err != nil {
			return nil, err
		}
		n.Children = append(n.Children, parsed)
	}
	return n, nil
}

func mysqlJSONObject(object map[string]any, depth int, budget *int) (*PlanNode, error) {
	for _, key := range []string{"table", "query_block", "nested_loop"} {
		if child, ok := object[key].(map[string]any); ok {
			return fromMySQLJSON(key, child, depth, budget)
		}
	}
	for key, child := range object {
		if m, ok := child.(map[string]any); ok {
			return fromMySQLJSON(key, m, depth, budget)
		}
	}
	return nil, ErrExplainMalformedPlan
}

var (
	treeCost       = regexp.MustCompile(`\bcost=([0-9.eE+-]+)`)
	treeRows       = regexp.MustCompile(`\brows=([0-9.eE+-]+)`)
	treeActualTime = regexp.MustCompile(`\bactual time=[0-9.eE+-]+\.\.([0-9.eE+-]+)`)
	treeIndex      = regexp.MustCompile(`\busing ([^ )]+)`)
)

func treeNumber(re *regexp.Regexp, line string) *float64 {
	match := re.FindStringSubmatch(line)
	if len(match) != 2 {
		return nil
	}
	v, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	return &v
}

func fromMySQLTree(raw string, budget *int) (*PlanNode, error) {
	type frame struct {
		indent int
		node   *PlanNode
	}
	var stack []frame
	var root *PlanNode
	for _, line := range strings.Split(raw, "\n") {
		pos := strings.Index(line, "-> ")
		if pos < 0 {
			if strings.TrimSpace(line) != "" {
				return nil, ErrExplainMalformedPlan
			}
			continue
		}
		if *budget <= 0 || len(stack) > maxPlanDepth {
			return nil, ErrExplainMalformedPlan
		}
		*budget--
		content := strings.TrimSpace(line[pos+3:])
		label := content
		if cut := strings.Index(content, "  ("); cut >= 0 {
			label = content[:cut]
		}
		if label == "" {
			return nil, ErrExplainMalformedPlan
		}
		node := &PlanNode{Label: label, Cost: treeNumber(treeCost, content), Rows: treeNumber(treeRows, content), ActualTime: treeNumber(treeActualTime, content)}
		if actual := strings.Index(content, "(actual time="); actual >= 0 {
			node.ActualRows = treeNumber(treeRows, content[actual:])
		}
		if m := treeIndex.FindStringSubmatch(label); len(m) == 2 {
			node.Index = m[1]
		}
		if strings.Contains(strings.ToLower(label), "scan") || strings.Contains(strings.ToLower(label), "lookup") {
			node.Scan = label
		}
		if strings.Contains(strings.ToLower(label), "join") {
			node.Join = label
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= pos {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			if root != nil {
				return nil, ErrExplainMalformedPlan
			}
			root = node
		} else {
			stack[len(stack)-1].node.Children = append(stack[len(stack)-1].node.Children, node)
		}
		stack = append(stack, frame{pos, node})
	}
	if root == nil {
		return nil, fmt.Errorf("%w: empty tree", ErrExplainMalformedPlan)
	}
	return root, nil
}
