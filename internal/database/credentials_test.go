package database

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCredentialsCipher(t *testing.T) {
	c, err := newCredentialCipher("12345678901234567890123456789012")
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.encrypt("password ' @ : / ? &")
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.encrypt("password ' @ : / ? &")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("nonce must be randomized")
	}
	plain, err := c.decrypt(a)
	if err != nil || plain != "password ' @ : / ? &" {
		t.Fatalf("roundtrip: %q %v", plain, err)
	}
	wrong, _ := newCredentialCipher("abcdefghijabcdefghijabcdefghij12")
	for _, tc := range []struct {
		cipher *credentialCipher
		value  string
	}{
		{wrong, a}, {c, "invalid"}, {c, a[:len(a)-4] + "AAAA"},
	} {
		if _, err := tc.cipher.decrypt(tc.value); err == nil || strings.Contains(err.Error(), "password") {
			t.Fatalf("expected safe decryption error, got %v", err)
		}
	}
	for _, key := range []string{"", "short", strings.Repeat("x", 16), strings.Repeat("x", 33)} {
		if _, err := newCredentialCipher(key); err == nil {
			t.Fatalf("accepted key length %d", len(key))
		}
	}
}

func TestConnectionValidation(t *testing.T) {
	valid := Connection{Name: "Example", DBMS: "postgres", Host: "localhost", Port: 5432, DBName: "sample", Username: "postgres"}
	if _, err := validateConnection(valid, "secret"); err != nil {
		t.Fatal(err)
	}
	mysqlConnection := valid
	mysqlConnection.DBMS, mysqlConnection.Port = "mysql", 3306
	if _, err := validateConnection(mysqlConnection, "secret"); err != nil {
		t.Fatalf("MySQL connection rejected: %v", err)
	}
	for _, change := range []func(*Connection){
		func(c *Connection) { c.Name = " " }, func(c *Connection) { c.Name = strings.Repeat("x", 101) },
		func(c *Connection) { c.DBMS = "sqlite" }, func(c *Connection) { c.Port = 0 }, func(c *Connection) { c.Port = 65536 },
		func(c *Connection) { c.Host = "localhost/x" }, func(c *Connection) { c.Host = "localhost:5432" },
		func(c *Connection) { c.Host = strings.Repeat("x", 256) }, func(c *Connection) { c.DBName = "" },
		func(c *Connection) { c.DBName = strings.Repeat("x", 101) }, func(c *Connection) { c.Username = "x\x00y" },
		func(c *Connection) { c.Username = strings.Repeat("x", 101) },
	} {
		c := valid
		change(&c)
		if _, err := validateConnection(c, "secret"); err == nil {
			t.Fatalf("accepted invalid connection: %+v", c)
		}
	}
	if _, err := validateConnection(valid, strings.Repeat("x", 4097)); err == nil {
		t.Fatal("accepted oversized password")
	}
}

func TestTargetDSNCredentialsAreData(t *testing.T) {
	c := Connection{Host: "::1", Port: 5432, DBName: "db /?&=#", Username: "user @/?'"}
	password := "x' sslmode=disable @/?&:#"
	dsn := postgresDSN(c, password)
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "::1" || parsed.Port != 5432 || parsed.Database != c.DBName || parsed.User != c.Username || parsed.Password != password {
		t.Fatal("DSN did not preserve credential boundaries")
	}
	if parsed.ConnectTimeout == 0 {
		t.Fatal("missing connection timeout")
	}
}

func TestMySQLConnectionConfigCredentialsAreData(t *testing.T) {
	c := Connection{Host: "::1", Port: 3306, DBName: "db /?&=#", Username: "user @/?'"}
	password := "x' multiStatements=true @/?&:#"
	cfg := mysqlConfig(c, password)
	if cfg.Net != "tcp" || cfg.Addr != "[::1]:3306" || cfg.DBName != c.DBName || cfg.User != c.Username || cfg.Passwd != password {
		t.Fatal("MySQL config did not preserve credential boundaries")
	}
	if cfg.MultiStatements || cfg.AllowAllFiles || cfg.InterpolateParams || cfg.Timeout == 0 || cfg.ReadTimeout == 0 || cfg.WriteTimeout == 0 {
		t.Fatal("unsafe MySQL client options or missing timeouts")
	}
}
