// Package clusterwrite decides whether a route of catalyst-api may write an
// object of the cluster directly, and records each direct write it allows.
//
//  1. An object whose desired state Git owns is never written directly: it
//     carries a Flux ownership label or owner reference, or it is of a kind
//     of the intent lock. The answer is 409 managed-by-git with the owner.
//  2. Any other object is written only by break-glass: the owner tier and a
//     reason of at least MinReasonLength characters (BreakGlass, Allow).
//  3. An operation (restart, exec, job-retry, reconcile) needs no reason; the
//     route keeps its own tier gate (Operation).
//
// Rules 2 and 3 record the write the same way. An audit event with phase
// intent must reach the stream before the write, or the answer is 503
// audit-unavailable and nothing is written. An event with phase result
// follows the write.
package clusterwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const (
	// AuditTypeBreakGlass is the audit type of a break-glass write.
	AuditTypeBreakGlass = "break-glass"
	// AuditTypeOperationPrefix + the operation name is the audit type of
	// an operation, for example operation-restart.
	AuditTypeOperationPrefix = "operation-"
	// MinReasonLength is the shortest reason a break-glass write accepts.
	MinReasonLength = 10
)

// lockedKinds are the kinds of the intent lock: intentLock.resources of
// products/catalyst/chart/values.yaml (a test compares the two).
var lockedKinds = map[schema.GroupResource]bool{
	{Group: "apps.openova.io", Resource: "applications"}:               true,
	{Group: "orgs.openova.io", Resource: "organizations"}:              true,
	{Group: "access.openova.io", Resource: "useraccesses"}:             true,
	{Group: "catalyst.openova.io", Resource: "environments"}:           true,
	{Group: "catalyst.openova.io", Resource: "environmentpolicies"}:    true,
	{Group: "catalyst.openova.io", Resource: "blueprints"}:             true,
	{Group: "dr.openova.io", Resource: "continuums"}:                   true,
	{Group: "dr.openova.io", Resource: "cnpgpairs"}:                    true,
	{Group: "source.toolkit.fluxcd.io", Resource: "gitrepositories"}:   true,
	{Group: "source.toolkit.fluxcd.io", Resource: "helmrepositories"}:  true,
	{Group: "source.toolkit.fluxcd.io", Resource: "ocirepositories"}:   true,
	{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}: true,
	{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}:        true,
	{Group: "machines.openova.io", Resource: "machinesources"}:         true,
	{Group: "machines.openova.io", Resource: "maintenancewindows"}:     true,
	{Group: "machines.openova.io", Resource: "patchpolicies"}:          true,
	{Group: "machines.openova.io", Resource: "scanpolicies"}:           true,
	{Group: "machines.openova.io", Resource: "hardeningbaselines"}:     true,
	{Group: "machines.openova.io", Resource: "testscopes"}:             true,
	{Group: "machines.openova.io", Resource: "machineimages"}:          true,
}

// Owner names the owner in Git of an object of the resource gvr. live is
// the object, or nil when it does not exist. owned is false when Git owns
// nothing of it.
func Owner(gvr schema.GroupVersionResource, live *unstructured.Unstructured) (owner string, owned bool) {
	if live != nil {
		labels := live.GetLabels()
		for _, flux := range []struct{ group, kind string }{
			{"kustomize.toolkit.fluxcd.io", "Kustomization"}, {"helm.toolkit.fluxcd.io", "HelmRelease"},
		} {
			if name := labels[flux.group+"/name"]; name != "" {
				return flux.kind + " " + qualified(labels[flux.group+"/namespace"], name), true
			}
		}
		for _, ref := range live.GetOwnerReferences() {
			if strings.Contains(ref.APIVersion, "fluxcd.io") {
				return ref.Kind + " " + qualified(live.GetNamespace(), ref.Name), true
			}
		}
		if strings.EqualFold(labels["app.kubernetes.io/managed-by"], "flux") {
			return "Flux", true
		}
	}
	if lockedKinds[gvr.GroupResource()] {
		return "the intent lock (" + gvr.GroupResource().String() + ")", true
	}
	return "", false
}

func qualified(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// Refuse writes the answer 409 managed-by-git with the owner and a detail.
func Refuse(w http.ResponseWriter, owner, detail string) {
	writeJSON(w, http.StatusConflict, "managed-by-git", owner, detail)
}

// RefuseGitOwned writes the 409 answer and returns true when Git owns the
// object; it writes nothing and returns false otherwise.
func RefuseGitOwned(w http.ResponseWriter, gvr schema.GroupVersionResource, live *unstructured.Unstructured, namespace, name string) bool {
	owner, owned := Owner(gvr, live)
	if owned {
		Refuse(w, owner, fmt.Sprintf("the desired state of %s %s is owned by Git (%s); change it in Git",
			gvr.Resource, qualified(namespace, name), owner))
	}
	return owned
}

// OwnerTier says whether claims carry the owner tier: the tier claim owner
// or the realm role catalyst-owner. Nil claims pass, as at every write gate
// of the resource browser (a request without the auth layer, in tests).
func OwnerTier(claims *auth.Claims) bool {
	return claims == nil || strings.EqualFold(strings.TrimSpace(claims.Tier), "owner") || claims.HasRealmRole("catalyst-owner")
}

// Actor names the caller for an audit event and a log line: the e-mail or
// subject of the claims, else the header X-User-Email, else "operator".
func Actor(claims *auth.Claims, r *http.Request) string {
	if claims != nil {
		if e := strings.TrimSpace(claims.Email); e != "" {
			return e
		}
		if s := strings.TrimSpace(claims.Sub); s != "" {
			return s
		}
	}
	if e := strings.TrimSpace(r.Header.Get("X-User-Email")); e != "" {
		return e
	}
	return "operator"
}

// Write describes one break-glass write: Verb is scale, update, apply or
// delete; Resource is the plural resource name.
type Write struct {
	Cluster, Verb, Resource, Namespace, Name, Reason string
}

// Op describes one operation: Name is restart, exec, job-retry or
// reconcile; Target names what it acts on.
type Op struct {
	Cluster, Name, Target string
}

// Finish records the result of a write: err is its error, or nil.
type Finish func(ctx context.Context, err error)

// BreakGlass applies rule 2. When it returns false it has written the
// answer (403, 400 or 503) and the caller writes nothing. Otherwise the
// caller writes and then calls the Finish. A nil bus records nothing (a
// test harness without an audit bus); main.go always sets one.
func BreakGlass(w http.ResponseWriter, r *http.Request, bus *audit.Bus, wr Write) (Finish, bool) {
	claims := auth.ClaimsFromContext(r.Context())
	reason := strings.TrimSpace(wr.Reason)
	if !OwnerTier(claims) {
		writeJSON(w, http.StatusForbidden, "break-glass-owner-only", "", "a direct write of an object that Git does not own needs the owner tier")
		return nil, false
	}
	if len(reason) < MinReasonLength {
		writeJSON(w, http.StatusBadRequest, "break-glass-reason-required", "", fmt.Sprintf(
			"a direct write needs a reason of at least %d characters in the field reason", MinReasonLength))
		return nil, false
	}
	return record(w, r, bus, audit.Event{AuditType: AuditTypeBreakGlass, SovereignID: wr.Cluster,
		Detail: fmt.Sprintf("%s %s %s: %s", wr.Verb, wr.Resource, qualified(wr.Namespace, wr.Name), reason)})
}

// Allow applies rules 1 and 2 to the write of one live object (nil when it
// does not exist yet).
func Allow(w http.ResponseWriter, r *http.Request, bus *audit.Bus, gvr schema.GroupVersionResource, live *unstructured.Unstructured, wr Write) (Finish, bool) {
	if RefuseGitOwned(w, gvr, live, wr.Namespace, wr.Name) {
		return nil, false
	}
	return BreakGlass(w, r, bus, wr)
}

// Operation applies rule 3: it records the intent of the operation and
// returns its Finish. When it returns false it has written the 503 answer
// and the caller acts on nothing. A nil bus records nothing, as above.
func Operation(w http.ResponseWriter, r *http.Request, bus *audit.Bus, op Op) (Finish, bool) {
	return record(w, r, bus, audit.Event{AuditType: AuditTypeOperationPrefix + op.Name, SovereignID: op.Cluster,
		Detail: op.Name + " " + op.Target})
}

// record publishes ev with phase intent and waits for the stream; its
// Finish publishes the event with phase result.
func record(w http.ResponseWriter, r *http.Request, bus *audit.Bus, ev audit.Event) (Finish, bool) {
	if bus == nil {
		return func(context.Context, error) {}, true
	}
	ev.Actor = Actor(auth.ClaimsFromContext(r.Context()), r)
	ev.ID, ev.Phase = natsbus.NewID(), audit.PhaseIntent
	if err := bus.PublishStrict(r.Context(), ev); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, "audit-unavailable", "", err.Error())
		return nil, false
	}
	return func(ctx context.Context, err error) {
		result := ev
		result.ID, result.Phase, result.Result = "", audit.PhaseResult, "ok"
		result.Detail = ev.Detail + " (intent " + ev.ID + ")"
		if err != nil {
			result.Result, result.Detail = "error", result.Detail+": "+err.Error()
		}
		bus.Publish(ctx, result)
	}, true
}

func writeJSON(w http.ResponseWriter, status int, code, owner, detail string) {
	body := map[string]string{"error": code, "code": fmt.Sprint(status), "detail": detail}
	if owner != "" {
		body["owner"] = owner
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
