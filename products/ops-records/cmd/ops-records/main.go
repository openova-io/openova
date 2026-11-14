// Command ops-records is the operations-records service.
//
//	ops-records [serve]   apply the pending migrations, then serve the health routes
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
//	OPS_RECORDS_LISTEN                    the address of the health routes; default ":8080"
//
// A password in the URL or in the environment of the driver (PGPASSWORD, a password file)
// is refused: the database credential reaches the program only as the mounted files.
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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openova-io/openova/products/ops-records/internal/health"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// version is set at build time: -ldflags "-X main.version=<version>".
var version = "dev"

const (
	envURL        = "OPS_RECORDS_DATABASE_URL"
	envLoginDir   = "OPS_RECORDS_DATABASE_CREDENTIALS_DIR"
	envListen     = "OPS_RECORDS_LISTEN"
	defaultListen = ":8080"
	shutdownGrace = 10 * time.Second
)

// database is what the program needs of the store.
type database interface {
	MigrateUp(ctx context.Context) ([]int, error)
	Ping(ctx context.Context) error
	Close() error
}

// program holds what the commands read and write, so that a test can replace each part.
type program struct {
	getenv    func(string) string
	stdout    io.Writer
	stderr    io.Writer
	open      func(ctx context.Context, url, credentialsDir string) (database, error)
	listening func(addr net.Addr) // called once the health routes listen; may be nil
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
	return p.serve(ctx, db)
}

func (p program) serve(ctx context.Context, db database) int {
	addr := p.getenv(envListen)
	if addr == "" {
		addr = defaultListen
	}
	h := health.New(version, 0)
	h.Add("database", db.Ping)
	mux := http.NewServeMux()
	h.Register(mux)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return p.fail(1, "listen on %s: %v", addr, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	p.logf("serving the health routes on %s, version %s", ln.Addr(), version)
	if p.listening != nil {
		p.listening(ln.Addr())
	}

	select {
	case err := <-served:
		return p.fail(1, "serve: %v", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return p.fail(1, "shutdown: %v", err)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return p.fail(1, "serve: %v", err)
	}
	p.logf("stopped")
	return 0
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
