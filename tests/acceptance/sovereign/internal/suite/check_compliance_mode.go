package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CMP-G02, the shown mode is the engine's mode. For every policy that
// the policies route lists, the mode in the answer must be the mode that the
// policy object of the engine carries in the first region: the region the API
// runs in. The engine object is the legacy ClusterPolicy of that name, or the
// ValidatingPolicy of that name when there is no ClusterPolicy, or the
// MutatingPolicy of that name when there is neither. A policy the route names
// <namespace>/<name> (the engine reports a namespaced policy so) is the
// legacy Policy of that name in that namespace, or the
// NamespacedValidatingPolicy, or the NamespacedMutatingPolicy, in that order.
// The other way round, every policy object of the engine in that region must
// be listed (CMP-024): a route that leaves a policy out shows no mode for it,
// and comparing only what the route lists would pass on that missing data.
const (
	complianceModeID   = "CMP-G02"
	legacyPolicies     = "clusterpolicies.kyverno.io"
	validatingPolicies = "validatingpolicies.policies.kyverno.io"
	mutatingPolicies   = "mutatingpolicies.policies.kyverno.io"
	legacyNsPolicies   = "policies.kyverno.io"
	validatingNsPolicy = "namespacedvalidatingpolicies.policies.kyverno.io"
	mutatingNsPolicy   = "namespacedmutatingpolicies.policies.kyverno.io"
	notServed          = "the server doesn't have a resource type" // kubectl says this of a kind the cluster does not serve
	modeEnforce        = "enforce"
	modeAudit          = "audit"
	sourceEvaluator    = "evaluator" // a policy that the API judges itself may have no engine object
)

// namespacedPolicyKinds are read in every namespace; their objects are keyed
// <namespace>/<name>.
var namespacedPolicyKinds = []string{legacyNsPolicies, validatingNsPolicy, mutatingNsPolicy}

type complianceModeCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(complianceModeCheck{kubectl: kube.Region.Run}) }

func (complianceModeCheck) ID() string { return complianceModeID }

// engineMode is the mode that a policy object of the engine carries. A
// ValidatingPolicy enforces when spec.validationActions holds Deny. A legacy
// ClusterPolicy enforces when spec.validationFailureAction is Enforce, or when
// validate.failureAction of one of its rules is. Every other policy audits.
// Upper and lower case are the same.
func engineMode(o kube.Object) string {
	actions, _ := o.Get("spec", "validationActions").([]any)
	for _, a := range actions {
		if s, _ := a.(string); strings.EqualFold(s, "Deny") {
			return modeEnforce
		}
	}
	if strings.EqualFold(o.Str("spec", "validationFailureAction"), "Enforce") {
		return modeEnforce
	}
	for _, rule := range o.Objects("spec", "rules") {
		if strings.EqualFold(rule.Str("validate", "failureAction"), "Enforce") {
			return modeEnforce
		}
	}
	return modeAudit
}

// shownMode turns the mode of the policies route into the words of the engine.
// It is empty for a value that is neither.
func shownMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "enforcing", modeEnforce:
		return modeEnforce
	case "permissive", modeAudit:
		return modeAudit
	}
	return ""
}

// policyObjects reads the policy objects of one kind, by name; the objects of
// a namespaced kind are read in every namespace and keyed <namespace>/<name>.
// served is false when the cluster does not serve the kind.
func (c complianceModeCheck) policyObjects(ctx context.Context, r kube.Region, resource string, namespaced bool) (byName map[string]kube.Object, served bool, err error) {
	args := []string{"get", resource, "-o", "json"}
	if namespaced {
		args = append(args, "-A")
	}
	out, err := c.kubectl(r, ctx, nil, args...)
	if err != nil && strings.Contains(err.Error(), notServed) {
		return nil, false, nil
	}
	var list kube.List
	if err == nil {
		err = json.Unmarshal(out, &list)
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", resource, err)
	}
	byName = map[string]kube.Object{}
	for _, o := range list.Items {
		key := o.Name()
		if namespaced {
			key = o.Namespace() + "/" + key
		}
		byName[key] = o
	}
	return byName, true, nil
}

func (c complianceModeCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	base, err := complianceBase(cc)
	if err != nil {
		return err
	}
	region := cc.Regions[0]
	var listed struct {
		Items []struct{ Name, Mode, Source string } `json:"items"`
	}
	var legacy, validating, mutating map[string]kube.Object
	namespaced := make([]map[string]kube.Object, len(namespacedPolicyKinds))

	if !checkStep(rec, complianceModeID, 1, "the API lists the policies (GET /api/v1/sovereigns/{id}/compliance/policies)", func(ev Evidence) error {
		_, err := cc.API.Get(ctx, base+"/policies", &listed)
		ev["policies"] = len(listed.Items)
		if err == nil && len(listed.Items) == 0 {
			err = errors.New("nothing was measured: the API lists no policy")
		}
		return err
	}) {
		return nil
	}
	if !checkStep(rec, complianceModeID, 2, "the policy objects of the engine are read in region "+region.ID, func(ev Evidence) error {
		var servedLegacy, servedValidating, servedMutating bool
		var err error
		if legacy, servedLegacy, err = c.policyObjects(ctx, region, legacyPolicies, false); err != nil {
			return err
		}
		if validating, servedValidating, err = c.policyObjects(ctx, region, validatingPolicies, false); err != nil {
			return err
		}
		if mutating, servedMutating, err = c.policyObjects(ctx, region, mutatingPolicies, false); err != nil {
			return err
		}
		ev[legacyPolicies], ev[validatingPolicies], ev[mutatingPolicies] = len(legacy), len(validating), len(mutating)
		for i, resource := range namespacedPolicyKinds { // a kind not served holds no object
			if namespaced[i], _, err = c.policyObjects(ctx, region, resource, true); err != nil {
				return err
			}
			ev[resource] = len(namespaced[i])
		}
		if !servedLegacy && !servedValidating && !servedMutating {
			return errors.New("the cluster serves none of " + legacyPolicies + ", " + validatingPolicies + " and " + mutatingPolicies)
		}
		return nil
	}) {
		return nil
	}
	checkStep(rec, complianceModeID, 3, "every listed policy shows the mode of its engine object, and every engine object is listed", func(ev Evidence) error {
		compared, differ, noObject, unlisted := 0, []string{}, []string{}, []string{}
		listedNames := map[string]bool{}
		for _, p := range listed.Items {
			listedNames[p.Name] = true
		}
		for _, byName := range append([]map[string]kube.Object{legacy, validating, mutating}, namespaced...) {
			for name := range byName { // two kinds hold one name during an in-place rewrite: listed once
				if !listedNames[name] && !slices.Contains(unlisted, name) {
					unlisted = append(unlisted, name)
				}
			}
		}
		for _, p := range listed.Items {
			kinds := []map[string]kube.Object{legacy, validating, mutating}
			if strings.Contains(p.Name, "/") { // <namespace>/<name>: no cluster-scoped object has a slash in its name
				kinds = namespaced
			}
			var o kube.Object
			found := false
			for _, byName := range kinds {
				if o, found = byName[p.Name]; found {
					break
				}
			}
			switch {
			case !found && p.Source == sourceEvaluator:
				noObject = append(noObject, p.Name)
			case !found:
				differ = append(differ, p.Name+": the engine has no policy object of that name")
			default:
				compared++
				if carried := engineMode(o); shownMode(p.Mode) != carried {
					differ = append(differ, fmt.Sprintf("%s: the API shows %q and the engine object carries %s", p.Name, p.Mode, carried))
				}
			}
		}
		sort.Strings(differ)
		sort.Strings(noObject)
		sort.Strings(unlisted)
		ev["compared"], ev["differ"], ev["evaluator_policies_without_object"], ev["engine_objects_not_listed"] = compared, len(differ), noObject, unlisted
		var problems []string
		if len(differ) > 0 {
			problems = append(problems, fmt.Sprintf("%d of %d policies differ: %s", len(differ), len(listed.Items), strings.Join(differ, "; ")))
		}
		if len(unlisted) > 0 {
			problems = append(problems, fmt.Sprintf("the API leaves out %d of the engine's policy objects: %s", len(unlisted), strings.Join(unlisted, ", ")))
		}
		if compared == 0 {
			problems = append(problems, "nothing was measured: no listed policy has an engine object")
		}
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		return nil
	})
	return nil
}
