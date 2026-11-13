package suite

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G04, Network census recorded: the record of a run holds the
// network posture of every region, read from the cluster API. The census
// records what it reads. Its content never decides the check: a block passes
// when it was read and fails when it could not be read, so an unmeasured
// block is never counted as present. The check only runs `kubectl get`.
//
// The blocks, each recorded once per region:
//
//	D1.1  kube-proxy: the pods of kube-system named kube-proxy or labelled
//	      k8s-app=kube-proxy, and for each node whether the arguments that
//	      k3s wrote into the node annotation k3s.io/node-args hold
//	      --disable-kube-proxy
//	D1.2  the values of the ConfigMap kube-system/cilium-config that the
//	      list censusCiliumKeys names
//	D1.3  every NetworkPolicy whose name ends in -default-deny, with its
//	      namespace, and the number of all NetworkPolicies
//	D1.4  the number of CiliumNetworkPolicies, and the names of the
//	      CiliumClusterwideNetworkPolicies
//	D1.7  every listener of every Gateway, with its port, protocol, host
//	      name and allowedRoutes
//	D1.8  every node with its labels, taints and addresses
//
// The blocks D1.5 and D1.6 (dials from a test pod) are not part of this
// check: a dial needs a pod, and this check writes nothing.
const networkCensusID = "NET-G04"

// censusNotServedText is what kubectl prints when the API server does not
// serve a kind: its custom resource definition is not installed.
const censusNotServedText = "the server doesn't have a resource type"

// censusCiliumKeys are the keys of cilium-config that block D1.2 records.
var censusCiliumKeys = []string{"ipv4-native-routing-cidr", "cluster-id", "cluster-name", "enable-wireguard", "kube-proxy-replacement"}

// censusBlock is one evidence block. read fills ev with what it read and
// returns an error only when it could not read.
type censusBlock struct {
	id   string
	name string
	read func(ctx context.Context, region kube.Region, ev Evidence) error
}

var censusBlocks = []censusBlock{
	{"D1.1", "kube-proxy pods and the k3s argument --disable-kube-proxy", censusKubeProxy},
	{"D1.2", "Cilium configuration values", censusCiliumConfig},
	{"D1.3", "NetworkPolicies named *-default-deny", censusDefaultDeny},
	{"D1.4", "CiliumNetworkPolicies and CiliumClusterwideNetworkPolicies", censusCiliumPolicies},
	{"D1.7", "Gateway listeners and the routes they admit", censusListeners},
	{"D1.8", "nodes with labels, taints and addresses", censusNodes},
}

type networkCensusCheck struct{}

func init() { RegisterCheck(networkCensusCheck{}) }

func (networkCensusCheck) ID() string { return networkCensusID }

func (networkCensusCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	for _, region := range cc.Regions {
		for _, b := range censusBlocks {
			n++
			rec.Step(networkCensusID+"."+strconv.Itoa(n), "census "+b.id+" in region "+region.ID+": "+b.name, func(ev Evidence) error {
				ev["block"], ev["region"] = b.id, region.ID
				return b.read(ctx, region, ev)
			})
		}
	}
	return nil
}

// censusList reads a list. served is false, with no error, when the API
// server does not serve the kind: that is a finding of the census.
func censusList(ctx context.Context, region kube.Region, resource, namespace string) (items []kube.Object, served bool, err error) {
	items, err = region.GetList(ctx, resource, namespace)
	if err != nil {
		if censusNotServed(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return items, true, nil
}

func censusNotServed(err error) bool {
	return err != nil && strings.Contains(err.Error(), censusNotServedText)
}

func censusKubeProxy(ctx context.Context, region kube.Region, ev Evidence) error {
	pods, err := region.GetList(ctx, "pods", "kube-system")
	if err != nil {
		return err
	}
	found := []string{}
	for _, p := range pods {
		if strings.HasPrefix(p.Name(), "kube-proxy") || p.Str("metadata", "labels", "k8s-app") == "kube-proxy" {
			found = append(found, p.Name())
		}
	}
	sort.Strings(found)
	nodes, err := region.GetList(ctx, "nodes", "")
	if err != nil {
		return err
	}
	flags := map[string]string{}
	for _, node := range nodes {
		flags[node.Name()] = censusDisableKubeProxy(node.Str("metadata", "annotations", "k3s.io/node-args"))
	}
	ev["kube_proxy_pods"], ev["k3s_disable_kube_proxy"] = found, flags
	return nil
}

// censusDisableKubeProxy reads the annotation k3s.io/node-args, a JSON list
// of the arguments k3s was started with.
func censusDisableKubeProxy(annotation string) string {
	if annotation == "" {
		return "no annotation k3s.io/node-args"
	}
	var args []string
	if err := json.Unmarshal([]byte(annotation), &args); err != nil {
		return "annotation k3s.io/node-args is not a JSON list"
	}
	for _, a := range args {
		if a == "--disable-kube-proxy" || a == "--disable-kube-proxy=true" {
			return "disabled"
		}
	}
	return "not disabled"
}

func censusCiliumConfig(ctx context.Context, region kube.Region, ev Evidence) error {
	maps, err := region.GetList(ctx, "configmaps", "kube-system")
	if err != nil {
		return err
	}
	for _, m := range maps {
		if m.Name() != "cilium-config" {
			continue
		}
		data, _ := m.Get("data").(map[string]any)
		values, absent := map[string]any{}, []string{}
		for _, k := range censusCiliumKeys {
			if v, ok := data[k]; ok {
				values[k] = v
			} else {
				absent = append(absent, k)
			}
		}
		ev["cilium_config"], ev["absent_keys"] = values, absent
		return nil
	}
	ev["cilium_config"] = "absent"
	return nil
}

func censusDefaultDeny(ctx context.Context, region kube.Region, ev Evidence) error {
	policies, err := region.GetList(ctx, "networkpolicies", "")
	if err != nil {
		return err
	}
	found := []string{}
	for _, p := range policies {
		if strings.HasSuffix(p.Name(), "-default-deny") {
			found = append(found, p.Namespace()+"/"+p.Name())
		}
	}
	sort.Strings(found)
	ev["default_deny"], ev["networkpolicies"] = found, len(policies)
	return nil
}

func censusCiliumPolicies(ctx context.Context, region kube.Region, ev Evidence) error {
	cnp, served, err := censusList(ctx, region, "ciliumnetworkpolicies.cilium.io", "")
	if err != nil {
		return err
	}
	if served {
		ev["ciliumnetworkpolicies"] = len(cnp)
	} else {
		ev["ciliumnetworkpolicies"] = "kind not served"
	}
	ccnp, served, err := censusList(ctx, region, "ciliumclusterwidenetworkpolicies.cilium.io", "")
	if err != nil {
		return err
	}
	if !served {
		ev["ciliumclusterwidenetworkpolicies"] = "kind not served"
		return nil
	}
	names := []string{}
	for _, p := range ccnp {
		names = append(names, p.Name())
	}
	sort.Strings(names)
	ev["ciliumclusterwidenetworkpolicies"] = names
	return nil
}

func censusListeners(ctx context.Context, region kube.Region, ev Evidence) error {
	gateways, served, err := censusList(ctx, region, "gateways.gateway.networking.k8s.io", "")
	if err != nil {
		return err
	}
	if !served {
		ev["listeners"] = "kind not served"
		return nil
	}
	sort.Slice(gateways, func(i, j int) bool {
		return gateways[i].Namespace()+"/"+gateways[i].Name() < gateways[j].Namespace()+"/"+gateways[j].Name()
	})
	listeners := []map[string]any{}
	for _, g := range gateways {
		for _, l := range g.Objects("spec", "listeners") {
			listeners = append(listeners, map[string]any{
				"gateway": g.Namespace() + "/" + g.Name(), "listener": l.Str("name"), "port": l.Get("port"),
				"protocol": l.Str("protocol"), "hostname": l.Str("hostname"), "allowedRoutes": l.Get("allowedRoutes"),
			})
		}
	}
	ev["listeners"] = listeners
	return nil
}

func censusNodes(ctx context.Context, region kube.Region, ev Evidence) error {
	nodes, err := region.GetList(ctx, "nodes", "")
	if err != nil {
		return err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name() < nodes[j].Name() })
	out := []map[string]any{}
	for _, node := range nodes {
		out = append(out, map[string]any{
			"name": node.Name(), "labels": node.Get("metadata", "labels"),
			"taints": node.Get("spec", "taints"), "addresses": node.Get("status", "addresses"),
		})
	}
	ev["nodes"] = out
	return nil
}
