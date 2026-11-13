package main

import (
	"bytes"
	"context"
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
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

// writeCert writes a fresh self-signed certificate for 127.0.0.1 with the given serial number
// and its key into dir, stamps both files with the given time, and returns the certificate.
func writeCert(t *testing.T, dir string, serial int64, stamp time.Time) *x509.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "sandbox-ledger"},
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
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}
	for name, b := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// syncBuffer is a buffer that the server goroutine and the test may use at once.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// serve starts the program on a free loopback port with the files of dir. It returns the
// address and a function that stops the program and returns its exit code and its log.
func serve(t *testing.T, dir string) (string, func() (int, string)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addrs := make(chan net.Addr, 1)
	codes := make(chan int, 1)
	log := &syncBuffer{}
	go func() {
		codes <- run(ctx, []string{"--listen", "127.0.0.1:0", "--cert-file", filepath.Join(dir, "tls.crt"), "--key-file", filepath.Join(dir, "tls.key")},
			&syncBuffer{}, log, func(a net.Addr) { addrs <- a })
	}()
	select {
	case a := <-addrs:
		stopped := false
		stop := func() (int, string) {
			if !stopped {
				stopped = true
				cancel()
			}
			return <-codes, log.String()
		}
		t.Cleanup(func() {
			if !stopped {
				stop()
			}
		})
		return a.String(), stop
	case code := <-codes:
		cancel()
		t.Fatalf("the program exited with code %d before it served:\n%s", code, log.String())
		return "", nil
	}
}

func pool(certs ...*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}

func TestServesThePortOverTLS(t *testing.T) {
	dir := t.TempDir()
	cert := writeCert(t, dir, 1, time.Now().Add(-time.Minute))
	addr, stop := serve(t, dir)

	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"--print-kit-fixture"}, &out, &errOut, nil); code != 0 {
		t.Fatalf("--print-kit-fixture: exit code %d\n%s", code, errOut.String())
	}
	var fx ledgertest.Fixture
	d := json.NewDecoder(&out)
	d.DisallowUnknownFields()
	if err := d.Decode(&fx); err != nil {
		t.Fatalf("the kit cannot read the printed fixture: %v", err)
	}

	creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool(cert)})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rep := ledgertest.Run(ctx, conn, fx)
	var report strings.Builder
	if err := rep.Print(&report); err != nil {
		t.Fatal(err)
	}
	if !rep.Passed() || !strings.HasSuffix(report.String(), "checks passed: 41/41\noperations called: 14/14\n") {
		t.Fatalf("the kit did not pass against the program:\n%s", report.String())
	}

	for _, service := range []string{"", "openova.fingate.ledger.v1.Ledger"} {
		h, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		if err != nil || h.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			t.Fatalf("health of %q: %v (%v), want SERVING", service, h.GetStatus(), err)
		}
	}

	code, log := stop()
	if code != 0 || !strings.Contains(log, "serving the ledger port on 127.0.0.1:") || !strings.HasSuffix(log, "stopped\n") {
		t.Fatalf("exit code %d, log:\n%s", code, log)
	}
}

// served returns the serial number of the certificate the server presents to a new connection.
func served(t *testing.T, addr string, roots *x509.CertPool) int64 {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	return c.ConnectionState().PeerCertificates[0].SerialNumber.Int64()
}

func TestServesARenewedCertificate(t *testing.T) {
	dir := t.TempDir()
	first := writeCert(t, dir, 1, time.Now().Add(-time.Hour))
	addr, _ := serve(t, dir)
	if got := served(t, addr, pool(first)); got != 1 {
		t.Fatalf("serial %d, want 1", got)
	}
	second := writeCert(t, dir, 2, time.Now())
	if got := served(t, addr, pool(first, second)); got != 2 {
		t.Fatalf("after renewal: serial %d, want 2", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte("half written\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := served(t, addr, pool(first, second)); got != 2 {
		t.Fatalf("with an unreadable file: serial %d, want the last good certificate 2", got)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	writeCert(t, dir, 1, time.Now())
	crt, key := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	cases := map[string][]string{
		"no certificate":               {"--listen", "127.0.0.1:0"},
		"no key":                       {"--listen", "127.0.0.1:0", "--cert-file", crt},
		"a certificate that is absent": {"--listen", "127.0.0.1:0", "--cert-file", filepath.Join(dir, "absent.crt"), "--key-file", key},
		"a key that is not the pair":   {"--listen", "127.0.0.1:0", "--cert-file", crt, "--key-file", crt},
		"an address in use":            {"--listen", busy.Addr().String(), "--cert-file", crt, "--key-file", key},
		"an argument":                  {"--cert-file", crt, "--key-file", key, "extra"},
		"the fixture with a key":       {"--print-kit-fixture", "--key-file", key},
		"an unknown flag":              {"--colour", "blue"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(context.Background(), args, &out, &errOut, nil); code != 2 {
				t.Fatalf("exit code %d, want 2\n%s%s", code, out.String(), errOut.String())
			}
		})
	}
}
