package lifecycle

import (
	"net/http"
	"testing"

	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestNetworkRegistryRouteTellsEachRetryCall: the route tells Accepted once
// for each request whose reconcile ran, and never for a refusal (gate check
// FND-G02 records each accepted call as a retry call of the deployment).
func TestNetworkRegistryRouteTellsEachRetryCall(t *testing.T) {
	doc := registry(t)
	cache := &regions{clients: map[string]*dynamicfake.FakeDynamicClient{routeDep: applyingClient()}}
	n := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: "ready", OwnerEmail: routeOwner}}, cache)
	var told []string
	n.Accepted = func(_ *http.Request, id string) { told = append(told, id) }

	if code, _ := post(t, n, routeDep, "someone@example.org"); code != http.StatusNotFound {
		t.Fatalf("another user's session: %d", code)
	}
	if code, _ := post(t, n, "eeee000000000006", routeOwner); code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", code)
	}
	if len(told) != 0 {
		t.Fatalf("a refused request was told as a retry call: %v", told)
	}
	for i := 1; i <= 2; i++ {
		if code, body := post(t, n, routeDep, routeOwner); code != http.StatusOK {
			t.Fatalf("call %d: %d %v", i, code, body)
		}
		if len(told) != i || told[i-1] != routeDep {
			t.Fatalf("after %d accepted calls Accepted was told %v", i, told)
		}
	}

	notReady := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: "failed", OwnerEmail: routeOwner}}, cache)
	notReady.Accepted = n.Accepted
	if code, _ := post(t, notReady, routeDep, routeOwner); code != http.StatusConflict || len(told) != 2 {
		t.Fatalf("a deployment that is not ready: %d, told %v", code, told)
	}
	noState := testRegistry(t, doc, memRecords{routeDep: {ID: routeDep, Status: "ready", OwnerEmail: routeOwner}}, cache)
	noState.StateDir = func() string { return t.TempDir() }
	noState.Accepted = n.Accepted
	if code, _ := post(t, noState, routeDep, routeOwner); code != http.StatusUnprocessableEntity || len(told) != 2 {
		t.Fatalf("no OpenTofu state: %d, told %v", code, told)
	}
}
