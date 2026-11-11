// Package regionfile is the one definition of the ADR-0015 D1 per-region
// Application file and of the two fields a topology transition (D8) owns.
//
//	<org>/catalyst-tenant @ main                          (OrgLayout)
//	└── regions/<region-id>/apps/<app>.yaml   that region's objects (HelmRelease(s)
//	                                           + the Application's Continuum)
//
//	openova/sovereign-regions @ main                       (PlatformLayout)
//	└── clusters/<fqdn>/regions/<region-id>/platform/<app>.yaml
//	      a PLATFORM Application (installed by a bootstrap-kit slot): the
//	      topology-values ConfigMap the slot's HelmRelease merges through
//	      valuesFrom + the Application's Continuum
//
// A platform Application cannot keep its HelmRelease in a Git-delivered region
// file: the platform databases (shared-pg) back the very Gitea that serves
// those files, so their HelmReleases stay in the bootstrap-kit slot — the one
// install path. The only per-region delta, the topology contract, rides in a
// carrier ConfigMap (TopologyValuesLabel) that the slot's HelmRelease reads
// with an optional `valuesFrom` (a fresh Sovereign installs before the file
// exists). Both carriers — a HelmRelease's spec.values and a carrier
// ConfigMap's values document — hold the two transition fields under the same
// value paths, so Continuum moves a platform pair exactly like an Org app.
//
// Two writers touch one file, each owning different fields:
//
//   - the application-controller owns EVERYTHING it renders from
//     Application.spec (chart, values, peers, the Continuum's desired primary);
//   - Continuum owns `openova.topology.primary` and
//     `database.topology.promotionToken` of every topology carrier in the file
//     of a Continuum-governed Application, because a primary move is an
//     ORDERED series of per-region commits (old primary first, then the new
//     one with the CNPG token, then the peers) — never one commit to every
//     region.
//
// Both write with Gitea's compare-and-swap (gitea.PutFileIfMatch), so a
// write based on a stale read loses and is retried; neither can silently
// revert the other.
package regionfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// Layout locates the region files and the lease inside a repository.
//
//   - Root   is the folder everything lives under ("" = the repository root).
//   - Folder is the per-region folder holding the Application files
//     ("" = "apps").
type Layout struct {
	Root   string
	Folder string
}

// OrgLayout is the per-Org repo layout: regions/<r>/apps/<app>.yaml.
var OrgLayout = Layout{}

// PlatformFolder is the per-region folder of platform Applications in the
// Sovereign region repo.
const PlatformFolder = "platform"

// PlatformLayout is the Sovereign region repo layout of platform
// Applications: clusters/<fqdn>/regions/<r>/platform/<app>.yaml. Each region's
// `sovereign-region` Flux Kustomization already applies
// clusters/<fqdn>/regions/<its-own-id>/ from its own Gitea
// (clusters/_template/sovereign-region), so the files need no new Flux object,
// and the lease (clusters/<fqdn>/continuum/…) is outside every applied path.
func PlatformLayout(sovereignFQDN string) Layout {
	return Layout{Root: "clusters/" + sovereignFQDN, Folder: PlatformFolder}
}

func (l Layout) prefix() string {
	if l.Root == "" {
		return ""
	}
	return strings.TrimSuffix(l.Root, "/") + "/"
}

func (l Layout) folder() string {
	if l.Folder == "" {
		return "apps"
	}
	return l.Folder
}

// AppsDir is the folder one region's Flux reconciles.
func (l Layout) AppsDir(region string) string {
	return fmt.Sprintf("%sregions/%s/%s", l.prefix(), region, l.folder())
}

// AppPath is the path of one Application's file for one region.
func (l Layout) AppPath(region, app string) string {
	return fmt.Sprintf("%s/%s.yaml", l.AppsDir(region), app)
}

// LeasePath is the path of a topology-transition lease + record. It lives
// OUTSIDE regions/ so no region's Flux ever applies it.
func (l Layout) LeasePath(group string) string {
	return fmt.Sprintf("%scontinuum/transitions/%s.yaml", l.prefix(), group)
}

// AppPath is the Org-repo path of one Application's file for one region.
func AppPath(region, app string) string { return OrgLayout.AppPath(region, app) }

// AppsDir is the folder a region's Flux reconciles from the Org repo.
func AppsDir(region string) string { return OrgLayout.AppsDir(region) }

// LeasePath is the Org-repo path of a topology-transition lease + record.
func LeasePath(group string) string { return OrgLayout.LeasePath(group) }

// TopologyValuesLabel marks a ConfigMap of a region file as a topology
// carrier: its TopologyValuesKey holds the Helm values (the openova.topology
// contract + database.topology.promotionToken) a bootstrap-kit HelmRelease
// merges through valuesFrom.
const (
	TopologyValuesLabel = "catalyst.openova.io/topology-values"
	TopologyValuesKey   = "values.yaml"
)

// TopologyValuesConfigMapName is the carrier name for a slot HelmRelease —
// the name its `valuesFrom` references.
func TopologyValuesConfigMapName(helmRelease string) string {
	return helmRelease + "-topology"
}

// Parse decodes a multi-document region file into objects.
func Parse(data []byte) ([]*unstructured.Unstructured, error) {
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
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || string(t) == "null" {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("region file: %w", err)
		}
		out = append(out, u)
	}
}

// Marshal serialises objects into one multi-document file, in order. The
// output is byte-stable for equal input (sorted keys), so an unchanged file
// is never re-committed.
func Marshal(objs []*unstructured.Unstructured) ([]byte, error) {
	var buf bytes.Buffer
	for i, o := range objs {
		b, err := yaml.Marshal(o.Object)
		if err != nil {
			return nil, fmt.Errorf("region file: marshal %s/%s: %w", o.GetNamespace(), o.GetName(), err)
		}
		if i > 0 {
			buf.WriteString("---\n")
		}
		buf.Write(b)
	}
	return buf.Bytes(), nil
}

// TransitionFields are the two values a topology transition commits.
type TransitionFields struct {
	// Primary is `openova.topology.primary` — the region the chart treats as
	// the writer. In the old primary's file it moves first (demotion).
	Primary string
	// PromotionToken is `database.topology.promotionToken` — the CNPG
	// demotion token the new primary replays to before promoting.
	PromotionToken string
}

func isHelmRelease(o *unstructured.Unstructured) bool {
	return o.GetKind() == "HelmRelease"
}

func isTopologyConfigMap(o *unstructured.Unstructured) bool {
	return o.GetKind() == "ConfigMap" && o.GetLabels()[TopologyValuesLabel] == "true"
}

// carrierValues returns the Helm values a topology carrier holds (a
// HelmRelease's spec.values, a carrier ConfigMap's values document);
// isCarrier=false for any other object.
func carrierValues(o *unstructured.Unstructured) (map[string]interface{}, bool, error) {
	switch {
	case isHelmRelease(o):
		v, _, _ := unstructured.NestedMap(o.Object, "spec", "values")
		return v, true, nil
	case isTopologyConfigMap(o):
		raw, _, _ := unstructured.NestedString(o.Object, "data", TopologyValuesKey)
		v := map[string]interface{}{}
		if strings.TrimSpace(raw) != "" {
			if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
				return nil, false, fmt.Errorf("region file: ConfigMap %s/%s %s: %w", o.GetNamespace(), o.GetName(), TopologyValuesKey, err)
			}
		}
		return v, true, nil
	}
	return nil, false, nil
}

func setCarrierValues(o *unstructured.Unstructured, v map[string]interface{}) error {
	if isHelmRelease(o) {
		return unstructured.SetNestedMap(o.Object, v, "spec", "values")
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return unstructured.SetNestedField(o.Object, string(b), "data", TopologyValuesKey)
}

// TopologyValuesConfigMap builds a carrier ConfigMap holding values.
func TopologyValuesConfigMap(name, namespace string, labels map[string]string, values map[string]interface{}) (*unstructured.Unstructured, error) {
	cm := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ConfigMap"}}
	cm.SetName(name)
	cm.SetNamespace(namespace)
	l := map[string]string{}
	for k, v := range labels {
		l[k] = v
	}
	l[TopologyValuesLabel] = "true"
	cm.SetLabels(l)
	if err := setCarrierValues(cm, values); err != nil {
		return nil, err
	}
	return cm, nil
}

// CarrierValues returns the Helm values of a topology carrier (nil, false for
// any other object).
func CarrierValues(o *unstructured.Unstructured) (map[string]interface{}, bool, error) {
	return carrierValues(o)
}

// ReadTransitionFields returns the fields carried by the topology carriers of
// a region file. ok=false when the file has no carrier with a topology
// contract. Every carrier of one file must agree; disagreement is an error
// (the file was hand-edited inconsistently).
func ReadTransitionFields(objs []*unstructured.Unstructured) (TransitionFields, bool, error) {
	var out TransitionFields
	found := false
	for _, o := range objs {
		v, isCarrier, err := carrierValues(o)
		if err != nil {
			return TransitionFields{}, false, err
		}
		if !isCarrier {
			continue
		}
		p, ok, _ := unstructured.NestedString(v, "openova", "topology", "primary")
		if !ok {
			continue
		}
		tok, _, _ := unstructured.NestedString(v, "database", "topology", "promotionToken")
		cur := TransitionFields{Primary: p, PromotionToken: tok}
		if found && cur != out {
			return TransitionFields{}, false, fmt.Errorf("region file: topology carriers disagree on the topology primary (%q vs %q)", out.Primary, cur.Primary)
		}
		out, found = cur, true
	}
	return out, found, nil
}

// SetTransitionFields writes f into every topology carrier of objs that
// carries a topology contract. An empty PromotionToken removes the key.
// Returns the number of carriers changed.
func SetTransitionFields(objs []*unstructured.Unstructured, f TransitionFields) (int, error) {
	changed := 0
	for _, o := range objs {
		v, isCarrier, err := carrierValues(o)
		if err != nil {
			return changed, err
		}
		if !isCarrier || v == nil {
			continue
		}
		cur, ok, _ := unstructured.NestedString(v, "openova", "topology", "primary")
		if !ok {
			continue
		}
		curTok, _, _ := unstructured.NestedString(v, "database", "topology", "promotionToken")
		if cur == f.Primary && curTok == f.PromotionToken {
			continue
		}
		if err := unstructured.SetNestedField(v, f.Primary, "openova", "topology", "primary"); err != nil {
			return changed, err
		}
		if f.PromotionToken != "" {
			if err := unstructured.SetNestedField(v, f.PromotionToken, "database", "topology", "promotionToken"); err != nil {
				return changed, err
			}
		} else {
			unstructured.RemoveNestedField(v, "database", "topology", "promotionToken")
		}
		if err := setCarrierValues(o, v); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}

// DeclarePromotionMechanism stamps `database.topology.promotion.mechanism`
// on every HelmRelease carrier of objs whose topology contract is
// multi-region (openova.topology.mode other than singleton) and that does not
// already declare one. The application-controller calls it only for files it
// governs with a Continuum, so the declared actor IS the actor it renders
// (ADR-0015 D8). An operator's explicit choice (e.g. "manual") is kept.
// Topology ConfigMaps (platform pairs) are left alone: their charts carry
// their own promotion wiring. Returns the number of carriers changed.
func DeclarePromotionMechanism(objs []*unstructured.Unstructured, mechanism string) (int, error) {
	changed := 0
	for _, o := range objs {
		if !isHelmRelease(o) {
			continue
		}
		v, _, err := carrierValues(o)
		if err != nil {
			return changed, err
		}
		if v == nil {
			continue
		}
		mode, ok, _ := unstructured.NestedString(v, "openova", "topology", "mode")
		if !ok || mode == "" || mode == "singleton" {
			continue
		}
		if cur, _, _ := unstructured.NestedString(v, "database", "topology", "promotion", "mechanism"); cur != "" {
			continue
		}
		if err := unstructured.SetNestedField(v, mechanism, "database", "topology", "promotion", "mechanism"); err != nil {
			return changed, err
		}
		if err := setCarrierValues(o, v); err != nil {
			return changed, err
		}
		changed++
	}
	return changed, nil
}
