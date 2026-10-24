package migration

import (
	"errors"
	"strings"
	"unicode"
)

const MaxSQLBytes = 16 * 1024

var ErrInvalidSQL = errors.New("Masukkan SQL DDL/DML yang valid (maksimum 16 KiB). Kontrol transaksi, sesi, routine/DDL TEMPORARY MySQL, dan tabel bukti internal tidak diizinkan.")

type Statement struct{ SQL, Kind string }

// ValidateSQL splits statements without splitting quoted strings, identifiers,
// comments or PostgreSQL dollar bodies. Transaction ownership stays with Runner.
// This is a bounded statement lexer; the target still validates SQL grammar.
func ValidateSQL(dbms, query string) ([]Statement, error) {
	if (dbms != "postgres" && dbms != "mysql") || len(query) > MaxSQLBytes || strings.ContainsRune(query, 0) || strings.Contains(strings.ToLower(query), "_dbplatform_migration_receipts") {
		return nil, ErrInvalidSQL
	}
	var statements []Statement
	var clean strings.Builder
	start := 0
	appendStatement := func(end int) error {
		words := strings.Fields(strings.ToUpper(clean.String()))
		if len(words) == 0 {
			clean.Reset()
			return nil
		}
		switch words[0] {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "CREATE", "ALTER", "DROP", "TRUNCATE", "COMMENT", "GRANT", "REVOKE":
		default:
			return ErrInvalidSQL
		}
		if dbms == "mysql" {
			for _, word := range words {
				if word == "PROCEDURE" || word == "FUNCTION" || word == "TRIGGER" || word == "EVENT" || word == "TEMPORARY" {
					return ErrInvalidSQL
				}
			}
		}
		statements = append(statements, Statement{SQL: strings.TrimSpace(query[start:end]), Kind: words[0]})
		clean.Reset()
		return nil
	}
	for i := 0; i < len(query); {
		c := query[i]
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			// MySQL recognizes -- only when followed by whitespace.
			if dbms == "mysql" && i+2 < len(query) && !unicode.IsSpace(rune(query[i+2])) {
				return nil, ErrInvalidSQL
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
				return nil, ErrInvalidSQL
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
				return nil, ErrInvalidSQL
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
				return nil, ErrInvalidSQL
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
					return nil, ErrInvalidSQL
				}
				i = j + 1 + end + len(tag)
				clean.WriteString(" quoted ")
				continue
			}
		}
		if c == ';' {
			if err := appendStatement(i); err != nil {
				return nil, err
			}
			i++
			start = i
			continue
		}
		clean.WriteByte(c)
		i++
	}
	if err := appendStatement(len(query)); err != nil {
		return nil, err
	}
	if len(statements) == 0 {
		return nil, ErrInvalidSQL
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
