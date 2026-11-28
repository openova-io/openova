// Command kit-birth-namespaces holds that kit slot 08 lists in bp-openbao's
// peerTrust.birthNamespaces exactly the namespaces of the kit's shared mounted
// values (SEC-237). A secrets-lib value with delivery mounted and
// sharedAcrossRegions true (the default) is born in every region by the
// primary's birth Job, which logs in at each peer as secrets-birth-<ns>, a role
// bp-openbao makes only for the listed namespaces (_config.tpl): a namespace the
// list lacks fails that login before anything is written; a listed namespace no
// value needs is a role nobody uses. The slot sets the list, [] when empty.
//
// It reads each include of openova.generatedSecrets in the templates of
// platform/*/chart and products/*/chart (the values it passes, .Values.<path>,
// and its namespace, a quoted literal or the release's), and each HelmRelease
// of such a chart in clusters/_template/bootstrap-kit/ and each pack's kit/,
// suspended or not, its spec.values laid over the chart's values.yaml. Not seen:
// valuesFrom. Run from tools/guards: go run ./cmd/kit-birth-namespaces [-root
// <tree>]; needs git and yq v4. Exit 0 the list is exact, 1 a finding, 2 nothing
// was judged (a file, an include or a tool cannot be read, or no kit release).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const slot08 = "clusters/_template/bootstrap-kit/08-openbao.yaml"

type obj = map[string]any

var (
	includeRe = regexp.MustCompile(`include "openova\.generatedSecrets" (.*)`)
	secretsRe = regexp.MustCompile(`"secrets" \.Values\.([A-Za-z0-9_.]+)[\s)]`)
	nsRe      = regexp.MustCompile(`"namespace" ("[^"]*"|\S+)`)
	literalRe = regexp.MustCompile(`^"[a-z0-9]([-a-z0-9]*[a-z0-9])?"$`)
)

// site is one include: the chart's values at the path it passes, and its
// namespace ("" = the release's).
type site struct {
	path     []string
	defaults obj
	ns       string
}

func main() {
	root := flag.String("root", "", "the tree to judge (default: the top of the Git work tree)")
	flag.Parse()
	if *root == "" {
		b, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			fmt.Println("kit-birth-namespaces: the top of the Git work tree cannot be read:", err)
			os.Exit(2)
		}
		*root = strings.TrimSpace(string(b))
	}
	lines, code := judge(*root)
	for _, l := range lines {
		fmt.Println("kit-birth-namespaces: " + l)
	}
	os.Exit(code)
}

// globs lists the paths, relative to root, that the patterns match.
func globs(root string, patterns ...string) (out []string) {
	for _, g := range patterns {
		m, _ := filepath.Glob(filepath.Join(root, g))
		for _, f := range m {
			rel, _ := filepath.Rel(root, f)
			out = append(out, rel)
		}
	}
	return out
}

// yq reads files (relative to root) with expr, one JSON value per document.
func yq(root, expr string, files ...string) (out []any, err error) {
	c := exec.Command("yq", append([]string{"-o=json", "-I=0", expr}, files...)...) // #nosec G204 -- the guard's own tool, files of the tree
	c.Dir = root
	b, err := c.Output()
	for d := json.NewDecoder(bytes.NewReader(b)); err == nil && d.More(); {
		var v any
		err = d.Decode(&v)
		out = append(out, v)
	}
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("yq cannot read %v: %v", files, err)
	}
	return out, nil
}

func dig(v any, path ...string) obj {
	for _, p := range path {
		m, _ := v.(obj)
		v = m[p]
	}
	m, _ := v.(obj)
	return m
}

func judge(root string) ([]string, int) {
	sites := map[string][]site{}
	for _, tdir := range globs(root, "platform/*/chart/templates", "products/*/chart/templates") {
		chart := filepath.Dir(tdir)
		err := filepath.WalkDir(filepath.Join(root, tdir), func(f string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			b, err := os.ReadFile(f) // #nosec G304 -- a chart template of the tree
			for _, m := range includeRe.FindAllStringSubmatch(string(b), -1) {
				s, n := secretsRe.FindStringSubmatch(m[1]), nsRe.FindStringSubmatch(m[1])
				if s == nil || (n != nil && !literalRe.MatchString(n[1])) {
					return fmt.Errorf("%s: the include passes no .Values.<path> as secrets, or a namespace that is not a quoted name: %s", f, m[0])
				}
				vals, err := yq(root, ".", chart+"/values.yaml")
				name, err2 := yq(root, ".name", chart+"/Chart.yaml")
				if err != nil || err2 != nil {
					return fmt.Errorf("%s: %v %v", chart, err, err2)
				}
				st := site{path: strings.Split(s[1], ".")}
				if st.defaults = dig(vals[0], st.path...); n != nil {
					st.ns = strings.Trim(n[1], `"`)
				}
				sites[fmt.Sprint(name[0])] = append(sites[fmt.Sprint(name[0])], st)
			}
			return err
		})
		if err != nil {
			return []string{err.Error()}, 2
		}
	}
	rels, err := yq(root, `select(.kind == "HelmRelease") | {"file": filename, "chart": .spec.chart.spec.chart, "ns": (.spec.targetNamespace // .metadata.namespace), "values": .spec.values}`,
		globs(root, "clusters/_template/bootstrap-kit/*.yaml", "clusters/_template/providers/*/kit/*.yaml")...)
	if err != nil {
		return []string{err.Error()}, 2
	}
	need, listed, set, judged := map[string][]string{}, map[string]bool{}, false, 0
	var found []string
	for _, rv := range rels {
		r := dig(rv)
		file, chart := fmt.Sprint(r["file"]), fmt.Sprint(r["chart"])
		if file == slot08 && chart == "bp-openbao" {
			l, ok := dig(r["values"], "peerTrust")["birthNamespaces"].([]any)
			set = ok
			for _, n := range l {
				listed[fmt.Sprint(n)] = true
			}
		}
		for _, s := range sites[chart] {
			judged++
			ns, values := s.ns, obj{}
			if ns == "" {
				ns = fmt.Sprint(r["ns"])
			}
			for k, v := range s.defaults {
				values[k] = v
			}
			for k, v := range dig(r["values"], s.path...) { // Helm's merge: a field of the slot wins, null removes
				spec := obj{}
				for f, x := range dig(values, k) {
					spec[f] = x
				}
				for f, x := range dig(v) {
					if spec[f] = x; x == nil {
						delete(spec, f)
					}
				}
				if values[k] = spec; v == nil {
					delete(values, k)
				}
			}
			for k := range values {
				spec := dig(values, k)
				shared, isSet := spec["sharedAcrossRegions"]
				b, isBool := shared.(bool)
				switch {
				case spec["delivery"] != "mounted" || (isSet && isBool && !b):
				case isSet && !isBool:
					found = append(found, fmt.Sprintf("FINDING %s (chart %s): generatedSecrets.%s sets sharedAcrossRegions %v, which is not a boolean", file, chart, k, shared))
				default:
					need[ns] = append(need[ns], fmt.Sprintf("%s (chart %s) generatedSecrets.%s", file, chart, k))
				}
			}
		}
	}
	if judged == 0 {
		return []string{"no kit release of a chart that includes openova.generatedSecrets: nothing was judged"}, 2
	}
	if !set {
		found = append(found, "FINDING "+slot08+" does not set bp-openbao peerTrust.birthNamespaces as a list: it must be explicit, [] when no namespace needs it")
	}
	for ns, vals := range need {
		if sort.Strings(vals); !listed[ns] {
			found = append(found, fmt.Sprintf("FINDING namespace %s holds shared mounted values (%s) and %s does not list it in peerTrust.birthNamespaces: the primary's birth Job cannot log in at a peer as secrets-birth-%s", ns, strings.Join(vals, ", "), slot08, ns))
		}
	}
	for ns := range listed {
		if need[ns] == nil {
			found = append(found, fmt.Sprintf("FINDING %s lists %s in peerTrust.birthNamespaces, where no kit chart declares a shared mounted value: a peer role nobody uses", slot08, ns))
		}
	}
	sort.Strings(found)
	n := len(found)
	found = append(found, fmt.Sprintf("%d include(s) of the library judged in kit releases; %d namespace(s) need a peer birth role; %d finding(s).", judged, len(need), n))
	if n > 0 {
		return found, 1
	}
	return found, 0
}
