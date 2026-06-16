// Package handler — catalog_edit_blueprint_yaml.go: #3648 (train/hw150).
//
// The bridge between the catalog-edit wire shape (catalogEdit — the
// commerce store.App subset the UI's CatalogEntryEdit carries) and the
// Blueprint CR YAML that lives in the catalog-sovereign Gitea Org. These
// helpers are PURE (no I/O) so they unit-test in isolation; the git
// read/write lives in catalog_edit_git.go.
//
// Field mapping (catalog edit ⇄ Blueprint CR spec), matching the seed CR
// shape in products/catalyst/chart/templates/catalog-seed/blueprints.yaml
// and the read overlay in catalog_overlay.go:
//
//	edit.Name                → spec.card.title
//	edit.Tagline             → spec.card.summary
//	edit.IconLight           → spec.card.iconLight
//	edit.IconDark            → spec.card.iconDark
//	edit.Icon                → spec.card.icon       (legacy single icon)
//	edit.SupportedTopologies → spec.topology.supported
//
// (Description is carried too for completeness — spec.card.description —
// even though the #3603 edit form doesn't expose it; an out-of-band git
// edit of that field still round-trips through the read path.)

package handler

import (
	"fmt"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
)

// mergeCatalogEditIntoBlueprintYAML applies the edited card fields onto
// an existing Blueprint CR (read-modify-write), or synthesises a minimal
// CR when `existing` is empty/unparseable. Returns the re-serialised
// YAML bytes ready to commit.
//
// The merge is a deep map merge that preserves every field the source CR
// already carries (spec.source, spec.version, spec.manifests, labels …)
// and only sets the edited card keys + supported topologies. A
// non-empty edit field overwrites; an empty edit field is left as-is so
// a partial edit never clobbers a value the admin didn't touch.
func mergeCatalogEditIntoBlueprintYAML(existing []byte, bpName string, edit catalogEdit) ([]byte, error) {
	doc := map[string]interface{}{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := yamlv3.Unmarshal(existing, &doc); err != nil {
			// Corrupt source — start from a fresh minimal CR rather than
			// fail the edit (the edit is the new source of truth anyway).
			doc = map[string]interface{}{}
		}
	}

	// Canonical envelope — set only when absent so an existing CR keeps
	// its own apiVersion/kind/metadata.name.
	setIfAbsent(doc, "apiVersion", "catalyst.openova.io/v1")
	setIfAbsent(doc, "kind", "Blueprint")
	meta := childMap(doc, "metadata")
	if strings.TrimSpace(asString(meta["name"])) == "" {
		meta["name"] = bpName
	}

	spec := childMap(doc, "spec")
	// A Blueprint CR requires spec.version; when synthesising fresh, stamp
	// a placeholder so the projector + validateBlueprintYAML accept it.
	// An existing CR keeps its real version.
	if strings.TrimSpace(asString(spec["version"])) == "" {
		spec["version"] = "0.0.0"
	}
	if strings.TrimSpace(asString(spec["visibility"])) == "" {
		spec["visibility"] = "listed"
	}

	card := childMap(spec, "card")
	setIfNonEmpty(card, "title", edit.Name)
	setIfNonEmpty(card, "summary", edit.Tagline)
	setIfNonEmpty(card, "description", edit.Description)
	setIfNonEmpty(card, "icon", edit.Icon)
	setIfNonEmpty(card, "iconLight", edit.IconLight)
	setIfNonEmpty(card, "iconDark", edit.IconDark)
	// A card needs a title; fall back to the bare slug when neither the
	// edit nor the existing CR carries one (the projector tolerates it,
	// the UI shows the slug).
	if strings.TrimSpace(asString(card["title"])) == "" {
		card["title"] = strings.TrimPrefix(bpName, "bp-")
	}

	if len(edit.SupportedTopologies) > 0 {
		topo := childMap(spec, "topology")
		// Store as a plain []interface{} so yaml emits a clean sequence.
		seq := make([]interface{}, 0, len(edit.SupportedTopologies))
		for _, t := range edit.SupportedTopologies {
			if s := strings.TrimSpace(t); s != "" {
				seq = append(seq, s)
			}
		}
		topo["supported"] = seq
	}

	out, err := yamlv3.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal blueprint.yaml: %w", err)
	}
	return out, nil
}

// catalogEditFromBlueprintYAML extracts the card-overlay fields back out
// of a Blueprint CR so the read path can surface a git-committed edit.
// Returns (edit, true) when the YAML parses as a Blueprint with a spec;
// (zero, false) otherwise. The slug is derived from metadata.name (its
// `bp-` prefix stripped) so it lines up with the store-keyed slugs.
func catalogEditFromBlueprintYAML(bpName string, raw []byte) (catalogEdit, bool) {
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		return catalogEdit{}, false
	}
	spec, _ := doc["spec"].(map[string]interface{})
	if spec == nil {
		return catalogEdit{}, false
	}
	slug := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(bpName)), "bp-")
	if meta, ok := doc["metadata"].(map[string]interface{}); ok {
		if n := strings.TrimSpace(asString(meta["name"])); n != "" {
			slug = strings.TrimPrefix(strings.ToLower(n), "bp-")
		}
	}
	edit := catalogEdit{Slug: slug}
	if card, ok := spec["card"].(map[string]interface{}); ok {
		edit.Name = asString(card["title"])
		edit.Tagline = asString(card["summary"])
		edit.Description = asString(card["description"])
		edit.Icon = asString(card["icon"])
		edit.IconLight = asString(card["iconLight"])
		edit.IconDark = asString(card["iconDark"])
	}
	if topo, ok := spec["topology"].(map[string]interface{}); ok {
		edit.SupportedTopologies = asStringSlice(topo["supported"])
	}
	return edit, true
}

// ── tiny map helpers (yaml.v3 decodes maps as map[string]interface{}) ──

// childMap returns m[key] as a map[string]interface{}, creating + storing
// an empty one when absent or of the wrong type.
func childMap(m map[string]interface{}, key string) map[string]interface{} {
	if existing, ok := m[key].(map[string]interface{}); ok {
		return existing
	}
	child := map[string]interface{}{}
	m[key] = child
	return child
}

func setIfAbsent(m map[string]interface{}, key, val string) {
	if _, ok := m[key]; !ok {
		m[key] = val
	}
}

func setIfNonEmpty(m map[string]interface{}, key, val string) {
	if strings.TrimSpace(val) != "" {
		m[key] = val
	}
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// asStringSlice tolerantly converts a yaml-decoded sequence (which may be
// []interface{} of strings) to []string. Returns nil for any other shape.
func asStringSlice(v interface{}) []string {
	seq, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(seq))
	for _, e := range seq {
		if s, ok := e.(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
