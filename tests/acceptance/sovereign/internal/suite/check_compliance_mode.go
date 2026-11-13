package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CMP-G02, the shown mode is the engine's mode. For every policy that
// the policies route lists, the mode in the answer must be the mode that the
// policy object of the engine carries in the first region: the region the API
// runs in. The engine object is the legacy ClusterPolicy of that name, or the
// ValidatingPolicy of that name when there is no ClusterPolicy.
const (
	complianceModeID   = "CMP-G02"
	legacyPolicies     = "clusterpolicies.kyverno.io"
	validatingPolicies = "validatingpolicies.policies.kyverno.io"
	notServed          = "the server doesn't have a resource type" // kubectl says this of a kind the cluster does not serve
	modeEnforce        = "enforce"
	modeAudit          = "audit"
	sourceEvaluator    = "evaluator" // a policy that the API judges itself may have no engine object
)

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

// policyObjects reads the policy objects of one kind, by name. served is false
// when the cluster does not serve the kind.
func (c complianceModeCheck) policyObjects(ctx context.Context, r kube.Region, resource string) (byName map[string]kube.Object, served bool, err error) {
	out, err := c.kubectl(r, ctx, nil, "get", resource, "-o", "json")
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
		byName[o.Name()] = o
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
	var legacy, validating map[string]kube.Object

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
		var servedLegacy, servedValidating bool
		var err error
		if legacy, servedLegacy, err = c.policyObjects(ctx, region, legacyPolicies); err != nil {
			return err
		}
		if validating, servedValidating, err = c.policyObjects(ctx, region, validatingPolicies); err != nil {
			return err
		}
		ev[legacyPolicies], ev[validatingPolicies] = len(legacy), len(validating)
		if !servedLegacy && !servedValidating {
			return errors.New("the cluster serves neither " + legacyPolicies + " nor " + validatingPolicies)
		}
		return nil
	}) {
		return nil
	}
	checkStep(rec, complianceModeID, 3, "every listed policy shows the mode of its engine object", func(ev Evidence) error {
		compared, differ, noObject := 0, []string{}, []string{}
		for _, p := range listed.Items {
			o, found := legacy[p.Name]
			if !found {
				o, found = validating[p.Name]
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
		ev["compared"], ev["differ"], ev["evaluator_policies_without_object"] = compared, len(differ), noObject
		if len(differ) > 0 {
			return fmt.Errorf("%d of %d policies differ: %s", len(differ), len(listed.Items), strings.Join(differ, "; "))
		}
		if compared == 0 {
			return errors.New("nothing was measured: no listed policy has an engine object")
		}
		return nil
	})
	return nil
}
