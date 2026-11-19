package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

type fakeDB struct {
	store.Store
	closed bool
}

func (f *fakeDB) Ping(context.Context) error { return nil }
func (f *fakeDB) Close()                     { f.closed = true }

func dbFile(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func flags(t *testing.T, more ...string) []string {
	return append([]string{"query", "--database-url-file", dbFile(t, "postgres://cortex@db/cortex\n"), "--issuer", "https://auth.acme.example/realms/acme",
		"--embed-alias", "embed-default", "--embed-dimensions", "1024", "--listen", "127.0.0.1:0", "--health-listen", "127.0.0.1:0"}, more...)
}

func TestItRefusesToStart(t *testing.T) {
	noDB := func(context.Context, string, int) (database, error) { return nil, errors.New("the database is away") }
	for what, args := range map[string][]string{
		"no command":             nil,
		"another command":        {"index"},
		"an unknown flag":        flags(t, "--verbose"),
		"an argument":            flags(t, "extra"),
		"no embed alias":         flags(t, "--embed-alias", ""),
		"a short vector":         flags(t, "--embed-dimensions", "32"),
		"no database file":       flags(t, "--database-url-file", filepath.Join(t.TempDir(), "absent")),
		"an empty database file": flags(t, "--database-url-file", dbFile(t, " \n")),
		"an http issuer":         flags(t, "--issuer", "http://auth.acme.example/realms/acme"),
		"a gateway of no scheme": flags(t, "--model-gateway-url", "model-gateway:8080/v1"),
		"a database that fails":  flags(t, "--rerank-alias", "rerank-default"),
		"an address in use":      flags(t, "--listen", "256.0.0.1:1"),
	} {
		var out bytes.Buffer
		open := func(context.Context, string, int) (database, error) { return &fakeDB{}, nil }
		if what == "a database that fails" {
			open = noDB
		}
		if code := run(context.Background(), args, &out, deps{openStore: open}); code != 2 {
			t.Errorf("%s: exit code %d, want 2:\n%s", what, code, &out)
		}
		if strings.Contains(out.String(), "postgres://") {
			t.Errorf("%s: the log holds the database address:\n%s", what, &out)
		}
	}
}

func TestItServesAndStops(t *testing.T) {
	db := &fakeDB{}
	var url string
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan [2]net.Addr, 1)
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		done <- run(ctx, flags(t), &out, deps{
			openStore: func(_ context.Context, u string, dims int) (database, error) { url = u; return db, nil },
			listening: func(api, health net.Addr) { addrs <- [2]net.Addr{api, health} },
		})
	}()
	var a [2]net.Addr
	select {
	case a = <-addrs:
	case <-time.After(10 * time.Second):
		t.Fatalf("it did not listen:\n%s", &out)
	}
	for addr, want := range map[string]int{"http://" + a[1].String() + "/readyz": 200, "http://" + a[0].String() + "/v1/sources": 401,
		"http://" + a[0].String() + "/healthz": 401, "http://" + a[1].String() + "/v1/sources": 404} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", addr, resp.StatusCode, want)
		}
	}
	cancel()
	if code := <-done; code != 0 || !db.closed || url != "postgres://cortex@db/cortex" {
		t.Fatalf("exit code %d, closed %v, address %q", code, db.closed, url)
	}
}
