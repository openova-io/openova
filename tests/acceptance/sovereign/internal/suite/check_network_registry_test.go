package suite

import (
	"encoding/json"
	"strings"
	"testing"
)

// networkRegistryDoc is a registry of two regions in the form of
// scripts/tests/check-network-registry/valid-two-regions.json, a document
// that scripts/check-network-registry.py accepts.
const networkRegistryDoc = `{"podSupernet": "10.42.0.0/15", "mtu": 1420, "regionSeparation": "none",
 "controlPlaneAccess": {"adminCIDRs": [], "provisionerCIDRs": []},
 "regions": [
  {"code": "region-a", "cloudRegion": "region-1", "az": "region-1a", "clusterName": "t99-region-a", "clusterID": 1,
   "nodeCIDR": "10.127.1.0/24", "podCIDR": "10.42.0.0/16", "serviceCIDR": "10.96.0.0/16",
   "meshEndpoint": {"ips": ["203.0.113.11"], "port": 12379}, "edgeAddress": "203.0.113.10", "egressAddresses": ["203.0.113.12", "203.0.113.11"]},
  {"code": "region-b", "cloudRegion": "region-1", "az": "region-1a", "clusterName": "t99-region-b", "clusterID": 2,
   "nodeCIDR": "10.137.1.0/24", "podCIDR": "10.43.0.0/16", "serviceCIDR": "10.97.0.0/16",
   "meshEndpoint": {"ips": ["203.0.113.11"], "port": 12379}, "edgeAddress": "203.0.113.10", "egressAddresses": ["203.0.113.12", "203.0.113.11"]}]}`

// networkRegistry returns the registry document with the change made by edit.
func networkRegistry(edit func(doc obj)) obj {
	doc := obj{}
	if err := json.Unmarshal([]byte(networkRegistryDoc), &doc); err != nil {
		panic(err)
	}
	if edit != nil {
		edit(doc)
	}
	return doc
}

// networkRegistrySovereign is a Sovereign object; a nil network gives an
// object without status.network.
func networkRegistrySovereign(name string, network obj) obj {
	o := obj{"metadata": obj{"name": name}, "spec": obj{"fqdn": "t99.example.test"}, "status": obj{"regionCount": 2}}
	if network != nil {
		o["status"].(obj)["network"] = network
	}
	return o
}

// networkRegistryCilium is the ConfigMap list of kube-system with
// cilium-config holding data, beside another ConfigMap.
func networkRegistryCilium(data obj) string {
	return kubeList(
		obj{"metadata": obj{"namespace": "kube-system", "name": "coredns"}, "data": obj{"cluster-name": "wrong", "mtu": "9000"}},
		obj{"metadata": obj{"namespace": "kube-system", "name": "cilium-config"}, "data": data})
}

func networkRegistryConfig(name, id string) obj {
	return obj{"ipv4-native-routing-cidr": "10.42.0.0/15", "mtu": "1420", "cluster-name": name, "cluster-id": id,
		"enable-wireguard": "true"}
}

func init() {
	sovA := kubeList(networkRegistrySovereign("sovereign-self", networkRegistry(nil)))
	sovB := kubeList(networkRegistrySovereign("sovereign-self", networkRegistry(nil)))
	cilA := networkRegistryCilium(networkRegistryConfig("t99-region-a", "1"))
	cilB := networkRegistryCilium(networkRegistryConfig("t99-region-b", "2"))
	scenario := func(change map[string]string) map[string]string {
		s := map[string]string{
			"a sovereigns.catalyst.openova.io": sovA, "a configmaps kube-system": cilA,
			"b sovereigns.catalyst.openova.io": sovB, "b configmaps kube-system": cilB,
		}
		for k, v := range change {
			if v == "" {
				delete(s, k)
			} else {
				s[k] = v
			}
		}
		return s
	}
	fakeAnswers["network-registry"] = scenario(nil)
	fakeAnswers["network-registry-supernet"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": kubeList(networkRegistrySovereign("sovereign-self", networkRegistry(func(doc obj) { doc["podSupernet"] = "10.40.0.0/13" }))),
	})
	fakeAnswers["network-registry-same-range"] = scenario(map[string]string{
		"a configmaps kube-system": networkRegistryCilium(obj{"ipv4-native-routing-cidr": " 10.42.0.0/15", "mtu": "1420", "cluster-name": "t99-region-a", "cluster-id": "1"}),
	})
	fakeAnswers["network-registry-mtu"] = scenario(map[string]string{
		"a configmaps kube-system": networkRegistryCilium(obj{"ipv4-native-routing-cidr": "10.42.0.0/15", "mtu": "1370", "cluster-name": "t99-region-a", "cluster-id": "1"}),
	})
	fakeAnswers["network-registry-cluster-id"] = scenario(map[string]string{
		"b configmaps kube-system": networkRegistryCilium(networkRegistryConfig("t99-region-b", "3")),
	})
	fakeAnswers["network-registry-cluster-name"] = scenario(map[string]string{
		"b configmaps kube-system": networkRegistryCilium(networkRegistryConfig("default", "0")),
	})
	fakeAnswers["network-registry-key-absent"] = scenario(map[string]string{
		"a configmaps kube-system": networkRegistryCilium(obj{"ipv4-native-routing-cidr": "10.42.0.0/15", "cluster-name": "t99-region-a", "cluster-id": "1"}),
	})
	fakeAnswers["network-registry-no-cilium-config"] = scenario(map[string]string{
		"b configmaps kube-system": kubeList(obj{"metadata": obj{"namespace": "kube-system", "name": "coredns"}}),
	})
	fakeAnswers["network-registry-no-network"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(networkRegistrySovereign("sovereign-self", nil)),
	})
	fakeAnswers["network-registry-no-object"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": kubeList(),
	})
	fakeAnswers["network-registry-two-objects"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(networkRegistrySovereign("sovereign-self", networkRegistry(nil)), networkRegistrySovereign("other", networkRegistry(nil))),
	})
	fakeAnswers["network-registry-mtu-text"] = scenario(map[string]string{
		"a sovereigns.catalyst.openova.io": kubeList(networkRegistrySovereign("sovereign-self", networkRegistry(func(doc obj) { doc["mtu"] = "1420" }))),
	})
	fakeAnswers["network-registry-region-b-unreachable"] = scenario(map[string]string{
		"b sovereigns.catalyst.openova.io": "",
	})
}

func TestNetworkRegistryCheck(t *testing.T) {
	if _, ok := registry[networkRegistryID]; !ok || networkRegistryID != "NET-G08" {
		t.Fatalf("check %s is not registered", networkRegistryID)
	}
	for name, c := range map[string]struct {
		scenario string
		want     string // "" = PASS, else a part of the reason of the FAIL
	}{
		"the registry equals the live values":  {"network-registry", ""},
		"a range written another way is equal": {"network-registry-same-range", ""},
		"the pod supernet differs":             {"network-registry-supernet", `region region-b: ipv4-native-routing-cidr is "10.42.0.0/15", status.network.podSupernet is 10.40.0.0/13`},
		"the MTU differs":                      {"network-registry-mtu", `region region-a: mtu is "1370", status.network.mtu is 1420`},
		"the MTU of the registry is text":      {"network-registry-mtu-text", `mtu is "1420", status.network.mtu is 1420`},
		"the cluster identifier differs":       {"network-registry-cluster-id", `cluster-id is "3", clusterID of the entry t99-region-b is 2`},
		"no entry has the cluster name":        {"network-registry-cluster-name", `no entry of status.network.regions has clusterName "default" (the entries name t99-region-a, t99-region-b)`},
		"cilium-config lacks a key":            {"network-registry-key-absent", "region region-a: cilium-config lacks the key(s) mtu"},
		"a region has no cilium-config":        {"network-registry-no-cilium-config", "region region-b has no ConfigMap kube-system/cilium-config"},
		"the object has no status.network":     {"network-registry-no-network", "region region-a: the Sovereign object sovereign-self has no status.network"},
		"a region has no Sovereign object":     {"network-registry-no-object", "region region-b has 0 Sovereign objects, want exactly 1"},
		"a region has two Sovereign objects":   {"network-registry-two-objects", "region region-a has 2 Sovereign objects, want exactly 1"},
		"a region cannot be reached":           {"network-registry-region-b-unreachable", "[region-b] kubectl get sovereigns.catalyst.openova.io"},
	} {
		r := runCheck(t, c.scenario, networkRegistryCheck{}, &CheckContext{})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}

	// One step per region, each with what it compared.
	r := runCheck(t, "network-registry", networkRegistryCheck{}, &CheckContext{})
	if len(r.run.Steps) != 2 || r.run.Steps[0].ID != "NET-G08.1" || r.run.Steps[1].ID != "NET-G08.2" {
		t.Fatalf("want the steps NET-G08.1 and NET-G08.2, got %+v", r.run.Steps)
	}
	b, err := json.Marshal(r.run.Steps[1].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sovereigns":["sovereign-self"]`, `"cluster-id":"2"`, `"cluster-name":"t99-region-b"`,
		`"clusterID":2`, `"code":"region-b"`, `"podSupernet":"10.42.0.0/15"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the evidence of NET-G08.2 lacks %s: %s", want, b)
		}
	}
}
