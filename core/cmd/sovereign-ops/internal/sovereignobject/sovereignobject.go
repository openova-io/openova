// Package sovereignobject is the subcommand sovereign-object of sovereign-ops.
//
// sovereign-object writes the one Sovereign object of the region's cluster,
// sovereigns.catalyst.openova.io/sovereign-self, with server-side apply under
// the field manager sovereign-ops. It runs in every region.
//
// The spec comes from the ConfigMap flux-system/sovereign-variables of the
// region (the variables document and its kit keys), one key per field:
//
//	spec field      key                       written when
//	fqdn            SOVEREIGN_FQDN            always; the run fails without it
//	deploymentId    SOVEREIGN_DEPLOYMENT_ID   the value is 16 hexadecimal digits
//	provider        PROVIDER                  the value is not empty
//	bcpTopology     SOVEREIGN_BCP_TOPOLOGY    the value is a topology of the kind
//	regions         SOVEREIGN_REGIONS_JSON    the value is a JSON list whose every
//	                                          entry has a cloudRegion; code is the
//	                                          cloudRegion, provider the entry's own
//	                                          when it is not empty
//	parentDomains   PARENT_DOMAINS_YAML       every entry has a name and the role
//	                                          primary or org-pool
//
// A field whose key is absent or whose value does not qualify is left out of
// the object, and the evidence names it with the reason. No value is made up.
// The list of providers belongs to the kind, not to this command: a provider
// the kind does not accept fails the write, and the run names the API
// server's message.
//
// status.network is the output network_registry of the provider module,
// copied unchanged from the key NETWORK_REGISTRY_JSON of the ConfigMap
// flux-system/sovereign-network-registry. The parent writes that ConfigMap
// into every region when the region is handed over. While the ConfigMap does
// not exist, status.network is absent, and a run removes a status.network it
// wrote before. A ConfigMap whose document is not a JSON object fails the run
// before anything is written.
//
// The run fails after writing when the cluster holds another Sovereign
// object: there must be exactly one per region cluster.
package sovereignobject

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// The names this subcommand reads and writes.
const (
	Name          = "sovereign-self"
	FieldManager  = "sovereign-ops"
	Namespace     = "flux-system"
	VariablesName = "sovereign-variables"
	RegistryName  = "sovereign-network-registry"
	RegistryKey   = "NETWORK_REGISTRY_JSON"
	RevisionKey   = "NETWORK_REGISTRY_REVISION"
	APIVersion    = "catalyst.openova.io/v1alpha1"
	Kind          = "Sovereign"
	Resource      = "/apis/catalyst.openova.io/v1alpha1/sovereigns"
)

// The values the kind accepts, from products/catalyst/chart/crds/sovereign.yaml.
var (
	topologies = []string{"single-region", "active-hotstandby", "active-active"}
	roles      = []string{"primary", "org-pool"}
	hex16      = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// Cluster is the part of the Kubernetes API this subcommand uses.
type Cluster interface {
	// Get returns one object, or nil and no error when it does not exist.
	Get(ctx context.Context, path string) (map[string]interface{}, error)
	// Apply server-side-applies body at path under FieldManager.
	Apply(ctx context.Context, path string, body []byte) error
}

// Write runs sovereign-object against the region's cluster.
func Write(ctx context.Context, c Cluster) ([]string, error) {
	vars, err := c.Get(ctx, "/api/v1/namespaces/"+Namespace+"/configmaps/"+VariablesName)
	if err != nil {
		return nil, fmt.Errorf("read ConfigMap %s/%s: %w", Namespace, VariablesName, err)
	}
	data := stringData(vars)
	if data["SOVEREIGN_FQDN"] == "" {
		return nil, fmt.Errorf("ConfigMap %s/%s does not exist or has no value SOVEREIGN_FQDN", Namespace, VariablesName)
	}
	spec, absent := Spec(data)

	reg, err := c.Get(ctx, "/api/v1/namespaces/"+Namespace+"/configmaps/"+RegistryName)
	if err != nil {
		return nil, fmt.Errorf("read ConfigMap %s/%s: %w", Namespace, RegistryName, err)
	}
	status := map[string]interface{}{}
	var note string
	if reg == nil {
		note = fmt.Sprintf("status.network absent: ConfigMap %s/%s does not exist", Namespace, RegistryName)
	} else {
		rd := stringData(reg)
		network, err := Network(rd[RegistryKey])
		if err != nil {
			return nil, fmt.Errorf("ConfigMap %s/%s key %s: %w; nothing is written", Namespace, RegistryName, RegistryKey, err)
		}
		status["network"] = network
		regions, _ := network["regions"].([]interface{})
		note = fmt.Sprintf("status.network from ConfigMap %s/%s revision %q: %d region(s)", Namespace, RegistryName, rd[RevisionKey], len(regions))
	}

	obj := map[string]interface{}{"apiVersion": APIVersion, "kind": Kind,
		"metadata": map[string]interface{}{"name": Name, "labels": map[string]interface{}{"app.kubernetes.io/managed-by": FieldManager}},
		"spec":     spec}
	if err := apply(ctx, c, Resource+"/"+Name, obj); err != nil {
		return nil, fmt.Errorf("apply Sovereign %s: %w", Name, err)
	}
	if err := apply(ctx, c, Resource+"/"+Name+"/status", map[string]interface{}{"apiVersion": APIVersion, "kind": Kind,
		"metadata": map[string]interface{}{"name": Name}, "status": status}); err != nil {
		return nil, fmt.Errorf("apply the status of Sovereign %s: %w", Name, err)
	}
	evidence := []string{"applied Sovereign " + Name + " spec: " + strings.Join(written(spec), ", ")}
	for _, a := range absent {
		evidence = append(evidence, "absent from spec: "+a)
	}
	evidence = append(evidence, note)

	list, err := c.Get(ctx, Resource)
	if err != nil {
		return evidence, fmt.Errorf("list Sovereign objects: %w", err)
	}
	var others []string
	items, _ := list["items"].([]interface{})
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		meta, _ := m["metadata"].(map[string]interface{})
		if n, _ := meta["name"].(string); n != Name {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		sort.Strings(others)
		return evidence, fmt.Errorf("the cluster holds other Sovereign objects besides %s: %s; there must be one per region cluster", Name, strings.Join(others, ", "))
	}
	return evidence, nil
}

// Spec returns the spec fields that the variables qualify for, and one line
// per field left out, with the reason.
func Spec(data map[string]string) (map[string]interface{}, []string) {
	spec := map[string]interface{}{"fqdn": data["SOVEREIGN_FQDN"]}
	var absent []string
	miss := func(field, key, why string) {
		if data[key] == "" {
			why = "the key " + key + " is absent or empty"
		}
		absent = append(absent, field+" ("+why+")")
	}
	if v := data["SOVEREIGN_DEPLOYMENT_ID"]; hex16.MatchString(v) {
		spec["deploymentId"] = v
	} else {
		miss("deploymentId", "SOVEREIGN_DEPLOYMENT_ID", fmt.Sprintf("SOVEREIGN_DEPLOYMENT_ID %q is not 16 hexadecimal digits", v))
	}
	if v := data["PROVIDER"]; v != "" {
		spec["provider"] = v
	} else {
		miss("provider", "PROVIDER", "")
	}
	if v := data["SOVEREIGN_BCP_TOPOLOGY"]; contains(topologies, v) {
		spec["bcpTopology"] = v
	} else {
		miss("bcpTopology", "SOVEREIGN_BCP_TOPOLOGY", fmt.Sprintf("SOVEREIGN_BCP_TOPOLOGY %q is not a topology of the kind", v))
	}
	if regions, why := specRegions(data["SOVEREIGN_REGIONS_JSON"]); why == "" {
		spec["regions"] = regions
	} else {
		miss("regions", "SOVEREIGN_REGIONS_JSON", why)
	}
	if domains, why := parentDomains(data["PARENT_DOMAINS_YAML"]); why == "" {
		spec["parentDomains"] = domains
	} else {
		miss("parentDomains", "PARENT_DOMAINS_YAML", why)
	}
	return spec, absent
}

// Network decodes the registry document. Numbers keep their text, so the
// document reaches the object unchanged.
func Network(doc string) (map[string]interface{}, error) {
	if strings.TrimSpace(doc) == "" {
		return nil, errors.New("the key is absent or empty")
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

func specRegions(doc string) ([]interface{}, string) {
	var list []map[string]interface{}
	if err := json.Unmarshal([]byte(doc), &list); err != nil || len(list) == 0 {
		return nil, "SOVEREIGN_REGIONS_JSON is not a JSON list of regions"
	}
	out := make([]interface{}, 0, len(list))
	for i, r := range list {
		code, _ := r["cloudRegion"].(string)
		if code == "" {
			return nil, fmt.Sprintf("entry %d of SOVEREIGN_REGIONS_JSON has no cloudRegion", i)
		}
		entry := map[string]interface{}{"code": code}
		if p, _ := r["provider"].(string); p != "" {
			entry["provider"] = p
		}
		out = append(out, entry)
	}
	return out, ""
}

func parentDomains(doc string) ([]interface{}, string) {
	var list []map[string]interface{}
	if err := yaml.Unmarshal([]byte(doc), &list); err != nil || len(list) == 0 {
		return nil, "PARENT_DOMAINS_YAML is not a list of domains"
	}
	out := make([]interface{}, 0, len(list))
	for i, d := range list {
		name, _ := d["name"].(string)
		role, _ := d["role"].(string)
		if name == "" || !contains(roles, role) {
			return nil, fmt.Sprintf("entry %d of PARENT_DOMAINS_YAML has no name or a role other than primary and org-pool", i)
		}
		out = append(out, map[string]interface{}{"name": name, "role": role})
	}
	return out, ""
}

// written lists the spec fields in a fixed order, with the count of a list.
func written(spec map[string]interface{}) []string {
	var out []string
	for _, f := range []string{"fqdn", "deploymentId", "provider", "bcpTopology", "regions", "parentDomains"} {
		switch v := spec[f].(type) {
		case nil:
		case []interface{}:
			out = append(out, fmt.Sprintf("%s (%d)", f, len(v)))
		default:
			out = append(out, f)
		}
	}
	return out
}

func apply(ctx context.Context, c Cluster, path string, obj map[string]interface{}) error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return err
	}
	return c.Apply(ctx, path, b.Bytes())
}

func stringData(cm map[string]interface{}) map[string]string {
	out := map[string]string{}
	d, _ := cm["data"].(map[string]interface{})
	for k, v := range d {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
