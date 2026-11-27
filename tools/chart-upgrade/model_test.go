package chartupgrade

import (
	"fmt"
	"strings"
	"testing"
)

// The model on objects built here: each rule can fail. It needs no tool.

const (
	bare      = `{"name":%q,"image":"registrar"}`
	httpGets  = `{"name":%q,"image":"registrar","livenessProbe":{"httpGet":{"path":"/healthz","port":9811}},"readinessProbe":{"httpGet":{"path":"/healthz","port":9811}}}`
	execs     = `{"name":%q,"image":"registrar","livenessProbe":{"exec":{"command":["/registrar","--mode=probe"]}},"readinessProbe":{"exec":{"command":["/registrar","--mode=probe"]}}}`
	twoOfThem = `DaemonSet.apps "x" is invalid: [spec.template.spec.containers[0].livenessProbe.httpGet: Forbidden: may not specify more than 1 handler type, spec.template.spec.containers[0].readinessProbe.httpGet: Forbidden: may not specify more than 1 handler type]`
)

// daemonSet is a DaemonSet x with one container (a format above and its name)
// and the selector app=<selector>.
func daemonSet(t *testing.T, container, name, selector string) *Object {
	t.Helper()
	j := `{"apiVersion":"apps/v1","kind":"DaemonSet","metadata":{"name":"x"},"spec":{"selector":{"matchLabels":{"app":"` + selector +
		`"}},"template":{"metadata":{"labels":{"app":"` + selector + `"}},"spec":{"containers":[` + fmt.Sprintf(container, name) + `]}}}}`
	objs, err := Parse([]byte(j))
	if err != nil {
		t.Fatal(err)
	}
	o := objs["DaemonSet/x"]
	return &o
}

func TestUpgradeModel(t *testing.T) {
	for _, c := range []struct {
		name           string
		deployed, live *Object
		modified       *Object
		refusal        string
	}{
		{name: "the kept name keeps the handler of the failed upgrade beside the new one",
			deployed: daemonSet(t, bare, "old", "a"), live: daemonSet(t, httpGets, "old", "a"), modified: daemonSet(t, execs, "old", "a"),
			refusal: twoOfThem},
		{name: "a new name replaces the container whole",
			deployed: daemonSet(t, bare, "old", "a"), live: daemonSet(t, httpGets, "old", "a"), modified: daemonSet(t, execs, "new", "a")},
		{name: "a handler of the deployed release is taken away",
			deployed: daemonSet(t, httpGets, "old", "a"), live: daemonSet(t, httpGets, "old", "a"), modified: daemonSet(t, execs, "old", "a")},
		{name: "an object that is not live is created as rendered",
			modified: daemonSet(t, execs, "old", "a")},
		{name: "a live object the deployed release lacks is Helm's error",
			live: daemonSet(t, bare, "old", "a"), modified: daemonSet(t, execs, "old", "a"),
			refusal: `no DaemonSet with the name "x" found`},
		{name: "a changed selector is refused",
			deployed: daemonSet(t, bare, "old", "a"), live: daemonSet(t, bare, "old", "a"), modified: daemonSet(t, bare, "old", "b"),
			refusal: `DaemonSet.apps "x" is invalid: spec.selector: Invalid value: field is immutable`},
		{name: "a probe with two handlers in the new render is refused at its creation",
			modified: daemonSet(t, strings.Replace(httpGets, `"httpGet":{`, `"tcpSocket":{"port":1},"httpGet":{`, 1), "old", "a"),
			refusal:  `DaemonSet.apps "x" is invalid: spec.template.spec.containers[0].livenessProbe.tcpSocket: Forbidden: may not specify more than 1 handler type`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Upgrade(c.deployed, c.live, *c.modified)
			if err != nil {
				t.Fatal(err)
			}
			if got.Refusal != c.refusal {
				t.Fatalf("refusal %q, want %q", got.Refusal, c.refusal)
			}
			if c.refusal != "" {
				return
			}
			drift, err := Drift(got.Object, *c.modified)
			if err != nil {
				t.Fatal(err)
			}
			if len(drift) > 0 {
				t.Fatalf("the object after the upgrade is not the new render: %v", drift)
			}
		})
	}
}

func TestDriftNamesWhatIsLeft(t *testing.T) {
	got := daemonSet(t, httpGets, "old", "a")
	want := daemonSet(t, bare, "old", "a")
	drift, err := Drift(*got, *want)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 2 || !strings.HasPrefix(drift[0], "spec.template.spec.containers[0].livenessProbe: ") ||
		!strings.HasPrefix(drift[1], "spec.template.spec.containers[0].readinessProbe: ") {
		t.Fatalf("drift %q, want the two probes of containers[0]", drift)
	}
	drift, err = Drift(*daemonSet(t, bare, "old", "a"), *daemonSet(t, bare, "new", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 1 || drift[0] != `spec.template.spec.containers[0].name: "old", want "new"` {
		t.Fatalf("drift %q, want the name of containers[0]", drift)
	}
}

func TestParseKeepsTheManifestOfARelease(t *testing.T) {
	render := "# Source: c/templates/a.yaml\n---\napiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: a\n" +
		"---\n# Source: c/templates/hook.yaml\napiVersion: batch/v1\nkind: Job\nmetadata:\n  name: h\n  annotations:\n    helm.sh/hook: pre-upgrade\n" +
		"---\n# only a comment\n"
	objs, err := Parse([]byte(render))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 || objs["ServiceAccount/a"].Name != "a" {
		t.Fatalf("objects %v, want ServiceAccount/a alone (no hook, no empty document)", objs)
	}
	if _, err := Upgrade(nil, nil, Object{GVK: objs["ServiceAccount/a"].GVK.GroupVersion().WithKind("Unknown"), Name: "u", JSON: []byte(`{}`)}); err == nil {
		t.Fatal("a kind this test does not model was judged")
	}
}
