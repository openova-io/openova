// Command cortex runs the processes of the AI hub's document search: the query process, and the
// index process of index.go:
//
//	cortex query --database-url-file <file> --issuer <https address> --embed-alias <alias>
//	    --embed-dimensions <64..4096> [--rerank-alias <alias>] [--model-gateway-url <address>]
//	    --originals-dir <folder> --definitions-file <file> [--max-upload-mb 50] [--model-key-file <file>]
//	    [--listen :8080] [--health-listen :9090]
//
// The query process applies the store's migrations, connects with the role cortex_query and
// serves on --listen POST /v1/search, GET /v1/sources, POST /v1/sources/{id}/documents, GET
// /v1/documents/{id}, GET /v1/documents/{id}/content, DELETE /v1/documents/{id}, GET /v1/events,
// GET /v1/models and POST /v1/chat/completions, every call behind the check of a token
// of the Organization's realm, whose issuer is --issuer (the chart value identity.issuerURL);
// the realm's key set is found through the issuer's OpenID configuration. On --health-listen,
// which no route publishes, it serves GET /healthz, GET /readyz (200 only while the database
// answers) and GET /metrics.
//
// --database-url-file names a file that holds the PostgreSQL connection address, as a secrets
// agent mounts it; the address is never a flag or an environment value. The model gateway is
// reached at --model-gateway-url (the chart value modelGateway.baseURL) with the virtual key in
// --model-key-file. --embed-alias and --embed-dimensions are models.embed of the parameters;
// --rerank-alias is models.rerank.alias, and without it a search is never reranked.
// --originals-dir is the folder of the originals binding (contract object-storage v1); an
// upload is at most --max-upload-mb megabytes (policy.maxUploadMb, 1 to 1024).
// --definitions-file is the definitions ConfigMap mounted as a file; the assistants, the chat
// alias and policy.allowExternalModels are read from it on each call (package assistant).
//
// Exit codes: 0 stopped by a signal; 1 a listener failed while it served; 2 it could not start
// (a wrong flag, a file that cannot be read, a database that cannot be reached or migrated, an
// address that cannot be listened on).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/openova-io/openova/products/cortex/internal/assistant"
	"github.com/openova-io/openova/products/cortex/internal/auth"
	"github.com/openova-io/openova/products/cortex/internal/events"
	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/httpapi"
	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/search"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// stopGrace is how long a stop waits for calls in flight.
const stopGrace = 10 * time.Second

// database is the store the query process serves from.
type database interface {
	store.Store
	Ping(ctx context.Context) error
	Close()
}

// deps are what run takes from outside. A nil field takes the real one.
type deps struct {
	openStore  func(ctx context.Context, url string, dimensions int) (database, error) // nil: migrate, then open as cortex_query
	openIndex  func(ctx context.Context, url string, dimensions int) (indexDB, error)  // nil: migrate, then open as cortex_index
	openBucket func(dir string) (ingest.Objects, error)                                // nil: ingest.OpenBucket
	kube       func() (kubernetes.Interface, error)                                    // nil: the in-cluster client
	listening  func(api, health net.Addr)                                              // called when the addresses are bound
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, deps{}))
}

func openPostgres(ctx context.Context, url string, dimensions int) (database, error) {
	if _, err := store.Migrate(ctx, url, dimensions); err != nil {
		return nil, err
	}
	return store.Open(ctx, url, store.RoleQuery, dimensions)
}

func run(ctx context.Context, args []string, stderr io.Writer, d deps) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if len(args) > 0 && args[0] == "index" {
		return runIndex(ctx, log, args[1:], stderr, d)
	}
	if len(args) == 0 || args[0] != "query" {
		_, _ = fmt.Fprintln(stderr, "usage: cortex query|index [flags]; run cortex query -h or cortex index -h for the flags")
		return 2
	}
	fs := flag.NewFlagSet("cortex query", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8080", "address of the routes")
	healthListen := fs.String("health-listen", ":9090", "address of /healthz, /readyz and /metrics")
	dbFile := fs.String("database-url-file", "", "file that holds the PostgreSQL connection address")
	issuer := fs.String("issuer", "", "issuer of the Organization's realm, an https address")
	embedAlias := fs.String("embed-alias", "", "gateway alias for embeddings")
	dims := fs.Int("embed-dimensions", 0, "vector length of the embedding alias, 64 to 4096")
	rerankAlias := fs.String("rerank-alias", "", "gateway alias for rerank; empty: no rerank")
	gatewayURL := fs.String("model-gateway-url", gateway.DefaultBaseURL, "address of the model gateway")
	keyFile := fs.String("model-key-file", gateway.DefaultKeyFile, "file that holds the virtual key of the gateway")
	originals := fs.String("originals-dir", "", "folder of the originals bucket's binding")
	maxUpload := fs.Int("max-upload-mb", 50, "largest upload in megabytes, 1 to 1024")
	defsFile := fs.String("definitions-file", "", "the definitions document, the ConfigMap mounted as a file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	start := func() (*httpapi.Server, database, error) {
		switch {
		case fs.NArg() != 0:
			return nil, nil, fmt.Errorf("unexpected arguments %q", fs.Args())
		case *dbFile == "" || *embedAlias == "" || *originals == "" || *defsFile == "":
			return nil, nil, errors.New("--database-url-file, --embed-alias, --originals-dir and --definitions-file are required")
		case *maxUpload < 1 || *maxUpload > 1024:
			return nil, nil, fmt.Errorf("--max-upload-mb %d is not between 1 and 1024", *maxUpload)
		case *dims < 64 || *dims > 4096:
			return nil, nil, fmt.Errorf("--embed-dimensions %d is not between 64 and 4096", *dims)
		}
		raw, err := os.ReadFile(filepath.Clean(*dbFile))
		if err != nil || strings.TrimSpace(string(raw)) == "" {
			return nil, nil, fmt.Errorf("the database address file %s cannot be read or is empty", *dbFile)
		}
		client := &http.Client{Timeout: 30 * time.Second}
		checker, err := auth.New(auth.Config{Issuer: *issuer, Keys: auth.DiscoveryKeySource(client, *issuer)})
		if err != nil {
			return nil, nil, err
		}
		gw, err := gateway.New(*gatewayURL, *keyFile, client)
		if err != nil {
			return nil, nil, err
		}
		bucket, err := openBucket(d, *originals)
		if err != nil {
			return nil, nil, err
		}
		defs := &assistant.File{Path: *defsFile, Log: log}
		if _, err := defs.Config(); err != nil {
			return nil, nil, err
		}
		open := d.openStore
		if open == nil {
			open = openPostgres
		}
		db, err := open(ctx, strings.TrimSpace(string(raw)), *dims)
		if err != nil {
			return nil, nil, err
		}
		svc := &search.Service{Store: db, Embedder: gw.Embedder(*embedAlias, *dims)}
		if *rerankAlias != "" {
			svc.Reranker = gw.Reranker(*rerankAlias)
		}
		hub := events.NewHub()
		go hub.Run(ctx, db, log, 5*time.Second)
		return httpapi.New(httpapi.Config{Auth: checker, Search: svc, Store: db, Uploads: &ingest.Intake{Objects: bucket, Queue: db},
			Assistant: &assistant.Service{Config: defs.Config, Search: svc, Gateway: gw},
			Originals: bucket, Hub: hub, MaxUpload: int64(*maxUpload) << 20, Ready: db.Ping, Log: log}), db, nil
	}
	srv, db, err := start()
	if err != nil {
		log.Error("cortex query cannot start", "error", err.Error())
		return 2
	}
	defer db.Close()
	return serve(ctx, log, d, srv, *listen, *healthListen)
}

// serve runs both listeners until ctx ends or one fails.
func serve(ctx context.Context, log *slog.Logger, d deps, srv *httpapi.Server, listen, healthListen string) int {
	var lc net.ListenConfig
	api, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		log.Error("cortex query cannot listen", "error", err.Error())
		return 2
	}
	health, err := lc.Listen(ctx, "tcp", healthListen)
	if err != nil {
		_ = api.Close()
		log.Error("cortex query cannot listen", "error", err.Error())
		return 2
	}
	if d.listening != nil {
		d.listening(api.Addr(), health.Addr())
	}
	servers := []*http.Server{{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second},
		{Handler: srv.Health(), ReadHeaderTimeout: 10 * time.Second}}
	errs := make(chan error, 2)
	for i, ln := range []net.Listener{api, health} {
		go func() { errs <- servers[i].Serve(ln) }()
	}
	log.Info("cortex query serves", "listen", api.Addr().String(), "health", health.Addr().String())
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errs:
		log.Error("a listener failed", "error", err.Error())
		code = 1
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(stop)
	}
	return code
}

// openBucket opens the originals binding in dir.
func openBucket(d deps, dir string) (ingest.Objects, error) {
	if d.openBucket != nil {
		return d.openBucket(dir)
	}
	return ingest.OpenBucket(dir)
}
