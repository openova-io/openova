package suite

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func censusNode(name, nodeArgs string) obj {
	meta := obj{"name": name, "labels": obj{"catalyst.openova.io/role": "control-plane"}}
	if nodeArgs != "" {
		meta["annotations"] = obj{"k3s.io/node-args": nodeArgs}
	}
	return obj{"metadata": meta,
		"spec":   obj{"taints": []obj{{"key": "edge", "effect": "NoSchedule"}}},
		"status": obj{"addresses": []obj{{"type": "InternalIP", "address": "10.20.0.5"}}}}
}

func init() {
	a := map[string]string{
		"a pods kube-system": kubeList(
			obj{"metadata": obj{"name": "kube-proxy-x1"}},
			obj{"metadata": obj{"name": "proxy-two", "labels": obj{"k8s-app": "kube-proxy"}}},
			obj{"metadata": obj{"name": "cilium-abcde", "labels": obj{"k8s-app": "cilium"}}}),
		"a nodes": kubeList(censusNode("a-w1", `["agent"]`), censusNode("a-cp1", `["server","--disable-kube-proxy"]`)),
		"a configmaps kube-system": kubeList(
			obj{"metadata": obj{"name": "coredns"}, "data": obj{"cluster-name": "wrong"}},
			obj{"metadata": obj{"name": "cilium-config"}, "data": obj{"cluster-id": "1", "cluster-name": "a", "kube-proxy-replacement": "false"}}),
		"a networkpolicies": kubeList(
			obj{"metadata": obj{"namespace": "openbao", "name": "openbao-default-deny"}},
			obj{"metadata": obj{"namespace": "gitea", "name": "allow-gitea"}}),
		"a ciliumnetworkpolicies.cilium.io":            kubeList(obj{"metadata": obj{"namespace": "x", "name": "p"}}),
		"a ciliumclusterwidenetworkpolicies.cilium.io": kubeList(obj{"metadata": obj{"name": "z"}}, obj{"metadata": obj{"name": "m"}}),
		"a gateways.gateway.networking.k8s.io": kubeList(obj{"metadata": obj{"namespace": "kube-system", "name": "cilium-gateway"},
			"spec": obj{"listeners": []obj{{"name": "https", "port": 443, "protocol": "HTTPS", "hostname": "*.example.test",
				"allowedRoutes": obj{"namespaces": obj{"from": "All"}}}}}}),
	}
	b := map[string]string{
		"b pods kube-system":                           kubeList(),
		"b nodes":                                      kubeList(censusNode("b-cp1", "")),
		"b configmaps kube-system":                     kubeList(),
		"b networkpolicies":                            kubeList(),
		"b ciliumnetworkpolicies.cilium.io":            kubeList(),
		"b ciliumclusterwidenetworkpolicies.cilium.io": kubeList(),
		"b gateways.gateway.networking.k8s.io":         kubeList(),
	}
	both := map[string]string{}
	noGateways := map[string]string{}
	for k, v := range a {
		both[k], noGateways[k] = v, v
	}
	for k, v := range b {
		both[k] = v
		if k != "b gateways.gateway.networking.k8s.io" {
			noGateways[k] = v
		}
	}
	fakeAnswers["network-census"] = both
	fakeAnswers["network-census-b-no-gateways"] = noGateways
}

// censusEvidence returns the evidence of each step as JSON, by step id.
func censusEvidence(t *testing.T, r *runner) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range r.run.Steps {
		b, err := json.Marshal(s.Evidence)
		if err != nil {
			t.Fatal(err)
		}
		out[s.ID] = string(b)
	}
	return out
}

func TestNetworkCensusCheck(t *testing.T) {
	if _, ok := registry[networkCensusID]; !ok || networkCensusID != "NET-G04" {
		t.Fatalf("check %s is not registered", networkCensusID)
	}
	// Every block of every region is read: the check passes, whatever it finds.
	r := runCheck(t, "network-census", networkCensusCheck{}, &CheckContext{})
	if got := r.run.Checks[0]; got.Status != Pass {
		t.Fatalf("want PASS, got %+v and failed steps %v", got, failedSteps(r))
	}
	want := []string{"D1.1 region-a", "D1.2 region-a", "D1.3 region-a", "D1.4 region-a", "D1.7 region-a", "D1.8 region-a",
		"D1.1 region-b", "D1.2 region-b", "D1.3 region-b", "D1.4 region-b", "D1.7 region-b", "D1.8 region-b"}
	if len(r.run.Steps) != len(want) {
		t.Fatalf("want %d steps, got %d", len(want), len(r.run.Steps))
	}
	for i, s := range r.run.Steps {
		got := fmt.Sprint(s.Journey, " ", s.ID, " ", s.Evidence["block"], " ", s.Evidence["region"])
		if w := fmt.Sprintf("NET-G04 NET-G04.%d %s", i+1, want[i]); got != w {
			t.Errorf("step %d: got %q, want %q", i+1, got, w)
		}
	}
	ev := censusEvidence(t, r)
	for id, parts := range map[string][]string{
		"NET-G04.1":  {`"kube_proxy_pods":["kube-proxy-x1","proxy-two"]`, `"k3s_disable_kube_proxy":{"a-cp1":"disabled","a-w1":"not disabled"}`},
		"NET-G04.2":  {`"cilium_config":{"cluster-id":"1","cluster-name":"a","kube-proxy-replacement":"false"}`, `"absent_keys":["ipv4-native-routing-cidr","enable-wireguard"]`},
		"NET-G04.3":  {`"default_deny":["openbao/openbao-default-deny"]`, `"networkpolicies":2`},
		"NET-G04.4":  {`"ciliumnetworkpolicies":1`, `"ciliumclusterwidenetworkpolicies":["m","z"]`},
		"NET-G04.5":  {`"listeners":[{"allowedRoutes":{"namespaces":{"from":"All"}},"gateway":"kube-system/cilium-gateway","hostname":"*.example.test","listener":"https","port":443,"protocol":"HTTPS"}]`},
		"NET-G04.6":  {`"nodes":[{"addresses":[{"address":"10.20.0.5","type":"InternalIP"}],"labels":{"catalyst.openova.io/role":"control-plane"},"name":"a-cp1","taints":[{"effect":"NoSchedule","key":"edge"}]},{"addresses"`},
		"NET-G04.7":  {`"kube_proxy_pods":[]`, `"k3s_disable_kube_proxy":{"b-cp1":"no annotation k3s.io/node-args"}`},
		"NET-G04.8":  {`"cilium_config":"absent"`},
		"NET-G04.9":  {`"default_deny":[]`, `"networkpolicies":0`},
		"NET-G04.10": {`"ciliumnetworkpolicies":0`, `"ciliumclusterwidenetworkpolicies":[]`},
		"NET-G04.11": {`"listeners":[]`},
		"NET-G04.12": {`"name":"b-cp1"`},
	} {
		for _, p := range parts {
			if !strings.Contains(ev[id], p) {
				t.Errorf("%s: evidence %s does not hold %s", id, ev[id], p)
			}
		}
	}
	// A block that cannot be read fails, and only that block.
	r = runCheck(t, "network-census-b-no-gateways", networkCensusCheck{}, &CheckContext{})
	if got := r.run.Checks[0]; got.Status != Fail || !strings.Contains(got.Reason, "[region-b] kubectl get gateways.gateway.networking.k8s.io") {
		t.Errorf("a region without an answer: want FAIL on the Gateways of region-b, got %+v", got)
	}
	if failed := failedSteps(r); len(failed) != 1 {
		t.Errorf("want one failed step, got %v", failed)
	}
	// A region that cannot be read at all fails every block of it.
	r = runCheck(t, "network-census-unreachable", networkCensusCheck{}, &CheckContext{})
	if failed := failedSteps(r); len(failed) != 12 || r.run.Checks[0].Status != Fail {
		t.Errorf("unreachable regions: want 12 failed steps, got %v", failed)
	}
}

func TestNetworkCensusNotServed(t *testing.T) {
	notServed := errors.New(`[region-a] kubectl get gateways.gateway.networking.k8s.io -o json -A: exit status 1: error: the server doesn't have a resource type "gateways"`)
	other := errors.New(`[region-a] kubectl get gateways.gateway.networking.k8s.io -o json -A: exit status 1: Unable to connect to the server`)
	if !censusNotServed(notServed) || censusNotServed(other) || censusNotServed(nil) {
		t.Errorf("censusNotServed: want true, false, false")
	}
	for annotation, want := range map[string]string{
		"":                                       "no annotation k3s.io/node-args",
		"--disable-kube-proxy":                   "annotation k3s.io/node-args is not a JSON list",
		`["server","--disable-kube-proxy"]`:      "disabled",
		`["server","--disable-kube-proxy=true"]`: "disabled",
		`["server","--disable","traefik"]`:       "not disabled",
	} {
		if got := censusDisableKubeProxy(annotation); got != want {
			t.Errorf("censusDisableKubeProxy(%q) = %q, want %q", annotation, got, want)
		}
	}
}
