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

// Check CMP-G03, the model is loaded in every region and agrees with the
// policies (CMP-042). The policy chart renders one ComplianceModel object
// named default in every region; catalyst-api scores each region with that
// region's object.
//
//  1. In every region the object default exists and holds at least one check
//     of source kyverno.
//  2. In every region the validating policy objects of the engine (every
//     ValidatingPolicy, and every ClusterPolicy with a validate rule) are by
//     name exactly the kyverno checks of that region's model.
//  3. The policies route of the API lists every kyverno check of the first
//     region's model with source kyverno and the model's weight (the weight of
//     the check, or the weight of its severity), and no kyverno policy with a
//     weight the model does not give: the API scores with that model, not
//     with no model or another one.
//  4. Every kyverno check of the first region's model has at least one result
//     in the policy reports of that region, or is listed as "no subject"; when
//     no check has a result, nothing was measured.
const (
	complianceModelID = "CMP-G03"
	modelResource     = "compliancemodels.compliance.openova.io"
	modelObject       = "default"
	policyReports     = "policyreports.wgpolicyk8s.io,clusterpolicyreports.wgpolicyk8s.io"
	sourceKyverno     = "kyverno"
)

type complianceModelCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(complianceModelCheck{kubectl: kube.Region.Run}) }

func (complianceModelCheck) ID() string { return complianceModelID }

// regionModel is what the check reads of one region's model: its version and
// the weight of every kyverno check.
type regionModel struct {
	version string
	weights map[string]int
}

// readModel reads the object default of the region and the weight of each of
// its kyverno checks.
func (c complianceModelCheck) readModel(ctx context.Context, r kube.Region) (regionModel, error) {
	out, err := c.kubectl(r, ctx, nil, "get", modelResource, modelObject, "-o", "json")
	if err != nil {
		return regionModel{}, fmt.Errorf("%s: %s %s: %w", r.ID, modelResource, modelObject, err)
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return regionModel{}, fmt.Errorf("%s: %s %s: %w", r.ID, modelResource, modelObject, err)
	}
	m := regionModel{version: o.Str("spec", "version"), weights: map[string]int{}}
	for _, chk := range o.Objects("spec", "checks") {
		if chk.Str("source") != sourceKyverno {
			continue
		}
		w, ok := chk.Get("weight").(float64)
		if !ok {
			w, ok = o.Get("spec", "severityWeights", chk.Str("severity")).(float64)
		}
		if !ok {
			return regionModel{}, fmt.Errorf("%s: the check %s has no weight and its severity %q has none in spec.severityWeights", r.ID, chk.Str("id"), chk.Str("severity"))
		}
		m.weights[chk.Str("id")] = int(w)
	}
	if len(m.weights) == 0 {
		return regionModel{}, fmt.Errorf("nothing was measured: the model of %s holds no check of source kyverno", r.ID)
	}
	return m, nil
}

// validating returns the names of the engine's validating policy objects of
// the region: every ValidatingPolicy, and every ClusterPolicy one of whose
// rules validates.
func (c complianceModelCheck) validating(ctx context.Context, r kube.Region) (map[string]bool, error) {
	engine := complianceModeCheck(c)
	legacy, servedLegacy, err := engine.policyObjects(ctx, r, legacyPolicies, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.ID, err)
	}
	vps, servedValidating, err := engine.policyObjects(ctx, r, validatingPolicies, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.ID, err)
	}
	if !servedLegacy && !servedValidating {
		return nil, fmt.Errorf("%s serves neither %s nor %s", r.ID, legacyPolicies, validatingPolicies)
	}
	names := map[string]bool{}
	for n := range vps {
		names[n] = true
	}
	for n, o := range legacy {
		for _, rule := range o.Objects("spec", "rules") {
			if _, ok := rule["validate"]; ok {
				names[n] = true
			}
		}
	}
	return names, nil
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (c complianceModelCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	base, err := complianceBase(cc)
	if err != nil {
		return err
	}
	models := make([]regionModel, len(cc.Regions))
	if !checkStep(rec, complianceModelID, 1, "the ComplianceModel object default exists in every region", func(ev Evidence) error {
		versions := map[string]string{}
		for i, r := range cc.Regions {
			m, err := c.readModel(ctx, r)
			if err != nil {
				return err
			}
			models[i] = m
			versions[r.ID] = m.version
			ev[r.ID+"_kyverno_checks"] = len(m.weights)
		}
		ev["versions"] = versions
		return nil
	}) {
		return nil
	}
	if !checkStep(rec, complianceModelID, 2, "in every region the engine's validating policies are the kyverno checks of the model", func(ev Evidence) error {
		var differ []string
		for i, r := range cc.Regions {
			names, err := c.validating(ctx, r)
			if err != nil {
				return err
			}
			ev[r.ID+"_validating_policies"] = len(names)
			for _, n := range sortedNames(names) {
				if _, ok := models[i].weights[n]; !ok {
					differ = append(differ, r.ID+": the policy "+n+" has no check in the model")
				}
			}
			for _, n := range sortedNames(models[i].weights) {
				if !names[n] {
					differ = append(differ, r.ID+": the check "+n+" has no validating policy object")
				}
			}
		}
		if len(differ) > 0 {
			return errors.New(strings.Join(differ, "; "))
		}
		return nil
	}) {
		return nil
	}
	first := cc.Regions[0]
	if !checkStep(rec, complianceModelID, 3, "the API scores with the model of region "+first.ID+" (GET /api/v1/sovereigns/{id}/compliance/policies)", func(ev Evidence) error {
		var listed struct {
			Items []struct {
				Name   string `json:"name"`
				Weight int    `json:"weight"`
				Source string `json:"source"`
			} `json:"items"`
		}
		if _, err := cc.API.Get(ctx, base+"/policies", &listed); err != nil {
			return err
		}
		byName := map[string]int{}
		var differ []string
		for _, p := range listed.Items {
			if p.Source != sourceKyverno {
				continue
			}
			byName[p.Name] = p.Weight
			if _, ok := models[0].weights[p.Name]; !ok && p.Weight != 0 {
				differ = append(differ, fmt.Sprintf("%s: the API weighs it %d and the model has no such check", p.Name, p.Weight))
			}
		}
		for _, n := range sortedNames(models[0].weights) {
			got, ok := byName[n]
			switch {
			case !ok:
				differ = append(differ, n+": the API does not list it as a kyverno policy")
			case got != models[0].weights[n]:
				differ = append(differ, fmt.Sprintf("%s: the API weighs it %d, the model %d", n, got, models[0].weights[n]))
			}
		}
		ev["compared"], ev["differ"] = len(models[0].weights), len(differ)
		if len(differ) > 0 {
			return errors.New(strings.Join(differ, "; "))
		}
		return nil
	}) {
		return nil
	}
	checkStep(rec, complianceModelID, 4, "every kyverno check of the model has a result in the policy reports of region "+first.ID+", or no subject", func(ev Evidence) error {
		out, err := c.kubectl(first, ctx, nil, "get", policyReports, "-A", "-o", "json")
		var list kube.List
		if err == nil {
			err = json.Unmarshal(out, &list)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", policyReports, err)
		}
		results := map[string]int{}
		for _, report := range list.Items {
			for _, r := range report.Objects("results") {
				results[r.Str("policy")]++
			}
		}
		withResults, noSubject := 0, []string{}
		perCheck := map[string]int{}
		for _, n := range sortedNames(models[0].weights) {
			perCheck[n] = results[n]
			if results[n] == 0 {
				noSubject = append(noSubject, n)
				continue
			}
			withResults++
		}
		ev["results_per_check"], ev["no_subject"], ev["reports"] = perCheck, noSubject, len(list.Items)
		if withResults == 0 {
			return fmt.Errorf("nothing was measured: no kyverno check of the model has a result in the %d policy reports of %s", len(list.Items), first.ID)
		}
		return nil
	})
	return nil
}
