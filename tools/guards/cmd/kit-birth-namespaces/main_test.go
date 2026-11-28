package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hr = "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata: {name: %[1]s, namespace: flux-system}\nspec: {targetNamespace: %[2]s, chart: {spec: {chart: %[1]s}}, values: %[3]s}\n"

// tree writes a chart bp-x that includes the library with .Values.gs (and
// args), its kit slot in namespace acme, and slot 08 with the peerTrust fields.
func tree(t *testing.T, chartGS, slotGS, peerTrust, args string) string {
	root := t.TempDir()
	for p, s := range map[string]string{
		"platform/x/chart/Chart.yaml":                "name: bp-x\n",
		"platform/x/chart/values.yaml":               "gs: " + chartGS + "\n",
		"platform/x/chart/templates/sub/g.yaml":      `{{ include "openova.generatedSecrets" (dict "ctx" . "secrets" .Values.gs "peers" (list)` + args + ") }}\n",
		slot08:                                       fmt.Sprintf(hr, "bp-openbao", "openbao", "{peerTrust: {selfRegion: a"+peerTrust+"}}"),
		"clusters/_template/bootstrap-kit/20-x.yaml": fmt.Sprintf(hr, "bp-x", "acme", "{gs: "+slotGS+"}"),
		"clusters/_template/providers/p/kit/k.yaml":  "kind: Kustomization\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestEveryRuleCanFail(t *testing.T) {
	m, off, none, acme := "{a: {delivery: mounted, epoch: 1}}", "{a: {delivery: mounted, epoch: 1, sharedAcrossRegions: false}}", ", birthNamespaces: []", ", birthNamespaces: [acme]"
	for _, c := range []struct {
		name, chartGS, slotGS, peerTrust, args string
		code                                   int
		want                                   string
	}{
		{"a shared mounted value in a namespace the list lacks", m, "{}", none, "", 1, "namespace acme holds shared mounted values (clusters/_template/bootstrap-kit/20-x.yaml (chart bp-x) generatedSecrets.a)"},
		{"the list names it", m, "{}", acme, "", 0, "0 finding(s)"},
		{"the list is not set", off, "{}", "", "", 1, "does not set bp-openbao peerTrust.birthNamespaces"},
		{"a value that is not shared needs no role", off, "{}", none, "", 0, "0 namespace(s) need"},
		{"a synced value needs no role", "{a: {delivery: synced, epoch: 1}}", "{}", none, "", 0, "0 namespace(s) need"},
		{"the slot makes the value shared", off, "{a: {sharedAcrossRegions: true}}", none, "", 1, "namespace acme holds"},
		{"the slot adds a value", "{}", m, none, "", 1, "namespace acme holds"},
		{"the slot removes the value", m, "{a: null}", none, "", 0, "0 finding(s)"},
		{"the include names its namespace", m, "{}", acme, ` "namespace" "other"`, 1, "namespace other holds"},
		{"a listed namespace no value needs", off, "{}", acme, "", 1, "lists acme in peerTrust.birthNamespaces, where no kit chart"},
		{"sharedAcrossRegions that is not a boolean", `{a: {delivery: mounted, epoch: 1, sharedAcrossRegions: "false"}}`, "{}", none, "", 1, "which is not a boolean"},
		{"a namespace that is not a quoted name", m, "{}", none, ` "namespace" $ns`, 2, "not a quoted name"},
	} {
		lines, code := judge(tree(t, c.chartGS, c.slotGS, c.peerTrust, c.args))
		if out := strings.Join(lines, "\n"); code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit %d, want %d and %q in:\n%s", c.name, code, c.code, c.want, out)
		}
	}
	root := tree(t, m, "{}", none, "")
	if err := os.WriteFile(filepath.Join(root, "clusters/_template/bootstrap-kit/20-x.yaml"), []byte(fmt.Sprintf(hr, "bp-y", "acme", "{}")), 0o600); err != nil {
		t.Fatal(err)
	}
	if lines, code := judge(root); code != 2 || !strings.Contains(lines[0], "nothing was judged") {
		t.Errorf("no kit release of a library chart: exit %d, want 2: %v", code, lines)
	}
}
