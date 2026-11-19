package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/mcp/mcptest"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// fakeDB stands in for the PostgreSQL store. The program's own use of PostgreSQL is the
// store's, which internal/store tests against a database.
type fakeDB struct {
	*store.Memory // the records; MigrateUp, Ping and Close are the fake's own
	mu            sync.Mutex
	migrated      int
	migrate       error
	ping          error
	closed        bool
	openedURL     string
	openedDir     string
}

func (f *fakeDB) MigrateUp(context.Context) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrated++
	if f.migrate != nil {
		return nil, f.migrate
	}
	return []int{1}, nil
}

func (f *fakeDB) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ping
}

func (f *fakeDB) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeDB) setPing(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ping = err
}

const goodURL = "postgres://db.ops-records.svc:5432/ops_records?sslmode=require"

// hermetic keeps the driver from finding a password in the environment of the test host.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("PGPASSWORD", "")
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "no-such-file"))
}

func newProgram(t *testing.T, env map[string]string, db *fakeDB) (program, *bytes.Buffer, *bytes.Buffer, *int) {
	t.Helper()
	hermetic(t)
	var stdout, stderr bytes.Buffer
	opened := 0
	if db.Memory == nil {
		db.Memory = store.NewMemory()
	}
	p := program{
		getenv: func(k string) string { return env[k] },
		stdout: &stdout,
		stderr: &stderr,
		open: func(_ context.Context, url, dir string) (database, error) {
			opened++
			db.openedURL, db.openedDir = url, dir
			return db, nil
		},
	}
	return p, &stdout, &stderr, &opened
}

// goodEnv is a configuration that serves on free local addresses. catalyst-api is an address
// where nothing answers; a test that needs it sets its own.
func goodEnv(t *testing.T) map[string]string {
	t.Helper()
	policy := filepath.Join(t.TempDir(), "policy.json")
	writeFile(t, policy, testPolicy)
	return map[string]string{envURL: goodURL, envLoginDir: "/var/run/secrets/ops-records/db", envListen: "127.0.0.1:0",
		envConsoleListen: "127.0.0.1:0", envCatalystAPI: "http://127.0.0.1:1", envIssuer: "https://auth.sovereign.test", envPolicy: policy}
}

// testPolicy is a judge policy with the correlator judge.
const testPolicy = `{"operationsOrganization":"operations",
 "agentRuntime":{"urlTemplate":"http://kagent-controller.{organization}.svc.cluster.local:8083/agents/{organization}/{agent}"},
 "judges":{"correlator":{"agent":"correlator","lane":"deciding","deadlineSeconds":150,"urgentDeadlineSeconds":30,"tokenTier":"viewer","tokenTTLSeconds":1800}},
 "lanes":{"deciding":2},"batchSeconds":60,"urgentBatchSeconds":15,"turnBudget":{"turns":120,"windowSeconds":3600}}`

func TestVersion(t *testing.T) {
	p, stdout, _, opened := newProgram(t, nil, &fakeDB{})
	if code := p.run(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if stdout.String() != "dev\n" || *opened != 0 {
		t.Fatalf("stdout %q, opened %d", stdout.String(), *opened)
	}
}

func TestBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown command":    {"drop"},
		"too many arguments": {"serve", "now"},
	} {
		p, _, stderr, opened := newProgram(t, goodEnv(t), &fakeDB{})
		if code := p.run(context.Background(), args); code != 2 || *opened != 0 {
			t.Errorf("%s: exit code %d, opened %d", name, code, *opened)
		}
		if !strings.Contains(stderr.String(), "usage: ops-records") {
			t.Errorf("%s: no usage line in %q", name, stderr.String())
		}
	}
}

func TestConfigurationIsRequired(t *testing.T) {
	for _, missing := range []string{envURL, envLoginDir} {
		env := goodEnv(t)
		delete(env, missing)
		p, _, stderr, opened := newProgram(t, env, &fakeDB{})
		if code := p.run(context.Background(), []string{"migrate"}); code != 2 || *opened != 0 {
			t.Errorf("without %s: exit code %d, opened %d", missing, code, *opened)
		}
		if !strings.Contains(stderr.String(), "must both be set") {
			t.Errorf("without %s: %q", missing, stderr.String())
		}
	}
}

func TestAPasswordOutsideTheFilesIsRefused(t *testing.T) {
	cases := map[string]func(t *testing.T, env map[string]string){
		"in the URL": func(_ *testing.T, env map[string]string) {
			env[envURL] = "postgres://ops:secret@db.ops-records.svc:5432/ops_records"
		},
		"in a key=value URL": func(_ *testing.T, env map[string]string) {
			env[envURL] = "host=db.ops-records.svc dbname=ops_records password=secret"
		},
		"in PGPASSWORD": func(t *testing.T, _ map[string]string) { t.Setenv("PGPASSWORD", "secret") },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			env := goodEnv(t)
			p, _, stderr, opened := newProgram(t, env, &fakeDB{})
			set(t, env)
			if code := p.run(context.Background(), []string{"migrate"}); code != 2 || *opened != 0 {
				t.Fatalf("exit code %d, opened %d", code, *opened)
			}
			if strings.Contains(stderr.String(), "secret") {
				t.Fatal("the password was written to the log")
			}
			if !strings.Contains(stderr.String(), "password was found outside") {
				t.Fatalf("stderr %q", stderr.String())
			}
		})
	}
}

func TestMigrate(t *testing.T) {
	db := &fakeDB{}
	p, _, stderr, opened := newProgram(t, goodEnv(t), db)
	p.listening = func(net.Addr, net.Addr) { t.Error("migrate started the health routes") }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if code := p.run(ctx, []string{"migrate"}); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	if *opened != 1 || db.migrated != 1 || !db.closed {
		t.Fatalf("opened %d, migrated %d, closed %v", *opened, db.migrated, db.closed)
	}
	if db.openedURL != goodURL || db.openedDir != "/var/run/secrets/ops-records/db" {
		t.Fatalf("opened %q with %q", db.openedURL, db.openedDir)
	}
	if !strings.Contains(stderr.String(), "applied 1 migration(s) [1]") {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestMigrateFailure(t *testing.T) {
	db := &fakeDB{migrate: errors.New("no database")}
	p, _, _, _ := newProgram(t, goodEnv(t), db)
	if code := p.run(context.Background(), nil); code != 1 {
		t.Fatalf("exit code %d, want 1", code)
	}
	if !db.closed {
		t.Fatal("the store was not closed")
	}
}

func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestServeConfigurationIsRequired(t *testing.T) {
	for _, missing := range []string{envCatalystAPI, envIssuer, envPolicy} {
		env := goodEnv(t)
		delete(env, missing)
		p, _, stderr, opened := newProgram(t, env, &fakeDB{})
		if code := p.run(context.Background(), []string{"serve"}); code != 2 || *opened != 0 {
			t.Errorf("without %s: exit code %d, opened %d", missing, code, *opened)
		}
		if !strings.Contains(stderr.String(), "must be set to serve") {
			t.Errorf("without %s: %q", missing, stderr.String())
		}
	}
	env := goodEnv(t)
	writeFile(t, env[envPolicy], `{"judges":{}}`)
	p, _, stderr, opened := newProgram(t, env, &fakeDB{})
	if code := p.run(context.Background(), nil); code != 2 || *opened != 0 || !strings.Contains(stderr.String(), "judge policy") {
		t.Errorf("an invalid policy: exit code %d, opened %d, %q", code, *opened, stderr.String())
	}
}

// call sends one request and decodes the JSON answer, if there is one.
func call(t *testing.T, method, url string, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestServe(t *testing.T) {
	signer := mcptest.NewSigner(t)
	catalyst := httptest.NewServer(http.HandlerFunc(signer.KeySet))
	defer catalyst.Close()
	db := &fakeDB{Memory: store.NewMemory()}
	at := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	acme, globex := ids.NewIncident(), ids.NewIncident()
	undecided := ids.New()
	if err := db.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		for org, id := range map[string]string{"acme": acme, "globex": globex} {
			if err := tx.InsertIncident(ctx, store.Incident{ID: id, Organization: org, Title: org + " web down", Severity: 3,
				SeveritySetBy: store.ByRules, State: store.IncidentOpen, OpenedBy: store.ByRules, OpenedAt: at}); err != nil {
				return err
			}
		}
		return tx.InsertJudgement(ctx, store.Judgement{ID: undecided, Organization: "acme", Judge: "correlator",
			SubjectKind: "signal_batch", SubjectID: "cluster/c1", AskedAt: at, DeadlineAt: at.Add(time.Minute)})
	}); err != nil {
		t.Fatal(err)
	}
	env := goodEnv(t)
	env[envCatalystAPI] = catalyst.URL + "/"
	env[envIssuer] = mcptest.Issuer
	p, _, stderr, _ := newProgram(t, env, db)
	addrs := make(chan [2]net.Addr, 1)
	p.listening = func(a, c net.Addr) { addrs <- [2]net.Addr{a, c} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	codes := make(chan int, 1)
	go func() { codes <- p.run(ctx, nil) }()

	var base, console string
	select {
	case a := <-addrs:
		base, console = "http://"+a[0].String(), "http://"+a[1].String()
	case code := <-codes:
		t.Fatalf("the program ended with exit code %d: %s", code, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("the program did not start listening")
	}
	if db.migrated != 1 {
		t.Fatalf("migrated %d times before serving", db.migrated)
	}
	if j, err := db.GetJudgement(context.Background(), "acme", undecided); err != nil || j.Outcome != store.OutcomeUnavailable {
		t.Fatalf("the undecided judgement was not recovered before serving: %+v %v", j, err)
	}

	status, body := get(t, base+"/healthz")
	if status != http.StatusOK || body["status"] != "ok" || body["version"] != "dev" {
		t.Fatalf("/healthz: %d %v", status, body)
	}
	status, body = get(t, base+"/readyz")
	if status != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("/readyz with a database: %d %v", status, body)
	}
	db.setPing(errors.New("connection refused"))
	status, body = get(t, base+"/readyz")
	checks, _ := body["checks"].(map[string]any)
	if status != http.StatusServiceUnavailable || checks["database"] != "connection refused" {
		t.Fatalf("/readyz without a database: %d %v", status, body)
	}
	db.setPing(nil)

	// The tool endpoint answers an agent token of acme with acme's incidents only.
	bearer := map[string]string{"Authorization": "Bearer " + signer.Token(t, signer.Kid, mcptest.Claims("acme", "correlator"))}
	status, body = call(t, http.MethodPost, base+"/mcp", bearer,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_incidents","arguments":{}}}`)
	text := fmt.Sprint(body["result"])
	if status != http.StatusOK || !strings.Contains(text, acme) || strings.Contains(text, globex) {
		t.Fatalf("POST /mcp list_incidents: %d %v", status, body)
	}
	if status, _ = call(t, http.MethodPost, base+"/mcp", nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without a token: %d", status)
	}
	// The console routes listen on their own address, and only there.
	person := map[string]string{"x-openova-organization": "acme", "x-openova-user": "ops@acme.test", "x-openova-tier": "operator"}
	status, body = call(t, http.MethodGet, console+"/api/v1/ops/incidents", person, "")
	if items, _ := body["items"].([]any); status != http.StatusOK || len(items) != 1 {
		t.Fatalf("GET /api/v1/ops/incidents on the console address: %d %v", status, body)
	}
	if status, _ = call(t, http.MethodGet, base+"/api/v1/ops/incidents", person, ""); status != http.StatusNotFound {
		t.Fatalf("the console routes answer on the tool address: %d", status)
	}
	status, _ = call(t, http.MethodPost, console+"/api/v1/ops/incidents/"+acme+"/actions", bearer, `{"action":"resolve"}`)
	if in, _ := db.GetIncident(context.Background(), "acme", acme); status != http.StatusForbidden || in.State != store.IncidentOpen {
		t.Fatalf("an agent token on the actions route: %d, state %s", status, in.State)
	}

	// An open stream does not keep the program from stopping.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, console+"/api/v1/ops/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range person {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/ops/stream: %v %v", resp, err)
	}
	defer func() { _ = resp.Body.Close() }()

	cancel()
	select {
	case code := <-codes:
		if code != 0 {
			t.Fatalf("exit code %d after the stop signal: %s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the program did not stop")
	}
	if !db.closed {
		t.Fatal("the store was not closed")
	}
}

func TestServeOnABusyAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	for _, key := range []string{envListen, envConsoleListen} {
		env := goodEnv(t)
		env[key] = ln.Addr().String()
		p, _, _, _ := newProgram(t, env, &fakeDB{})
		if code := p.run(context.Background(), []string{"serve"}); code != 1 {
			t.Fatalf("%s busy: exit code %d, want 1", key, code)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadCredentials(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "username"), "v-ops-records-abc\n")
	writeFile(t, filepath.Join(dir, "password"), "first\r\n")
	user, password, err := readCredentials(dir)
	if err != nil || user != "v-ops-records-abc" || password != "first" {
		t.Fatalf("got %q %q %v", user, password, err)
	}
	// A rotated credential is read on the next call.
	writeFile(t, filepath.Join(dir, "password"), "second")
	if _, password, _ = readCredentials(dir); password != "second" {
		t.Fatalf("after rotation: %q", password)
	}
	writeFile(t, filepath.Join(dir, "password"), "\n")
	if _, _, err := readCredentials(dir); err == nil {
		t.Fatal("an empty password was accepted")
	}
	if err := os.Remove(filepath.Join(dir, "password")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCredentials(dir); err == nil {
		t.Fatal("a missing password file was accepted")
	}
	if _, _, err := readCredentials(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("a missing directory was accepted")
	}
}

func TestCredentialsAreReadForEachConnection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "username"), "v-ops-records-1")
	writeFile(t, filepath.Join(dir, "password"), "first")
	cfg := postgresConfig(goodURL, dir)
	if cfg.URL != goodURL || cfg.Credentials == nil {
		t.Fatalf("config %+v", cfg)
	}
	user, password, err := cfg.Credentials(context.Background())
	if err != nil || user != "v-ops-records-1" || password != "first" {
		t.Fatalf("first connection: %q %q %v", user, password, err)
	}
	writeFile(t, filepath.Join(dir, "username"), "v-ops-records-2")
	writeFile(t, filepath.Join(dir, "password"), "second")
	user, password, err = cfg.Credentials(context.Background())
	if err != nil || user != "v-ops-records-2" || password != "second" {
		t.Fatalf("after rotation: %q %q %v", user, password, err)
	}
}

func TestOpenPostgresNeedsTheFiles(t *testing.T) {
	hermetic(t)
	if _, err := openPostgres(context.Background(), goodURL, t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "database credentials") {
		t.Fatalf("open without credential files: %v", err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "username"), "u")
	writeFile(t, filepath.Join(dir, "password"), "p")
	db, err := openPostgres(context.Background(), goodURL, dir)
	if err != nil {
		t.Fatalf("open with credential files: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
