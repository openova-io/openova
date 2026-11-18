// NET-011: POST /api/v1/deployments is refused with HTTP 422 while
// CATALYST_EGRESS_CIDRS is empty, and when the request field adminCIDRs
// opens the API port to every address. With the egress list set, the same
// request is created.
package handler

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/pdm"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// postControlPlaneSourcesRequest posts a BYO single-region request, with
// adminCIDRs when admin is not nil, and returns the recorder.
func postControlPlaneSourcesRequest(t *testing.T, egress string, admin []string) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("DYNADOT_MANAGED_DOMAINS", "omani.works")
	t.Setenv("CATALYST_HARBOR_ROBOT_TOKEN", "harbor_TEST_PLACEHOLDER")
	t.Setenv(provisioner.EnvEgressCIDRs, egress)
	t.Setenv(provisioner.EnvDefaultAdminCIDRs, "")
	pdm.ResetManagedDomains()
	h := NewWithPDM(slog.Default(), &fakePDM{})
	fields := map[string]any{
		"sovereignFQDN":          "k8s.acme.io",
		"sovereignDomainMode":    "byo",
		"sovereignPoolDomain":    "acme.io",
		"sovereignSubdomain":     "k8s",
		"provider":               "hetzner",
		"hetznerToken":           "tok",
		"hetznerProjectID":       "proj",
		"region":                 "fsn1",
		"orgName":                "Acme",
		"bcpTopology":            "single-region",
		"orgEmail":               "ops@acme.io",
		"sshPublicKey":           "ssh-ed25519 AAAA test",
		"objectStorageRegion":    "fsn1",
		"objectStorageAccessKey": "TESTACCESSKEY1234567",
		"objectStorageSecretKey": "TESTSECRETKEY1234567890123456789012345678",
	}
	if admin != nil {
		fields["adminCIDRs"] = admin
	}
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	h.CreateDeployment(w, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body)))
	return w
}

// cpsErrorOf returns the field "error" of a JSON answer.
func cpsErrorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("answer is not a JSON object: %v: %s", err, w.Body.String())
	}
	text, _ := got["error"].(string)
	return text
}

func TestCreateDeployment_RefusesWithoutEgressCIDRs(t *testing.T) {
	w := postControlPlaneSourcesRequest(t, "", nil)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s; want 422", w.Code, w.Body.String())
	}
	if got := cpsErrorOf(t, w); got != provisioner.ErrEgressCIDRsUnset.Error() {
		t.Fatalf("error=%q; want %q", got, provisioner.ErrEgressCIDRsUnset.Error())
	}
}

func TestCreateDeployment_RefusesAnySourceAdminCIDRs(t *testing.T) {
	w := postControlPlaneSourcesRequest(t, "192.0.2.10/32", []string{"0.0.0.0/0"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s; want 422", w.Code, w.Body.String())
	}
	if got := cpsErrorOf(t, w); !strings.Contains(got, "adminCIDRs entry \"0.0.0.0/0\"") {
		t.Fatalf("error=%q; want the refusal of adminCIDRs entry \"0.0.0.0/0\"", got)
	}
}

func TestCreateDeployment_AcceptsWithEgressCIDRs(t *testing.T) {
	w := postControlPlaneSourcesRequest(t, "192.0.2.10/32", []string{"198.51.100.0/28"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s; want 201", w.Code, w.Body.String())
	}
}
