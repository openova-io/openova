// sovereign_secondary_kubeconfig_test.go — #3991 cross-region datapath fix.
//
// Proves the chroot rewrites a secondary region's kubeconfig `server:`
// host from the VPC-external EIP to the VPC-peered private node IP it can
// route to, while the mothership leaves the EIP untouched. Root-caused
// live on hw173: the in-cluster catalyst-api (region-a) times out on
// region-b's DNAT'd EIP (212.72.24.35:6443) but reaches region-b's
// private cp1 IP (10.179.1.131:6443) over the existing cross-VPC peering.

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secondaryKubeconfigFixture is a minimal but valid kubeconfig whose
// server points at the VPC-external EIP, mirroring what the IaC stamps.
const secondaryKubeconfigFixture = `apiVersion: v1
kind: Config
clusters:
- name: default
  cluster:
    certificate-authority-data: ` + fakeCAData + `
    server: https://212.72.24.35:6443
contexts:
- name: default
  context:
    cluster: default
    user: default
current-context: default
users:
- name: default
  user:
    token: fake-token
`

// fakeCAData is a syntactically valid base64 CA blob so clientcmd parses
// the kubeconfig without reaching the network.
const fakeCAData = "LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSUJoRENDQVN1Z0F3SUJBZ0lRREMtLS0tLUVORCBDRVJUSUZJQ0FURS0tLS0tCg=="

func TestRewriteKubeconfigServerHost(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		newHost    string
		wantHost   string
		wantChange int
	}{
		{
			name:       "eip swapped for private ip, port + scheme preserved",
			raw:        "    server: https://212.72.24.35:6443\n",
			newHost:    "10.179.1.131",
			wantHost:   "    server: https://10.179.1.131:6443\n",
			wantChange: 1,
		},
		{
			name:       "empty newHost is a no-op",
			raw:        "    server: https://212.72.24.35:6443\n",
			newHost:    "",
			wantHost:   "    server: https://212.72.24.35:6443\n",
			wantChange: 0,
		},
		{
			name:       "no server line is a no-op",
			raw:        "apiVersion: v1\nkind: Config\n",
			newHost:    "10.179.1.131",
			wantHost:   "apiVersion: v1\nkind: Config\n",
			wantChange: 0,
		},
		{
			name:       "indentation preserved",
			raw:        "        server: https://1.2.3.4:6443",
			newHost:    "10.0.0.9",
			wantHost:   "        server: https://10.0.0.9:6443",
			wantChange: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, n := rewriteKubeconfigServerHost(tc.raw, tc.newHost)
			if got != tc.wantHost {
				t.Errorf("rewrite host = %q, want %q", got, tc.wantHost)
			}
			if n != tc.wantChange {
				t.Errorf("changed count = %d, want %d", n, tc.wantChange)
			}
		})
	}
}

func TestIsChroot(t *testing.T) {
	t.Run("set => chroot", func(t *testing.T) {
		t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
		if !isChroot() {
			t.Fatal("isChroot()=false with SOVEREIGN_FQDN set")
		}
	})
	t.Run("unset => mothership", func(t *testing.T) {
		t.Setenv("SOVEREIGN_FQDN", "")
		if isChroot() {
			t.Fatal("isChroot()=true with SOVEREIGN_FQDN unset")
		}
	})
}

// postSecondaryKubeconfig drives the handler and returns the recorder.
func postSecondaryKubeconfig(t *testing.T, h *Handler, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/secondary-kubeconfig", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	h.HandleSovereignSecondaryKubeconfig(rec, req)
	return rec
}

// TestSecondaryKubeconfig_ChrootRewritesServerToPrivateIP is the #3991
// acceptance unit: on a chroot, a POST carrying nodeInternalIp persists a
// kubeconfig whose server host is the PRIVATE IP (region-a-routable), not
// the EIP.
func TestSecondaryKubeconfig_ChrootRewritesServerToPrivateIP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works") // chroot

	cache := newK8sCacheWithClusters(t, nil)
	h := &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}

	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "dep3991",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": secondaryKubeconfigFixture,
		"nodeInternalIp": "10.179.1.131",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, "dep3991-me-east-215-b-1.yaml"))
	if err != nil {
		t.Fatalf("read persisted kubeconfig: %v", err)
	}
	got := string(onDisk)
	if !strings.Contains(got, "server: https://10.179.1.131:6443") {
		t.Errorf("persisted kubeconfig did not get private-IP server; got:\n%s", got)
	}
	if strings.Contains(got, "212.72.24.35") {
		t.Errorf("persisted kubeconfig still carries the EIP; got:\n%s", got)
	}
}

// TestSecondaryKubeconfig_MothershipKeepsEIP proves the no-regression
// guarantee: with SOVEREIGN_FQDN unset (mothership) the server host is
// left as the EIP — the external mothership needs it.
func TestSecondaryKubeconfig_MothershipKeepsEIP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "") // mothership

	cache := newK8sCacheWithClusters(t, nil)
	h := &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}

	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "dep3991",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": secondaryKubeconfigFixture,
		"nodeInternalIp": "10.179.1.131",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	onDisk, err := os.ReadFile(filepath.Join(dir, "dep3991-me-east-215-b-1.yaml"))
	if err != nil {
		t.Fatalf("read persisted kubeconfig: %v", err)
	}
	got := string(onDisk)
	if !strings.Contains(got, "server: https://212.72.24.35:6443") {
		t.Errorf("mothership rewrote the EIP; expected it preserved. got:\n%s", got)
	}

	// The mothership stashes the private IP as a sidecar for handover replay.
	sidecar, err := os.ReadFile(filepath.Join(dir, "dep3991-me-east-215-b-1.nodeip"))
	if err != nil {
		t.Fatalf("expected node-ip sidecar on mothership: %v", err)
	}
	if strings.TrimSpace(string(sidecar)) != "10.179.1.131" {
		t.Errorf("sidecar = %q, want 10.179.1.131", string(sidecar))
	}
}

// TestSecondaryKubeconfig_InvalidNodeIPRejected guards the IP validation.
func TestSecondaryKubeconfig_InvalidNodeIPRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	cache := newK8sCacheWithClusters(t, nil)
	h := &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}
	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "dep3991",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": secondaryKubeconfigFixture,
		"nodeInternalIp": "not-an-ip; rm -rf /",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (want 400), body = %s", rec.Code, rec.Body.String())
	}
}

// TestSecondaryKubeconfig_NoNodeIPLeavesServerUnchanged proves the
// backward-compat path: a pre-#3991 IaC that omits nodeInternalIp yields
// the legacy behaviour (EIP server, no rewrite) even on a chroot.
func TestSecondaryKubeconfig_NoNodeIPLeavesServerUnchanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")

	cache := newK8sCacheWithClusters(t, nil)
	h := &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: cache,
	}
	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "dep3991",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": secondaryKubeconfigFixture,
		// no nodeInternalIp
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	onDisk, _ := os.ReadFile(filepath.Join(dir, "dep3991-me-east-215-b-1.yaml"))
	if !strings.Contains(string(onDisk), "server: https://212.72.24.35:6443") {
		t.Errorf("expected unchanged EIP server without nodeInternalIp; got:\n%s", string(onDisk))
	}
}
