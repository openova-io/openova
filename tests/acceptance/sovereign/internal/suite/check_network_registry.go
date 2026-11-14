package suite

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G08, Registry equals live values: the network registry that the
// Sovereign object of a region holds names the values that Cilium of that
// region runs with.
//
// The registry is status.network of the Sovereign object: the output
// network_registry of the provider module, copied unchanged
// (infra/providers/PROVIDER-INTERFACE.md, section 2.1). For each region the
// check reads the Sovereign objects of that cluster (exactly one must exist)
// and the ConfigMap kube-system/cilium-config, and compares four values:
//
//	cilium-config key          registry field
//	ipv4-native-routing-cidr   status.network.podSupernet
//	mtu                        status.network.mtu
//	cluster-name               status.network.regions[].clusterName
//	cluster-id                 status.network.regions[].clusterID
//
// The registry entry of a cluster is the entry of status.network.regions
// whose clusterName equals the cluster-name of that cluster. A region fails
// when it has no Sovereign object or more than one, when its object has no
// status.network, when it has no cilium-config or the ConfigMap lacks one of
// the four keys, when no entry carries its cluster-name, or when a value
// differs. Two ranges are equal when they name the same network. The check
// only runs `kubectl get`.
const networkRegistryID = "NET-G08"

const (
	networkRegistryKind      = "sovereigns.catalyst.openova.io"
	networkRegistryNamespace = "kube-system"
	networkRegistryConfigMap = "cilium-config"
)

// networkRegistryKeys are the keys of cilium-config that the check compares.
var networkRegistryKeys = []string{"ipv4-native-routing-cidr", "mtu", "cluster-name", "cluster-id"}

type networkRegistryCheck struct{}

func init() { RegisterCheck(networkRegistryCheck{}) }

func (networkRegistryCheck) ID() string { return networkRegistryID }

func (networkRegistryCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, region := range cc.Regions {
		rec.Step(networkRegistryID+"."+strconv.Itoa(i+1), "the network registry of region "+region.ID+" equals its Cilium configuration", func(ev Evidence) error {
			return networkRegistryRegion(ctx, region, ev)
		})
	}
	return nil
}

func networkRegistryRegion(ctx context.Context, region kube.Region, ev Evidence) error {
	objects, err := region.GetList(ctx, networkRegistryKind, "")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(objects))
	for _, o := range objects {
		names = append(names, o.Name())
	}
	sort.Strings(names)
	ev["sovereigns"] = names
	if len(objects) != 1 {
		return fmt.Errorf("region %s has %d Sovereign objects, want exactly 1", region.ID, len(objects))
	}
	network, ok := objects[0].Get("status", "network").(map[string]any)
	if !ok {
		return fmt.Errorf("region %s: the Sovereign object %s has no status.network", region.ID, objects[0].Name())
	}
	registry := kube.Object(network)

	maps, err := region.GetList(ctx, "configmaps", networkRegistryNamespace)
	if err != nil {
		return err
	}
	var data map[string]any
	found := false
	for _, m := range maps {
		if m.Name() == networkRegistryConfigMap {
			data, _ = m.Get("data").(map[string]any)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("region %s has no ConfigMap %s/%s", region.ID, networkRegistryNamespace, networkRegistryConfigMap)
	}
	live, absent := map[string]string{}, []string{}
	for _, k := range networkRegistryKeys {
		if v, ok := data[k].(string); ok {
			live[k] = v
		} else {
			absent = append(absent, k)
		}
	}
	ev["live"] = live
	if len(absent) > 0 {
		return fmt.Errorf("region %s: %s lacks the key(s) %s", region.ID, networkRegistryConfigMap, strings.Join(absent, ", "))
	}

	entries := registry.Objects("regions")
	entryNames := make([]string, 0, len(entries))
	var entry kube.Object
	for _, e := range entries {
		entryNames = append(entryNames, e.Str("clusterName"))
		if entry == nil && e.Str("clusterName") == live["cluster-name"] {
			entry = e
		}
	}
	seen := Evidence{"podSupernet": registry.Get("podSupernet"), "mtu": registry.Get("mtu")}
	if entry != nil {
		seen["code"], seen["clusterName"], seen["clusterID"] = entry.Get("code"), entry.Get("clusterName"), entry.Get("clusterID")
	}
	ev["registry"] = seen

	failures := []string{}
	if !networkRegistrySameRange(live["ipv4-native-routing-cidr"], registry.Get("podSupernet")) {
		failures = append(failures, fmt.Sprintf("ipv4-native-routing-cidr is %q, status.network.podSupernet is %v", live["ipv4-native-routing-cidr"], registry.Get("podSupernet")))
	}
	if !networkRegistrySameNumber(live["mtu"], registry.Get("mtu")) {
		failures = append(failures, fmt.Sprintf("mtu is %q, status.network.mtu is %v", live["mtu"], registry.Get("mtu")))
	}
	if entry == nil {
		failures = append(failures, fmt.Sprintf("no entry of status.network.regions has clusterName %q (the entries name %s)", live["cluster-name"], strings.Join(entryNames, ", ")))
	} else if !networkRegistrySameNumber(live["cluster-id"], entry.Get("clusterID")) {
		failures = append(failures, fmt.Sprintf("cluster-id is %q, clusterID of the entry %s is %v", live["cluster-id"], live["cluster-name"], entry.Get("clusterID")))
	}
	if len(failures) > 0 {
		return fmt.Errorf("region %s: %s", region.ID, strings.Join(failures, "; "))
	}
	return nil
}

// networkRegistrySameRange reports whether the text of cilium-config and the
// registry value name the same IPv4 or IPv6 network.
func networkRegistrySameRange(live string, want any) bool {
	text, ok := want.(string)
	if !ok {
		return false
	}
	_, a, errA := net.ParseCIDR(strings.TrimSpace(live))
	_, b, errB := net.ParseCIDR(text)
	return errA == nil && errB == nil && a.String() == b.String()
}

// networkRegistrySameNumber reports whether the text of cilium-config is the
// whole number that the registry holds. JSON numbers decode as float64.
func networkRegistrySameNumber(live string, want any) bool {
	n, ok := want.(float64)
	if !ok || n != float64(int64(n)) {
		return false
	}
	got, err := strconv.ParseInt(strings.TrimSpace(live), 10, 64)
	return err == nil && got == int64(n)
}
