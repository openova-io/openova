package blueprintedit

import (
	"errors"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
)

// FluxPayload returns what the Flux leg commits for whole, a Blueprint document,
// over live, the in-cluster object of that name as the API returns it (nil when
// there is none or it cannot be seen): the Partial of whole when Flux may apply
// it without losing a spec field of live (partialKeeps), else whole, which Flux
// is then the only writer of.
func FluxPayload(whole []byte, live map[string]interface{}) ([]byte, error) {
	if !partialKeeps(live) {
		return whole, nil
	}
	return Partial(whole)
}

// Partial returns what a catalogue edit owns of a Blueprint document:
// apiVersion, kind, metadata.name and each Editable path the document holds.
// Flux applies it server-side over the in-cluster Blueprint, so every other
// field keeps its own writer and an upstream upgrade lands on it.
func Partial(doc []byte) ([]byte, error) {
	var d map[string]interface{}
	if err := yamlv3.Unmarshal(doc, &d); err != nil || d == nil {
		return nil, errors.New("the Blueprint document is not a mapping")
	}
	meta, _ := d["metadata"].(map[string]interface{})
	name, _ := meta["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("the Blueprint document has no metadata.name")
	}
	out := map[string]interface{}{"apiVersion": d["apiVersion"], "kind": d["kind"],
		"metadata": map[string]interface{}{"name": name}}
	for _, e := range Editable {
		keys := strings.Split(e, ".")
		var v interface{} = d
		for _, k := range keys {
			m, _ := v.(map[string]interface{})
			if v = m[k]; v == nil {
				break
			}
		}
		if v == nil {
			continue
		}
		to := out
		for _, k := range keys[:len(keys)-1] {
			if _, ok := to[k].(map[string]interface{}); !ok {
				to[k] = map[string]interface{}{}
			}
			to = to[k].(map[string]interface{})
		}
		to[keys[len(keys)-1]] = v
	}
	return yamlv3.Marshal(out)
}

// fluxOwned reports whether Flux's kustomize-controller owns the fields of
// manager at its next server-side apply: its own, and those of the managers
// its apply takes over (kubectl and before-first-apply).
func fluxOwned(manager string) bool {
	return manager == "kustomize-controller" || manager == "before-first-apply" || strings.HasPrefix(manager, "kubectl")
}

// partialKeeps reports whether Flux may apply Partial over live without losing
// a spec field. A server-side apply removes each field its manager owned before
// and no longer sets, unless another manager owns it too, so each spec field
// outside Editable that Flux owns needs a second owner. A nil live is false:
// a partial object cannot create a Blueprint (the CRD requires spec.version).
func partialKeeps(live map[string]interface{}) bool {
	if live == nil {
		return false
	}
	meta, _ := live["metadata"].(map[string]interface{})
	entries, _ := meta["managedFields"].([]interface{})
	var flux, others []interface{}
	for _, e := range entries {
		m, _ := e.(map[string]interface{})
		fields, _ := m["fieldsV1"].(map[string]interface{})
		spec, ok := fields["f:spec"]
		if manager, _ := m["manager"].(string); ok && fluxOwned(manager) {
			flux = append(flux, spec)
		} else if ok {
			others = append(others, spec)
		}
	}
	for _, f := range flux {
		if !kept([]string{"spec"}, f, others) {
			return false
		}
	}
	return true
}

// kept reports whether every field of set (a fieldsV1 tree at path) outside
// Editable is also in one of others, the trees of the other managers at path.
func kept(path []string, set interface{}, others []interface{}) bool {
	for _, e := range Editable {
		if within(path, strings.Split(e, ".")) {
			return true
		}
	}
	fields, _ := set.(map[string]interface{})
	if len(fields) == 0 {
		return len(others) > 0
	}
	for k, v := range fields {
		var sub []interface{}
		for _, o := range others {
			om, _ := o.(map[string]interface{})
			if ov, ok := om[k]; ok {
				sub = append(sub, ov)
			}
		}
		if !kept(append(path[:len(path):len(path)], strings.TrimPrefix(k, "f:")), v, sub) {
			return false
		}
	}
	return true
}
