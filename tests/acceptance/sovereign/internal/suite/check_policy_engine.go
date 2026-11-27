package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CMP-G06, no legacy policy object but the image verifier, and every
// policy ready with results (CMP-087). Each region has four steps of its own:
//
//  1. No ClusterPolicy or Policy of kyverno.io exists but the image verifier,
//     the ClusterPolicy cosign-verified whose every rule only verifies images
//     (the one legacy rule that SEC-272 keeps).
//  2. Every ValidatingPolicy and MutatingPolicy, of the cluster and of every
//     namespace, has status.conditionStatus.ready true, and the image verifier
//     the condition Ready True. A region with no ValidatingPolicy fails.
//  3. The ComplianceModel default holds a check of source kyverno. A check
//     whose every report result is error fails, and so does a region where
//     no check has a result. A check without a result is listed, as the design
//     of CMP-G03 lists a check without a subject: "no subject" when its
//     ValidatingPolicy exists (it matches no object, or judges at admission
//     only), "not installed" when it does not (CMP-G03 judges that agreement).
//  4. The legacy counter of the engine, kyverno_policy_rule_info_total of the
//     admission controller (one series of value 1 per loaded legacy rule),
//     read through the API server with kubectl get --raw, names no policy but
//     the image verifier, and names it when step 1 read it. The metrics must
//     hold kyverno_info, and a series of the counter that cannot be read fails.
//
// The check only reads.
const (
	policyEngineID      = "CMP-G06"
	engineImageVerifier = "cosign-verified"
	engineModel         = "compliancemodels.compliance.openova.io"
	sourceKyvernoCheck  = "kyverno" // the source of a check of the model that a policy of the engine judges
	engineReports       = "policyreports.wgpolicyk8s.io,clusterpolicyreports.wgpolicyk8s.io"
	engineMetricsLabel  = "app.kubernetes.io/name=kyverno-admission-controller"
	engineMetricsPort   = "metrics-port"
	engineInfoMetric    = "kyverno_info"
	engineLegacyMetric  = "kyverno_policy_rule_info_total"
)

// engineLegacySeries is one series of the legacy counter: its policy_name and its value.
var engineLegacySeries = regexp.MustCompile(`^` + engineLegacyMetric + `\{(?:.*,)?policy_name="((?:[^"\\]|\\.)*)"(?:,.*)?\}\s+(\S+)(?:\s+\d+)?$`)

type policyEngineCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(policyEngineCheck{kubectl: kube.Region.Run}) }

func (policyEngineCheck) ID() string { return policyEngineID }

func (c policyEngineCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	step := func(name string, fn func(ev Evidence) error) {
		n++
		checkStep(rec, policyEngineID, n, name, fn)
	}
	for _, r := range cc.Regions {
		var verifier kube.Object              // the image verifier, when step 1 read it
		var validating map[string]kube.Object // the ValidatingPolicy objects, when step 2 read them
		step("no legacy policy object but the image verifier "+engineImageVerifier+" in region "+r.ID, func(ev Evidence) (err error) {
			verifier, err = c.legacy(ctx, r, ev)
			return err
		})
		step("every ValidatingPolicy and MutatingPolicy of region "+r.ID+" is ready", func(ev Evidence) (err error) {
			validating, err = c.ready(ctx, r, verifier, ev)
			return err
		})
		step("every kyverno check of the model of region "+r.ID+" has a report result, or no subject", func(ev Evidence) error {
			return c.results(ctx, r, validating, ev)
		})
		step("the legacy counter of the engine of region "+r.ID+" names no policy but the image verifier", func(ev Evidence) error {
			return c.counter(ctx, r, verifier != nil, ev)
		})
	}
	return nil
}

// getJSON runs kubectl get with args and decodes its output into v.
func (c policyEngineCheck) getJSON(ctx context.Context, r kube.Region, v any, args ...string) error {
	out, err := c.kubectl(r, ctx, nil, append(append([]string{"get"}, args...), "-o", "json")...)
	if err == nil {
		err = json.Unmarshal(out, v)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// verifierProblem says why a ClusterPolicy is not the image verifier; "" when it is.
func verifierProblem(o kube.Object) string {
	rules := o.Objects("spec", "rules")
	switch {
	case o.Name() != engineImageVerifier:
		return "is a legacy policy object"
	case len(rules) == 0:
		return "has no rule"
	}
	for _, rule := range rules {
		_, verifies := rule["verifyImages"]
		_, validates := rule["validate"]
		_, mutates := rule["mutate"]
		_, generates := rule["generate"]
		if !verifies || validates || mutates || generates {
			return "has the rule " + rule.Str("name") + ", which does more than verify images"
		}
	}
	return ""
}

// legacy reads the legacy policy objects of the region and returns the image
// verifier, or nil when the region has none.
func (c policyEngineCheck) legacy(ctx context.Context, r kube.Region, ev Evidence) (kube.Object, error) {
	engine := complianceModeCheck(c)
	cluster, _, err := engine.policyObjects(ctx, r, legacyPolicies, false)
	if err != nil {
		return nil, err
	}
	namespaced, _, err := engine.policyObjects(ctx, r, legacyNsPolicies, true)
	if err != nil {
		return nil, err
	}
	var verifier kube.Object
	refused := []string{}
	for name, o := range cluster {
		if why := verifierProblem(o); why != "" {
			refused = append(refused, "the ClusterPolicy "+name+" "+why)
		} else {
			verifier = o
		}
	}
	for key := range namespaced {
		refused = append(refused, "the Policy "+key+" is a legacy policy object")
	}
	sort.Strings(refused)
	ev[legacyPolicies], ev[legacyNsPolicies], ev["image_verifier"] = len(cluster), len(namespaced), verifier != nil
	if len(refused) > 0 {
		return verifier, errors.New(strings.Join(refused, "; "))
	}
	return verifier, nil
}

// policyReady says why a policy object of the engine is not ready; "" when it
// is. A legacy object is ready by its condition Ready, a new one by
// status.conditionStatus.ready.
func policyReady(o kube.Object, legacy bool) string {
	if legacy {
		if status, reason, message, ok := o.Condition("Ready"); !ok || status != "True" {
			return fmt.Sprintf("has Ready %q (%s: %s)", status, reason, message)
		}
		return ""
	}
	ready, ok := o.Get("status", "conditionStatus", "ready").(bool)
	switch {
	case !ok:
		return "carries no status.conditionStatus.ready"
	case !ready:
		return "is not ready: " + o.Str("status", "conditionStatus", "message")
	}
	return ""
}

// ready reads the ValidatingPolicy and MutatingPolicy objects of the region and
// fails unless each is ready, as is the image verifier when there is one. It
// returns the cluster's ValidatingPolicy objects by name once they were read.
func (c policyEngineCheck) ready(ctx context.Context, r kube.Region, verifier kube.Object, ev Evidence) (map[string]kube.Object, error) {
	engine := complianceModeCheck(c)
	var validating map[string]kube.Object
	notReady := []string{}
	for i, resource := range []string{validatingPolicies, mutatingPolicies, validatingNsPolicy, mutatingNsPolicy} {
		objects, served, err := engine.policyObjects(ctx, r, resource, i >= 2)
		if err != nil {
			return validating, err
		}
		if i == 0 && !served {
			return nil, errors.New("the region does not serve " + validatingPolicies)
		}
		if i == 0 {
			validating = objects
		}
		for key, o := range objects {
			if why := policyReady(o, false); why != "" {
				notReady = append(notReady, resource+" "+key+" "+why)
			}
		}
		ev[resource] = len(objects)
	}
	if verifier != nil {
		if why := policyReady(verifier, true); why != "" {
			notReady = append(notReady, legacyPolicies+" "+verifier.Name()+" "+why)
		}
	}
	sort.Strings(notReady)
	ev["not_ready"] = notReady
	switch {
	case len(notReady) > 0:
		return validating, errors.New(strings.Join(notReady, "; "))
	case len(validating) == 0:
		return validating, errors.New("nothing was measured: the region holds no ValidatingPolicy")
	}
	return validating, nil
}

// results reads the model and the policy reports of the region and judges the
// results of every kyverno check of the model.
func (c policyEngineCheck) results(ctx context.Context, r kube.Region, validating map[string]kube.Object, ev Evidence) error {
	if validating == nil {
		return errors.New("the ValidatingPolicy objects of the region were not read, so no check can be told apart from one that is not installed")
	}
	var model kube.Object
	if err := c.getJSON(ctx, r, &model, engineModel, "default"); err != nil {
		return err
	}
	checks := []string{}
	for _, chk := range model.Objects("spec", "checks") {
		if chk.Str("source") == sourceKyvernoCheck {
			checks = append(checks, chk.Str("id"))
		}
	}
	if len(checks) == 0 {
		return errors.New("nothing was measured: the model holds no check of source " + sourceKyvernoCheck)
	}
	var reports kube.List
	if err := c.getJSON(ctx, r, &reports, engineReports, "-A"); err != nil {
		return err
	}
	evaluated, errored := map[string]int{}, map[string]int{}
	for _, report := range reports.Items {
		for _, res := range report.Objects("results") {
			if strings.EqualFold(res.Str("result"), "error") {
				errored[res.Str("policy")]++
			} else {
				evaluated[res.Str("policy")]++
			}
		}
	}
	sort.Strings(checks)
	withResults, noSubject, notInstalled, onlyErrors := 0, []string{}, []string{}, []string{}
	for _, id := range checks {
		_, installed := validating[id]
		switch {
		case evaluated[id] > 0:
			withResults++
		case errored[id] > 0:
			onlyErrors = append(onlyErrors, fmt.Sprintf("%s (%d results, each error)", id, errored[id]))
		case installed:
			noSubject = append(noSubject, id)
		default:
			notInstalled = append(notInstalled, id)
		}
	}
	ev["reports"], ev["checks"], ev["results_per_check"], ev["error_results_per_check"] = len(reports.Items), len(checks), evaluated, errored
	ev["no_subject"], ev["not_installed"] = noSubject, notInstalled
	switch {
	case len(onlyErrors) > 0:
		return errors.New("the engine evaluated no object for " + strings.Join(onlyErrors, ", "))
	case withResults == 0:
		return fmt.Errorf("nothing was measured: no kyverno check of the model has a result in the %d policy reports", len(reports.Items))
	}
	return nil
}

// legacyRules reads the metrics text: the samples of kyverno_info, and by policy
// name the series of the legacy counter whose value is not 0 (0 is a deleted
// rule). A series of the counter that it cannot read is an error.
func legacyRules(text string) (rules map[string]int, info int, err error) {
	rules = map[string]int{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, engineInfoMetric+"{"), strings.HasPrefix(line, engineInfoMetric+" "):
			info++
		case strings.HasPrefix(line, engineLegacyMetric+"{"), strings.HasPrefix(line, engineLegacyMetric+" "):
			m := engineLegacySeries.FindStringSubmatch(line)
			v := 0.0
			if m != nil {
				v, err = strconv.ParseFloat(m[2], 64)
			}
			if m == nil || err != nil {
				return nil, 0, fmt.Errorf("line %d of the metrics cannot be read: %q", i+1, line)
			}
			if v != 0 {
				rules[strings.ReplaceAll(m[1], `\"`, `"`)]++
			}
		}
	}
	return rules, info, nil
}

// counter reads the metrics of the admission controller of the region and
// judges the legacy counter of the engine.
func (c policyEngineCheck) counter(ctx context.Context, r kube.Region, hasVerifier bool, ev Evidence) error {
	var services kube.List
	if err := c.getJSON(ctx, r, &services, "services", "-A", "-l", engineMetricsLabel); err != nil {
		return err
	}
	found := []string{}
	for _, svc := range services.Items {
		for _, port := range svc.Objects("spec", "ports") {
			if port.Str("name") == engineMetricsPort {
				found = append(found, svc.Namespace()+"/"+svc.Name())
			}
		}
	}
	ev["metrics_services"] = found
	if len(found) != 1 {
		return fmt.Errorf("the region has %d Services %s with a port %s (%s); want one", len(found), engineMetricsLabel, engineMetricsPort, strings.Join(found, ", "))
	}
	ns, name, _ := strings.Cut(found[0], "/")
	path := "/api/v1/namespaces/" + ns + "/services/" + name + ":" + engineMetricsPort + "/proxy/metrics"
	ev["metrics_path"] = path
	out, err := c.kubectl(r, ctx, nil, "get", "--raw", path)
	if err != nil {
		return err
	}
	rules, info, err := legacyRules(string(out))
	if err != nil {
		return err
	}
	others := []string{}
	for policy, n := range rules {
		if policy != engineImageVerifier {
			others = append(others, fmt.Sprintf("%q (%d rules)", policy, n))
		}
	}
	sort.Strings(others)
	ev["kyverno_info_samples"], ev["legacy_rules_by_policy"] = info, rules
	switch {
	case info == 0:
		return fmt.Errorf("nothing was measured: the metrics of %s hold no sample of %s", found[0], engineInfoMetric)
	case len(others) > 0:
		return fmt.Errorf("%s counts loaded rules of the legacy policies %s", engineLegacyMetric, strings.Join(others, ", "))
	case hasVerifier && rules[engineImageVerifier] == 0:
		return fmt.Errorf("the image verifier %s exists and %s counts no rule of it: the counter does not see the legacy objects", engineImageVerifier, engineLegacyMetric)
	}
	return nil
}
