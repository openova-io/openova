// Command kit-policies holds that every workload the kit installs is admitted by
// the policies that enforce after the bootstrap window, and that every kind a
// policy of the kit matches or looks up is granted to the Kyverno controller
// that evaluates it (CMP-067). Three defects passed every other check and were
// refused only on a running Sovereign: the dnsdist probes of bp-powerdns
// (pull request 8788), the agent sidecar of the OpenBao CSI provider
// (8852) and the reports controller without read on PersistentVolumes (8854).
//
// The render (render.go). Every HelmRelease of the kit
// (clusters/_template/bootstrap-kit/kustomization.yaml of -root) is rendered
// the way Flux installs it: the document substituted as Flux's post-build step
// substitutes the kit (tools/guards/cmd/image-path subst) with the value set
// -vars and, laid over it, SOVEREIGN_CONSUME=local, the hand-over value that
// closes the window, so every policy renders with the action it has after the
// window, and QA_FIXTURES_ENABLED=false (afterWindow in render.go); a
// suspended release skipped; the chart rendered with its
// dependencies, upstream sub-charts and their sidecars included, with the
// release's name, target namespace and values (valuesFrom names objects of the
// cluster and is not read); then the release's postRenderers (kustomize) and
// helm-controller's origin labels; the release namespace on an object of a
// namespaced kind without one. Helm 3 renders the hooks apart from the release
// manifest, so a hook gets neither the postRenderers nor the labels, as on a
// cluster; a test hook is kept only when the release enables its tests
// (spec.test.enable) and its spec.test.filters do not leave it out, the only
// case in which helm-controller creates it.
//
// The verdict (judge.go). The policies are those of the render: every
// ClusterPolicy or Policy with a validate rule whose action is Enforce, every
// ValidatingPolicy or NamespacedValidatingPolicy whose validationActions hold
// Deny, and every mutating policy, which runs before them at admission. The
// exceptions are the PolicyExceptions of the render that the engine honours:
// --enablePolicyException and --exceptionNamespace of its admission controller
// in the render. The Kyverno CLI applies them to every object of the render; it
// must be the version of the engine (the appVersion of platform/kyverno/chart),
// another stops the guard. A fail or an error of an enforcing rule is a
// refusal.
//
// The register (gaps.go). known-gaps.json beside this file names the refusals
// that are known: object, policy, reason, the day the row was written and the
// package that retires it. A refusal it names is printed and passes. A row
// whose object the render does not hold, or that its policy does not refuse, is
// a finding: the row is retired by deleting it.
//
// The permissions (rbac.go). For every policy of policies.kyverno.io of the
// render, each kind it matches and each lookup it makes is granted to the
// controller that acts on it, read from the ClusterRoles of the render.
//
// Usage, from tools/guards:
//
//	go run ./cmd/kit-policies [-root <tree>] [-gaps <file>] [-vars <file>]...
//
// It needs git, bash, Go, helm, yq (mikefarah v4), kubectl (its kustomize) and
// the Kyverno CLI (-kyverno).
//
// Exit codes: 0 nothing is refused outside the register, every row of the
// register holds and every permission is granted; 1 a finding (each is named);
// 2 nothing could be judged: a tool is missing or of another version, a release
// cannot be rendered, the register cannot be read, or the render holds no
// enforcing policy, no engine or a policy the guard cannot read.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

type obj = map[string]any

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// gapsFile is the register, relative to the root of a tree.
const gapsFile = "tools/guards/cmd/kit-policies/known-gaps.json"

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("kit-policies", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the tree whose kit is judged (default: the top of the Git work tree)")
	gaps := flags.String("gaps", "", "the register of known refusals (default: "+gapsFile+" of -root)")
	kyverno := flags.String("kyverno", "kyverno", "the Kyverno CLI")
	var vars listFlag
	flags.Var(&vars, "vars", "a KEY=VALUE file of the value set under -root; later files win (default tests/fixtures/variables/today.env)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		say(errw, "kit-policies: usage: kit-policies [-root <tree>] [-gaps <file>] [-vars <file>]... [-kyverno <cli>]\n")
		return 2
	}
	self, err := gitTop(".")
	if err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	if *root == "" {
		*root = self
	}
	if *gaps == "" {
		*gaps = filepath.Join(*root, gapsFile)
	}
	if len(vars) == 0 {
		vars = listFlag{"tests/fixtures/variables/today.env"}
	}
	register, err := readGaps(*gaps)
	if err != nil {
		say(errw, "kit-policies: the register %s: %v\n", *gaps, err)
		return 2
	}
	work, err := os.MkdirTemp("", "kit-policies-")
	if err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(work) }()
	e := &env{root: *root, self: self, work: work, kyverno: *kyverno}
	for _, v := range vars {
		e.vars = append(e.vars, filepath.Join(*root, v))
	}
	if err := e.setup(); err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	releases, err := e.renderKit()
	if err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	var all []obj
	for _, r := range releases {
		all = append(all, r.objects...)
	}
	verdict, err := e.judge(all)
	if err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	perms, err := checkPermissions(all)
	if err != nil {
		say(errw, "kit-policies: %v\n", err)
		return 2
	}
	findings := report(out, releases, verdict, register, perms)
	say(out, "kit-policies: %d release render(s), %d object(s); %d enforcing and %d mutating policies, %d exception(s) honoured; %d refusal(s), %d of them in the register; %d matched kind(s) and %d lookup(s) checked; %d finding(s).\n",
		len(releases), len(all), verdict.enforcing, verdict.mutating, verdict.exceptions, len(verdict.refusals), verdict.known, perms.matched, perms.lookups, findings)
	if findings > 0 {
		return 1
	}
	return 0
}

// report prints every finding and every refusal the register holds, and
// returns the number of findings. It sets verdict.known.
func report(out io.Writer, releases []release, v *verdict, register []gapRow, perms *permissions) int {
	origin := map[string][]string{}
	for _, r := range releases {
		where := fmt.Sprintf("%s (release %s/%s, chart %s)", r.file, r.namespace, r.name, r.chart)
		for _, o := range r.objects {
			k := key(str(o["kind"]), str(dig(o, "metadata", "namespace")), str(dig(o, "metadata", "name")))
			if !contains(origin[k], where) {
				origin[k] = append(origin[k], where)
			}
		}
	}
	var lines []string
	held := make([]bool, len(register))
	for _, f := range v.refusals {
		where := strings.Join(origin[f.key()], "; ")
		if where == "" {
			where = "an object the render does not name"
		}
		row := -1
		for i, g := range register {
			if g.Kind == f.Kind && g.Namespace == f.Namespace && g.Name == f.Name && g.Policy == f.Policy {
				row = i
			}
		}
		if row >= 0 {
			held[row] = true
			v.known++
			say(out, "kit-policies: KNOWN %s %s/%s, refused by %s, in the register (since %s, retired by %s): %s\n",
				f.Kind, f.Namespace, f.Name, f.Policy, register[row].Since, register[row].RetiredBy, where)
			continue
		}
		lines = append(lines, fmt.Sprintf("kit-policies: REFUSED %s %s/%s by %s: %s; rendered by %s", f.Kind, f.Namespace, f.Name, f.label(), f.Message, where))
	}
	for i, g := range register {
		if held[i] {
			continue
		}
		if len(origin[key(g.Kind, g.Namespace, g.Name)]) == 0 {
			lines = append(lines, fmt.Sprintf("kit-policies: GAP ROW %s %s/%s (%s): the render holds no such object; delete the row from %s", g.Kind, g.Namespace, g.Name, g.Policy, gapsFile))
		} else {
			lines = append(lines, fmt.Sprintf("kit-policies: GAP ROW %s %s/%s (%s): the policy admits it; delete the row from %s", g.Kind, g.Namespace, g.Name, g.Policy, gapsFile))
		}
	}
	lines = append(lines, perms.findings...)
	sort.Strings(lines)
	for _, l := range lines {
		say(out, "%s\n", l)
	}
	return len(lines)
}

func gitTop(dir string) (string, error) {
	c := exec.Command("git", "rev-parse", "--show-toplevel")
	c.Dir = dir
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("the top of the Git work tree cannot be read: %v", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func key(kind, namespace, name string) string { return kind + "|" + namespace + "|" + name }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(obj)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

// group is the API group of an apiVersion ("" for the core group).
func group(apiVersion string) string {
	if g, _, ok := strings.Cut(apiVersion, "/"); ok {
		return g
	}
	return ""
}
