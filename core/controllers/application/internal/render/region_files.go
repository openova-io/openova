// region_files.go — ADR-0015 D1/D2: the per-region Git files an Application
// placement translates into.
//
// The application-controller is the single writer of placement (D3). It
// renders one HelmRelease per placement leg, then COMMITS them — one file per
// region — into the Org repo at RegionAppPath. Each region's Flux applies its
// own folder from its own Gitea (the per-Org `catalyst-tenant-<slug>-region-apps`
// Kustomization the organization-controller commits for every region). No
// HelmRelease is ever applied by the controller, and no leg is ever applied by
// a region other than its own.

package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// ParseRegionFile decodes a multi-document region file back into objects.
func ParseRegionFile(data []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	var out []*unstructured.Unstructured
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, fmt.Errorf("region file: %w", err)
		}
		if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			continue
		}
		// UnmarshalJSON keeps integers int64 (DeepCopy-safe, and the same
		// types the renderer produced).
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("region file: %w", err)
		}
		out = append(out, u)
	}
}

// TopologyValuesKey is the top-level values key of the ADR-0015 D2 contract.
const TopologyValuesKey = "openova"

// TopologyContract is the ADR-0015 D2 values contract every multi-region
// chart consumes, stated per region:
//
//	openova:
//	  topology:
//	    mode: <canonical mode>          # singleton | active-hot-standby | active-passive | active-active
//	    self: <region id of this file>
//	    primary: <region id of the Primary target>
//	    peers: [<other region ids in the placement>]
type TopologyContract struct {
	Mode    string
	Self    string
	Primary string
	Peers   []string
}

// WithTopologyContract returns a COPY of values carrying
// `openova.topology` = c. Other keys under `openova` are preserved; the input
// map is never mutated (it is the Blueprint-derived values shared by every
// leg). Slices are []interface{} so the result stays DeepCopy-safe inside an
// unstructured object.
func WithTopologyContract(values map[string]interface{}, c TopologyContract) map[string]interface{} {
	out := make(map[string]interface{}, len(values)+1)
	for k, v := range values {
		out[k] = v
	}
	ns := map[string]interface{}{}
	if existing, ok := values[TopologyValuesKey].(map[string]interface{}); ok {
		for k, v := range existing {
			ns[k] = v
		}
	}
	peers := make([]interface{}, 0, len(c.Peers))
	for _, p := range c.Peers {
		peers = append(peers, p)
	}
	ns["topology"] = map[string]interface{}{
		"mode":    c.Mode,
		"self":    c.Self,
		"primary": c.Primary,
		"peers":   peers,
	}
	out[TopologyValuesKey] = ns
	return out
}

// RegionAppPath is the Org-repo path of one Application's file for one
// region: `regions/<region-id>/apps/<app>.yaml`.
func RegionAppPath(region, app string) string {
	return fmt.Sprintf("regions/%s/apps/%s.yaml", region, app)
}

// RegionAppsDir is the folder a region's Flux reconciles from the Org repo.
func RegionAppsDir(region string) string {
	return fmt.Sprintf("regions/%s/apps", region)
}

// RegionFiles groups rendered objects by LabelRegion and serialises each
// region's objects into one multi-document YAML file. Object order inside a
// file follows the input order (callers sort with SortHRsForReconcile first),
// so the bytes are stable across reconciles and PutFile's byte-equality
// short-circuit keeps steady state write-free. An object with no LabelRegion
// is an error — it would have no region to be applied in.
func RegionFiles(objs []*unstructured.Unstructured) (map[string][]byte, error) {
	byRegion := map[string][]*unstructured.Unstructured{}
	for _, o := range objs {
		region := o.GetLabels()[LabelRegion]
		if region == "" {
			return nil, fmt.Errorf("region files: %s %s/%s carries no %s label", o.GetKind(), o.GetNamespace(), o.GetName(), LabelRegion)
		}
		byRegion[region] = append(byRegion[region], o)
	}
	regions := make([]string, 0, len(byRegion))
	for r := range byRegion {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	out := make(map[string][]byte, len(byRegion))
	for _, r := range regions {
		var buf bytes.Buffer
		for i, o := range byRegion[r] {
			b, err := yaml.Marshal(o.Object)
			if err != nil {
				return nil, fmt.Errorf("region files: marshal %s/%s: %w", o.GetNamespace(), o.GetName(), err)
			}
			if i > 0 {
				buf.WriteString("---\n")
			}
			buf.Write(b)
		}
		out[r] = buf.Bytes()
	}
	return out, nil
}
