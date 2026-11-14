package definitions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const informerNS = "open-banking"

// informerDoc is a valid definitions document whose one partner is in the given state.
func informerDoc(host, state string) string {
	return `profile: rw-4.0.0
host: ` + host + `
ledger: {target: "dns:///ledger-adapter:8443"}
partners:
  - id: acme-pay
    legalName: Acme Pay Ltd
    state: ` + state + `
    applications:
      - id: wallet
        clientAuth: tls_client_auth
        redirectUris: ["https://wallet.acmepay.example/callback"]
        certificates: [{sha256: "pZGm1Av0IEBKARczz7exkNYsZb8LzaMrV7J32a2fFG4", notAfter: "2027-11-12"}]
        products: [accounts]
`
}

func cm(name string, data map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: informerNS}, Data: data}
}

type informerFixture struct {
	client *fake.Clientset
	holder *Holder
	inf    *Informer
	lines  chan string
}

// startInformer runs an informer on ConfigMapName whose accept refuses the host "refused.example".
func startInformer(t *testing.T, objects ...*corev1.ConfigMap) *informerFixture {
	t.Helper()
	f := &informerFixture{client: fake.NewClientset(), holder: &Holder{}, lines: make(chan string, 64)}
	for _, o := range objects {
		f.create(t, o)
	}
	accept := func(d *Definitions) error {
		if d.Host == "refused.example" {
			return errors.New("the host may not change")
		}
		return nil
	}
	f.inf = NewInformer(f.client, informerNS, ConfigMapName, f.holder, accept, func(format string, a ...any) {
		f.lines <- fmt.Sprintf(format, a...)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.inf.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	return f
}

func (f *informerFixture) create(t *testing.T, o *corev1.ConfigMap) {
	t.Helper()
	if _, err := f.client.CoreV1().ConfigMaps(informerNS).Create(context.Background(), o, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func (f *informerFixture) update(t *testing.T, o *corev1.ConfigMap) {
	t.Helper()
	if _, err := f.client.CoreV1().ConfigMaps(informerNS).Update(context.Background(), o, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// next returns the next line the informer reported, and fails after ten seconds.
func (f *informerFixture) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-f.lines:
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("the informer reported nothing within ten seconds")
		return ""
	}
}

func (f *informerFixture) expect(t *testing.T, prefix string) {
	t.Helper()
	if l := f.next(t); !strings.HasPrefix(l, prefix) {
		t.Fatalf("reported %q, want a line that starts with %q", l, prefix)
	}
}

func (f *informerFixture) partnerState(t *testing.T) string {
	t.Helper()
	app, ok, err := f.holder.Application(context.Background(), "acme-pay.wallet")
	if err != nil || !ok {
		t.Fatalf("lookup: %v %v", ok, err)
	}
	return app.PartnerState
}

func TestInformerLoadsAndFollowsTheConfigMap(t *testing.T) {
	f := startInformer(t, cm(ConfigMapName, map[string]string{DataKey: informerDoc("api.bank.example", "approved")}))
	f.expect(t, "definitions: loaded ConfigMap open-banking/fingate-definitions")
	select {
	case <-f.inf.Loaded():
	default:
		t.Fatal("Loaded is not closed after the first valid document")
	}
	if got := f.holder.Current().Host; got != "api.bank.example" || f.partnerState(t) != "approved" {
		t.Fatalf("host %q, partner %q", got, f.partnerState(t))
	}

	f.update(t, cm(ConfigMapName, map[string]string{DataKey: informerDoc("api.bank.example", "suspended")}))
	f.expect(t, "definitions: loaded")
	if s := f.partnerState(t); s != "suspended" {
		t.Fatalf("after the update the partner is %q, want suspended", s)
	}

	refused := []struct {
		name string
		data map[string]string
		line string
	}{
		{"broken document", map[string]string{DataKey: informerDoc("api.bank.example", "dormant")}, `state "dormant" is not one of`},
		{"no data key", map[string]string{"other.yaml": informerDoc("api.bank.example", "approved")}, "it has no key definitions.yaml"},
		{"refused by accept", map[string]string{DataKey: informerDoc("refused.example", "approved")}, "the host may not change"},
	}
	for _, r := range refused {
		f.update(t, cm(ConfigMapName, r.data))
		l := f.next(t)
		if !strings.HasPrefix(l, "definitions: refused") || !strings.Contains(l, r.line) || !strings.HasSuffix(l, "the definitions in use stay") {
			t.Fatalf("%s: reported %q", r.name, l)
		}
		if f.holder.Current().Host != "api.bank.example" || f.partnerState(t) != "suspended" {
			t.Fatalf("%s: the definitions in use changed", r.name)
		}
	}

	if err := f.client.CoreV1().ConfigMaps(informerNS).Delete(context.Background(), ConfigMapName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	f.expect(t, "definitions: ConfigMap open-banking/fingate-definitions was deleted")
	if f.partnerState(t) != "suspended" {
		t.Fatal("a deleted ConfigMap changed the definitions in use")
	}
}

func TestInformerIgnoresOtherConfigMaps(t *testing.T) {
	f := startInformer(t, cm("other", map[string]string{DataKey: "not: [valid"}))
	f.create(t, cm(ConfigMapName, map[string]string{DataKey: informerDoc("api.bank.example", "approved")}))
	f.expect(t, "definitions: loaded ConfigMap open-banking/fingate-definitions")
	f.update(t, cm("other", map[string]string{DataKey: informerDoc("other.example", "revoked")}))
	f.update(t, cm(ConfigMapName, map[string]string{DataKey: informerDoc("api.bank.example", "revoked")}))
	f.expect(t, "definitions: loaded ConfigMap open-banking/fingate-definitions")
	if f.holder.Current().Host != "api.bank.example" || f.partnerState(t) != "revoked" {
		t.Fatal("another ConfigMap reached the definitions")
	}
}

func TestInformerIsNotLoadedByAnInvalidFirstDocument(t *testing.T) {
	f := startInformer(t, cm(ConfigMapName, map[string]string{DataKey: "profile: rw-4.0.0\n"}))
	f.expect(t, "definitions: refused")
	select {
	case <-f.inf.Loaded():
		t.Fatal("Loaded is closed although no valid document was seen")
	default:
	}
	if f.holder.Current() != nil {
		t.Fatal("an invalid document was set")
	}
}
