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
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

// keyPair writes a fresh self-signed certificate for 127.0.0.1 and its key into the test's
// folder. It returns the two paths and a pool that trusts the certificate.
func keyPair(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "adapter"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return certFile, keyFile, pool
}

// TestServesThePortOverTLS starts the program, calls the port and the health service over TLS,
// and stops it as a signal would.
func TestServesThePortOverTLS(t *testing.T) {
	certFile, keyFile, pool := keyPair(t)
	ctx, stop := context.WithCancel(context.Background())
	addr := make(chan net.Addr, 1)
	done := make(chan int, 1)
	var stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--cert-file", certFile, "--key-file", keyFile}, &stderr, func(a net.Addr) { addr <- a })
	}()
	var target string
	select {
	case a := <-addr:
		target = a.String()
	case code := <-done:
		t.Fatalf("the program ended with exit code %d: %s", code, stderr.String())
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ledgerv1.NewLedgerClient(conn).GetCapabilities(cctx, &ledgerv1.GetCapabilitiesRequest{}); err != nil {
		t.Fatalf("GetCapabilities over TLS: %v", err)
	}
	h, err := healthpb.NewHealthClient(conn).Check(cctx, &healthpb.HealthCheckRequest{Service: ledgerv1.Ledger_ServiceDesc.ServiceName})
	if err != nil || h.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health of the ledger service: %v, %v", h.GetStatus(), err)
	}
	stop()
	if code := <-done; code != 0 {
		t.Fatalf("exit code %d after the stop, want 0: %s", code, stderr.String())
	}
}

// TestUsageErrors checks that a wrong start ends with exit code 2.
func TestUsageErrors(t *testing.T) {
	certFile, keyFile, _ := keyPair(t)
	for _, args := range [][]string{
		{},
		{"--cert-file", certFile},
		{"--cert-file", certFile, "--key-file", keyFile, "extra"},
		{"--cert-file", keyFile, "--key-file", certFile},
		{"--no-such-flag"},
	} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, &stderr, nil); code != 2 {
			t.Errorf("%v: exit code %d, want 2", args, code)
		}
	}
}
