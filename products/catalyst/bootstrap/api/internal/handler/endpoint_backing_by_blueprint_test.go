package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestCreateInstance_BackingByBlueprint_ObjectsAsBeforeCapabilities — a
// backing element {blueprint, mode} takes the path it took before
// capabilities existed: for bp-newapi with bp-valkey the two Application
// objects are, field for field, the ones main wrote at 631202aa17 (the
// expected JSON below was read from that commit with this request). The
// instance id is random per create, so it is checked for its form and for
// being the same in the label and in spec.instanceId, then compared as
// INSTANCE.
func TestCreateInstance_BackingByBlueprint_ObjectsAsBeforeCapabilities(t *testing.T) {
	h, dyn := newTestHandlerWithEndpoint(t, refTestBlueprint("bp-newapi"), refTestBlueprint("bp-valkey"))
	newapi := fakeBlueprintInCatalog("bp-newapi", []map[string]interface{}{}, true, []string{"active-passive", "singleton"}).byKey["bp-newapi@1.0.0"]
	valkey := fakeBlueprintInCatalog("bp-valkey", []map[string]interface{}{}, true, []string{"active-passive", "singleton"}).byKey["bp-valkey@1.0.0"]
	h.SetCatalogClient(newFakeCatalog(newapi, valkey))
	r := newTestRouter(h)

	body := []byte(`{"blueprint":"bp-newapi","org":"acme","name":"chat","topology":"singleton",` +
		`"backing":[{"blueprint":"bp-valkey","mode":"create"}]}`)
	req := httptest.NewRequest("POST", "/catalyst/v1/apps/instances", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	instanceID := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for name, want := range map[string]string{"chat": wantConsumerAsOnMain, "chat-valkey": wantProviderAsOnMain} {
		got, err := dyn.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Application %s: %v", name, err)
		}
		id, _, _ := unstructured.NestedString(got.Object, "spec", "instanceId")
		if !instanceID.MatchString(id) || got.GetLabels()["catalyst.openova.io/instance"] != id {
			t.Fatalf("Application %s: instance id %q (label %q), want 8 hex characters in both", name, id, got.GetLabels()["catalyst.openova.io/instance"])
		}
		_ = unstructured.SetNestedField(got.Object, "INSTANCE", "spec", "instanceId")
		_ = unstructured.SetNestedField(got.Object, "INSTANCE", "metadata", "labels", "catalyst.openova.io/instance")
		raw, err := json.Marshal(got.Object)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Errorf("Application %s differs from main:\n got %s\nwant %s", name, raw, want)
		}
	}
}

const wantConsumerAsOnMain = `{"apiVersion":"apps.openova.io/v1","kind":"Application","metadata":{"labels":{"catalyst.openova.io/blueprint":"newapi","catalyst.openova.io/instance":"INSTANCE","catalyst.openova.io/managed-by":"catalyst-api","catalyst.openova.io/organization":"acme","catalyst.openova.io/topology":"singleton"},"name":"chat","namespace":"acme"},"spec":{"blueprintRef":{"name":"bp-newapi","version":"1.0.0"},"dependsOn":[{"context":"keyspace/chat","name":"chat-valkey","namespace":"acme"}],"environmentRef":"acme-prod","instanceId":"INSTANCE","isolationLevel":"namespace","namingTemplate":"{{.AppName}}-{{.InstanceID}}","organizationRef":"acme","parameters":{},"placement":{"mode":"singleton","regions":["rgn-a"]},"regions":["rgn-a"]}}`

const wantProviderAsOnMain = `{"apiVersion":"apps.openova.io/v1","kind":"Application","metadata":{"labels":{"catalyst.openova.io/blueprint":"valkey","catalyst.openova.io/instance":"INSTANCE","catalyst.openova.io/managed-by":"catalyst-api","catalyst.openova.io/organization":"acme","catalyst.openova.io/topology":"singleton"},"name":"chat-valkey","namespace":"acme"},"spec":{"blueprintRef":{"name":"bp-valkey","version":"1.0.0"},"environmentRef":"acme-prod","instanceId":"INSTANCE","isolationLevel":"namespace","namingTemplate":"{{.AppName}}-{{.InstanceID}}","organizationRef":"acme","parameters":{"keyspaces":[{"consumer":{"blueprint":"bp-newapi","mode":"shared"},"name":"chat","owner":"chat","reflect":{"namespaces":["acme"],"secretName":"chat-keyspace-credential"}}]},"placement":{"mode":"singleton","regions":["rgn-a"]},"regions":["rgn-a"]}}`
