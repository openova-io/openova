// Package binding is the install route's side of a capability (docs/adr/0020 S4): which provider
// Application serves each backing element of a consumer, the Context entry that provider carries for the
// consumer, and the binding the consumer gets under spec.parameters.bindings.<alias>.
//
// A backing element that names a capability is checked against the consumer Blueprint's spec.requires and
// resolved by the capability registry before anything is written (Resolve). The install route then takes its
// create or reuse branch with the resolved implementation Blueprint and plans each element's write (Plan); the
// route writes only when the whole backing list is judged. A backing element that names a Blueprint (a depends[]
// entry on a shareable Blueprint) keeps the Context entry it had before capabilities existed.
package binding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// firstEpoch is the epoch of a binding the install route creates. A rotation writes a new epoch; that is the
// secrets area's work, not the install route's.
const firstEpoch = 1

// Path is the OpenBao path of one binding, in the mounted tier of the secrets area:
// mounted/data/<consumer namespace>/shared/binding-<alias>/e<epoch>. One object at that path holds exactly the
// contract keys. The install route computes it; nothing else does.
func Path(consumerNamespace, alias string) string {
	return fmt.Sprintf("mounted/data/%s/shared/binding-%s/e%d", consumerNamespace, alias, firstEpoch)
}

// MountPath is where a consumer container mounts the files of one binding.
func MountPath(alias string) string { return "/bindings/" + alias }

// Refusal is a request the capability path refuses, with the status and error code of the install route.
type Refusal struct {
	Status        int
	Code, Message string
}

// Resolved is one backing element that names a capability, resolved to one implementation.
type Resolved struct {
	capability.Requirement
	Implementation capability.Implementation
	// Context holds the contract's contextFields, defaults filled in.
	Context map[string]interface{}
}

// embeddedIndex is the capability index of this binary, internal/catalog/capabilities.json.
var embeddedIndex = sync.OnceValues(func() (capability.Index, error) {
	return capability.LoadIndex(catalog.CapabilitiesJSON())
})

// Resolve checks a request against the consumer Blueprint's spec.requires and resolves every element of sels
// that names a capability to one implementation. It writes nothing, so a refused request leaves no provider
// Application behind. The result is indexed like sels; an element that names a Blueprint has nil. idx nil
// means the embedded index. The Blueprint objects are listed only when an element names a capability.
//
// Refusals, HTTP 422: requirement-missing (a requires entry that is not optional has no element);
// capability-not-required (the element's alias and capability are not a requires entry);
// implementation-not-offered (the requested or default implementation is not offered on this Sovereign, or it
// satisfies another contract version); implementation-wrong-capability (the requested Blueprint does not
// provide the capability); context-field-invalid. HTTP 500 capability-index-invalid; HTTP 503
// blueprint-unavailable when the Blueprint objects cannot be listed.
func Resolve(ctx context.Context, client dynamic.Interface, idx capability.Index, consumer string, requires []capability.Requirement, sels []instances.BackingSelection) ([]*Resolved, *Refusal) {
	refuse := func(code, format string, args ...interface{}) *Refusal {
		return &Refusal{Status: http.StatusUnprocessableEntity, Code: code, Message: fmt.Sprintf(format, args...)}
	}
	sent := map[string]bool{}
	for _, sel := range sels {
		if sel.Capability != "" {
			sent[sel.Alias] = true
		}
	}
	byAlias := map[string]capability.Requirement{}
	for _, req := range requires {
		byAlias[req.Alias] = req
		if !req.Optional && !sent[req.Alias] {
			return nil, refuse("requirement-missing", "Blueprint %s requires %s %s as %q; the request has no backing element with that capability and alias",
				consumer, req.Capability, req.Contract, req.Alias)
		}
	}
	out := make([]*Resolved, len(sels))
	var reg capability.Registry
	for i, sel := range sels {
		if sel.Capability == "" {
			continue
		}
		req, ok := byAlias[sel.Alias]
		if !ok || req.Capability != sel.Capability {
			return nil, refuse("capability-not-required", "Blueprint %s does not require %s as %q", consumer, sel.Capability, sel.Alias)
		}
		if reg == nil {
			var err error
			if idx == nil {
				if idx, err = embeddedIndex(); err != nil {
					return nil, &Refusal{Status: http.StatusInternalServerError, Code: "capability-index-invalid", Message: err.Error()}
				}
			}
			bps, err := ListBlueprints(ctx, client)
			if err != nil {
				return nil, &Refusal{Status: http.StatusServiceUnavailable, Code: "blueprint-unavailable", Message: err.Error()}
			}
			reg = capability.NewRegistry(idx, bps, nil, capability.NoCloudProfile{})
		}
		impl, err := reg.Resolve(sel.Capability, sel.Implementation)
		if errors.Is(err, capability.ErrWrongCapability) {
			return nil, refuse("implementation-wrong-capability", "%v", err)
		}
		if err != nil {
			return nil, refuse("implementation-not-offered", "%v", err)
		}
		if impl.Contract != req.Contract {
			return nil, refuse("implementation-not-offered", "%s provides %s %s; %s requires %s", impl.Blueprint, sel.Capability, impl.Contract, consumer, req.Contract)
		}
		contract, ok := idx.Contract(sel.Capability, req.Contract)
		if !ok {
			return nil, refuse("implementation-not-offered", "the capability index declares no %s contract %s", sel.Capability, req.Contract)
		}
		values, err := contract.ContextValues(sel.Context)
		if err != nil {
			return nil, refuse("context-field-invalid", "backing[%d]: %v", i, err)
		}
		out[i] = &Resolved{Requirement: req, Implementation: impl, Context: values}
	}
	return out, nil
}

// Provider is the Blueprint that serves a backing element: the implementation its capability resolved to, or
// the Blueprint the element names.
func Provider(r *Resolved, blueprint string) string {
	if r != nil {
		return r.Implementation.Blueprint
	}
	return blueprint
}

// ProviderName is the name of the provider Application the create branch makes: <consumer>-<alias> for a
// capability (the alias is unique per consumer, so two bindings of one capability get two Applications),
// <consumer>-<provider slug> for an element that names a Blueprint.
func ProviderName(r *Resolved, consumerApp, providerSlug string) string {
	if r != nil {
		return consumerApp + "-" + r.Alias
	}
	return consumerApp + "-" + providerSlug
}

// ContextEntry is the Context entry the provider Application carries for the consumer, under
// spec.parameters[<contextSchema.valuesKey>]. For a resolved capability it is the canonical entry: name,
// capability, contract, alias, the consumer and the binding path, then the capability's context fields. For an
// element that names a Blueprint (r nil) it is the entry every shareable chart materializes from (the
// bp-postgres databases[] machinery, generalized by declaration only): name, owner, the consumer, and the
// credential Secret reflected into the consumer namespace.
func ContextEntry(r *Resolved, consumerBlueprint, consumerApp, consumerNS, contextKind string) map[string]interface{} {
	consumerBP := "bp-" + strings.TrimPrefix(consumerBlueprint, "bp-")
	if r == nil {
		return map[string]interface{}{
			"name":     consumerApp,
			"owner":    consumerApp,
			"consumer": map[string]interface{}{"blueprint": consumerBP, "mode": "shared"},
			"reflect": map[string]interface{}{
				"secretName": fmt.Sprintf("%s-%s-credential", consumerApp, contextKind),
				"namespaces": []interface{}{consumerNS},
			},
		}
	}
	e := map[string]interface{}{
		"name":       consumerApp,
		"capability": r.Capability,
		"contract":   r.Contract,
		"alias":      r.Alias,
		"consumer":   map[string]interface{}{"blueprint": consumerBP, "application": consumerApp, "namespace": consumerNS, "mode": "shared"},
		"binding":    map[string]interface{}{"path": Path(consumerNS, r.Alias)},
	}
	for k, v := range r.Context {
		e[k] = v
	}
	return e
}

// Add puts the consumer's binding of a resolved capability into bindings, keyed by its alias; an element
// that names a Blueprint (r nil) adds nothing.
func Add(bindings map[string]interface{}, r *Resolved, consumerNS string) {
	if r == nil {
		return
	}
	bindings[r.Alias] = map[string]interface{}{
		"capability": r.Capability,
		"contract":   r.Contract,
		"path":       Path(consumerNS, r.Alias),
		"mountPath":  MountPath(r.Alias),
	}
}

// Plan holds the writes of one request's backing list. The install route judges every element first and plans
// its write here; it makes the writes (Writes) only after the whole list is judged, so a refused element
// leaves no provider Application and no Context behind. Plan itself writes nothing to the cluster.
type Plan struct {
	writes []PlannedWrite
	// reused maps the namespace/name of each instance the request reuses to the element that reuses it.
	reused map[string]int
}

// PlannedWrite is one write of a backing list: the create of a provider Application (Create true) or the
// update of a reused instance that carries the consumer's new Context. Org is the Organization whose IaC repo
// receives the object.
type PlannedWrite struct {
	Obj            *unstructured.Unstructured
	Namespace, Org string
	Create         bool
}

// Create plans the create of a provider Application in namespace.
func (p *Plan) Create(namespace, org string, obj *unstructured.Unstructured) {
	p.writes = append(p.writes, PlannedWrite{Obj: obj, Namespace: namespace, Org: org, Create: true})
}

// Writes are the planned writes in the order of the backing list.
func (p *Plan) Writes() []PlannedWrite { return p.writes }

// Reuse plans element i's Context entry on target, an existing instance, appended to
// spec.parameters[valuesKey]; kind names the Context in a refusal. The entry is named after the consumer, so
// it refuses, HTTP 409 context-exists, an instance that already declares a Context of that name and an
// instance an earlier element of the request already reuses: their two entries would carry one name.
func (p *Plan) Reuse(i int, target *unstructured.Unstructured, valuesKey, kind string, entry map[string]interface{}) *Refusal {
	name, _ := entry["name"].(string)
	key := target.GetNamespace() + "/" + target.GetName()
	if j, ok := p.reused[key]; ok {
		return &Refusal{Status: http.StatusConflict, Code: "context-exists", Message: fmt.Sprintf(
			"backing[%d] and backing[%d] both reuse instance %q, and the consumer has one Context %s/%s on an instance", j, i, target.GetName(), kind, name)}
	}
	params, _, _ := unstructured.NestedMap(target.Object, "spec", "parameters")
	if params == nil {
		params = map[string]interface{}{}
	}
	entries, _ := params[valuesKey].([]interface{})
	for _, e := range entries {
		if em, ok := e.(map[string]interface{}); ok {
			if n, _ := em["name"].(string); n == name {
				return &Refusal{Status: http.StatusConflict, Code: "context-exists",
					Message: fmt.Sprintf("instance %q already declares Context %s/%s", target.GetName(), kind, name)}
			}
		}
	}
	params[valuesKey] = append(entries, entry)
	if err := unstructured.SetNestedMap(target.Object, params, "spec", "parameters"); err != nil {
		return &Refusal{Status: http.StatusInternalServerError, Code: "context-write-failed", Message: err.Error()}
	}
	if p.reused == nil {
		p.reused = map[string]int{}
	}
	p.reused[key] = i
	p.writes = append(p.writes, PlannedWrite{Obj: target, Namespace: target.GetNamespace(), Org: target.GetNamespace()})
	return nil
}

// Write sets spec.parameters.bindings of a consumer Application object, the reserved key the consumer chart
// reads as .Values.bindings; the install route is its only writer. No bindings writes nothing.
func Write(app map[string]interface{}, bindings map[string]interface{}) error {
	if len(bindings) == 0 {
		return nil
	}
	params, _, err := unstructured.NestedMap(app, "spec", "parameters")
	if err != nil {
		return err
	}
	if params == nil {
		params = map[string]interface{}{}
	}
	params["bindings"] = bindings
	return unstructured.SetNestedMap(app, params, "spec", "parameters")
}

var (
	blueprintsV1       = schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}
	blueprintsV1alpha1 = schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1alpha1", Resource: "blueprints"}
)

// ListBlueprints lists every Blueprint object of the cluster, v1, or v1alpha1 where v1 is not served: the
// input of the capability registry.
func ListBlueprints(ctx context.Context, client dynamic.Interface) ([]*unstructured.Unstructured, error) {
	if client == nil {
		return nil, errors.New("list Blueprint objects: no cluster client")
	}
	list, err := client.Resource(blueprintsV1).List(ctx, metav1.ListOptions{})
	if err != nil && (apierrors.IsNotFound(err) || strings.Contains(err.Error(), "could not find the requested resource")) {
		list, err = client.Resource(blueprintsV1alpha1).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		return nil, fmt.Errorf("list Blueprint objects: %w", err)
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}
