package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
)

// TestMarkHandedOverRecordsThePhase (SOV-180): after the custody bundle is
// stored, the cutover status ConfigMap holds phase HandedOver, also when the
// ConfigMap did not exist yet.
func TestMarkHandedOverRecordsThePhase(t *testing.T) {
	for _, existing := range []bool{false, true} {
		cs := fake.NewSimpleClientset()
		if existing {
			cs = fake.NewSimpleClientset(&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Namespace: "catalyst", Name: cutoverStatusConfigMapName()},
				Data:       map[string]string{"cutoverComplete": "false"},
			})
		}
		deps := &cutoverDeps{core: cs, ns: "catalyst"}
		if err := markHandedOver(context.Background(), deps); err != nil {
			t.Fatalf("existing=%v: markHandedOver: %v", existing, err)
		}
		status, err := readCutoverStatus(context.Background(), deps)
		if err != nil {
			t.Fatalf("existing=%v: read status: %v", existing, err)
		}
		if status["phase"] != "HandedOver" {
			t.Errorf("existing=%v: phase=%q, want HandedOver", existing, status["phase"])
		}
		if existing && status["cutoverComplete"] != "false" {
			t.Errorf("existing=%v: another key changed: cutoverComplete=%q", existing, status["cutoverComplete"])
		}
	}
}

// TestMarkHandedOverKeepsALaterPhase (SOV-180): a phase that is already
// recorded is never changed back.
func TestMarkHandedOverKeepsALaterPhase(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "catalyst", Name: cutoverStatusConfigMapName()},
		Data:       map[string]string{"phase": "Independent"},
	})
	deps := &cutoverDeps{core: cs, ns: "catalyst"}
	if err := markHandedOver(context.Background(), deps); err != nil {
		t.Fatalf("markHandedOver: %v", err)
	}
	status, err := readCutoverStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status["phase"] != "Independent" {
		t.Errorf("phase=%q, want Independent kept", status["phase"])
	}
}

// TestPinIssuerIsTheSovereignsOwnConsole (SOV-180): without
// CATALYST_PIN_ISSUER a Sovereign stamps its own console origin, and the
// mothership (no SOVEREIGN_FQDN) keeps the mothership origin.
func TestPinIssuerIsTheSovereignsOwnConsole(t *testing.T) {
	t.Setenv("CATALYST_PIN_ISSUER", "")
	if err := os.Unsetenv("CATALYST_PIN_ISSUER"); err != nil {
		t.Fatalf("unset CATALYST_PIN_ISSUER: %v", err)
	}
	t.Setenv("SOVEREIGN_FQDN", "alpha.example.org")
	if got := pinIssuer(); got != "https://console.alpha.example.org" {
		t.Errorf("Sovereign: pinIssuer()=%q, want https://console.alpha.example.org", got)
	}
	t.Setenv("SOVEREIGN_FQDN", "")
	if got := pinIssuer(); got != "https://console.openova.io" {
		t.Errorf("mothership: pinIssuer()=%q, want the mothership origin", got)
	}
	t.Setenv("CATALYST_PIN_ISSUER", "https://console.beta.example.org")
	t.Setenv("SOVEREIGN_FQDN", "alpha.example.org")
	if got := pinIssuer(); got != "https://console.beta.example.org" {
		t.Errorf("override: pinIssuer()=%q, want the CATALYST_PIN_ISSUER value", got)
	}
}

// TestReceiveTofuArchiveRecordsHandedOver (SOV-180): the hand-over import
// writes phase HandedOver once the archive is sealed.
func TestReceiveTofuArchiveRecordsHandedOver(t *testing.T) {
	t.Setenv("CATALYST_FIRE_CUTOVER_ON_HANDOVER", "false")
	h := newTestHandler(t)
	bao := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer bao.Close()
	c := openbao.New(bao.URL, "test-token")
	c.HTTP = bao.Client()
	h.SetOpenBao(c)
	deps := &cutoverDeps{core: fake.NewSimpleClientset(), ns: "catalyst"}
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) { return deps, nil })

	body, _ := json.Marshal(tofuArchiveRequest{
		DeploymentID:  "dep-180",
		SovereignFQDN: "alpha.example.org",
		Files:         map[string]string{"terraform.tfstate": base64.StdEncoding.EncodeToString([]byte(`{"v":1}`))},
	})
	rec := httptest.NewRecorder()
	h.ReceiveTofuArchive(rec, httptest.NewRequest(http.MethodPost, "/api/v1/handover/tofu-archive", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("seal status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	status, err := readCutoverStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status["phase"] != "HandedOver" {
		t.Errorf("phase=%q after the archive was sealed, want HandedOver", status["phase"])
	}
}
