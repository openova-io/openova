// Package capability is the capability model of the install API (docs/adr/0020, S4, S6 and S7).
//
// A capability is a named contract (platform/_capabilities/<name>.yaml, schema
// platform/_schemas/capability.schema.json). A Blueprint offers it with spec.provides and needs it with
// spec.requires. The index is the static part: every contract file and, per capability, the Blueprints of
// the tree that provide it, written by write.sh beside this file into
// internal/catalog/capabilities.json. The registry is the live part: the Blueprint objects of the cluster
// grouped by the capabilities they provide, with the Sovereign's catalogue choices applied. The package
// binding beside it is the install route's use of both.
package capability

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Contract is one version of one capability.
type Contract struct {
	Capability    string         `json:"-"`
	Version       string         `json:"version"`
	Status        string         `json:"status"` // "current" | "deprecated"
	Keys          []Key          `json:"keys"`
	ContextFields []ContextField `json:"contextFields"`
	Probe         string         `json:"probe"`
}

// Key is one key of the binding: one file in the consumer pod.
type Key struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Description string `json:"description"`
}

// ContextField is one field of a Context entry beyond the canonical ones.
type ContextField struct {
	Name        string        `json:"name"`
	Type        string        `json:"type"` // "string" | "integer" | "boolean" | "array"
	Required    bool          `json:"required"`
	Default     interface{}   `json:"default,omitempty"`
	Enum        []interface{} `json:"enum,omitempty"`
	Description string        `json:"description"`
}

// Provider is one Blueprint of the tree that provides a capability, as the index records it.
type Provider struct {
	Blueprint      string `json:"blueprint"`
	Contract       string `json:"contract"`
	Implementation string `json:"implementation"`
}

// Requirement is one entry of a consumer Blueprint's spec.requires.
type Requirement struct {
	Capability string `json:"capability" yaml:"capability"`
	Contract   string `json:"contract" yaml:"contract"`
	Alias      string `json:"alias" yaml:"alias"`
	Optional   bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

// entry is one capability of capabilities.json.
type entry struct {
	Name                  string     `json:"name"`
	Title                 string     `json:"title"`
	DefaultImplementation string     `json:"defaultImplementation"`
	CompositeKind         string     `json:"compositeKind,omitempty"`
	Contracts             []Contract `json:"contracts"`
	Providers             []Provider `json:"providers"`
}

// file is the whole of capabilities.json.
type file struct {
	Comment      string  `json:"comment"`
	Capabilities []entry `json:"capabilities"`
}

// Index is the static capability index loaded from capabilities.json.
type Index interface {
	// Contract returns one version of a capability.
	Contract(capability, version string) (Contract, bool)
	// Current returns the newest version whose status is current.
	Current(capability string) (Contract, bool)
	// Capabilities returns every capability name, sorted.
	Capabilities() []string
	// DefaultImplementation returns the contract file's defaultImplementation; empty for an unknown capability.
	DefaultImplementation(capability string) string
	// Title returns the screen label of a capability; empty for an unknown capability.
	Title(capability string) string
}

type index struct {
	byName map[string]entry
}

// LoadIndex parses the generated capabilities.json. A file without the capabilities list is an error: an
// empty index is written as an empty list, never as a missing one.
func LoadIndex(raw []byte) (Index, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("capability index: %w", err)
	}
	if _, ok := probe["capabilities"]; !ok {
		return nil, errors.New("capability index: no capabilities list")
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("capability index: %w", err)
	}
	idx := &index{byName: map[string]entry{}}
	for _, e := range f.Capabilities {
		if e.Name == "" {
			return nil, errors.New("capability index: a capability has no name")
		}
		if _, dup := idx.byName[e.Name]; dup {
			return nil, fmt.Errorf("capability index: capability %s is listed twice", e.Name)
		}
		for i := range e.Contracts {
			e.Contracts[i].Capability = e.Name
		}
		idx.byName[e.Name] = e
	}
	return idx, nil
}

func (x *index) Contract(capability, version string) (Contract, bool) {
	for _, c := range x.byName[capability].Contracts {
		if c.Version == version {
			return c, true
		}
	}
	return Contract{}, false
}

func (x *index) Current(capability string) (Contract, bool) {
	var best Contract
	found := false
	for _, c := range x.byName[capability].Contracts {
		if c.Status == "current" && (!found || versionNumber(c.Version) > versionNumber(best.Version)) {
			best, found = c, true
		}
	}
	return best, found
}

func (x *index) Capabilities() []string {
	out := make([]string, 0, len(x.byName))
	for n := range x.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (x *index) DefaultImplementation(capability string) string {
	return x.byName[capability].DefaultImplementation
}

func (x *index) Title(capability string) string { return x.byName[capability].Title }

// versionNumber is N of a contract version vN; 0 when it is not of that form.
func versionNumber(v string) int {
	n := 0
	if len(v) < 2 || v[0] != 'v' {
		return 0
	}
	for _, r := range v[1:] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
