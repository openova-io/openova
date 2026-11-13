package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

func runCmd(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestInMemoryTargetPasses(t *testing.T) {
	code, out, errOut := runCmd("--target", "inmemory")
	if code != 0 {
		t.Fatalf("exit code %d, want 0\n%s%s", code, out, errOut)
	}
	if !strings.HasSuffix(out, "checks passed: 41/41\noperations called: 14/14\n") {
		t.Fatalf("the summary lines are missing:\n%s", out)
	}
}

// tlsLedger serves the in-memory ledger over TLS on the loopback address with a fresh
// self-signed certificate for 127.0.0.1. It returns the address and the certificate in PEM.
func tlsLedger(t *testing.T) (string, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ledger"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(grpc.Creds(credentials.NewServerTLSFromCert(&cert)))
	ledgerv1.RegisterLedgerServer(s, ledgertest.NewMemory(ledgertest.FullCapabilities()))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// write writes a file into the test's folder and returns its path.
func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureFile(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(ledgertest.MemoryFixture())
	if err != nil {
		t.Fatal(err)
	}
	return write(t, "fixture.json", b)
}

func TestTLSTargetPasses(t *testing.T) {
	addr, caPEM := tlsLedger(t)
	code, out, errOut := runCmd("--target", addr, "--fixture", fixtureFile(t), "--ca-file", write(t, "ca.pem", caPEM))
	if code != 0 {
		t.Fatalf("exit code %d, want 0\n%s%s", code, out, errOut)
	}
	if !strings.HasSuffix(out, "checks passed: 41/41\noperations called: 14/14\n") {
		t.Fatalf("the summary lines are missing:\n%s", out)
	}
}

func TestUntrustedCertificateFails(t *testing.T) {
	addr, _ := tlsLedger(t)
	code, out, errOut := runCmd("--target", addr, "--fixture", fixtureFile(t), "--timeout", "20s")
	if code != 1 {
		t.Fatalf("exit code %d, want 1\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "FAIL capabilities: ") {
		t.Fatalf("the capabilities check did not fail:\n%s", out)
	}
}

func TestUsageErrors(t *testing.T) {
	unknownKey := write(t, "unknown.json", []byte(`{"customer":{"scheme":"kit","subject":"alice"},"colour":"blue"}`))
	cases := map[string][]string{
		"no target":                   {},
		"an argument":                 {"--target", "inmemory", "extra"},
		"inmemory with a fixture":     {"--target", "inmemory", "--fixture", "x.json"},
		"an address without fixture":  {"--target", "127.0.0.1:1"},
		"a fixture that is missing":   {"--target", "127.0.0.1:1", "--fixture", filepath.Join(t.TempDir(), "absent.json")},
		"a fixture with unknown keys": {"--target", "127.0.0.1:1", "--fixture", unknownKey},
		"a certificate file of no PEM": {"--target", "127.0.0.1:1", "--fixture", fixtureFile(t),
			"--ca-file", write(t, "empty.pem", []byte("no certificate here\n"))},
		"an unknown flag": {"--colour", "blue"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if code, out, errOut := runCmd(args...); code != 2 {
				t.Fatalf("exit code %d, want 2\n%s%s", code, out, errOut)
			}
		})
	}
}
