package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// refusal is a fail or an error of an enforcing rule on one object.
type refusal struct {
	Kind, Namespace, Name, Policy, Rule, Message string
}

func (f refusal) key() string { return key(f.Kind, f.Namespace, f.Name) }

func (f refusal) label() string {
	if f.Rule != "" {
		return f.Policy + " rule " + f.Rule
	}
	return f.Policy
}

type verdict struct {
	enforcing, mutating, exceptions, known int
	refusals                               []refusal
}

// policySet is what the guard gives the CLI: the enforcing and the mutating
// policies, and which rules enforce.
type policySet struct {
	docs []obj
	// legacy: ClusterPolicy or Policy name -> rule name -> the rule validates with Enforce.
	legacy map[string]map[string]bool
	// cel: the ValidatingPolicy and NamespacedValidatingPolicy names whose validationActions hold Deny.
	cel map[string]bool
	// mutators: the MutatingPolicy and NamespacedMutatingPolicy names.
	mutators            map[string]bool
	enforcing, mutating int
}

// readPolicies reads the policies of the render. A policy whose action the
// guard cannot read, or an enforcing kind the CLI cannot judge without a
// registry, stops it.
func readPolicies(objects []obj) (*policySet, error) {
	ps := &policySet{legacy: map[string]map[string]bool{}, cel: map[string]bool{}, mutators: map[string]bool{}}
	for _, o := range objects {
		g, kind, name := group(str(o["apiVersion"])), str(o["kind"]), str(dig(o, "metadata", "name"))
		switch {
		case g == "kyverno.io" && (kind == "ClusterPolicy" || kind == "Policy"):
			if dig(o, "spec", "validationFailureActionOverrides") != nil {
				return nil, fmt.Errorf("%s %s sets validationFailureActionOverrides, which the guard does not read", kind, name)
			}
			spec := strings.ToLower(str(dig(o, "spec", "validationFailureAction")))
			rules, enforces, mutates := map[string]bool{}, false, false
			for _, r := range list(dig(o, "spec", "rules")) {
				rn := str(dig(r, "name"))
				if dig(r, "mutate") != nil {
					mutates = true
				}
				if v, ok := dig(r, "validate").(obj); ok {
					if v["failureActionOverrides"] != nil {
						return nil, fmt.Errorf("%s %s rule %s sets failureActionOverrides, which the guard does not read", kind, name, rn)
					}
					action := strings.ToLower(str(v["failureAction"]))
					if action == "" {
						action = spec
					}
					rules[rn] = action == "enforce"
					enforces = enforces || rules[rn]
				}
				if dig(r, "verifyImages") != nil && spec == "enforce" {
					return nil, fmt.Errorf("%s %s rule %s verifies images with Enforce, which the guard cannot judge without the registry", kind, name, rn)
				}
			}
			if enforces || mutates {
				ps.legacy[name] = rules
				if kind == "Policy" { // the CLI names a namespaced policy <namespace>/<name>
					ps.legacy[str(dig(o, "metadata", "namespace"))+"/"+name] = rules
				}
				ps.docs = append(ps.docs, o)
			}
			if enforces {
				ps.enforcing++
			}
			if mutates {
				ps.mutating++
			}
		case g == "policies.kyverno.io" && (kind == "ValidatingPolicy" || kind == "NamespacedValidatingPolicy"):
			if contains(stringList(dig(o, "spec", "validationActions")), "Deny") {
				ps.cel[name] = true
				ps.docs = append(ps.docs, o)
				ps.enforcing++
			}
		case g == "policies.kyverno.io" && (kind == "ImageValidatingPolicy" || kind == "NamespacedImageValidatingPolicy"):
			if contains(stringList(dig(o, "spec", "validationActions")), "Deny") {
				return nil, fmt.Errorf("%s %s denies, and the guard cannot judge an image verification without the registry", kind, name)
			}
		case g == "policies.kyverno.io" && (kind == "MutatingPolicy" || kind == "NamespacedMutatingPolicy"):
			ps.mutators[name] = true
			ps.docs = append(ps.docs, o)
			ps.mutating++
		}
	}
	if ps.enforcing == 0 {
		return nil, fmt.Errorf("the render holds no enforcing policy: nothing can be judged (is the window closed?)")
	}
	return ps, nil
}

// honoured returns the PolicyExceptions the engine of the render honours: the
// flags --enablePolicyException and --exceptionNamespace of the container of its
// admission controller. Without the first flag the engine honours none; without
// the second, every namespace.
func honoured(objects []obj) ([]obj, error) {
	var args []string
	found := false
	for _, o := range objects {
		if str(o["kind"]) != "Deployment" || !isController(o, "admission-controller") {
			continue
		}
		found = true
		for _, c := range list(dig(o, "spec", "template", "spec", "containers")) {
			args = append(args, stringList(dig(c, "args"))...)
		}
	}
	if !found {
		return nil, fmt.Errorf("the render holds no Kyverno admission controller (a Deployment labelled app.kubernetes.io/part-of=kyverno, app.kubernetes.io/component=admission-controller)")
	}
	enabled, ns, nsSet := false, "", false
	for _, a := range args {
		switch {
		case a == "--enablePolicyException" || a == "--enablePolicyException=true":
			enabled = true
		case strings.HasPrefix(a, "--exceptionNamespace="):
			ns, nsSet = strings.TrimPrefix(a, "--exceptionNamespace="), true
		}
	}
	var out []obj
	for _, o := range objects {
		g := group(str(o["apiVersion"]))
		if str(o["kind"]) != "PolicyException" || (g != "kyverno.io" && g != "policies.kyverno.io") || !enabled {
			continue
		}
		if !nsSet || ns == "" || str(dig(o, "metadata", "namespace")) == ns {
			out = append(out, o)
		}
	}
	return out, nil
}

// isController: a Deployment of the Kyverno engine with that component label.
func isController(o obj, component string) bool {
	return str(dig(o, "metadata", "labels", "app.kubernetes.io/part-of")) == "kyverno" &&
		str(dig(o, "metadata", "labels", "app.kubernetes.io/component")) == component
}

// judge applies the policies of the render to every object of it with the CLI.
// The CLI keeps the objects it is given in one fake cluster, which refuses two
// objects of one identity, so the copies of an identity that a second release
// renders are judged in a run of their own.
func (e *env) judge(objects []obj) (*verdict, error) {
	ps, err := readPolicies(objects)
	if err != nil {
		return nil, err
	}
	exceptions, err := honoured(objects)
	if err != nil {
		return nil, err
	}
	v := &verdict{enforcing: ps.enforcing, mutating: ps.mutating, exceptions: len(exceptions)}
	var layers [][]obj
	seen := map[string]int{}
	for _, o := range objects {
		if str(o["kind"]) == "" {
			continue
		}
		id := str(o["apiVersion"]) + "|" + key(str(o["kind"]), str(dig(o, "metadata", "namespace")), str(dig(o, "metadata", "name")))
		n := seen[id]
		seen[id] = n + 1
		if n == len(layers) {
			layers = append(layers, nil)
		}
		layers[n] = append(layers[n], o)
	}
	for i, layer := range layers {
		results, err := e.apply(i, ps.docs, layer, exceptions)
		if err != nil {
			return nil, err
		}
		if i == 0 && len(results) == 0 {
			return nil, fmt.Errorf("the Kyverno CLI judged nothing: no policy of the render matched an object of it")
		}
		for _, r := range results {
			if f, ok := ps.refusal(r); ok {
				v.refusals = append(v.refusals, f)
			}
		}
	}
	return v, nil
}

// result is one result of the CLI's report.
type result struct {
	Source    string `json:"source"`
	Policy    string `json:"policy"`
	Rule      string `json:"rule"`
	Result    string `json:"result"`
	Message   string `json:"message"`
	Resources []struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"resources"`
}

// refusal: a fail or an error of an enforcing rule. A result of a legacy policy
// (source kyverno) names its rule, an autogen rule with the prefix autogen- or
// autogen-cronjob-; a fail of one of its mutate rules is not a refusal. Only
// denying ValidatingPolicies are given to the CLI. A fail of a policy the guard
// cannot map to one it gave is a refusal.
func (ps *policySet) refusal(r result) (refusal, bool) {
	if (r.Result != "fail" && r.Result != "error") || len(r.Resources) == 0 {
		return refusal{}, false
	}
	enforcing := true
	if rules, ok := ps.legacy[r.Policy]; ok && r.Source == "kyverno" {
		base := strings.TrimPrefix(strings.TrimPrefix(r.Rule, "autogen-cronjob-"), "autogen-")
		enforcing = rules[base]
	} else if ps.mutators[r.Policy] && !ps.cel[r.Policy] {
		enforcing = false
	}
	if !enforcing {
		return refusal{}, false
	}
	res := r.Resources[0]
	return refusal{Kind: res.Kind, Namespace: res.Namespace, Name: res.Name, Policy: r.Policy, Rule: r.Rule, Message: oneLine(r.Message)}, true
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// apply runs the CLI once and returns the results of its report. The CLI
// applies the mutating policies first and validates what they return, as the
// admission chain does; --output keeps the mutated objects off the report.
func (e *env) apply(layer int, policies, objects, exceptions []obj) ([]result, error) {
	dir := filepath.Join(e.work, fmt.Sprintf("judge-%d", layer))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	files := map[string][]obj{"policies.yaml": policies, "resources.yaml": objects, "exceptions.yaml": exceptions}
	for name, docs := range files {
		var b bytes.Buffer
		for _, d := range docs {
			j, err := json.Marshal(d)
			if err != nil {
				return nil, err
			}
			b.WriteString("---\n")
			b.Write(j)
			b.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o600); err != nil {
			return nil, err
		}
	}
	args := []string{"apply", filepath.Join(dir, "policies.yaml"), "--resource", filepath.Join(dir, "resources.yaml"),
		"--policy-report", "--output-format", "json", "--output", filepath.Join(dir, "mutated.yaml"),
		"--remove-color", "--continue-on-fail"}
	if len(exceptions) > 0 {
		args = append(args, "--exception", filepath.Join(dir, "exceptions.yaml"))
	}
	// The CLI exits 1 when a result fails, and writes no report when there is
	// no result at all (exit 0); the report decides. A failed run without a
	// report is an error.
	stdout, runErr := e.cmd(nil, "", nil, e.kyverno, args...)
	i := bytes.LastIndex(stdout, []byte(`{"kind":"ClusterReport"`))
	if i < 0 && runErr == nil {
		return nil, nil
	}
	if i < 0 {
		return nil, fmt.Errorf("the Kyverno CLI wrote no report (run %d of %d objects): %v; its output: %s", layer, len(objects), runErr, firstLine(strings.ReplaceAll(string(stdout), "Warning:", "WARNING")))
	}
	var report struct {
		Results []result `json:"results"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout[i:]), &report); err != nil {
		return nil, fmt.Errorf("the report of the Kyverno CLI cannot be read: %v", err)
	}
	return report.Results, nil
}

func stringList(v any) []string {
	var out []string
	for _, x := range list(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
