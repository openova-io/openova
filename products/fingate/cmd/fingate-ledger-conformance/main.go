// Command fingate-ledger-conformance proves that a ledger serves the ledger port of the
// open-banking product. It runs the conformance kit against the ledger and prints one line per
// check, PASS or FAIL with the reason, then the number of checks passed and of operations
// called.
//
// Usage:
//
//	fingate-ledger-conformance --target inmemory
//	fingate-ledger-conformance --target <host:port> --fixture <file.json> [--ca-file <file.pem>] [--server-name <name>]
//
// With --target inmemory the kit runs against the in-memory ledger of the product, in the same
// process. Otherwise it dials the ledger over TLS and reads its test data from the fixture, a
// JSON object with the keys customer and other_customer (each an object with scheme and
// subject), debtor_account_id, amount, refused_amount, creditor_name, creditor_scheme_name and
// creditor_identification. The kit submits payments: run it against a test ledger only.
//
// Exit codes: 0 every check passed; 1 a check failed; 2 the run could not start (a wrong flag,
// a fixture or certificate file that cannot be read).
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// readFixture reads a fixture file; an unknown key is an error.
func readFixture(path string) (ledgertest.Fixture, error) {
	var fx ledgertest.Fixture
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fx, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&fx); err != nil {
		return fx, fmt.Errorf("%s: %w", path, err)
	}
	return fx, fx.Validate()
}

// transport returns the TLS credentials that verify the ledger's certificate.
func transport(caFile, serverName string) (credentials.TransportCredentials, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile != "" {
		pem, err := os.ReadFile(filepath.Clean(caFile))
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = x509.NewCertPool()
		if !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no PEM certificate", caFile)
		}
	}
	return credentials.NewTLS(cfg), nil
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fingate-ledger-conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "inmemory, or the host:port of a ledger served over TLS")
	fixture := fs.String("fixture", "", "the JSON file that describes the ledger's test data; required unless the target is inmemory")
	caFile := fs.String("ca-file", "", "a PEM file of the authorities that sign the ledger's certificate; default: the system's")
	serverName := fs.String("server-name", "", "the name the ledger's certificate must carry; default: the host of the target")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long the whole run may take")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(err error) int {
		_, _ = fmt.Fprintf(stderr, "fingate-ledger-conformance: %v\n", err)
		return 2
	}
	if fs.NArg() > 0 {
		return usage(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}

	var (
		conn *grpc.ClientConn
		fx   ledgertest.Fixture
		err  error
	)
	switch *target {
	case "":
		return usage(errors.New("--target is required"))
	case "inmemory":
		if *fixture != "" || *caFile != "" || *serverName != "" {
			return usage(errors.New("--target inmemory takes no --fixture, --ca-file or --server-name"))
		}
		var stop func()
		conn, stop, err = ledgertest.Serve(ledgertest.NewMemory(ledgertest.FullCapabilities()))
		if err != nil {
			return usage(err)
		}
		defer stop()
		fx = ledgertest.MemoryFixture()
	default:
		if *fixture == "" {
			return usage(errors.New("--fixture is required unless the target is inmemory"))
		}
		if fx, err = readFixture(*fixture); err != nil {
			return usage(err)
		}
		creds, err := transport(*caFile, *serverName)
		if err != nil {
			return usage(err)
		}
		if conn, err = grpc.NewClient(*target, grpc.WithTransportCredentials(creds)); err != nil {
			return usage(err)
		}
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	rep := ledgertest.Run(ctx, conn, fx)
	if err := rep.Print(stdout); err != nil {
		return usage(err)
	}
	if !rep.Passed() {
		return 1
	}
	return 0
}
