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
	"fmt"
	"github.com/openova-io/openova/core/controllers/pkg/endpoints"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// ParseRegionFile decodes a multi-document region file back into objects
// (pkg/regionfile is the one definition of the file format).
func ParseRegionFile(data []byte) ([]*unstructured.Unstructured, error) {
	return regionfile.Parse(data)
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
	// AppName / AppNamespace — the Application's identity, the same in every
	// region (the release name is not: it carries a per-region suffix).
	// Rendered as `openova.application: {name, namespace}` so a chart can
	// derive region-independent, per-app names (e.g. a CNPG base name)
	// instead of one fixed default every install in a namespace collides on.
	AppName      string
	AppNamespace string
	// Endpoints — the Application's public endpoints with their resolved
	// hostnames (core/controllers/pkg/endpoints, the same resolver
	// catalyst-api reports from). Rendered as `openova.endpoints` so a chart
	// routes exactly the host the console advertises.
	Endpoints []endpoints.Endpoint
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
	if c.AppName != "" {
		ns["application"] = map[string]interface{}{"name": c.AppName, "namespace": c.AppNamespace}
	}
	if len(c.Endpoints) > 0 {
		eps := make([]interface{}, 0, len(c.Endpoints))
		for _, e := range c.Endpoints {
			eps = append(eps, map[string]interface{}{
				"name": e.Name, "hostname": e.Hostname, "port": e.Port, "tls": e.TLS, "launchDefault": e.LaunchDefault,
			})
		}
		ns["endpoints"] = eps
	}
	out[TopologyValuesKey] = ns
	return out
}

// RegionAppPath is the Org-repo path of one Application's file for one
// region: `regions/<region-id>/apps/<app>.yaml`.
func RegionAppPath(region, app string) string {
	return regionfile.AppPath(region, app)
}

// RegionAppsDir is the folder a region's Flux reconciles from the Org repo.
func RegionAppsDir(region string) string {
	return regionfile.AppsDir(region)
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
		b, err := regionfile.Marshal(byRegion[r])
		if err != nil {
			return nil, fmt.Errorf("region files: %w", err)
		}
		out[r] = b
	}
	return out, nil
}
