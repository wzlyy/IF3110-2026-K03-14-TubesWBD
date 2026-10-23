package database

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

const targetTimeout = 5 * time.Second

// TargetManager owns bounded target pools. Callers must not close returned pools.
// Open returns a pool and DBMS; authorization belongs to the caller's group policy.
type TargetManager struct {
	store   *ConnectionStore
	mu      sync.Mutex
	pools   map[string]*targetPool
	opening map[string]chan struct{}
	closed  bool
}

type targetPool struct {
	db   *sql.DB
	dbms string
}

func NewTargetManager(store *ConnectionStore) *TargetManager {
	return &TargetManager{store: store, pools: make(map[string]*targetPool), opening: make(map[string]chan struct{})}
}

func postgresDSN(c Connection, password string) string {
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Path: "/" + c.DBName, User: url.UserPassword(c.Username, password)}
	q := u.Query()
	q.Set("sslmode", "prefer")
	q.Set("connect_timeout", "4")
	u.RawQuery = q.Encode()
	return u.String()
}

func mysqlConfig(c Connection, password string) *mysql.Config {
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = c.Username, password, c.DBName
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 4*time.Second, 5*time.Second, 5*time.Second
	cfg.ParseTime = true
	cfg.MultiStatements, cfg.AllowAllFiles, cfg.InterpolateParams = false, false, false
	return cfg
}

// Open coalesces creation per ID. Waiting callers can cancel; metadata IO and
// pool closure never hold the map lock. Invalidation detaches an in-progress
// creation so that it cannot publish stale credentials afterward.
func (m *TargetManager) Open(ctx context.Context, id string) (*sql.DB, string, error) {
	ctx, cancel := context.WithTimeout(ctx, targetTimeout)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, "", errors.New("target manager is closed")
		}
		if cached := m.pools[id]; cached != nil {
			m.mu.Unlock()
			return cached.db, cached.dbms, nil
		}
		if ready := m.opening[id]; ready != nil {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-ready:
				continue
			}
		}
		ready := make(chan struct{})
		m.opening[id] = ready
		m.mu.Unlock()

		db, dbms, err := m.openPool(ctx, id)
		m.mu.Lock()
		current := m.opening[id] == ready && !m.closed
		if current {
			delete(m.opening, id)
			if err == nil {
				m.pools[id] = &targetPool{db: db, dbms: dbms}
			}
		}
		close(ready)
		m.mu.Unlock()
		if current {
			return db, dbms, err
		}
		if db != nil {
			_ = db.Close()
		}
		// Metadata changed (or the manager closed) during IO. Check again
		// rather than returning or publishing the detached pool.
	}
}

func (m *TargetManager) openPool(ctx context.Context, id string) (*sql.DB, string, error) {
	c, err := m.store.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if c.DBMS != "postgres" && c.DBMS != "mysql" {
		return nil, "", errors.New("unsupported database type")
	}
	var encrypted string
	if err := m.store.db.QueryRowContext(ctx, "SELECT password_encrypted FROM database_connections WHERE id=$1", id).Scan(&encrypted); err != nil {
		return nil, "", errors.New("cannot load saved credentials")
	}
	password, err := m.store.cipher.decrypt(encrypted)
	if err != nil {
		return nil, "", err
	}
	var db *sql.DB
	if c.DBMS == "postgres" {
		db, err = sql.Open("pgx", postgresDSN(*c, password))
	} else {
		connector, e := mysql.NewConnector(mysqlConfig(*c, password))
		err = e
		if err == nil {
			db = sql.OpenDB(connector)
		}
	}
	if err != nil {
		return nil, "", errors.New("cannot open target connection")
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)
	return db, c.DBMS, nil
}

func (m *TargetManager) Invalidate(id string) {
	m.mu.Lock()
	db := m.pools[id]
	delete(m.pools, id)
	delete(m.opening, id)
	m.mu.Unlock()
	if db != nil {
		_ = db.db.Close()
	}
}

func (m *TargetManager) Close() error {
	m.mu.Lock()
	m.closed = true
	pools := m.pools
	m.pools = make(map[string]*targetPool)
	m.opening = make(map[string]chan struct{})
	m.mu.Unlock()
	for _, db := range pools {
		_ = db.db.Close()
	}
	return nil
}

// Test checks saved settings within five seconds. Driver errors never escape.
func (m *TargetManager) Test(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, targetTimeout)
	defer cancel()
	db, _, err := m.Open(ctx, id)
	if err != nil {
		return sanitizeConnectionError(err)
	}
	return sanitizeConnectionError(db.PingContext(ctx))
}

// sanitizeConnectionError keeps driver details and credentials out of responses.
func sanitizeConnectionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrConnectionNotFound) {
		return ErrConnectionNotFound
	}
	return errors.New("connection failed; check host, port, database, credentials and network access")
}
