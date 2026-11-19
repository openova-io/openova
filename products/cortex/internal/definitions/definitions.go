// Package definitions loads the sources of a cortex installation into the store.
//
// The definitions are the Application's parameters, which the chart writes as one YAML or JSON
// document under the key DataKey of the ConfigMap ConfigMapName in the program's namespace.
// This package reads its list sources; the other keys have other readers. Parse checks the
// whole list and refuses it on the first entry that breaks a rule, so a store never holds half
// of a version. A Loader watches the ConfigMap through an informer and hands every valid
// version to ApplyDefinitions of the store; a version that is refused, a ConfigMap without the
// key and a deleted ConfigMap change nothing, and the sources in use stay.
package definitions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

// ConfigMapName is the default name of the ConfigMap that holds the definitions.
const ConfigMapName = "cortex-definitions"

// DataKey is the key of the ConfigMap's data that holds the definitions document.
const DataKey = "definitions.yaml"

// MaxSources is the largest number of sources an installation defines.
const MaxSources = 500

// The forms of the configuration schema of bp-cortex.
var (
	idForm      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	readerForm  = regexp.MustCompile(`^(user|group|agent):[^\s]{1,200}$|^org:members$`)
	managerForm = regexp.MustCompile(`^(user|group):[^\s]{1,200}$`)
	connectors  = map[string]bool{"upload": true, "git": true, "web": true}
)

type entry struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Connector *struct {
		Type string `json:"type"`
	} `json:"connector"`
	Access *struct {
		Readers  []string `json:"readers"`
		Managers []string `json:"managers"`
	} `json:"access"`
}

// Parse returns the sources of a definitions document. The digest of each is the SHA-256 of
// its entry in canonical JSON (keys sorted, no space), so a change of any field of the entry
// changes it and a change of layout does not.
func Parse(doc []byte) ([]store.SourceDefinition, error) {
	j, err := yaml.YAMLToJSON(doc)
	if err != nil {
		return nil, fmt.Errorf("definitions: not YAML: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(j, &top); err != nil || top == nil {
		return nil, errors.New("definitions: the document is not an object")
	}
	// A missing or null sources is refused: only an explicit empty list means no sources, so a
	// typo or an empty chart value cannot delete every source of the installation.
	s, ok := top["sources"]
	if !ok || string(s) == "null" {
		return nil, errors.New("definitions: the document has no list sources; write sources: [] for none")
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(s, &raws); err != nil {
		return nil, errors.New("definitions: sources is not a list")
	}
	if len(raws) > MaxSources {
		return nil, fmt.Errorf("definitions: %d sources, at most %d", len(raws), MaxSources)
	}
	defs := make([]store.SourceDefinition, 0, len(raws))
	seen := map[string]bool{}
	for i, raw := range raws {
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("definitions: sources[%d] is not a source: %w", i, err)
		}
		if err := check(e); err != nil {
			return nil, fmt.Errorf("definitions: sources[%d] %q: %w", i, e.ID, err)
		}
		if seen[e.ID] {
			return nil, fmt.Errorf("definitions: source %q is defined twice", e.ID)
		}
		seen[e.ID] = true
		var v any
		_ = json.Unmarshal(raw, &v)
		canonical, _ := json.Marshal(v)
		sum := sha256.Sum256(canonical)
		defs = append(defs, store.SourceDefinition{ID: e.ID, Title: e.Title, Digest: hex.EncodeToString(sum[:]),
			Readers: e.Access.Readers, Managers: e.Access.Managers})
	}
	return defs, nil
}

// check applies the rules of one entry that the store relies on.
func check(e entry) error {
	switch n := utf8.RuneCountInString(e.Title); {
	case !idForm.MatchString(e.ID):
		return fmt.Errorf("id does not match %s", idForm)
	case n < 1 || n > 120:
		return errors.New("title must be 1 to 120 characters")
	case e.Connector == nil || !connectors[e.Connector.Type]:
		return errors.New("connector.type must be upload, git or web")
	case e.Access == nil || e.Access.Readers == nil || len(e.Access.Managers) == 0:
		return errors.New("access needs readers and at least one manager")
	}
	for _, p := range e.Access.Readers {
		if !readerForm.MatchString(p) || store.CheckPrincipal(p) != nil {
			return fmt.Errorf("reader %q is not a principal", p)
		}
	}
	for _, p := range e.Access.Managers {
		if !managerForm.MatchString(p) || store.CheckPrincipal(p) != nil {
			return fmt.Errorf("manager %q is not a user or a group", p)
		}
	}
	return nil
}
