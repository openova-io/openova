// Command ops-records is the operations-records service.
//
//	ops-records [serve]   apply the pending migrations, then serve and decide (below)
//	ops-records migrate   apply the pending migrations and exit
//	ops-records version   print the version and exit
//
// Environment:
//
//	OPS_RECORDS_DATABASE_URL              required: the database, as a postgres:// URL or a
//	                                      key=value string, with no password
//	OPS_RECORDS_DATABASE_CREDENTIALS_DIR  required: a directory with the two files username
//	                                      and password, mounted from OpenBao; both are read
//	                                      again for every new connection, so a rotated
//	                                      credential is used without a restart
//	OPS_RECORDS_LISTEN                    the address of the health routes and of the tool
//	                                      endpoint POST /mcp; default ":8080"
//	OPS_RECORDS_CONSOLE_LISTEN            the address of the console routes /api/v1/ops/*, which
//	                                      the chart opens to catalyst-api pods only; default ":8081"
//	OPS_RECORDS_CATALYST_API_URL          serve, required: catalyst-api's base address; the
//	                                      agent-token route and the key set route are under it
//	OPS_RECORDS_TOKEN_ISSUER              serve, required: the iss every agent token must carry
//	OPS_RECORDS_JUDGE_POLICY              serve, required: the file of the judge policy (JSON,
//	                                      package judge: ParsePolicy)
//
// A password in the URL or in the environment of the driver (PGPASSWORD, a password file)
// is refused: the database credential reaches the program only as the mounted files.
//
// Serving: the program marks every judgement a stopped run left undecided (judge.Recover),
// then decides the batches of pending signals: with the correlator judge when the policy has
// one, else by the rules alone (decide.go). It answers the tools of package mcp and the
// console routes of package api, and sends every change of an incident to the stream.
//
// Exit codes: 0 done; 1 the database or the server failed; 2 the configuration is wrong.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openova-io/openova/products/ops-records/internal/api"
	"github.com/openova-io/openova/products/ops-records/internal/health"
	"github.com/openova-io/openova/products/ops-records/internal/judge"
	"github.com/openova-io/openova/products/ops-records/internal/mcp"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// version is set at build time: -ldflags "-X main.version=<version>".
var version = "dev"

const (
	envURL               = "OPS_RECORDS_DATABASE_URL"
	envLoginDir          = "OPS_RECORDS_DATABASE_CREDENTIALS_DIR"
	envListen            = "OPS_RECORDS_LISTEN"
	envConsoleListen     = "OPS_RECORDS_CONSOLE_LISTEN"
	envCatalystAPI       = "OPS_RECORDS_CATALYST_API_URL"
	envIssuer            = "OPS_RECORDS_TOKEN_ISSUER"
	envPolicy            = "OPS_RECORDS_JUDGE_POLICY"
	defaultListen        = ":8080"
	defaultConsoleListen = ":8081"
	shutdownGrace        = 10 * time.Second
)

// database is what the program needs of the store.
type database interface {
	store.Store
	MigrateUp(ctx context.Context) ([]int, error)
}

// program holds what the commands read and write, so that a test can replace each part.
type program struct {
	getenv    func(string) string
	stdout    io.Writer
	stderr    io.Writer
	open      func(ctx context.Context, url, credentialsDir string) (database, error)
	listening func(main, console net.Addr) // called once both addresses listen; may be nil
}

// serveConfig is what serving needs beyond the database.
type serveConfig struct {
	catalystAPI, issuer string
	policy              judge.Policy
}

// readServeConfig reads the configuration of serve.
func (p program) readServeConfig() (serveConfig, error) {
	c := serveConfig{catalystAPI: strings.TrimRight(p.getenv(envCatalystAPI), "/"), issuer: p.getenv(envIssuer)}
	file := p.getenv(envPolicy)
	if c.catalystAPI == "" || c.issuer == "" || file == "" {
		return c, fmt.Errorf("%s, %s and %s must be set to serve", envCatalystAPI, envIssuer, envPolicy)
	}
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return c, fmt.Errorf("the judge policy cannot be read: %w", err)
	}
	if c.policy, err = judge.ParsePolicy(raw); err != nil {
		return c, fmt.Errorf("the judge policy %s: %w", file, err)
	}
	return c, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	p := program{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr, open: openPostgres}
	code := p.run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func (p program) run(ctx context.Context, args []string) int {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	if len(args) > 1 {
		return p.fail(2, "too many arguments; usage: ops-records [serve|migrate|version]")
	}
	switch command {
	case "version":
		_, _ = fmt.Fprintln(p.stdout, version)
		return 0
	case "serve", "migrate":
	default:
		return p.fail(2, "unknown command %q; usage: ops-records [serve|migrate|version]", command)
	}

	url, dir := p.getenv(envURL), p.getenv(envLoginDir)
	if url == "" || dir == "" {
		return p.fail(2, "%s and %s must both be set", envURL, envLoginDir)
	}
	if err := refusePassword(url); err != nil {
		return p.fail(2, "%v", err)
	}
	var cfg serveConfig
	if command == "serve" {
		var err error
		if cfg, err = p.readServeConfig(); err != nil {
			return p.fail(2, "%v", err)
		}
	}
	db, err := p.open(ctx, url, dir)
	if err != nil {
		return p.fail(2, "%v", err)
	}
	defer func() { _ = db.Close() }()

	applied, err := db.MigrateUp(ctx)
	if err != nil {
		return p.fail(1, "migrate: %v", err)
	}
	p.logf("schema ops: applied %d migration(s) %v", len(applied), applied)
	if command == "migrate" {
		return 0
	}
	return p.serve(ctx, db, cfg)
}

func (p program) serve(ctx context.Context, db database, cfg serveConfig) int {
	addr, consoleAddr := p.getenv(envListen), p.getenv(envConsoleListen)
	if addr == "" {
		addr = defaultListen
	}
	if consoleAddr == "" {
		consoleAddr = defaultConsoleListen
	}
	n, err := judge.Recover(ctx, db)
	if err != nil {
		return p.fail(1, "recover the undecided judgements: %v", err)
	}
	p.logf("marked %d undecided judgement(s) unavailable", n)
	hub := api.NewHub()
	dec, err := newDecider(db, cfg, hub, p.logf)
	if err != nil {
		return p.fail(2, "%v", err)
	}
	defer dec.close()

	h := health.New(version, 0)
	h.Add("database", db.Ping)
	mux := http.NewServeMux()
	h.Register(mux)
	verifier := &mcp.Verifier{Keys: &mcp.KeySet{URL: cfg.catalystAPI + mcp.KeySetPath}, Issuer: cfg.issuer}
	mux.Handle("/mcp", &mcp.Server{Store: db, Verify: verifier.Verify, Version: version, Hub: hub})
	console := http.NewServeMux()
	(&api.Server{Store: db, Hub: hub, Wake: dec.wake}).Register(console)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return p.fail(1, "listen on %s: %v", addr, err)
	}
	cln, err := net.Listen("tcp", consoleAddr)
	if err != nil {
		_ = ln.Close()
		return p.fail(1, "listen on %s: %v", consoleAddr, err)
	}
	servers := []*http.Server{{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		{Handler: console, ReadHeaderTimeout: 5 * time.Second}}
	servers[1].RegisterOnShutdown(hub.Close) // ends the streams, which are never idle
	served := make(chan error, 2)
	go func() { served <- servers[0].Serve(ln) }()
	go func() { served <- servers[1].Serve(cln) }()
	dctx, stopDeciding := context.WithCancel(ctx)
	decided := make(chan struct{})
	go func() { dec.run(dctx); close(decided) }()
	p.logf("serving the health routes and POST /mcp on %s, the console routes on %s, version %s", ln.Addr(), cln.Addr(), version)
	if p.listening != nil {
		p.listening(ln.Addr(), cln.Addr())
	}

	code, running := 0, len(servers)
	select {
	case err := <-served:
		code, running = p.fail(1, "serve: %v", err), running-1
	case <-ctx.Done():
	}
	stopDeciding()
	<-decided
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(shutdown); err != nil {
			code = p.fail(1, "shutdown: %v", err)
		}
	}
	for ; running > 0; running-- {
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			code = p.fail(1, "serve: %v", err)
		}
	}
	if code == 0 {
		p.logf("stopped")
	}
	return code
}

// refusePassword returns an error when the driver would find a password anywhere but in the
// credential files: in the URL, in PGPASSWORD or in a password file.
func refusePassword(url string) error {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return fmt.Errorf("%s cannot be read: %v", envURL, err)
	}
	if cfg.Password != "" {
		return fmt.Errorf("a database password was found outside %s; put it in the file password there, "+
			"never in %s, PGPASSWORD or a password file", envLoginDir, envURL)
	}
	return nil
}

// readCredentials reads the files username and password of dir. One final line end is removed.
func readCredentials(dir string) (string, string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = root.Close() }()
	var values [2]string
	for i, name := range []string{"username", "password"} {
		f, err := root.Open(name)
		if err != nil {
			return "", "", err
		}
		raw, err := io.ReadAll(io.LimitReader(f, 4096))
		_ = f.Close()
		if err != nil {
			return "", "", err
		}
		v := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
		if v == "" {
			return "", "", fmt.Errorf("the file %s in %s is empty", name, dir)
		}
		values[i] = v
	}
	return values[0], values[1], nil
}

// postgresConfig is the store configuration: the URL, and the credential files of dir read
// for every new connection.
func postgresConfig(url, dir string) store.PostgresConfig {
	return store.PostgresConfig{
		URL: url,
		Credentials: func(context.Context) (string, string, error) {
			return readCredentials(dir)
		},
	}
}

// openPostgres opens the PostgreSQL store with the credential files of dir. It fails when the
// files cannot be read now.
func openPostgres(ctx context.Context, url, dir string) (database, error) {
	if _, _, err := readCredentials(dir); err != nil {
		return nil, fmt.Errorf("database credentials: %w", err)
	}
	return store.OpenPostgres(ctx, postgresConfig(url, dir))
}

func (p program) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.stderr, "ops-records: "+format+"\n", args...)
}

func (p program) fail(code int, format string, args ...any) int {
	p.logf(format, args...)
	return code
}
