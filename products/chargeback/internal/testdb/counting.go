package testdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// StatementLog is every statement a Store issued, in order — the seam a
// test uses to pin how many round trips a request costs. OpenCounted wraps
// the lib/pq connector so each Exec, Query and prepared execution lands
// here with its SQL text; the statements themselves are unchanged.
type StatementLog struct {
	mu   sync.Mutex
	sqls []string
}

func (l *StatementLog) add(q string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sqls = append(l.sqls, q)
}

// Reset forgets what was recorded so far.
func (l *StatementLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sqls = nil
}

// Statements returns the SQL recorded since the last Reset.
func (l *StatementLog) Statements() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sqls...)
}

// Ledger returns the recorded statements that read the usage ledger — the
// priced CTE over usage_records / cost_usage_daily and the rollup
// freshness read over cost_rollup_state.
func (l *StatementLog) Ledger() []string {
	var out []string
	for _, q := range l.Statements() {
		if strings.Contains(q, "usage_records") || strings.Contains(q, "cost_usage_daily") || strings.Contains(q, "cost_rollup_state") {
			out = append(out, q)
		}
	}
	return out
}

// OpenCounted is Open with a StatementLog on the connection. The database
// is the one Open migrates and wipes; the counted pool is a second
// connection to it, closed with the test.
func OpenCounted(t *testing.T) (*store.Store, *StatementLog) {
	t.Helper()
	Open(t)
	dsn := os.Getenv(EnvVar)
	inner, err := pq.NewConnector(dsn)
	if err != nil {
		t.Fatalf("connector: %v", err)
	}
	log := &StatementLog{}
	db := sql.OpenDB(countingConnector{inner: inner, log: log})
	db.SetMaxOpenConns(16)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping counted pool: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return store.New(db), log
}

type countingConnector struct {
	inner driver.Connector
	log   *StatementLog
}

func (c countingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, log: c.log}, nil
}

func (c countingConnector) Driver() driver.Driver { return c.inner.Driver() }

// countingConn forwards to the lib/pq connection and records every
// statement. It implements the context-aware interfaces database/sql probes
// for, so the driver keeps its own wire behaviour (no fallback to Prepare).
type countingConn struct {
	driver.Conn
	log *StatementLog
}

func (c *countingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.log.add(query)
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

func (c *countingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.log.add(query)
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *countingConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	st, err := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &countingStmt{Stmt: st, query: query, log: c.log}, nil
}

func (c *countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *countingConn) Ping(ctx context.Context) error {
	if p, ok := c.Conn.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *countingConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.Conn.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

type countingStmt struct {
	driver.Stmt
	query string
	log   *StatementLog
}

func (s *countingStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.log.add(s.query)
	return s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func (s *countingStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	s.log.add(s.query)
	return s.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}
