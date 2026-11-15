package operations

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

// The store reads one object over HTTPS with the key pair and the certificate of the
// mounted binding, signs the request, and reads nothing without the binding.
func TestBucketStoreReadsWithTheMountedBinding(t *testing.T) {
	var authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/machine-evidence/catalyst-infra/cp-1/r1/hardening.results.json" {
			http.Error(w, "<Error><Code>NoSuchKey</Code></Error>", http.StatusNotFound)
			return
		}
		w.Header().Set("Last-Modified", "Tue, 01 Dec 2026 01:00:00 GMT")
		w.Header().Set("ETag", `"0"`)
		_, _ = w.Write([]byte(`{"rules":[]}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	for name, content := range map[string][]byte{runner.BindingAccessKeyID: []byte("AK1\n"), runner.BindingSecretAccessKey: []byte("secret-1\n"), runner.BindingCA: ca} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := BucketStore{Endpoint: server.URL, Region: "region-1", Bucket: "machine-evidence", BindingDir: dir}
	data, err := store.Get(context.Background(), "catalyst-infra/cp-1/r1/hardening.results.json")
	if err != nil || string(data) != `{"rules":[]}` {
		t.Fatalf("Get = %q, %v", data, err)
	}
	if !strings.Contains(authorization, "Credential=AK1/") || !strings.Contains(authorization, "/region-1/s3/aws4_request") {
		t.Errorf("the request was signed with %q", authorization)
	}
	if err := os.Remove(filepath.Join(dir, runner.BindingCA)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "catalyst-infra/cp-1/r1/hardening.results.json"); err == nil {
		t.Error("the store trusted a certificate that the binding does not hold")
	}
	if err := os.Remove(filepath.Join(dir, runner.BindingSecretAccessKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), "k"); err == nil || !strings.Contains(err.Error(), runner.BindingSecretAccessKey) {
		t.Errorf("without the secret key file: %v", err)
	}
	plain := BucketStore{Endpoint: "http://" + server.Listener.Addr().String(), Region: "region-1", Bucket: "machine-evidence", BindingDir: dir}
	if _, err := plain.Get(context.Background(), "k"); err == nil {
		t.Error("the store accepted an endpoint that is not https")
	}
}
