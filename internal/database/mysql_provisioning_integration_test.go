//go:build integration

package database

import (
	"database/sql"
	"net"
	"os"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
)

// Administrative setup is never routed through application SQL execution.
func mysqlProvisioningFixture(t *testing.T) *sql.DB {
	t.Helper()
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", net.JoinHostPort(os.Getenv("TARGET_MYSQL_HOST"), os.Getenv("TARGET_MYSQL_PORT")), os.Getenv("TARGET_MYSQL_NAME")
	cfg.User, cfg.Passwd = os.Getenv("TARGET_MYSQL_ADMIN_USER"), os.Getenv("TARGET_MYSQL_ADMIN_PASSWORD")
	if cfg.User == "" {
		cfg.User = "root"
	}
	if cfg.Passwd == "" {
		cfg.Passwd = os.Getenv("TARGET_MYSQL_PASSWORD")
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid test provisioning configuration")
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatal("test provisioning connection unavailable")
	}
	return db
}
