package database

import (
	"math"
	"testing"
	"time"
)

func TestQuoteIdentifier(t *testing.T) {
	for _, tc := range []struct{ value, want string }{{"public", `"public"`}, {"order", `"order"`}, {"a.b", `"a.b"`}, {`a"b`, `"a""b"`}, {"雪 & /", `"雪 & /"`}} {
		got, err := QuoteIdentifier("postgres", tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("quote %q = %q %v", tc.value, got, err)
		}
	}
	for _, value := range []string{"", "a\x00b"} {
		if _, err := QuoteIdentifier("postgres", value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, tc := range []struct{ value, want string }{{"public", "`public`"}, {"a.b", "`a.b`"}, {"a`b", "`a``b`"}, {"雪 & /", "`雪 & /`"}} {
		got, err := QuoteIdentifier("mysql", tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("MySQL quote %q = %q %v", tc.value, got, err)
		}
	}
	if _, err := QuoteIdentifier("sqlite", "test"); err == nil {
		t.Fatal("accepted unsupported DBMS")
	}
}

func TestPageBounds(t *testing.T) {
	for _, raw := range []string{"0", "-1", "1.5", "abc", "9223372036854775808", "18446744073709551616"} {
		if _, err := ParseDataPage(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"", "1", "3"} {
		if _, err := ParseDataPage(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	if _, err := pageOffset(math.MaxInt64); err == nil {
		t.Fatal("offset overflow accepted")
	}
	if got, err := pageOffset(3); err != nil || got != 100 {
		t.Fatalf("offset=%d %v", got, err)
	}
}

func TestDisplayCell(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 13, 14, 0, time.UTC)
	for _, tc := range []struct {
		value any
		text  string
		null  bool
	}{
		{nil, "", true}, {"", "", false}, {[]byte("hello"), "hello", false}, {[]byte{0, 255}, "\\x00ff", false},
		{now, "2026-10-22T12:13:14Z", false}, {"<script>", "<script>", false}, {42, "42", false},
	} {
		got := displayCell(tc.value)
		if got.Text != tc.text || got.Null != tc.null {
			t.Errorf("%#v: %+v", tc.value, got)
		}
	}
}
