package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SOV-G17, The Sovereign object is written: every region's cluster holds
// exactly one Sovereign object, sovereign-self, written by its writer
// `sovereign-ops sovereign-object` (core/cmd/sovereign-ops/internal/
// sovereignobject) from the region's own values.
//
// For each region the check reads the Sovereign objects of the cluster with
// their field managers, and the ConfigMaps of flux-system. A region fails when:
//
//   - it has no Sovereign object, more than one, or one not named sovereign-self;
//   - the field manager sovereign-ops wrote neither the object nor its status;
//   - one of spec.fqdn, spec.deploymentId, spec.provider is absent or differs
//     from SOVEREIGN_FQDN, SOVEREIGN_DEPLOYMENT_ID, PROVIDER of the ConfigMap
//     sovereign-variables;
//   - the codes of spec.regions differ from the cloudRegion values of
//     SOVEREIGN_REGIONS_JSON, in order;
//   - the ConfigMap sovereign-network-registry, which the parent writes at
//     hand-over, is absent, or status.network differs from its document
//     NETWORK_REGISTRY_JSON.
//
// A value the ConfigMap lacks fails the region too: the check never passes on
// a field it could not compare. It only runs `kubectl get`.
const sovereignObjectID = "SOV-G17"

const (
	sovereignObjectName     = "sovereign-self"
	sovereignObjectManager  = "sovereign-ops"
	sovereignObjectRegistry = "sovereign-network-registry"
)

type sovereignObjectCheck struct{}

func init() { RegisterCheck(sovereignObjectCheck{}) }

func (sovereignObjectCheck) ID() string { return sovereignObjectID }

func (sovereignObjectCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, region := range cc.Regions {
		rec.Step(sovereignObjectID+"."+strconv.Itoa(i+1), "region "+region.ID+" holds its Sovereign object, written from its own values", func(ev Evidence) error {
			return sovereignObjectRegion(ctx, region, ev)
		})
	}
	return nil
}

func sovereignObjectRegion(ctx context.Context, region kube.Region, ev Evidence) error {
	objects, err := region.GetList(ctx, networkRegistryKind, "", "--show-managed-fields")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(objects))
	for _, o := range objects {
		names = append(names, o.Name())
	}
	sort.Strings(names)
	ev["sovereigns"] = names
	if len(objects) != 1 || objects[0].Name() != sovereignObjectName {
		return fmt.Errorf("region %s has the Sovereign objects [%s], want exactly [%s]", region.ID, strings.Join(names, " "), sovereignObjectName)
	}
	o := objects[0]
	var managers []string
	for _, f := range o.Objects("metadata", "managedFields") {
		m := f.Str("manager")
		if s := f.Str("subresource"); s != "" {
			m += "/" + s
		}
		managers = append(managers, m)
	}
	sort.Strings(managers)
	ev["managers"] = managers
	if !sovereignObjectHas(managers, sovereignObjectManager) || !sovereignObjectHas(managers, sovereignObjectManager+"/status") {
		return fmt.Errorf("region %s: the field manager %s did not write both %s and its status; the managers are [%s]", region.ID, sovereignObjectManager,
			sovereignObjectName, strings.Join(managers, " "))
	}

	maps, err := region.GetList(ctx, "configmaps", "flux-system")
	if err != nil {
		return err
	}
	data := map[string]map[string]string{}
	for _, m := range maps {
		d := map[string]string{}
		for k, v := range sovereignObjectStrings(m.Get("data")) {
			d[k] = v
		}
		data[m.Name()] = d
	}
	vars, ok := data["sovereign-variables"]
	if !ok {
		return fmt.Errorf("region %s has no ConfigMap flux-system/sovereign-variables", region.ID)
	}

	failures := []string{}
	compared := Evidence{}
	for _, f := range [][2]string{{"fqdn", "SOVEREIGN_FQDN"}, {"deploymentId", "SOVEREIGN_DEPLOYMENT_ID"}, {"provider", "PROVIDER"}} {
		got, want := o.Str("spec", f[0]), vars[f[1]]
		compared[f[0]] = got
		switch {
		case want == "":
			failures = append(failures, fmt.Sprintf("sovereign-variables has no %s to compare spec.%s with", f[1], f[0]))
		case got != want:
			failures = append(failures, fmt.Sprintf("spec.%s is %q, %s is %q", f[0], got, f[1], want))
		}
	}
	var want []string
	var list []map[string]any
	if err := json.Unmarshal([]byte(vars["SOVEREIGN_REGIONS_JSON"]), &list); err != nil || len(list) == 0 {
		failures = append(failures, "SOVEREIGN_REGIONS_JSON of sovereign-variables is not a list of regions")
	}
	for _, r := range list {
		s, _ := r["cloudRegion"].(string)
		want = append(want, s)
	}
	got := []string{}
	for _, r := range o.Objects("spec", "regions") {
		got = append(got, r.Str("code"))
	}
	compared["regions"] = got
	if len(list) > 0 && !reflect.DeepEqual(got, want) {
		failures = append(failures, fmt.Sprintf("the codes of spec.regions are [%s], the cloudRegion values of SOVEREIGN_REGIONS_JSON are [%s]",
			strings.Join(got, " "), strings.Join(want, " ")))
	}

	network := o.Get("status", "network")
	compared["network"] = network != nil
	if reg, ok := data[sovereignObjectRegistry]; !ok {
		failures = append(failures, "no ConfigMap flux-system/"+sovereignObjectRegistry+": the parent delivered no network registry to this region")
	} else {
		var doc any
		if err := json.Unmarshal([]byte(reg["NETWORK_REGISTRY_JSON"]), &doc); err != nil {
			failures = append(failures, "NETWORK_REGISTRY_JSON of "+sovereignObjectRegistry+" is not JSON")
		} else if !reflect.DeepEqual(network, doc) {
			failures = append(failures, "status.network differs from NETWORK_REGISTRY_JSON of "+sovereignObjectRegistry+
				" revision "+strconv.Quote(reg["NETWORK_REGISTRY_REVISION"]))
		}
		compared["revision"] = reg["NETWORK_REGISTRY_REVISION"]
	}
	ev["object"] = compared
	if len(failures) > 0 {
		return fmt.Errorf("region %s: %s", region.ID, strings.Join(failures, "; "))
	}
	return nil
}

func sovereignObjectHas(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// sovereignObjectStrings returns the string values of a JSON object.
func sovereignObjectStrings(v any) map[string]string {
	out := map[string]string{}
	m, _ := v.(map[string]any)
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}
