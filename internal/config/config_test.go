package config

import (
	"strings"
	"testing"
)

func TestEncryptionKeyContract(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat("x", 31), strings.Repeat("x", 33)} {
		t.Setenv("ENCRYPTION_KEY", key)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
			t.Fatalf("key length %d: expected configuration error, got %v", len(key), err)
		}
	}
	key := "12345678901234567890123456789012"
	t.Setenv("ENCRYPTION_KEY", key)
	cfg, err := Load()
	if err != nil || cfg.EncryptionKey != key {
		t.Fatalf("valid key rejected: %v", err)
	}
}
