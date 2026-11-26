package migration

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxSQLBytes = 16 * 1024

var ErrInvalidSQL = errors.New("Masukkan SQL DDL/DML yang valid (maksimum 16 KiB). Kontrol transaksi, sesi, routine/DDL TEMPORARY MySQL, dan tabel bukti internal tidak diizinkan.")

type Statement struct{ SQL, Kind string }

func canonicalSQLKind(word string) (string, bool) {
	switch word {
	case "SELECT":
		return "SELECT", true
	case "INSERT":
		return "INSERT", true
	case "UPDATE":
		return "UPDATE", true
	case "DELETE":
		return "DELETE", true
	case "WITH":
		return "WITH", true
	case "CREATE":
		return "CREATE", true
	case "ALTER":
		return "ALTER", true
	case "DROP":
		return "DROP", true
	case "TRUNCATE":
		return "TRUNCATE", true
	case "COMMENT":
		return "COMMENT", true
	case "GRANT":
		return "GRANT", true
	case "REVOKE":
		return "REVOKE", true
	default:
		return "", false
	}
}

func trimSQLBounds(query string, start, end int) (int, int) {
	for start < end {
		r, size := utf8.DecodeRuneInString(query[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		start += size
	}
	for end > start {
		r, size := utf8.DecodeLastRuneInString(query[start:end])
		if !unicode.IsSpace(r) {
			break
		}
		end -= size
	}
	return start, end
}

func validateCleanStatement(dbms string, clean *strings.Builder) (string, bool, error) {
	words := strings.Fields(strings.ToUpper(clean.String()))
	clean.Reset()
	if len(words) == 0 {
		return "", false, nil
	}
	kind, ok := canonicalSQLKind(words[0])
	if !ok {
		return "", false, ErrInvalidSQL
	}
	if dbms == "mysql" {
		for _, word := range words {
			if word == "PROCEDURE" || word == "FUNCTION" || word == "TRIGGER" || word == "EVENT" || word == "TEMPORARY" {
				return "", false, ErrInvalidSQL
			}
		}
	}
	return kind, true, nil
}

func lexSQL(dbms, query string) ([]int, []int, []string, error) {
	lowerQuery := strings.ToLower(query)
	if (dbms != "postgres" && dbms != "mysql") || len(query) > MaxSQLBytes || strings.ContainsRune(query, 0) ||
		strings.Contains(lowerQuery, "_dbplatform_migration_receipts") || strings.Contains(lowerQuery, "_platform_meta") {
		return nil, nil, nil, ErrInvalidSQL
	}
	var starts, ends []int
	var kinds []string
	var clean strings.Builder
	start := 0
	for i := 0; i < len(query); {
		c := query[i]
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			// MySQL recognizes -- only when followed by whitespace.
			if dbms == "mysql" && i+2 < len(query) && !unicode.IsSpace(rune(query[i+2])) {
				return nil, nil, nil, ErrInvalidSQL
			}
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				i = len(query)
			} else {
				i += end + 1
			}
			clean.WriteByte(' ')
			continue
		}
		if c == '#' && dbms == "mysql" {
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				i = len(query)
			} else {
				i += end + 1
			}
			clean.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			if i+2 < len(query) && query[i+2] == '!' {
				return nil, nil, nil, ErrInvalidSQL
			}
			i += 2
			depth := 1
			for i < len(query) && depth > 0 {
				if i+1 < len(query) && query[i:i+2] == "/*" && dbms == "postgres" {
					depth++
					i += 2
				} else if i+1 < len(query) && query[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth != 0 {
				return nil, nil, nil, ErrInvalidSQL
			}
			clean.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' || (c == '`' && dbms == "mysql") {
			quote := c
			backslash := dbms == "mysql" || (quote == '\'' && i > 0 && (query[i-1] == 'e' || query[i-1] == 'E') && (i < 2 || !isIdentifier(query[i-2])))
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' && backslash {
					i += 2
					continue
				}
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, nil, nil, ErrInvalidSQL
			}
			clean.WriteString(" quoted ")
			continue
		}
		if c == '$' && dbms == "postgres" {
			j := i + 1
			for j < len(query) && (isIdentifier(query[j]) && query[j] != '$') {
				j++
			}
			if j < len(query) && query[j] == '$' && (j == i+1 || query[i+1] < '0' || query[i+1] > '9') {
				tag := query[i : j+1]
				end := strings.Index(query[j+1:], tag)
				if end < 0 {
					return nil, nil, nil, ErrInvalidSQL
				}
				i = j + 1 + end + len(tag)
				clean.WriteString(" quoted ")
				continue
			}
		}
		if c == ';' {
			kind, ok, err := validateCleanStatement(dbms, &clean)
			if err != nil {
				return nil, nil, nil, err
			}
			if ok {
				s, e := trimSQLBounds(query, start, i)
				starts = append(starts, s)
				ends = append(ends, e)
				kinds = append(kinds, kind)
			}
			i++
			start = i
			continue
		}
		clean.WriteByte(c)
		i++
	}
	kind, ok, err := validateCleanStatement(dbms, &clean)
	if err != nil {
		return nil, nil, nil, err
	}
	if ok {
		s, e := trimSQLBounds(query, start, len(query))
		starts = append(starts, s)
		ends = append(ends, e)
		kinds = append(kinds, kind)
	}
	if len(starts) == 0 {
		return nil, nil, nil, ErrInvalidSQL
	}
	return starts, ends, kinds, nil
}

// ValidateSQL splits statements without splitting quoted strings, identifiers,
// comments or PostgreSQL dollar bodies. Transaction ownership stays with Runner.
// This is a bounded statement lexer; the target still validates SQL grammar.
func ValidateSQL(dbms, query string) ([]Statement, error) {
	starts, ends, kinds, err := lexSQL(dbms, query)
	if err != nil {
		return nil, err
	}
	statements := make([]Statement, len(starts))
	for i := range starts {
		statements[i] = Statement{SQL: query[starts[i]:ends[i]], Kind: kinds[i]}
	}
	return statements, nil
}

// SplitPostgresStatements splits an approved PostgreSQL migration snapshot into
// validated statements for the async worker without sharing ValidateSQL's HTTP caller context.
func SplitPostgresStatements(query string) ([]Statement, error) {
	starts, ends, kinds, err := lexSQL("postgres", query)
	if err != nil {
		return nil, err
	}
	statements := make([]Statement, len(starts))
	for i := range starts {
		statements[i] = Statement{SQL: query[starts[i]:ends[i]], Kind: kinds[i]}
	}
	return statements, nil
}

func isIdentifier(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c >= 128
}

// Driver messages can contain SQL values, passwords and connection URLs.
func executionError(_ error) string {
	return "SQL migration gagal. Periksa sintaks, objek, data, dan izin akun target. Detail driver disembunyikan untuk melindungi secret."
}
