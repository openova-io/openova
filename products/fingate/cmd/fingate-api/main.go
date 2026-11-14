// Command fingate-api is the resource server of the open-banking product. It serves every
// operation of the profile's documents behind the token rules, over mutual TLS: a caller that
// presents no client certificate, or one that no authority of --client-ca-file issued, is refused
// in the handshake. The consents are kept in PostgreSQL, whose migrations it applies at start.
// The definitions of the installation are read from a ConfigMap of its own namespace through an
// informer, so a change of a partner's state takes effect on the next call. Core banking is
// reached only through the ledger port, over gRPC and TLS, at the target the definitions name.
//
// Usage:
//
//	fingate-api --cert-file <tls.crt> --key-file <tls.key> --client-ca-file <ca.crt>
//	    --database-url-file <file> --namespace <namespace> --profiles-dir <folder>
//	    --issuer <https address> --jwks-url <https address> [--issuer-ca-file <ca.crt>]
//	    [--ledger-ca-file <ca.crt>] [--definitions-configmap fingate-definitions]
//	    [--listen :8443] [--health-listen :8080]
//
// The certificate, the key and the authority bundles are PEM files, as a certificate issuer
// mounts them. The server certificate and key are read again on the first handshake after either
// file changed. --database-url-file names a file that holds the PostgreSQL connection address,
// as a secrets agent mounts it; the address is never a flag or an environment variable.
// --profiles-dir is the folder that holds one folder per profile; the profile named by the
// definitions is loaded from it. --issuer is the realm's issuer (rule T3), --jwks-url the address
// of its key set (rule T2); --issuer-ca-file, when given, holds the only authorities trusted for
// that address. --ledger-ca-file, when given, holds the only authorities trusted for the
// certificate of the ledger adapter; without it the system's authorities are trusted.
//
// The program waits for the first valid definitions, then builds the routes and the token check
// from the profile and the host they name, opens the connection to the ledger adapter at their
// ledger.target, and starts to serve. The adapter's certificate must carry the name
// ledger.serverName, or the host of the target when that is empty; the connection is made on the
// first call to the ledger. The profile, the host and the ledger are read once: a later version of
// the definitions that changes one of them is refused, and a restart applies it. TLS 1.2 is
// limited to the cipher suites that FAPI 1.0 Advanced permits and Go implements; TLS 1.3 is
// allowed.
//
// The health listener answers GET /healthz with 200 while the program runs, and GET /readyz with
// 200 only while the resource server serves and the database answers, 503 otherwise. The ledger
// is not part of readiness: while it cannot answer, an operation that reads it answers 500 and
// the consent operations keep working.
//
// Exit codes: 0 stopped by a signal; 1 a listener failed while it served; 2 it could not start
// (a wrong flag, a file that cannot be read, an address that cannot be listened on, a database
// that cannot be reached or migrated, a profile that cannot be loaded, a ledger target that
// cannot be used).
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/fingate/internal/api"
	"github.com/openova-io/openova/products/fingate/internal/definitions"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
	"github.com/openova-io/openova/products/fingate/internal/profile"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/token"
)

// stopGrace is how long a stop waits for calls in flight before it closes every connection.
const stopGrace = 10 * time.Second

// fapiSuites are the TLS 1.2 cipher suites of FAPI 1.0 Advanced, section 8.5, that Go implements.
var fapiSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384}

// database is the consent store the program serves from.
type database interface {
	store.Store
	Ping(ctx context.Context) error
	Close()
}

// deps are what run takes from outside. A nil field takes the real one.
type deps struct {
	kube      func() (kubernetes.Interface, error)                    // nil: the in-cluster client
	openStore func(ctx context.Context, url string) (database, error) // nil: store.OpenPostgres
	listening func(api, health net.Addr)                              // called when both addresses are bound
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, deps{}))
}

// keyPair serves a certificate and key from two files and reads them again when either changed.
type keyPair struct {
	certFile, keyFile string

	mu      sync.Mutex
	stamp   [2]time.Time
	current *tls.Certificate
}

func (k *keyPair) stamps() ([2]time.Time, error) {
	var s [2]time.Time
	for i, f := range []string{k.certFile, k.keyFile} {
		fi, err := os.Stat(f)
		if err != nil {
			return s, err
		}
		s[i] = fi.ModTime()
	}
	return s, nil
}

// load reads the files when they changed since the last read. When a changed pair cannot be
// read, the certificate read before stays in use.
func (k *keyPair) load() (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s, err := k.stamps()
	if err == nil && k.current != nil && s == k.stamp {
		return k.current, nil
	}
	if err == nil {
		var c tls.Certificate
		if c, err = tls.LoadX509KeyPair(filepath.Clean(k.certFile), filepath.Clean(k.keyFile)); err == nil {
			k.current, k.stamp = &c, s
			return k.current, nil
		}
	}
	if k.current != nil {
		return k.current, nil
	}
	return nil, err
}

func (k *keyPair) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) { return k.load() }

// readPool reads a PEM bundle that must hold at least one certificate.
func readPool(file string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("%s holds no PEM certificate", file)
	}
	return pool, nil
}

func httpsAddress(flagName, value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("--%s %q is not an https address", flagName, value)
	}
	return nil
}

// pin accepts the first definitions and then only versions with the same profile, host and
// ledger: the routes, the token audience and the connection to the ledger adapter are built from
// them once, at start.
type pin struct {
	mu            sync.Mutex
	profile, host string
	ledger        definitions.Ledger
}

// ledgerText is a ledger in the form of the program's messages: the target, and the name its
// certificate must carry when the definitions give one.
func ledgerText(l definitions.Ledger) string {
	if l.ServerName == "" {
		return l.Target
	}
	return l.Target + " as " + l.ServerName
}

func (p *pin) accept(d *definitions.Definitions) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.profile == "" {
		p.profile, p.host, p.ledger = d.Profile, d.Host, d.Ledger
		return nil
	}
	if d.Profile != p.profile || d.Host != p.host || d.Ledger != p.ledger {
		return fmt.Errorf("the profile, the host and the ledger are read once at start (%s, %s, %s); a restart applies profile %s, host %s, ledger %s",
			p.profile, p.host, ledgerText(p.ledger), d.Profile, d.Host, ledgerText(d.Ledger))
	}
	return nil
}

func run(ctx context.Context, args []string, stderr io.Writer, d deps) int {
	fs := flag.NewFlagSet("fingate-api", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8443", "the address of the resource server, served with mutual TLS")
	healthListen := fs.String("health-listen", ":8080", "the address of /healthz and /readyz, plain HTTP")
	certFile := fs.String("cert-file", "", "the PEM file of the server certificate; required")
	keyFile := fs.String("key-file", "", "the PEM file of the certificate's private key; required")
	clientCAFile := fs.String("client-ca-file", "", "the PEM file of the authorities that issue client certificates; required")
	dbFile := fs.String("database-url-file", "", "the file that holds the PostgreSQL connection address; required")
	namespace := fs.String("namespace", "", "the namespace of the definitions ConfigMap; required")
	configMap := fs.String("definitions-configmap", definitions.ConfigMapName, "the name of the definitions ConfigMap")
	profilesDir := fs.String("profiles-dir", "", "the folder that holds one folder per profile; required")
	issuer := fs.String("issuer", "", "the realm's issuer, an https address; required")
	jwksURL := fs.String("jwks-url", "", "the https address of the realm's key set; required")
	issuerCAFile := fs.String("issuer-ca-file", "", "the PEM file of the authorities trusted for --jwks-url; default the system's")
	ledgerCAFile := fs.String("ledger-ca-file", "", "the PEM file of the authorities trusted for the ledger adapter's certificate; default the system's")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logf := func(format string, a ...any) { _, _ = fmt.Fprintf(stderr, "fingate-api: "+format+"\n", a...) }
	usage := func(err error) int { logf("%v", err); return 2 }
	if fs.NArg() > 0 {
		return usage(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	var missing []string
	for _, f := range []struct{ name, value string }{{"cert-file", *certFile}, {"key-file", *keyFile},
		{"client-ca-file", *clientCAFile}, {"database-url-file", *dbFile}, {"namespace", *namespace},
		{"profiles-dir", *profilesDir}, {"issuer", *issuer}, {"jwks-url", *jwksURL}} {
		if f.value == "" {
			missing = append(missing, "--"+f.name)
		}
	}
	if len(missing) > 0 {
		return usage(fmt.Errorf("required: %s", strings.Join(missing, ", ")))
	}
	if *configMap == "" {
		return usage(errors.New("--definitions-configmap is empty"))
	}
	for _, f := range []struct{ name, value string }{{"issuer", *issuer}, {"jwks-url", *jwksURL}} {
		if err := httpsAddress(f.name, f.value); err != nil {
			return usage(err)
		}
	}
	kp := &keyPair{certFile: *certFile, keyFile: *keyFile}
	if _, err := kp.load(); err != nil {
		return usage(err)
	}
	clientCAs, err := readPool(*clientCAFile)
	if err != nil {
		return usage(err)
	}
	jwksClient := &http.Client{Timeout: 10 * time.Second}
	if *issuerCAFile != "" {
		roots, err := readPool(*issuerCAFile)
		if err != nil {
			return usage(err)
		}
		jwksClient.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}
	}
	ledgerTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	if *ledgerCAFile != "" {
		if ledgerTLS.RootCAs, err = readPool(*ledgerCAFile); err != nil {
			return usage(err)
		}
	}
	raw, err := os.ReadFile(filepath.Clean(*dbFile))
	if err != nil {
		return usage(err)
	}
	dbURL := strings.TrimSpace(string(raw))
	if dbURL == "" {
		return usage(fmt.Errorf("--database-url-file %s is empty", *dbFile))
	}

	apiLis, err := net.Listen("tcp", *listen)
	if err != nil {
		return usage(err)
	}
	defer func() { _ = apiLis.Close() }()
	healthLis, err := net.Listen("tcp", *healthListen)
	if err != nil {
		return usage(err)
	}
	defer func() { _ = healthLis.Close() }()
	if d.listening != nil {
		d.listening(apiLis.Addr(), healthLis.Addr())
	}

	openStore := d.openStore
	if openStore == nil {
		openStore = func(ctx context.Context, url string) (database, error) { return store.OpenPostgres(ctx, url, nil) }
	}
	db, err := openStore(ctx, dbURL)
	if err != nil {
		return usage(fmt.Errorf("the database: %w", err))
	}
	defer db.Close()
	kube := d.kube
	if kube == nil {
		kube = func() (kubernetes.Interface, error) {
			cfg, err := rest.InClusterConfig()
			if err != nil {
				return nil, err
			}
			return kubernetes.NewForConfig(cfg)
		}
	}
	client, err := kube()
	if err != nil {
		return usage(fmt.Errorf("the Kubernetes client: %w", err))
	}

	holder := &definitions.Holder{}
	inf := definitions.NewInformer(client, *namespace, *configMap, holder, (&pin{}).accept, logf)
	infCtx, stopInformer := context.WithCancel(ctx)
	informed := make(chan error, 1)
	go func() { informed <- inf.Run(infCtx) }()
	defer func() { stopInformer(); <-informed }()

	var ready atomic.Bool
	health := http.NewServeMux()
	health.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	health.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		pctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !ready.Load() || db.Ping(pctx) != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ready\n")
	})
	errorLog := log.New(stderr, "fingate-api: ", 0)
	healthSrv := &http.Server{Handler: health, ReadHeaderTimeout: 10 * time.Second, ErrorLog: errorLog}
	served := make(chan error, 2)
	go func() { served <- healthSrv.Serve(healthLis) }()
	defer func() { _ = healthSrv.Close() }()

	logf("waiting for the definitions in ConfigMap %s/%s", *namespace, *configMap)
	select {
	case <-inf.Loaded():
	case err := <-served:
		logf("%v", err)
		return 1
	case <-ctx.Done():
		logf("stopped before the definitions were loaded")
		return 0
	}
	defs := holder.Current()
	dir := filepath.Join(filepath.Clean(*profilesDir), defs.Profile)
	prof, err := profile.LoadDir(dir)
	if err != nil {
		return usage(fmt.Errorf("profile %s: %w", defs.Profile, err))
	}
	checker, err := token.New(token.Config{Profile: prof, Issuer: *issuer, Audience: "https://" + defs.Host,
		Keys: token.HTTPKeySource(jwksClient, *jwksURL), Partners: holder, Consents: api.Consents{Store: db}})
	if err != nil {
		return usage(err)
	}
	ledgerConn, err := ledgerclient.Dial(defs.Ledger.Target, defs.Ledger.ServerName, ledgerTLS)
	if err != nil {
		return usage(err)
	}
	defer func() { _ = ledgerConn.Close() }()
	srv, err := api.New(api.Config{Profile: prof, Documents: os.DirFS(dir), Definitions: holder, Checker: checker, Store: db,
		Ledger: ledgerclient.New(ledgerConn, 0)})
	if err != nil {
		return usage(err)
	}
	apiSrv := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second, ErrorLog: errorLog,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, CipherSuites: fapiSuites, GetCertificate: kp.getCertificate,
			ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}}
	go func() { served <- apiSrv.ServeTLS(apiLis, "", "") }()
	ready.Store(true)
	logf("serving the resource server on %s with mutual TLS, profile %s, host %s, ledger %s; health on %s",
		apiLis.Addr(), defs.Profile, defs.Host, ledgerText(defs.Ledger), healthLis.Addr())
	select {
	case err := <-served:
		logf("%v", err)
		return 1
	case <-ctx.Done():
	}
	ready.Store(false)
	sctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	_ = apiSrv.Shutdown(sctx)
	_ = healthSrv.Shutdown(sctx)
	logf("stopped")
	return 0
}
