package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The tables of platform/kyverno-policies/chart/tests/apicall-rbac-correspondence.sh
// (pull request 8854), which checks the policy chart's default render only;
// this guard applies them to the render of the kit. The sources they were read
// from (Kyverno v1.19.2) are named there.
//
// controllers: the controllers that evaluate a policy kind, so each needs read on
// what a policy of the kind looks up (resource.Get: get, resource.List: list).
var controllers = map[string][]string{
	"ValidatingPolicy": {"admission-controller", "reports-controller"},
	"DeletingPolicy":   {"cleanup-controller"},
}

// matcher is the controller that acts on the kinds a policy matches and the
// verbs it needs; background: only while spec.evaluation.background.enabled is
// not false.
type matcher struct {
	controller string
	verbs      []string
	background bool
}

var read = []string{"get", "list", "watch"}

var matchers = map[string]matcher{
	"ValidatingPolicy":                {"reports-controller", read, true},
	"NamespacedValidatingPolicy":      {"reports-controller", read, true},
	"ImageValidatingPolicy":           {"reports-controller", read, true},
	"NamespacedImageValidatingPolicy": {"reports-controller", read, true},
	"MutatingPolicy":                  {"reports-controller", read, true},
	"NamespacedMutatingPolicy":        {"reports-controller", read, true},
	"GeneratingPolicy":                {"reports-controller", read, false},
	"NamespacedGeneratingPolicy":      {"reports-controller", read, false},
	"DeletingPolicy":                  {"cleanup-controller", []string{"list", "delete"}, false},
	"NamespacedDeletingPolicy":        {"cleanup-controller", []string{"list", "delete"}, false},
}

// autogenKinds compute autogen targets, and autogenDefault are the pod
// controllers autogen targets when a policy names none
// (pkg/cel/policies/*/autogen, pkg/cel/autogen/data.go).
var autogenKinds = map[string]bool{"ValidatingPolicy": true, "NamespacedValidatingPolicy": true, "ImageValidatingPolicy": true,
	"NamespacedImageValidatingPolicy": true, "MutatingPolicy": true, "NamespacedMutatingPolicy": true}

var autogenDefault = []string{"daemonsets", "deployments", "replicasets", "statefulsets", "jobs", "cronjobs"}

// builtinView is the Kubernetes role view, which is not in the render: the rules
// of system:aggregate-to-view (bootstrap policy, get, list and watch), and the
// ClusterRoles of the render that carry its aggregation label.
var builtinView = []rule{
	{groups: []string{""}, resources: []string{"configmaps", "endpoints", "events", "limitranges", "namespaces", "persistentvolumeclaims",
		"pods", "replicationcontrollers", "resourcequotas", "serviceaccounts", "services"}, verbs: read},
	{groups: []string{"apps"}, resources: []string{"controllerrevisions", "daemonsets", "deployments", "replicasets", "statefulsets"}, verbs: read},
	{groups: []string{"autoscaling"}, resources: []string{"horizontalpodautoscalers"}, verbs: read},
	{groups: []string{"batch"}, resources: []string{"cronjobs", "jobs"}, verbs: read},
	{groups: []string{"discovery.k8s.io"}, resources: []string{"endpointslices"}, verbs: read},
	{groups: []string{"networking.k8s.io"}, resources: []string{"ingresses", "networkpolicies"}, verbs: read},
	{groups: []string{"policy"}, resources: []string{"poddisruptionbudgets"}, verbs: read},
}

var viewLabel = map[string]string{"rbac.authorization.k8s.io/aggregate-to-view": "true"}

type rule struct{ groups, resources, verbs []string }

type permissions struct {
	matched, lookups int
	findings         []string
}

var (
	anyLookup = regexp.MustCompile(`resource[.][A-Za-z]+[(]`)
	lookup    = regexp.MustCompile(`resource[.](Get|List)[(]\s*"([^"]*)"\s*,\s*"([^"]*)"`)
)

// checkPermissions: every kind a policy of policies.kyverno.io matches, with
// its autogen targets, is granted to the controller that acts on it, and every
// lookup it makes to the controllers that evaluate it. A grant is a rule of a
// ClusterRole that a ClusterRoleBinding of the render binds to the controller's
// ServiceAccount, with the rules of the ClusterRoles its aggregationRule
// selects; view is the built-in role. A policy of kyverno.io is not judged.
func checkPermissions(objects []obj) (*permissions, error) {
	p := &permissions{}
	grants := map[string][]rule{}
	for _, c := range []string{"admission-controller", "background-controller", "reports-controller", "cleanup-controller"} {
		g, ok := controllerRules(objects, c)
		if ok {
			grants[c] = g
		}
	}
	need := func(policy, ctl, groupName, resource, verb, why string) {
		rules, ok := grants[ctl]
		if !ok {
			p.findings = append(p.findings, fmt.Sprintf("kit-policies: PERMISSION %s: %s, and the render holds no Kyverno %s to grant it", policy, why, ctl))
			return
		}
		if !granted(rules, groupName, resource, verb) {
			p.findings = append(p.findings, fmt.Sprintf("kit-policies: PERMISSION %s: %s; the %s lacks %s on %s/%s (no ClusterRole of the render bound or aggregated to it grants it; the policy is never ready)",
				policy, why, ctl, verb, groupName, resource))
		}
	}
	for _, o := range objects {
		if group(str(o["apiVersion"])) != "policies.kyverno.io" {
			continue
		}
		kind, name := str(o["kind"]), str(dig(o, "metadata", "name"))
		policy := kind + " " + name
		m, isMatcher := matchers[kind]
		mc, hasMatch := dig(o, "spec", "matchConstraints").(obj)
		if hasMatch && !isMatcher {
			p.findings = append(p.findings, fmt.Sprintf("kit-policies: PERMISSION %s: a policy kind the table matchers of this guard does not name", policy))
		}
		scanOff := m.background && dig(o, "spec", "evaluation", "background", "enabled") == false
		if hasMatch && isMatcher && !scanOff {
			targets, err := matchTargets(kind, o, mc)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", policy, err)
			}
			for _, t := range targets {
				p.matched++
				for _, verb := range m.verbs {
					need(policy, m.controller, t[0], t[1], verb, fmt.Sprintf("it matches %s/%s", t[0], t[1]))
				}
			}
		}
		for _, s := range allStrings(o) {
			calls := anyLookup.FindAllString(s, -1)
			found := lookup.FindAllStringSubmatch(s, -1)
			if len(found) != len(calls) {
				p.findings = append(p.findings, fmt.Sprintf("kit-policies: PERMISSION %s: %d lookup(s), %d of them resource.Get or resource.List with a literal apiVersion and resource, so the rest cannot be read: %s", policy, len(calls), len(found), oneLine(s)))
			}
			for _, f := range found {
				p.lookups++
				verb, res := "get", f[3]
				if f[1] == "List" {
					verb = "list"
				}
				gname := ""
				if g, _, ok := strings.Cut(f[2], "/"); ok {
					gname = g
				}
				ctls, ok := controllers[kind]
				if !ok {
					p.findings = append(p.findings, fmt.Sprintf("kit-policies: PERMISSION %s: resource.%s of %s/%s, and the table controllers of this guard does not name %s", policy, f[1], gname, res, kind))
				}
				for _, ctl := range ctls {
					need(policy, ctl, gname, res, verb, fmt.Sprintf("it looks up %s/%s (resource.%s)", gname, res, f[1]))
				}
			}
		}
	}
	if p.matched == 0 {
		return nil, fmt.Errorf("the render holds no kind a policy of policies.kyverno.io matches: the permissions inspect nothing")
	}
	sort.Strings(p.findings)
	return p, nil
}

// matchTargets: group and resource of every resource rule, and the autogen
// targets: CanAutoGen of pkg/cel/autogen (one rule of the core group, v1, pods
// only, no resource names, no excluded rule, no object selector), then the
// policy's pod controllers or the default ones.
func matchTargets(kind string, o, mc obj) ([][2]string, error) {
	var out [][2]string
	rules := list(mc["resourceRules"])
	for _, r := range rules {
		for _, g := range stringList(dig(r, "apiGroups")) {
			for _, res := range stringList(dig(r, "resources")) {
				out = append(out, [2]string{g, res})
			}
		}
	}
	if !autogenKinds[kind] || len(rules) != 1 {
		return out, nil
	}
	r := rules[0]
	g, v, res := stringList(dig(r, "apiGroups")), stringList(dig(r, "apiVersions")), stringList(dig(r, "resources"))
	if len(g) != 1 || g[0] != "" || len(v) != 1 || v[0] != "v1" || len(res) != 1 || res[0] != "pods" ||
		len(list(dig(r, "resourceNames"))) > 0 || len(list(mc["excludeResourceRules"])) > 0 || mc["objectSelector"] != nil {
		return out, nil
	}
	names := autogenDefault
	if c := dig(o, "spec", "autogen", "podControllers", "controllers"); c != nil {
		names = stringList(c)
	}
	for _, n := range names {
		switch {
		case n == "daemonsets" || n == "deployments" || n == "replicasets" || n == "statefulsets":
			out = append(out, [2]string{"apps", n})
		case n == "jobs" || n == "cronjobs":
			out = append(out, [2]string{"batch", n})
		case strings.Count(n, ".") >= 2:
			parts := strings.SplitN(n, ".", 3)
			out = append(out, [2]string{parts[2], parts[0]})
		default:
			return nil, fmt.Errorf("the autogen controller %s resolves only against a cluster; name it as <resource>.<version>.<group>", n)
		}
	}
	return out, nil
}

// controllerRules: the rules granted to the ServiceAccount of a controller's
// Deployment; false when the render holds no such Deployment.
func controllerRules(objects []obj, component string) ([]rule, bool) {
	sa, ns, found := "", "", false
	for _, o := range objects {
		if str(o["kind"]) == "Deployment" && isController(o, component) {
			sa, ns, found = str(dig(o, "spec", "template", "spec", "serviceAccountName")), str(dig(o, "metadata", "namespace")), true
		}
	}
	if !found {
		return nil, false
	}
	if sa == "" {
		sa = "default"
	}
	roles := map[string]obj{}
	for _, o := range objects {
		if str(o["kind"]) == "ClusterRole" {
			roles[str(dig(o, "metadata", "name"))] = o
		}
	}
	var out []rule
	for _, o := range objects {
		if str(o["kind"]) != "ClusterRoleBinding" || str(dig(o, "roleRef", "kind")) != "ClusterRole" {
			continue
		}
		bound := false
		for _, s := range list(o["subjects"]) {
			if str(dig(s, "kind")) == "ServiceAccount" && str(dig(s, "name")) == sa && str(dig(s, "namespace")) == ns {
				bound = true
			}
		}
		if bound {
			out = append(out, roleRules(str(dig(o, "roleRef", "name")), roles)...)
		}
	}
	return out, true
}

// roleRules: the rules of a ClusterRole and of every ClusterRole its
// aggregationRule selects; view is the built-in role; a role the render does
// not hold grants nothing.
func roleRules(name string, roles map[string]obj) []rule {
	var selectors []any
	var out []rule
	if role, ok := roles[name]; ok {
		out = rulesOf(role)
		selectors = list(dig(role, "aggregationRule", "clusterRoleSelectors"))
	} else if name == "view" {
		out = append(out, builtinView...)
		selectors = []any{obj{"matchLabels": toAny(viewLabel)}}
	}
	for _, other := range roles {
		labels := stringMap(dig(other, "metadata", "labels"))
		for _, s := range selectors {
			if selects(s, labels) {
				out = append(out, rulesOf(other)...)
				break
			}
		}
	}
	return out
}

func rulesOf(role obj) []rule {
	var out []rule
	for _, r := range list(role["rules"]) {
		if len(list(dig(r, "resourceNames"))) > 0 {
			continue // a grant on named objects grants no list or watch
		}
		out = append(out, rule{groups: stringList(dig(r, "apiGroups")), resources: stringList(dig(r, "resources")), verbs: stringList(dig(r, "verbs"))})
	}
	return out
}

// selects: a label selector of matchLabels and matchExpressions (In, NotIn,
// Exists, DoesNotExist). An empty selector selects nothing here: an
// aggregationRule with one would select every ClusterRole.
func selects(sel any, labels map[string]string) bool {
	ml, me := stringMap(dig(sel, "matchLabels")), list(dig(sel, "matchExpressions"))
	if len(ml) == 0 && len(me) == 0 {
		return false
	}
	for k, v := range ml {
		if labels[k] != v {
			return false
		}
	}
	for _, x := range me {
		k, op, vals := str(dig(x, "key")), str(dig(x, "operator")), stringList(dig(x, "values"))
		v, has := labels[k]
		switch op {
		case "In":
			if !has || !contains(vals, v) {
				return false
			}
		case "NotIn":
			if has && contains(vals, v) {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func granted(rules []rule, groupName, resource, verb string) bool {
	for _, r := range rules {
		if (contains(r.groups, groupName) || contains(r.groups, "*")) &&
			(contains(r.resources, resource) || contains(r.resources, "*")) &&
			(contains(r.verbs, verb) || contains(r.verbs, "*")) {
			return true
		}
	}
	return false
}

// allStrings: every string of an object, depth first.
func allStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case obj:
		var out []string
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, allStrings(x[k])...)
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, allStrings(e)...)
		}
		return out
	}
	return nil
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	m, _ := v.(obj)
	for k, x := range m {
		if s, ok := x.(string); ok {
			out[k] = s
		}
	}
	return out
}

func toAny(m map[string]string) obj {
	out := obj{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
