package orglistener

import (
	"encoding/json"
	"testing"
)

const wantAcme = `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
	`{"key":"kubernetes.io/metadata.name","operator":"In","values":["acme","catalyst-system"]}]}}}`

// The admission is a selector on the namespace name label with exactly the
// two namespaces, in the order given; never every namespace.
func TestAllowedRoutes_SelectsTheTwoNamespacesByName(t *testing.T) {
	for _, tc := range []struct{ org, console, want string }{
		{"acme", "catalyst-system", wantAcme},
		{"finance", "console-routes", `{"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
			`{"key":"kubernetes.io/metadata.name","operator":"In","values":["finance","console-routes"]}]}}}`},
	} {
		b, err := json.Marshal(AllowedRoutes(tc.org, tc.console))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != tc.want {
			t.Errorf("AllowedRoutes(%q, %q):\n got %s\nwant %s", tc.org, tc.console, b, tc.want)
		}
	}
}

// Each call returns a value of its own: a caller that edits one listener's map
// cannot change the admission of another listener.
func TestAllowedRoutes_ReturnsAFreshValue(t *testing.T) {
	first := AllowedRoutes("acme", "catalyst-system")
	first["namespaces"].(map[string]any)["from"] = "All"
	b, err := json.Marshal(AllowedRoutes("acme", "catalyst-system"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != wantAcme {
		t.Errorf("a second call is changed by an edit of the first:\n got %s\nwant %s", b, wantAcme)
	}
}
