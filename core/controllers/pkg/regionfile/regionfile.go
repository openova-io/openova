// Package regionfile is the one definition of the ADR-0015 D1 per-region
// Application file and of the two fields a topology transition (D8) owns.
//
//	<org>/catalyst-tenant @ main
//	└── regions/<region-id>/apps/<app>.yaml   that region's objects (HelmRelease(s)
//	                                           + the Application's Continuum)
//
// Two writers touch one file, each owning different fields:
//
//   - the application-controller owns EVERYTHING it renders from
//     Application.spec (chart, values, peers, the Continuum's desired primary);
//   - Continuum owns `openova.topology.primary` and
//     `database.topology.promotionToken` of every HelmRelease in the file of a
//     Continuum-governed Application, because a primary move is an ORDERED
//     series of per-region commits (old primary first, then the new one with
//     the CNPG token, then the peers) — never one commit to every region.
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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// AppPath is the Org-repo path of one Application's file for one region.
func AppPath(region, app string) string {
	return fmt.Sprintf("regions/%s/apps/%s.yaml", region, app)
}

// AppsDir is the folder a region's Flux reconciles from the Org repo.
func AppsDir(region string) string {
	return fmt.Sprintf("regions/%s/apps", region)
}

// LeasePath is the Org-repo path of a topology-transition lease + record.
// It lives OUTSIDE regions/ so no region's Flux ever applies it.
func LeasePath(group string) string {
	return fmt.Sprintf("continuum/transitions/%s.yaml", group)
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

// ReadTransitionFields returns the fields carried by the HelmReleases of a
// region file. ok=false when the file has no HelmRelease carrying a topology
// contract. Every HelmRelease of one file must agree; disagreement is an error
// (the file was hand-edited inconsistently).
func ReadTransitionFields(objs []*unstructured.Unstructured) (TransitionFields, bool, error) {
	var out TransitionFields
	found := false
	for _, o := range objs {
		if !isHelmRelease(o) {
			continue
		}
		p, ok, _ := unstructured.NestedString(o.Object, "spec", "values", "openova", "topology", "primary")
		if !ok {
			continue
		}
		tok, _, _ := unstructured.NestedString(o.Object, "spec", "values", "database", "topology", "promotionToken")
		cur := TransitionFields{Primary: p, PromotionToken: tok}
		if found && cur != out {
			return TransitionFields{}, false, fmt.Errorf("region file: HelmReleases disagree on the topology primary (%q vs %q)", out.Primary, cur.Primary)
		}
		out, found = cur, true
	}
	return out, found, nil
}

// SetTransitionFields writes f into every HelmRelease of objs that carries a
// topology contract. An empty PromotionToken removes the key. Returns the
// number of HelmReleases changed.
func SetTransitionFields(objs []*unstructured.Unstructured, f TransitionFields) (int, error) {
	changed := 0
	for _, o := range objs {
		if !isHelmRelease(o) {
			continue
		}
		cur, ok, _ := unstructured.NestedString(o.Object, "spec", "values", "openova", "topology", "primary")
		if !ok {
			continue
		}
		curTok, _, _ := unstructured.NestedString(o.Object, "spec", "values", "database", "topology", "promotionToken")
		if cur == f.Primary && curTok == f.PromotionToken {
			continue
		}
		if err := unstructured.SetNestedField(o.Object, f.Primary, "spec", "values", "openova", "topology", "primary"); err != nil {
			return changed, err
		}
		if f.PromotionToken != "" {
			if err := unstructured.SetNestedField(o.Object, f.PromotionToken, "spec", "values", "database", "topology", "promotionToken"); err != nil {
				return changed, err
			}
		} else {
			unstructured.RemoveNestedField(o.Object, "spec", "values", "database", "topology", "promotionToken")
		}
		changed++
	}
	return changed, nil
}
