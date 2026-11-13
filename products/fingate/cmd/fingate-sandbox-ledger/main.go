// Command fingate-sandbox-ledger serves the ledger port of the open-banking product over
// gRPC and TLS, from the sandbox data set of package sandbox: three made-up customers and five
// accounts, held in memory. It is the test bank that the product and its partners use; it never
// holds a bank's data.
//
// Usage:
//
//	fingate-sandbox-ledger --cert-file <tls.crt> --key-file <tls.key> [--listen :8443]
//	fingate-sandbox-ledger --print-kit-fixture
//
// The certificate and key are PEM files, as a certificate issuer mounts them. They are read
// again on the first connection after either file changed, so a renewed certificate is served
// without a restart. The server also answers the standard gRPC health service, for the empty
// service name and for openova.fingate.ledger.v1.Ledger. It stops on SIGTERM or SIGINT.
//
// --print-kit-fixture prints the fixture that describes the data set to the ledger
// conformance kit, the file that fingate-ledger-conformance reads with --fixture, and exits.
//
// Exit codes: 0 stopped by a signal, or the fixture was printed; 1 the server failed while it
// served; 2 it could not start (a wrong flag, a certificate that cannot be read, an address
// that cannot be listened on).
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/sandbox"
)

// stopGrace is how long a stop waits for calls in flight before it closes every connection.
const stopGrace = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, nil))
}

// keyPair serves a certificate and key from two files and reads them again when either changed.
type keyPair struct {
	certFile, keyFile string

	mu      sync.Mutex
	stamp   [2]time.Time
	current *tls.Certificate
}

// stamps returns the modification times of the two files.
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

func run(ctx context.Context, args []string, stdout, stderr io.Writer, listening func(net.Addr)) int {
	fs := flag.NewFlagSet("fingate-sandbox-ledger", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8443", "the address to serve the ledger port on")
	certFile := fs.String("cert-file", "", "the PEM file of the server certificate; required to serve")
	keyFile := fs.String("key-file", "", "the PEM file of the certificate's private key; required to serve")
	printFixture := fs.Bool("print-kit-fixture", false, "print the conformance kit's fixture of the data set and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "fingate-sandbox-ledger: %v\n", err)
		return 2
	}
	if fs.NArg() > 0 {
		return usage(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *printFixture {
		if *certFile != "" || *keyFile != "" {
			return usage(errors.New("--print-kit-fixture takes no --cert-file or --key-file"))
		}
		b, err := json.MarshalIndent(sandbox.KitFixture(), "", "  ")
		if err != nil {
			return usage(err)
		}
		if _, err := fmt.Fprintf(stdout, "%s\n", b); err != nil {
			return usage(err)
		}
		return 0
	}
	if *certFile == "" || *keyFile == "" {
		return usage(errors.New("--cert-file and --key-file are required"))
	}
	kp := &keyPair{certFile: *certFile, keyFile: *keyFile}
	if _, err := kp.load(); err != nil {
		return usage(err)
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return usage(err)
	}

	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: kp.getCertificate})
	s := grpc.NewServer(grpc.Creds(creds))
	ledgerv1.RegisterLedgerServer(s, sandbox.New())
	hs := health.NewServer()
	hs.SetServingStatus(ledgerv1.Ledger_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)

	served := make(chan error, 1)
	go func() { served <- s.Serve(lis) }()
	_, _ = fmt.Fprintf(stderr, "fingate-sandbox-ledger: serving the ledger port on %s\n", lis.Addr())
	if listening != nil {
		listening(lis.Addr())
	}
	select {
	case err := <-served:
		_, _ = fmt.Fprintf(stderr, "fingate-sandbox-ledger: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	hs.Shutdown()
	force := time.AfterFunc(stopGrace, s.Stop)
	defer force.Stop()
	s.GracefulStop()
	_, _ = fmt.Fprintln(stderr, "fingate-sandbox-ledger: stopped")
	return 0
}
