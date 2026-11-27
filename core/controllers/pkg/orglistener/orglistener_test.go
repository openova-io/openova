package orglistener

import (
	"encoding/json"
	"strings"
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

// wantAcmeAPI is the passthrough listener of host api.acme.omani.homes, written
// out in full: the shape PRD-050 ran (answer 8), with the admission of the pair
// and TLSRoutes only.
const wantAcmeAPI = `{"allowedRoutes":{"kinds":[{"kind":"TLSRoute"}],"namespaces":{"from":"Selector","selector":{"matchExpressions":[` +
	`{"key":"kubernetes.io/metadata.name","operator":"In","values":["acme","catalyst-system"]}]}}},` +
	`"hostname":"api.acme.omani.homes","name":"console-tls-acme-api","port":8443,"protocol":"TLS","tls":{"mode":"Passthrough"}}`

// One listener per host label: the exact host, TLS passthrough, on the port given.
func TestPassthroughListeners_NET195_ShapeOfTheSpike(t *testing.T) {
	got, err := json.Marshal(PassthroughListeners("acme", "acme.omani.homes", []string{"api"}, 8443, "catalyst-system"))
	if err != nil || string(got) != "["+wantAcmeAPI+"]" {
		t.Errorf("PassthroughListeners (err %v):\n got %s\nwant [%s]", err, got, wantAcmeAPI)
	}
}

// Only a label of letters and digits gets a listener, never the console's host,
// once per label, in the order of the labels.
func TestPassthroughListeners_NET195_OnlyValidLabelsOnce(t *testing.T) {
	ls := PassthroughListeners("acme", "acme.omani.homes",
		[]string{"mtls", "console", "a-b", "API", "", "x.y", "api", "mtls"}, 8443, "catalyst-system")
	var names []string
	for _, l := range ls {
		names = append(names, l["name"].(string))
	}
	if strings.Join(names, ",") != "console-tls-acme-api,console-tls-acme-mtls" {
		t.Errorf("listener names: got %v, want [console-tls-acme-api console-tls-acme-mtls]", names)
	}
}

// A name belongs to exactly one Organization, also when slugs have hyphens.
func TestIsPassthroughListenerOf_NET195_OneOwnerPerName(t *testing.T) {
	for _, tc := range []struct {
		name, slug string
		want       bool
	}{
		{"console-tls-acme-api", "acme", true},
		{"console-tls-acme-x-api", "acme", false},
		{"console-tls-acme-x-api", "acme-x", true},
		{"console-tls-acme-", "acme", false},
		{"console-https-acme", "acme", false},
		{"console-tls-acmeapi", "acme", false},
	} {
		if got := IsPassthroughListenerOf(tc.name, tc.slug); got != tc.want {
			t.Errorf("IsPassthroughListenerOf(%q, %q) = %v, want %v", tc.name, tc.slug, got, tc.want)
		}
	}
}

// The merge appends the missing listener, repairs a changed one, removes the
// one no longer listed, keeps every other listener, and a second pass is a
// no-op, also when the API server has defaulted a field.
func TestMergePassthrough_NET195_ConvergesOnlyTheOrganizationsListeners(t *testing.T) {
	want := PassthroughListeners("acme", "acme.omani.homes", []string{"api", "mtls"}, 8443, "catalyst-system")
	apex := map[string]any{"name": "console-https", "port": int64(8443), "protocol": "HTTPS"}
	other := map[string]any{"name": "console-tls-acme-x-api", "hostname": "api.acme-x.omani.homes", "protocol": "TLS"}
	stale := map[string]any{"name": "console-tls-acme-old", "hostname": "old.acme.omani.homes", "protocol": "TLS"}
	drift := map[string]any{"name": "console-tls-acme-mtls", "hostname": "mtls.acme.omani.homes", "port": int64(31443), "protocol": "TLS"}

	out, changed := MergePassthrough([]any{apex, other, stale, drift}, "acme", want)
	if !changed {
		t.Fatal("merge must report a change")
	}
	var names []string
	for _, l := range out {
		names = append(names, l.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "console-https,console-tls-acme-x-api,console-tls-acme-mtls,console-tls-acme-api" {
		t.Fatalf("listeners after merge: got %v", names)
	}
	b, _ := json.Marshal(out[3])
	if string(b) != wantAcmeAPI {
		t.Errorf("appended listener:\n got %s\nwant %s", b, wantAcmeAPI)
	}
	if out[2].(map[string]any)["port"] != int64(8443) {
		t.Errorf("drifted port not repaired: %v", out[2])
	}

	// The API server defaults the group of a route kind; that is no drift.
	out[3].(map[string]any)["allowedRoutes"].(map[string]any)["kinds"] =
		[]any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "TLSRoute"}}
	fresh := PassthroughListeners("acme", "acme.omani.homes", []string{"api", "mtls"}, 8443, "catalyst-system")
	if _, again := MergePassthrough(out, "acme", fresh); again {
		t.Error("a second pass must change nothing")
	}

	gone, changed := MergePassthrough(out, "acme", nil)
	if !changed || len(gone) != 2 {
		t.Errorf("no labels must remove only acme's two passthrough listeners: changed=%v, %d left", changed, len(gone))
	}
}
