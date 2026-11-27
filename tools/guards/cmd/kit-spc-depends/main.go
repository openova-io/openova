// Command kit-spc-depends holds that every kit release whose chart renders a
// SecretProviderClass waits for the release that installs the kind (SEC-218).
//
// The kind's CRD comes from the Secrets Store CSI driver, the release of chart
// bp-secrets-store-csi (kit slot 15b), whose upstream chart applies it in a
// pre-install hook. A release that renders a SecretProviderClass and installs
// before the driver fails with `no matches for kind "SecretProviderClass"`; in
// a kit without the driver the kind never arrives. On 27 Nov 2026 the
// conformance run of SEC-212 (46000535054) failed that way for 2400 seconds:
// the local pack's kit held slot 13, which renders one, and neither slot 15b
// nor bp-openbao, on which slot 15b depends.
//
// The kits (packs.go). The shared kit, clusters/_template/bootstrap-kit/
// kustomization.yaml, which a pack applies unless it has its own, and the own
// kit of a pack, clusters/_template/providers/<pack>/kit/kustomization.yaml (a
// pack is a folder infra/providers/<pack> with a pack.yaml). Each kit is judged
// at the values of each pack that applies it (a kit that no pack applies, at
// the base values alone), in two region roles: primary, the value set
// tests/fixtures/variables/today.env with the pack's substitutes laid over it,
// and secondary, with tests/fixtures/variables/two-region-secondary.env
// between the two. A pack's substitutes are the literal entries of its
// provider-substitutes.yaml.tftpl (KEY: "value") and of the block
// pack_substitutes of its main.tf (KEY = "value"); a value the pack computes
// keeps the base value.
//
// The render (render.go). Every HelmRelease of a kit's files is substituted as
// Flux's post-build step substitutes the kit (tools/guards/cmd/image-path
// subst); a suspended release installs nothing and is not rendered; the chart
// (a chart of platform/ or products/, by its name) is rendered with its
// dependencies, the release's name, target namespace and values, and the API
// version of the kind declared, so a template that renders the object only
// when the cluster serves the kind renders it. A test hook counts only when the
// release enables its tests and its filters keep it. A release whose chart is
// not in this tree must be named in outsideTree below with the reason it
// renders no SecretProviderClass; an entry that no kit holds is a finding.
//
// The verdict, for each kit, pack and role. Every release that renders a
// SecretProviderClass, other than the driver, is in a kit that holds the
// driver's release not suspended, names it in spec.dependsOn (name, and a
// namespace that defaults to the dependent's own), and is not itself among the
// releases the driver depends on (the edge would be a cycle). In a kit that
// holds the driver, every release the driver depends on, directly or through
// others, is a release of the kit and not suspended: a missing one holds the
// driver back for ever.
//
// Not seen: an object a postRenderer adds, a value of valuesFrom, a
// SecretProviderClass that a controller or a Job writes at run time, and a Pod
// that mounts a SecretProviderClass another release renders (the mount fails
// at run time; the install does not).
//
// Usage, from tools/guards:
//
//	go run ./cmd/kit-spc-depends [-root <tree>]
//
// It needs git, bash, Go, helm and yq (mikefarah v4).
//
// Exit codes: 0 every release that renders the kind waits for the driver in
// every kit; 1 a finding (each names the kit, the pack, the role, the slot file
// and the release); 2 nothing could be judged: a tool is missing, a kit or a
// value set cannot be read, a release cannot be rendered or its chart is not in
// this tree and not named in outsideTree, or no release of any kit renders the
// kind.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

type obj = map[string]any

const (
	// driverChart is the chart of the release that installs the kind.
	driverChart = "bp-secrets-store-csi"
	kindName    = "SecretProviderClass"
	kindGroup   = "secrets-store.csi.x-k8s.io"
)

// outsideTree names, by namespace/name, the kit releases whose chart is not a
// chart of this tree, so the guard cannot render them, each with the reason it
// renders no SecretProviderClass.
var outsideTree = map[string]string{
	"flux-system/local-path": "the upstream local-path provisioner chart from the GitRepository local-path-provisioner (a pack's own kit file local-path.yaml): a storage class, its provisioner and their RBAC, no Secrets Store volume",
}

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("kit-spc-depends", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the tree whose kits are judged (default: the top of the Git work tree)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		say(errw, "kit-spc-depends: usage: kit-spc-depends [-root <tree>]\n")
		return 2
	}
	self, err := gitTop(".")
	if err != nil {
		say(errw, "kit-spc-depends: %v\n", err)
		return 2
	}
	if *root == "" {
		*root = self
	}
	work, err := os.MkdirTemp("", "kit-spc-depends-")
	if err != nil {
		say(errw, "kit-spc-depends: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(work) }()
	units, err := plan(*root, work)
	if err != nil {
		say(errw, "kit-spc-depends: %v\n", err)
		return 2
	}
	e := &env{root: *root, self: self, work: work}
	if err := e.setup(); err != nil {
		say(errw, "kit-spc-depends: %v\n", err)
		return 2
	}
	var findings []string
	users, held := 0, map[string]bool{}
	for _, u := range units {
		rels, err := e.readKit(u.kit, u.vars)
		if err != nil {
			say(errw, "kit-spc-depends: %s: %v\n", u, err)
			return 2
		}
		for _, r := range rels {
			if r.outside {
				held[r.id()] = true
			}
		}
		notes, found, n := verdict(u.String(), rels)
		users += n
		for _, l := range notes {
			say(out, "%s\n", l)
		}
		findings = append(findings, found...)
	}
	for id := range outsideTree {
		if !held[id] {
			findings = append(findings, fmt.Sprintf("kit-spc-depends: STALE outsideTree names %s, which no kit holds (or which is suspended in every unit): delete the entry from main.go", id))
		}
	}
	sort.Strings(findings)
	for _, l := range findings {
		say(out, "%s\n", l)
	}
	say(out, "kit-spc-depends: %d unit(s) (kit, pack, role); %d release(s) render %s across them; %d finding(s).\n", len(units), users, kindName, len(findings))
	if users == 0 {
		say(errw, "kit-spc-depends: no release of any kit renders a %s: the render cannot see the kind, so nothing was judged\n", kindName)
		return 2
	}
	if len(findings) > 0 {
		return 1
	}
	return 0
}

// verdict judges the releases of one kit at one pack's values in one role. It
// returns a line for each release that renders the kind and holds, the
// findings, and the number of releases that render the kind.
func verdict(where string, rels []release) (notes, findings []string, users int) {
	byID := map[string]release{}
	var drivers []release
	for _, r := range rels {
		byID[r.id()] = r
		if r.chart == driverChart && !r.suspended {
			drivers = append(drivers, r)
		}
	}
	bad := func(format string, args ...any) {
		findings = append(findings, "kit-spc-depends: FINDING "+where+": "+fmt.Sprintf(format, args...))
	}
	if len(drivers) > 1 {
		var ids []string
		for _, d := range drivers {
			ids = append(ids, d.id()+" ("+d.file+")")
		}
		bad("%d releases of chart %s: %s; one installs the kind", len(drivers), driverChart, strings.Join(ids, ", "))
		return nil, findings, countUsers(rels)
	}
	// The releases the driver depends on, directly or through others.
	closure := map[string]bool{}
	if len(drivers) == 1 {
		d := drivers[0]
		stack := append([]string(nil), d.deps...)
		via := map[string]string{}
		for _, n := range d.deps {
			via[n] = d.id()
		}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if closure[n] {
				continue
			}
			closure[n] = true
			r, ok := byID[n]
			switch {
			case !ok:
				bad("the driver's release %s (%s) depends on %s (through %s), which the kit does not hold: the driver never installs", d.id(), d.file, n, via[n])
				continue
			case r.suspended:
				bad("the driver's release %s (%s) depends on %s (%s, through %s), which is suspended here: the driver never installs", d.id(), d.file, n, r.file, via[n])
			}
			for _, m := range r.deps {
				if _, seen := via[m]; !seen {
					via[m] = n
				}
				stack = append(stack, m)
			}
		}
	}
	for _, r := range rels {
		if r.suspended || len(r.kinds) == 0 || r.chart == driverChart {
			continue
		}
		users++
		what := fmt.Sprintf("%s (%s, chart %s) renders %s %s", r.id(), r.file, r.chart, kindName, strings.Join(r.kinds, ", "))
		if len(drivers) == 0 {
			bad("%s, and the kit holds no release of chart %s that is not suspended: the kind never arrives", what, driverChart)
			continue
		}
		d := drivers[0]
		if closure[r.id()] {
			bad("%s, and the driver's release %s depends on it: an edge to the driver would be a cycle", what, d.id())
			continue
		}
		if !contains(r.deps, d.id()) {
			bad("%s, and its spec.dependsOn does not name %s (%s), which installs the kind", what, d.id(), d.file)
			continue
		}
		notes = append(notes, fmt.Sprintf("kit-spc-depends: ok %s: %s, and waits for %s", where, what, d.id()))
	}
	return notes, findings, users
}

func countUsers(rels []release) int {
	n := 0
	for _, r := range rels {
		if !r.suspended && len(r.kinds) > 0 && r.chart != driverChart {
			n++
		}
	}
	return n
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
