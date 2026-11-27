package capability

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Implementation is one Blueprint that provides a capability.
type Implementation struct {
	Blueprint      string
	Title          string
	Contract       string
	Implementation string // "in-cluster" | "cloud-native"
	Lifecycle      string // effective lifecycle: spec.lifecycle, experimental when unset (the definition's default)
	Shareable      bool
	ContextKind    string
	ValuesKey      string
}

// Offer is the effective per-Sovereign choice for one capability.
type Offer struct {
	Capability         string
	Offered            []Implementation
	Default            string // Blueprint name; empty when no offered implementation qualifies
	OrganizationChoice string // "allowed" | "fixed"
}

// Registry is the live view: Blueprints grouped by the capabilities they provide.
type Registry interface {
	// Implementations returns every Blueprint that provides the capability, sorted by name.
	Implementations(capability string) []Implementation
	// Offer applies the default rule, or the Sovereign's entry for the capability when it has one.
	Offer(capability string) Offer
	// Resolve returns the implementation to use. requested may be empty: then the offer's default.
	// Errors: ErrUnknownCapability, ErrNotOffered, ErrWrongCapability, ErrNoImplementation.
	Resolve(capability, requested string) (Implementation, error)
}

// Availability reports whether a cloud-native implementation of a capability can be used on this Sovereign.
type Availability interface {
	Available(capability string) (ok bool, reason string)
}

// NoCloudProfile is the Availability catalyst-api uses until the cloud profile says which composite kinds
// the Sovereign's cloud offers: no cloud-native implementation is available.
type NoCloudProfile struct{}

// Available always answers false.
func (NoCloudProfile) Available(string) (bool, string) { return false, "cloud profile not available" }

var (
	ErrUnknownCapability = errors.New("capability: unknown capability")
	ErrNotOffered        = errors.New("capability: implementation not offered")
	ErrWrongCapability   = errors.New("capability: blueprint does not provide capability")
	ErrNoImplementation  = errors.New("capability: no implementation available")
	// ErrContextField refuses a Context value that the contract's contextFields do not allow.
	ErrContextField = errors.New("capability: context field invalid")
)

// choice is one entry of the Sovereign's catalogue choices (spec.catalog.capabilities[] of the Sovereign
// object).
type choice struct {
	offered            map[string]bool
	def                string
	organizationChoice string
}

type registry struct {
	idx     Index
	byCap   map[string][]Implementation
	choices map[string]choice
	avail   Availability
}

// NewRegistry builds a Registry. blueprints are Blueprint objects (any served version). policy is the
// Sovereign object whose spec.catalog.capabilities[] holds the catalogue choices; nil means the default
// rule for every capability. avail nil means no cloud-native implementation is available. The result is
// immutable; build a new one when the objects change.
func NewRegistry(idx Index, blueprints []*unstructured.Unstructured, policy *unstructured.Unstructured, avail Availability) Registry {
	if avail == nil {
		avail = NoCloudProfile{}
	}
	r := &registry{idx: idx, byCap: map[string][]Implementation{}, choices: map[string]choice{}, avail: avail}
	for _, bp := range blueprints {
		if bp == nil {
			continue
		}
		provides, _, _ := unstructured.NestedSlice(bp.Object, "spec", "provides")
		for _, p := range provides {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			c, _ := pm["capability"].(string)
			if c == "" {
				continue
			}
			r.byCap[c] = append(r.byCap[c], implementationOf(bp, pm))
		}
	}
	for c := range r.byCap {
		sort.Slice(r.byCap[c], func(i, j int) bool { return r.byCap[c][i].Blueprint < r.byCap[c][j].Blueprint })
	}
	if policy != nil {
		items, _, _ := unstructured.NestedSlice(policy.Object, "spec", "catalog", "capabilities")
		for _, it := range items {
			m, ok := it.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			ch := choice{offered: map[string]bool{}, organizationChoice: "allowed"}
			ch.def, _ = m["default"].(string)
			if oc, _ := m["organizationChoice"].(string); oc != "" {
				ch.organizationChoice = oc
			}
			offered, _ := m["offered"].([]interface{})
			for _, o := range offered {
				if s, ok := o.(string); ok {
					ch.offered[s] = true
				}
			}
			r.choices[name] = ch
		}
	}
	return r
}

func implementationOf(bp *unstructured.Unstructured, p map[string]interface{}) Implementation {
	spec, _ := bp.Object["spec"].(map[string]interface{})
	im := Implementation{Blueprint: bp.GetName(), Title: bp.GetName(), Lifecycle: "experimental"}
	im.Contract, _ = p["contract"].(string)
	im.Implementation, _ = p["implementation"].(string)
	if t, _, _ := unstructured.NestedString(spec, "card", "title"); t != "" {
		im.Title = t
	}
	if l, _ := spec["lifecycle"].(string); l != "" {
		im.Lifecycle = l
	}
	im.Shareable, _ = spec["shareable"].(bool)
	im.ContextKind, _, _ = unstructured.NestedString(spec, "contextSchema", "kind")
	im.ValuesKey, _, _ = unstructured.NestedString(spec, "contextSchema", "valuesKey")
	return im
}

// known: the index has a contract file for the capability.
func (r *registry) known(capability string) bool {
	if r.idx == nil {
		return false
	}
	for _, c := range r.idx.Capabilities() {
		if c == capability {
			return true
		}
	}
	return false
}

func (r *registry) Implementations(capability string) []Implementation {
	return append([]Implementation(nil), r.byCap[capability]...)
}

// usable: not deprecated, and available (in-cluster always; cloud-native only when avail says so).
func (r *registry) usable(capability string, im Implementation) bool {
	if im.Lifecycle == "deprecated" {
		return false
	}
	if im.Implementation == "cloud-native" {
		ok, _ := r.avail.Available(capability)
		return ok
	}
	return im.Implementation == "in-cluster"
}

// Offer: offered = every usable provider (narrowed to the Sovereign's offered list when it has an entry for
// the capability). Default = the entry's default when it is offered; else the contract's
// defaultImplementation when it is offered; else the offered in-cluster implementation with the lowest name
// in byte order; else empty.
func (r *registry) Offer(capability string) Offer {
	o := Offer{Capability: capability, OrganizationChoice: "allowed"}
	ch, hasChoice := r.choices[capability]
	for _, im := range r.byCap[capability] {
		if r.usable(capability, im) && (!hasChoice || ch.offered[im.Blueprint]) {
			o.Offered = append(o.Offered, im)
		}
	}
	in := func(name string) bool {
		for _, im := range o.Offered {
			if im.Blueprint == name {
				return true
			}
		}
		return false
	}
	switch {
	case hasChoice && ch.def != "" && in(ch.def):
		o.Default = ch.def
	case r.idx != nil && in(r.idx.DefaultImplementation(capability)):
		o.Default = r.idx.DefaultImplementation(capability)
	default:
		for _, im := range o.Offered { // sorted by name
			if im.Implementation == "in-cluster" {
				o.Default = im.Blueprint
				break
			}
		}
	}
	if hasChoice {
		o.OrganizationChoice = ch.organizationChoice
	}
	return o
}

func (r *registry) Resolve(capability, requested string) (Implementation, error) {
	if !r.known(capability) {
		return Implementation{}, fmt.Errorf("%w: %s", ErrUnknownCapability, capability)
	}
	o := r.Offer(capability)
	if requested == "" {
		requested = o.Default
		if requested == "" {
			return Implementation{}, ErrNoImplementation
		}
	} else {
		provides := false
		for _, im := range r.byCap[capability] {
			provides = provides || im.Blueprint == requested
		}
		if !provides {
			return Implementation{}, fmt.Errorf("%w: %s does not provide %s", ErrWrongCapability, requested, capability)
		}
		if o.OrganizationChoice == "fixed" && requested != o.Default {
			return Implementation{}, fmt.Errorf("%w: %s is fixed to %s on this Sovereign", ErrNotOffered, capability, o.Default)
		}
	}
	for _, im := range o.Offered {
		if im.Blueprint == requested {
			return im, nil
		}
	}
	return Implementation{}, fmt.Errorf("%w: %s for %s", ErrNotOffered, requested, capability)
}

// ContextValues checks the values a caller sent for the contract's contextFields and returns them with the
// defaults of absent fields filled in. Refused (ErrContextField): a field the contract does not declare, a
// value of the wrong type or outside the field's enum, a required field with no value and no default.
func (c Contract) ContextValues(in map[string]interface{}) (map[string]interface{}, error) {
	declared := map[string]ContextField{}
	for _, f := range c.ContextFields {
		declared[f.Name] = f
	}
	names := make([]string, 0, len(in))
	for n := range in {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, ok := declared[n]; !ok {
			return nil, fmt.Errorf("%w: %s %s declares no context field %q", ErrContextField, c.Capability, c.Version, n)
		}
	}
	out := map[string]interface{}{}
	for _, f := range c.ContextFields {
		v, given := in[f.Name]
		if !given && f.Default == nil {
			if f.Required {
				return nil, fmt.Errorf("%w: context field %q is required", ErrContextField, f.Name)
			}
			continue
		}
		if !given {
			v = f.Default
		}
		nv, err := f.check(v)
		if err != nil {
			return nil, err
		}
		out[f.Name] = nv
	}
	return out, nil
}

// check returns v in the form a Kubernetes object carries (an integral JSON number becomes int64), or
// ErrContextField.
func (f ContextField) check(v interface{}) (interface{}, error) {
	bad := fmt.Errorf("%w: context field %q must be of type %s, got %v", ErrContextField, f.Name, f.Type, v)
	switch f.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return nil, bad
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return nil, bad
		}
	case "array":
		if _, ok := v.([]interface{}); !ok {
			return nil, bad
		}
	case "integer":
		switch n := v.(type) {
		case int64:
		case int:
			v = int64(n)
		case float64:
			if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
				return nil, bad
			}
			v = int64(n)
		default:
			return nil, bad
		}
	default:
		return nil, fmt.Errorf("%w: context field %q has an unknown type %q", ErrContextField, f.Name, f.Type)
	}
	if len(f.Enum) == 0 {
		return v, nil
	}
	// The enum of an array field lists the values each item may take.
	items := []interface{}{v}
	if list, ok := v.([]interface{}); ok {
		items = list
	}
	for _, it := range items {
		if !inEnum(f.Enum, it) {
			return nil, fmt.Errorf("%w: context field %q must be one of %v, got %v", ErrContextField, f.Name, f.Enum, it)
		}
	}
	return v, nil
}

// inEnum compares a value with the enum of the index, where JSON gives every number as float64.
func inEnum(enum []interface{}, v interface{}) bool {
	for _, e := range enum {
		switch ev := e.(type) {
		case string:
			if s, ok := v.(string); ok && s == ev {
				return true
			}
		case bool:
			if b, ok := v.(bool); ok && b == ev {
				return true
			}
		case float64:
			switch n := v.(type) {
			case int64:
				if float64(n) == ev {
					return true
				}
			case float64:
				if n == ev {
					return true
				}
			}
		}
	}
	return false
}
