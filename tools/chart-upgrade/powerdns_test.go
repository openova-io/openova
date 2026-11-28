package chartupgrade

// The case of bp-powerdns (run by platform/powerdns/chart/tests/
// upgrade-over-deployed.sh), NET-187. Two objects a Sovereign holds change:
// the ConfigMap dnsdist-config, whose one backend moves from port 5353 of the
// powerdns Service (a port that Service does not declare, so dnsdist never
// reached PowerDNS) to its DNS port 53; and the DaemonSet dnsdist, whose Pod
// template carries the checksum of that configuration, so its Pods roll and
// read the new one. The chart under test applies over a fresh install and over
// the release the Sovereign ran on 27 Nov 2026 (1.2.33, gate records
// hw307-20261127T143743Z-596b and hw307-20261127T174728Z-1f8f).
//
// Each chart is rendered with the block dnsdist of kit slot 11 at the same
// commit, with POWERDNS_DNSDIST_HOSTPORT=true as the reference cloud's
// substitutes set it (the DaemonSet with host port 53 on every node).
//
// The known answer: the release 1.2.33 sends dnsdist's queries to port 5353.
//
// What it cannot see: the Secret powerdns-api-credentials is carried over on
// an upgrade by a lookup of the live Secret, which helm template cannot do, so
// both renders get the same key by value (pdnsValues), and the case proves
// that the upgrade then sends no Secret a new value. Nor does it see the
// defaults the API server adds to the live objects, or dnsdist reaching the
// server (the gate check NET-G19 on the Sovereign shows that).

// The case of bp-powerdns, run by platform/powerdns/chart/tests/
// api-credentials-kept.sh (SEC-239). Before Helm deletes an object that left
// the manifest it reads helm.sh/resource-policy on the LIVE object (Helm
// v3.18.4 pkg/kube/client.go 436-454), so the upgrade one release before
// SEC-235 must put keep on the live Secret powerdns-api-credentials. Deployed:
// 1.2.33, the release the Sovereign ran on 27 Nov 2026, and 1.2.34, the
// release of NET-187 and the newest of main (pdnsKeptFrom); every release
// since 1.2.10 renders the same Secret (its template is unchanged since
// ef26f52255). helm template has no cluster: the values set here
// (pdnsLiveValues) stand in for those its lookup reads back from the live
// Secret. Known answer: from each of them live, a release without the Secret
// deletes it (gap G4 of the spike SEC-227). Not seen: the Helm that Flux's
// helm-controller embeds, and the reflector's copies of the Secret.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	pdnsChart     = "platform/powerdns/chart"
	pdnsSlot      = "clusters/_template/bootstrap-kit/11-powerdns.yaml"
	pdnsRelease   = "powerdns"
	pdnsConfig    = "ConfigMap/dnsdist-config"
	pdnsDaemonSet = "DaemonSet/dnsdist"
	pdnsHostPort  = "${POWERDNS_DNSDIST_HOSTPORT:=false}"
	// The one backend line of dnsdist.conf before and after the change.
	pdnsBackendOld = `newServer({ address = backend .. ":5353", name = 'powerdns-auth' })`
	pdnsBackendNew = `newServer({ address = backend .. ":53", name = 'powerdns-auth' })`
)

// The values both renders share: a fixed key in place of the lookup of the
// live Secret (see above).
var pdnsValues = []string{"--set", "powerdns.apiKey=k-net187", "--set", "powerdns.webserverPassword=w-net187"}

// The release the Sovereign ran on 27 Nov 2026.
var pdnsDeployed = []string{"1.2.33"}

func TestPowerDNSAppliesOverEveryState(t *testing.T) {
	root := needTools(t)
	renders := map[string]map[string]Object{}
	for _, v := range pdnsDeployed {
		c := releaseCommit(t, root, pdnsChart, v)
		t.Logf("%s: the chart and slot 11 at commit %s", v, c)
		values := pdnsSlotValues(t, []byte(git(t, root, "show", c+":"+pdnsSlot)))
		renders[v] = render(t, withDependencies(t, extract(t, root, pdnsChart, c)), pdnsRelease, append(append([]string{}, pdnsValues...), "-f", values))
	}
	chart := chartUnderTest(root, pdnsChart)
	t.Logf("the chart under test: %s", chart)
	copied := filepath.Join(t.TempDir(), "chart")
	if err := os.CopyFS(copied, os.DirFS(chart)); err != nil {
		t.Fatal(err)
	}
	slot, err := os.ReadFile(filepath.Join(root, pdnsSlot)) // #nosec G304 -- a slot file of the repository
	if err != nil {
		t.Fatal(err)
	}
	next := render(t, withDependencies(t, copied), pdnsRelease, append(append([]string{}, pdnsValues...), "-f", pdnsSlotValues(t, slot)))

	t.Run("known answer: 1.2.33 sends dnsdist's queries to port 5353", func(t *testing.T) {
		if got := backendLine(t, renders["1.2.33"][pdnsConfig]); got != pdnsBackendOld {
			t.Errorf("%s of 1.2.33: backend %q, want %q", pdnsConfig, got, pdnsBackendOld)
		}
	})
	t.Run("fresh install", func(t *testing.T) { pdnsCheck(t, nil, nil, next) })
	for _, v := range pdnsDeployed {
		t.Run(v+" deployed and live", func(t *testing.T) { pdnsCheck(t, renders[v], renders[v], next) })
	}
}

// pdnsCheck: every object the model judges applies and becomes the new render;
// dnsdist's backend is port 53; when a release is live the DaemonSet's Pods
// roll, and no Secret gets a new value.
func pdnsCheck(t *testing.T, deployed, live, next map[string]Object) {
	t.Helper()
	outcomes := map[string]Outcome{}
	var unjudged []string
	for k, m := range next {
		if !kinds.Recognizes(m.GVK) {
			unjudged = append(unjudged, k)
			continue
		}
		o, err := Upgrade(objPtr(deployed[k]), objPtr(live[k]), m)
		if err != nil {
			t.Fatal(err)
		}
		outcomes[k] = o
	}
	sort.Strings(unjudged)
	t.Logf("not judged (kinds the model does not hold): %v", unjudged)
	converges(t, outcomes, next)
	for _, k := range []string{pdnsConfig, pdnsDaemonSet} {
		if _, ok := outcomes[k]; !ok {
			t.Fatalf("the new render has no %s", k)
		}
	}
	if got := backendLine(t, outcomes[pdnsConfig].Object); got != pdnsBackendNew {
		t.Errorf("%s after the upgrade: backend %q, want %q", pdnsConfig, got, pdnsBackendNew)
	}
	if live == nil {
		return
	}
	before, after := podChecksum(t, live[pdnsDaemonSet]), podChecksum(t, outcomes[pdnsDaemonSet].Object)
	if before == after {
		t.Errorf("%s: checksum/config stays %q: the running dnsdist keeps the configuration it read at start", pdnsDaemonSet, before)
	} else {
		t.Logf("%s: checksum/config %s -> %s: the Pods are replaced node by node (RollingUpdate)", pdnsDaemonSet, before, after)
	}
	secrets := 0
	for k, l := range live {
		if l.GVK.Kind != "Secret" {
			continue
		}
		secrets++
		o, ok := outcomes[k]
		if !ok {
			t.Errorf("%s: not in the new release, Helm deletes it", k)
			continue
		}
		if a, b := secretData(t, l), secretData(t, o.Object); !reflect.DeepEqual(a, b) {
			t.Errorf("%s: the upgrade changes the value of the Secret", k)
		}
	}
	if secrets == 0 {
		t.Errorf("the live release holds no Secret: the value check judged nothing")
	}
}

// pdnsSlotValues writes the block dnsdist of the HelmRelease bp-powerdns of a
// slot file into a values file, with the host-port switch substituted as the
// reference cloud sets it.
func pdnsSlotValues(t *testing.T, slot []byte) string {
	t.Helper()
	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(slot)))
	var found map[string]any
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var hr struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Values map[string]any `json:"values"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal(bytes.ReplaceAll(doc, []byte(pdnsHostPort), []byte("true")), &hr); err != nil {
			t.Fatal(err)
		}
		if hr.Kind == "HelmRelease" && hr.Metadata.Name == "bp-powerdns" {
			found = hr.Spec.Values
		}
	}
	if found == nil {
		t.Fatalf("%s: no HelmRelease bp-powerdns", pdnsSlot)
	}
	block, ok := found["dnsdist"]
	if !ok {
		t.Fatalf("%s: the values of bp-powerdns have no block dnsdist", pdnsSlot)
	}
	b, err := yaml.Marshal(map[string]any{"dnsdist": block})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("hostPortMode: true")) || bytes.Contains(b, []byte("${")) {
		t.Fatalf("%s: the block dnsdist is not the host-port DaemonSet with every substitution made:\n%s", pdnsSlot, b)
	}
	f := filepath.Join(t.TempDir(), "slot-11.yaml")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// backendLine is the one newServer line of dnsdist.conf that is not a comment.
func backendLine(t *testing.T, o Object) string {
	t.Helper()
	var c corev1.ConfigMap
	if err := json.Unmarshal(o.JSON, &c); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(c.Data["dnsdist.conf"], "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "newServer(") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("%s: dnsdist.conf holds %d newServer lines, want 1: %q", o.Key(), len(lines), lines)
	}
	return lines[0]
}

func podChecksum(t *testing.T, o Object) string {
	t.Helper()
	var d appsv1.DaemonSet
	if err := json.Unmarshal(o.JSON, &d); err != nil {
		t.Fatal(err)
	}
	sum := d.Spec.Template.Annotations["checksum/config"]
	if sum == "" {
		t.Fatalf("%s: the Pod template has no checksum/config", o.Key())
	}
	return sum
}

func secretData(t *testing.T, o Object) map[string]string {
	t.Helper()
	var s corev1.Secret
	if err := json.Unmarshal(o.JSON, &s); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for k, v := range s.Data {
		out[k] = string(v)
	}
	for k, v := range s.StringData {
		out[k] = v
	}
	return out
}

// The case of SEC-239 (see the head of this file).
const (
	pdnsSecret                 = "Secret/powerdns-api-credentials"
	pdnsKeyValue, pdnsWebValue = "live-api-key", "live-webserver-password"
)

// The values that stand in for those the lookup reads back from the live Secret.
var pdnsLiveValues = []string{"--set", "powerdns.apiKey=" + pdnsKeyValue, "--set", "powerdns.webserverPassword=" + pdnsWebValue}

// pdnsWithout drops the Secret from the manifest, as the release of SEC-235 will.
var pdnsWithout = []string{"--set", "powerdns.useExistingApiSecret=true"}

// The releases a Sovereign may run when this one lands: 1.2.33 (27 Nov 2026)
// and 1.2.34 (NET-187, the newest of main).
var pdnsKeptFrom = []string{"1.2.33", "1.2.34"}

func TestPowerDNSCredentialsKeptOverDeployed(t *testing.T) {
	root := needTools(t)
	chart := filepath.Join(t.TempDir(), "chart")
	if err := os.CopyFS(chart, os.DirFS(chartUnderTest(root, pdnsChart))); err != nil {
		t.Fatal(err)
	}
	chart = withDependencies(t, chart)
	next := render(t, chart, pdnsRelease, pdnsLiveValues)
	if _, ok := next[pdnsSecret]; !ok {
		t.Fatalf("the chart under test does not render %s", pdnsSecret)
	}
	for _, v := range pdnsKeptFrom {
		old := withDependencies(t, extract(t, root, pdnsChart, releaseCommit(t, root, pdnsChart, v)))
		deployed := render(t, old, pdnsRelease, pdnsLiveValues)
		live := stored(t, deployed[pdnsSecret])
		live.Annotations = merged(live.Annotations, map[string]string{"meta.helm.sh/release-name": pdnsRelease, "meta.helm.sh/release-namespace": pdnsRelease})
		b, err := json.Marshal(live)
		if err != nil {
			t.Fatal(err)
		}
		liveObj := Object{GVK: deployed[pdnsSecret].GVK, Name: deployed[pdnsSecret].Name, JSON: b}

		t.Run("known answer: "+v+" live, a release without the Secret deletes it", func(t *testing.T) {
			if !helmDeletes(t, deployed, render(t, old, pdnsRelease, pdnsWithout), live) {
				t.Fatalf("%s is kept; the Secret of %s has no keep annotation", pdnsSecret, v)
			}
		})
		t.Run(v+" deployed and live", func(t *testing.T) {
			meta := strategicpatch.PatchMetaFromStruct{T: reflect.TypeOf(corev1.Secret{})}
			patch, err := strategicpatch.CreateThreeWayMergePatch(deployed[pdnsSecret].JSON, next[pdnsSecret].JSON, liveObj.JSON, meta, true)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("the patch Helm sends for %s: %s", pdnsSecret, patch)
			o, err := Upgrade(objPtr(deployed[pdnsSecret]), &liveObj, next[pdnsSecret])
			if err != nil || o.Refusal != "" {
				t.Fatalf("%s: error %v, refusal %q", pdnsSecret, err, o.Refusal)
			}
			after := stored(t, o.Object)
			if got := after.Annotations["helm.sh/resource-policy"]; got != "keep" {
				t.Errorf("annotation helm.sh/resource-policy of the live Secret: %q, want keep", got)
			}
			want := map[string][]byte{"api-key": []byte(pdnsKeyValue), "webserver-password": []byte(pdnsWebValue)}
			if !reflect.DeepEqual(live.Data, want) || !reflect.DeepEqual(after.Data, want) {
				t.Errorf("data of the live Secret: %q before the upgrade, %q after, want %q", live.Data, after.Data, want)
			}
			for k, v := range live.Annotations {
				if after.Annotations[k] != v {
					t.Errorf("annotation %s of the live Secret: %q after the upgrade, was %q", k, after.Annotations[k], v)
				}
			}
			if helmDeletes(t, next, render(t, chart, pdnsRelease, pdnsWithout), after) {
				t.Errorf("a release without the Secret deletes %s after the upgrade", pdnsSecret)
			}
		})
	}
}

// stored is the Secret as the API server keeps it: stringData written into data
// and never stored (k8s.io/kubernetes v1.33.2 pkg/apis/core/v1/conversion.go
// 407-423).
func stored(t *testing.T, o Object) corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := json.Unmarshal(o.JSON, &s); err != nil {
		t.Fatal(err)
	}
	for k, v := range s.StringData {
		if s.Data == nil {
			s.Data = map[string][]byte{}
		}
		s.Data[k] = []byte(v)
	}
	s.StringData = nil
	return s
}

// helmDeletes is Helm's step for an object of the deployed release that the
// new release does not hold (pkg/kube/client.go 436-454): it reads the live
// object and deletes it unless its annotation helm.sh/resource-policy is keep.
func helmDeletes(t *testing.T, deployed, next map[string]Object, live corev1.Secret) bool {
	t.Helper()
	k := "Secret/" + live.Name
	if _, ok := deployed[k]; !ok {
		t.Fatalf("%s is not in the deployed release", k)
	}
	if _, ok := next[k]; ok {
		t.Fatalf("%s is in the new release: Helm patches it", k)
	}
	return live.Annotations["helm.sh/resource-policy"] != "keep"
}
