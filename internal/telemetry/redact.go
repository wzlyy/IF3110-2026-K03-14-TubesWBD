package telemetry

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const RedactedValue = "[REDACTED]"

var sensitiveKeyTokens = []string{
	"password",
	"passwd",
	"pass",
	"secret",
	"token",
	"session",
	"csrf",
	"authorization",
	"cookie",
	"encryption_key",
	"credential",
	"api_key",
	"apikey",
	"private_key",
}

var (
	dsnRegex       = regexp.MustCompile(`(?i)\b(postgres|postgresql|mysql|amqp|amqps|redis|rediss)://[^\s"']+`)
	kvSecretRegex  = regexp.MustCompile(`(?i)\b([a-z0-9_]*(?:password|passwd|pwd|secret|token|session_id|csrf|encryption_key|api_key)[a-z0-9_]*)\s*=\s*([^\s;,&'"]+)`)
	bearerRegex    = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	cookieValRegex = regexp.MustCompile(`(?i)\b(session_id)=([^;\s]+)`)
	placeholderRe  = regexp.MustCompile(`^(\$\d+|\?)$`)
)

// IsSensitiveKey memeriksa apakah nama field berisi data sensitif.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	if lower == "" {
		return false
	}
	for _, tok := range sensitiveKeyTokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// RedactString menyamarkan kredensial DSN, token bearer, cookie, dan rahasia key=value.
func RedactString(s string) string {
	if s == "" {
		return s
	}
	out := dsnRegex.ReplaceAllStringFunc(s, func(match string) string {
		u, err := url.Parse(match)
		if err != nil {
			return RedactedValue
		}
		if u.User != nil {
			username := u.User.Username()
			if username != "" {
				u.User = url.UserPassword(username, "REDACTED")
			} else {
				u.User = url.User("REDACTED")
			}
		}
		return u.String()
	})
	out = kvSecretRegex.ReplaceAllStringFunc(out, func(match string) string {
		parts := kvSecretRegex.FindStringSubmatch(match)
		if len(parts) == 3 && placeholderRe.MatchString(parts[2]) {
			return match
		}
		if len(parts) == 3 {
			return parts[1] + "=" + RedactedValue
		}
		return RedactedValue
	})
	out = bearerRegex.ReplaceAllString(out, "${1} "+RedactedValue)
	out = cookieValRegex.ReplaceAllString(out, "${1}="+RedactedValue)
	return SanitizeLogString(out)
}

// SanitizeLogString menghapus karakter CR/LF agar mencegah log injection.
func SanitizeLogString(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// RedactValue menyamarkan nilai sensitif berdasarkan nama key atau pola rahasia.
func RedactValue(key string, value any) any {
	if IsSensitiveKey(key) {
		return RedactedValue
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return RedactString(v)
	case error:
		return RedactString(v.Error())
	case []byte:
		return fmt.Sprintf("<%d bytes>", len(v))
	default:
		return v
	}
}

// RedactParams menyamarkan argumen query SQL sebelum ditulis ke log.
func RedactParams(params []any) []any {
	if len(params) == 0 {
		return nil
	}
	sanitized := make([]any, len(params))
	for i, p := range params {
		switch v := p.(type) {
		case nil:
			sanitized[i] = nil
		case string:
			redacted := RedactString(v)
			if len(redacted) > 256 {
				redacted = redacted[:256] + "...(truncated)"
			}
			sanitized[i] = redacted
		case []byte:
			sanitized[i] = fmt.Sprintf("<%d bytes>", len(v))
		case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
			sanitized[i] = v
		default:
			sanitized[i] = SanitizeLogString(RedactString(fmt.Sprint(v)))
		}
	}
	return sanitized
}
