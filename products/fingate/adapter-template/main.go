// Command adapter-template is the starting point of a ledger adapter: a program that serves the
// ledger port of the open-banking product, openova.fingate.ledger.v1.Ledger, over gRPC and TLS,
// in front of one bank's core-banking system.
//
// As it is, every operation answers UNIMPLEMENTED and GetCapabilities answers empty lists. The
// author of an adapter copies this folder into a repository of their own and fills in the
// methods of ledger.go, one operation at a time, until the conformance program
// fingate-ledger-conformance passes against it. The guide is
// products/fingate/docs/writing-an-adapter.md in the platform repository.
//
// Usage:
//
//	adapter-template --cert-file <tls.crt> --key-file <tls.key> [--listen :8443]
//
// The certificate and key are PEM files. They are read once, at start. The server also answers
// the standard gRPC health service, for the empty service name and for
// openova.fingate.ledger.v1.Ledger. It stops on SIGTERM or SIGINT.
//
// Exit codes: 0 stopped by a signal; 1 the server failed while it served; 2 it could not start
// (a wrong flag, a certificate that cannot be read, an address that cannot be listened on).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// stopGrace is how long a stop waits for calls in flight before it closes every connection.
const stopGrace = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stderr, nil))
}

func run(ctx context.Context, args []string, stderr io.Writer, listening func(net.Addr)) int {
	fs := flag.NewFlagSet("adapter-template", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", ":8443", "the address to serve the ledger port on")
	certFile := fs.String("cert-file", "", "the PEM file of the server certificate; required")
	keyFile := fs.String("key-file", "", "the PEM file of the certificate's private key; required")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "adapter-template: %v\n", err)
		return 2
	}
	if fs.NArg() > 0 {
		return usage(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if *certFile == "" || *keyFile == "" {
		return usage(errors.New("--cert-file and --key-file are required"))
	}
	cert, err := tls.LoadX509KeyPair(filepath.Clean(*certFile), filepath.Clean(*keyFile))
	if err != nil {
		return usage(err)
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return usage(err)
	}

	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	s := grpc.NewServer(grpc.Creds(creds))
	ledgerv1.RegisterLedgerServer(s, newLedger())
	hs := health.NewServer()
	hs.SetServingStatus(ledgerv1.Ledger_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, hs)

	served := make(chan error, 1)
	go func() { served <- s.Serve(lis) }()
	_, _ = fmt.Fprintf(stderr, "adapter-template: serving the ledger port on %s\n", lis.Addr())
	if listening != nil {
		listening(lis.Addr())
	}
	select {
	case err := <-served:
		_, _ = fmt.Fprintf(stderr, "adapter-template: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	hs.Shutdown()
	force := time.AfterFunc(stopGrace, s.Stop)
	defer force.Stop()
	s.GracefulStop()
	_, _ = fmt.Fprintln(stderr, "adapter-template: stopped")
	return 0
}
