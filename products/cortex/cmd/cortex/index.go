package main

// The index process:
//
//	cortex index --database-url-file <file> --embed-alias <alias> --embed-dimensions <64..4096>
//	    --originals-dir <folder> [--namespace <namespace>] [--definitions-configmap cortex-definitions]
//	    [--model-gateway-url <address>] [--model-key-file <file>] [--health-listen :9090]
//
// It applies the store's migrations, connects with the role cortex_index, runs the definitions
// loader on the ConfigMap --definitions-configmap of --namespace (default: the service account's
// namespace) and runs the index and delete jobs. On --health-listen, which no route publishes,
// it serves GET /healthz, GET /readyz and GET /metrics. Shared flags and exit codes are those
// of the query process.

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
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/cortex/internal/definitions"
	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/httpapi"
	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/jobs"
	"github.com/openova-io/openova/products/cortex/internal/parse"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// namespaceFile holds the namespace of the pod's service account.
const namespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// indexDB is the store the index process writes; *store.Postgres is one.
type indexDB interface {
	definitions.Applier
	ingest.IndexStore
	jobs.Queue
	Ping(ctx context.Context) error
	Close()
}

func openIndex(ctx context.Context, url string, dimensions int) (indexDB, error) {
	if _, err := store.Migrate(ctx, url, dimensions); err != nil {
		return nil, err
	}
	return store.Open(ctx, url, store.RoleIndex, dimensions)
}

func inCluster() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func runIndex(ctx context.Context, log *slog.Logger, args []string, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("cortex index", flag.ContinueOnError)
	fs.SetOutput(stderr)
	healthListen := fs.String("health-listen", ":9090", "address of /healthz and /readyz")
	dbFile := fs.String("database-url-file", "", "file that holds the PostgreSQL connection address")
	embedAlias := fs.String("embed-alias", "", "gateway alias for embeddings")
	dims := fs.Int("embed-dimensions", 0, "vector length of the embedding alias, 64 to 4096")
	gatewayURL := fs.String("model-gateway-url", gateway.DefaultBaseURL, "address of the model gateway")
	keyFile := fs.String("model-key-file", gateway.DefaultKeyFile, "file that holds the virtual key of the gateway")
	originals := fs.String("originals-dir", "", "folder of the originals bucket's binding")
	namespace := fs.String("namespace", "", "namespace of the definitions ConfigMap; empty: that of the service account")
	configMap := fs.String("definitions-configmap", definitions.ConfigMapName, "name of the definitions ConfigMap")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	start := func() (indexDB, *definitions.Loader, *jobs.Worker, error) {
		switch {
		case fs.NArg() != 0:
			return nil, nil, nil, fmt.Errorf("unexpected arguments %q", fs.Args())
		case *dbFile == "" || *embedAlias == "" || *originals == "":
			return nil, nil, nil, errors.New("--database-url-file, --embed-alias and --originals-dir are required")
		case *dims < 64 || *dims > 4096:
			return nil, nil, nil, fmt.Errorf("--embed-dimensions %d is not between 64 and 4096", *dims)
		}
		if *namespace == "" {
			b, err := os.ReadFile(namespaceFile)
			if err != nil || strings.TrimSpace(string(b)) == "" {
				return nil, nil, nil, errors.New("--namespace is empty and the service account's namespace cannot be read")
			}
			*namespace = strings.TrimSpace(string(b))
		}
		raw, err := os.ReadFile(filepath.Clean(*dbFile))
		if err != nil || strings.TrimSpace(string(raw)) == "" {
			return nil, nil, nil, fmt.Errorf("the database address file %s cannot be read or is empty", *dbFile)
		}
		gw, err := gateway.New(*gatewayURL, *keyFile, &http.Client{Timeout: 5 * time.Minute})
		if err != nil {
			return nil, nil, nil, err
		}
		bucket, err := openBucket(d, *originals)
		if err != nil {
			return nil, nil, nil, err
		}
		kube := d.kube
		if kube == nil {
			kube = inCluster
		}
		kc, err := kube()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("the cluster cannot be reached: %w", err)
		}
		open := d.openIndex
		if open == nil {
			open = openIndex
		}
		db, err := open(ctx, strings.TrimSpace(string(raw)), *dims)
		if err != nil {
			return nil, nil, nil, err
		}
		loader := definitions.NewLoader(kc, *namespace, *configMap, db, func(format string, a ...any) { log.Info(fmt.Sprintf(format, a...)) })
		indexer := &ingest.Indexer{Store: db, Objects: bucket, Parser: parse.Text{}, Embedder: gw.Embedder(*embedAlias, *dims)}
		return db, loader, &jobs.Worker{Queue: db, Handle: indexer.Handle, Log: log}, nil
	}
	db, loader, worker, err := start()
	if err != nil {
		log.Error("cortex index cannot start", "error", err.Error())
		return 2
	}
	defer db.Close()
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", *healthListen)
	if err != nil {
		log.Error("cortex index cannot listen", "error", err.Error())
		return 2
	}
	work, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		if err := loader.Run(work); err != nil {
			log.Error("the definitions loader stopped", "error", err.Error())
		}
	}()
	go func() { _ = worker.Run(work) }()
	// The health paths are those of the query process; the index process has no route.
	health := httpapi.New(httpapi.Config{Ready: db.Ping, Log: log}).Health()
	srv := &http.Server{Handler: health, ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	if d.listening != nil {
		d.listening(nil, ln.Addr())
	}
	log.Info("cortex index serves", "health", ln.Addr().String())
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errs:
		log.Error("a listener failed", "error", err.Error())
		code = 1
	}
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
	defer cancel()
	_ = srv.Shutdown(grace)
	return code
}
