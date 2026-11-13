// Command consumer is the probe of SEC-280 step 5. It holds a database/sql
// pool whose connector asks OpenBao for dynamic database credentials: log in
// with the projected service account token, fetch new credentials at half the
// lease, and hand out only credentials that stay valid for at least one more
// minute. The pool settings
// are those of the chargeback store (16 open, 4 idle, 30 minute lifetime).
// It writes one JSON line every 10 seconds.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lib/pq"
)

type creds struct {
	User    string
	Pass    string
	Issued  time.Time
	Expires time.Time
	Lease   time.Duration
}

type source struct {
	addr, authRole, dbRole, tokenPath string
	renewAt                           float64
	hc                                *http.Client

	mu       sync.Mutex
	cur      *creds
	fetchOK  int64
	fetchErr int64
	lastErr  string
}

func (s *source) call(ctx context.Context, method, path, token string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.addr+path, rd)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, out)
}

func (s *source) fetch(ctx context.Context) (*creds, error) {
	jwt, err := os.ReadFile(s.tokenPath)
	if err != nil {
		return nil, err
	}
	var login struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := s.call(ctx, http.MethodPost, "/v1/auth/kubernetes/login", "",
		map[string]string{"role": s.authRole, "jwt": strings.TrimSpace(string(jwt))}, &login); err != nil {
		return nil, err
	}
	var cr struct {
		LeaseDuration int `json:"lease_duration"`
		Data          struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"data"`
	}
	if err := s.call(ctx, http.MethodGet, "/v1/db/creds/"+s.dbRole, login.Auth.ClientToken, nil, &cr); err != nil {
		return nil, err
	}
	now := time.Now()
	d := time.Duration(cr.LeaseDuration) * time.Second
	return &creds{User: cr.Data.Username, Pass: cr.Data.Password, Issued: now, Expires: now.Add(d), Lease: d}, nil
}

// Current returns credentials valid for at least one more minute, fetching new ones when due.
func (s *source) Current(ctx context.Context) (*creds, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil || time.Now().After(s.cur.Issued.Add(time.Duration(float64(s.cur.Lease)*s.renewAt))) {
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, err := s.fetch(fctx)
		cancel()
		if err != nil {
			s.fetchErr++
			s.lastErr = err.Error()
		} else {
			s.cur = c
			s.fetchOK++
		}
	}
	if s.cur == nil || time.Until(s.cur.Expires) < time.Minute {
		return nil, errors.New("no credentials valid for one more minute")
	}
	c := *s.cur
	return &c, nil
}

type connector struct {
	src  *source
	base string
}

func quote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	cr, err := c.src.Current(ctx)
	if err != nil {
		return nil, err
	}
	pc, err := pq.NewConnector(c.base + " user=" + quote(cr.User) + " password=" + quote(cr.Pass))
	if err != nil {
		return nil, err
	}
	return pc.Connect(ctx)
}

func (c *connector) Driver() driver.Driver { return &pq.Driver{} }

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type text struct{ v atomic.Value }

func (t *text) set(s string) { t.v.Store(s) }
func (t *text) get() string {
	if s, ok := t.v.Load().(string); ok {
		return s
	}
	return ""
}

func main() {
	src := &source{
		addr:      env("OPENBAO_ADDR", "http://openbao.bao-a.svc.cluster.local:8200"),
		authRole:  env("AUTH_ROLE", "workload"),
		dbRole:    os.Getenv("DB_ROLE"),
		tokenPath: env("TOKEN_PATH", "/var/run/secrets/openova-token/token"),
		renewAt:   0.5,
		hc:        &http.Client{Timeout: 3 * time.Second},
	}
	db := sql.OpenDB(&connector{src: src, base: os.Getenv("PG_BASE")})
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx := context.Background()

	// Renewal loop: fetch new credentials when half the lease has passed.
	go func() {
		for range time.Tick(10 * time.Second) {
			_, _ = src.Current(ctx)
		}
	}()

	var ok, bad, pOK, pBad atomic.Int64
	var lastErr, pLastErr, pUser text
	for i := 0; i < 4; i++ {
		go func() {
			for range time.Tick(time.Second) {
				qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				var n int
				err := db.QueryRowContext(qctx, "select count(*) from t").Scan(&n)
				cancel()
				if err != nil {
					bad.Add(1)
					lastErr.set(err.Error())
				} else {
					ok.Add(1)
				}
			}
		}()
	}

	// One long-lived connection taken out of the pool, as a session that never reconnects.
	go func() {
		var conn *sql.Conn
		for range time.Tick(10 * time.Second) {
			qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if conn == nil {
				c, err := db.Conn(qctx)
				if err != nil {
					cancel()
					pBad.Add(1)
					pLastErr.set("acquire: " + err.Error())
					continue
				}
				conn = c
			}
			var u string
			err := conn.QueryRowContext(qctx, "select current_user").Scan(&u)
			cancel()
			if err != nil {
				pBad.Add(1)
				pLastErr.set(err.Error())
				_ = conn.Close()
				conn = nil
			} else {
				pOK.Add(1)
				pUser.set(u)
			}
		}
	}()

	enc := json.NewEncoder(os.Stdout)
	for range time.Tick(10 * time.Second) {
		st := db.Stats()
		src.mu.Lock()
		user, exp := "", ""
		if src.cur != nil {
			user, exp = src.cur.User, src.cur.Expires.UTC().Format(time.RFC3339)
		}
		fo, fe, le := src.fetchOK, src.fetchErr, src.lastErr
		src.mu.Unlock()
		_ = enc.Encode(map[string]any{
			"t": time.Now().Unix(), "role": src.dbRole,
			"ok": ok.Swap(0), "err": bad.Swap(0), "last_err": lastErr.get(),
			"pinned_ok": pOK.Swap(0), "pinned_err": pBad.Swap(0), "pinned_last_err": pLastErr.get(),
			"pinned_user": pUser.get(), "user": user, "expires": exp,
			"fetch_ok": fo, "fetch_err": fe, "last_fetch_err": le,
			"open": st.OpenConnections, "in_use": st.InUse, "idle": st.Idle,
			"lifetime_closed": st.MaxLifetimeClosed,
		})
		lastErr.set("")
		pLastErr.set("")
	}
}
