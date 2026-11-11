package observe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func TestClusterHandler(t *testing.T) {
	standby := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]interface{}{"name": "blog-db-5ff1066a", "namespace": "acme"},
		"spec":     map[string]interface{}{"replica": map[string]interface{}{"self": "blog-db-5ff1066a", "primary": "blog-db-6e1e3f9c", "source": "blog-db-6e1e3f9c"}},
		"status": map[string]interface{}{"phase": "Cluster in healthy state",
			"conditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}},
	}}
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{CNPGClusterGVR: "ClusterList"}, standby)
	h := ClusterHandler(&Observer{Dyn: dyn, Region: "hw-me-east-215-b-rtz-prod"})

	get := func(q string) (int, map[string]interface{}) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/clusters?"+q, nil))
		var m map[string]interface{}
		_ = json.Unmarshal(rr.Body.Bytes(), &m)
		return rr.Code, m
	}
	code, m := get("namespace=acme&name=blog-db-5ff1066a")
	if code != 200 || m["found"] != true || m["ready"] != true || m["primary"] != false || m["region"] != "hw-me-east-215-b-rtz-prod" {
		t.Fatalf("standby: code=%d body=%v", code, m)
	}
	code, m = get("namespace=acme&name=nope")
	if code != 200 || m["found"] != false {
		t.Fatalf("absent: code=%d body=%v", code, m)
	}
	if code, _ = get("namespace=acme"); code != 400 {
		t.Fatalf("missing name: code=%d", code)
	}
}
