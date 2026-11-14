package ledgerclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/ledgertest"
)

// memory serves the in-memory ledger of the kit with the given resources and returns a Client of it.
func memory(t *testing.T, resources ...string) *Client {
	t.Helper()
	conn, stop, err := ledgertest.Serve(ledgertest.NewMemory(&ledgerv1.Capabilities{Resources: resources, Currencies: []string{"GBP"}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); stop() })
	return New(conn, 0)
}

func TestEveryReadReachesTheLedger(t *testing.T) {
	c := memory(t, ledgertest.Resources...)
	ctx := context.Background()
	cust, err := c.ResolveCustomer(ctx, "kit", "alice", nil)
	if err != nil || cust.GetCustomerId() != "cust-alice" {
		t.Fatalf("ResolveCustomer: %v %v", cust, err)
	}
	accs, err := c.Accounts(ctx, "cust-alice")
	if err != nil || len(accs) != 2 {
		t.Fatalf("Accounts: %d %v", len(accs), err)
	}
	if a, err := c.Account(ctx, "cust-alice", "acc-1001"); err != nil || a.GetAccountId() != "acc-1001" {
		t.Fatalf("Account: %v %v", a, err)
	}
	if b, err := c.Balances(ctx, "cust-alice", "acc-1001"); err != nil || len(b) != 1 {
		t.Fatalf("Balances: %v %v", b, err)
	}
	if caps, err := c.Capabilities(ctx); err != nil || len(caps.GetResources()) != 5 {
		t.Fatalf("Capabilities: %v %v", caps, err)
	}
	// Walk every page of the transactions.
	var ids []string
	page := ""
	for i := 0; i < 5; i++ {
		p, err := c.Transactions(ctx, "cust-alice", "acc-1001", "", "", page)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range p.Items {
			ids = append(ids, x.GetTransactionId())
		}
		if page = p.Next; page == "" {
			break
		}
	}
	if strings.Join(ids, ",") != "tx-1001-1,tx-1001-2,tx-1001-3" {
		t.Fatalf("transactions: %v", ids)
	}
	lists := map[string]func() (int, error){
		"beneficiaries": func() (int, error) {
			p, err := c.Beneficiaries(ctx, "cust-alice", "acc-1001", "")
			return len(p.Items), err
		},
		"standing-orders": func() (int, error) {
			p, err := c.StandingOrders(ctx, "cust-alice", "acc-1001", "")
			return len(p.Items), err
		},
		"direct-debits": func() (int, error) {
			p, err := c.DirectDebits(ctx, "cust-alice", "acc-1001", "")
			return len(p.Items), err
		},
		"scheduled-payments": func() (int, error) {
			p, err := c.ScheduledPayments(ctx, "cust-alice", "acc-1001", "")
			return len(p.Items), err
		},
	}
	for name, f := range lists {
		if n, err := f(); err != nil || n != 1 {
			t.Errorf("%s: %d items, %v", name, n, err)
		}
	}
	if p, err := c.Party(ctx, "cust-alice", ""); err != nil || p.GetPartyId() != "party-alice" {
		t.Fatalf("Party of the customer: %v %v", p, err)
	}
	if p, err := c.Party(ctx, "cust-alice", "acc-1001"); err != nil || p.GetPartyId() != "party-alice-1001" {
		t.Fatalf("Party of the account: %v %v", p, err)
	}
}

func TestTheLedgersCodesAreMapped(t *testing.T) {
	c := memory(t) // no resource offered
	ctx := context.Background()
	_, err := c.Account(ctx, "cust-alice", "acc-unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown account: %v", err)
	}
	_, err = c.Account(ctx, "cust-alice", "acc-2001")
	if !errors.Is(err, ErrNotOwned) {
		t.Errorf("another customer's account: %v", err)
	}
	_, err = c.Beneficiaries(ctx, "cust-alice", "acc-1001", "")
	if !errors.Is(err, ErrNotOffered) {
		t.Errorf("a resource that is not offered: %v", err)
	}
	_, err = c.Transactions(ctx, "cust-alice", "acc-1001", "", "", "not-a-token")
	if !errors.Is(err, ErrBadRequest) {
		t.Errorf("a page token the ledger did not issue: %v", err)
	}
	for code, want := range map[codes.Code]error{
		codes.FailedPrecondition: ErrRefused, codes.AlreadyExists: ErrConflict, codes.Unavailable: ErrUnavailable,
		codes.DeadlineExceeded: ErrUnavailable, codes.Internal: ErrUnavailable, codes.Unknown: ErrUnavailable,
	} {
		got := mapErr("Op", status.Error(code, "why"))
		if !errors.Is(got, want) || got.Error() != "ledger Op: "+want.Error()+": why" {
			t.Errorf("%s: %v", code, got)
		}
	}
	if mapErr("Op", nil) != nil {
		t.Error("nil is not mapped to nil")
	}
}

// slow is a ledger that answers GetCapabilities only after the caller gave up.
type slow struct {
	ledgerv1.UnimplementedLedgerServer
}

func (slow) GetCapabilities(ctx context.Context, _ *ledgerv1.GetCapabilitiesRequest) (*ledgerv1.Capabilities, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestEveryCallHasADeadline(t *testing.T) {
	conn, stop, err := ledgertest.Serve(slow{})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	defer func() { _ = conn.Close() }()
	start := time.Now()
	_, err = New(conn, 50*time.Millisecond).Capabilities(context.Background())
	if !errors.Is(err, ErrUnavailable) || time.Since(start) > 5*time.Second {
		t.Fatalf("after %s: %v", time.Since(start), err)
	}
	if New(conn, 0).timeout != DefaultTimeout {
		t.Fatal("a zero timeout is not the default")
	}
}

// selfSigned returns a certificate for the name ledger.test and a pool that trusts it.
func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "ledger.test"},
		DNSNames: []string{"ledger.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}, pool
}

func TestDialSpeaksTLSAndChecksTheName(t *testing.T) {
	cert, pool := selfSigned(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})))
	ledgerv1.RegisterLedgerServer(s, ledgertest.NewMemory(ledgertest.FullCapabilities()))
	go func() { _ = s.Serve(lis) }()
	defer s.Stop()
	target := "dns:///" + lis.Addr().String()

	tc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	conn, err := Dial(target, "ledger.test", tc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := New(conn, 5*time.Second).Capabilities(context.Background()); err != nil {
		t.Fatalf("over TLS with the right name: %v", err)
	}
	if tc.ServerName != "" {
		t.Fatal("Dial changed the configuration it was given")
	}

	wrong, err := Dial(target, "other.test", tc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wrong.Close() }()
	if _, err := New(wrong, 5*time.Second).Capabilities(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a certificate for another name was accepted: %v", err)
	}
	if _, err := Dial(target, "ledger.test", nil); err == nil {
		t.Fatal("Dial without a TLS configuration was accepted")
	}
}
