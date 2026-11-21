// Command cilium-mtu holds the MTU given to Cilium to the MTU of the node
// interface of each provider, never a value with an overhead taken off.
//
// Cilium reads its setting MTU as the MTU of the underlying network and takes
// the WireGuard overhead (80 bytes) off itself: the pods and cilium_wg0 get
// MTU - 80, cilium_net and cilium_host get MTU. With WireGuard node encryption
// every decrypted packet for the host passes cilium_net. The kit pinned 1420
// (1500 - 80), so the overhead was taken twice: on a two-region Huawei install
// (21 Nov 2026, eth0 1500) every decrypted host packet above 1420 was dropped,
// the largest do-not-fragment packet between two workers was 1424 bytes, and
// image copies between workers took 64 to 257 s for 15 MB. With 1500 the
// limit was 1500 and the copies took 0.4 to 0.6 s.
//
// One place states the MTU of the node interface of a provider: the local
// node_interface_mtu of infra/providers/<pack>/main.tf, a plain integer from
// 1280 to 9000. The guard reads every folder of infra/providers with a main.tf
// whose name does not start with "_" and fails on
//
//	interface    the module states node_interface_mtu not exactly once, or
//	             not as a plain integer from 1280 to 9000
//	subtraction  any value below is written as a subtraction (1500 - 80)
//	registry     an attribute mtu of the module (the network registry, which
//	             gate check NET-G08 compares with cilium-config mtu) is not
//	             local.node_interface_mtu, or the module has none
//	pack         the substitute CILIUM_MTU the pack writes (a line
//	             CILIUM_MTU: "<n>" of provider-substitutes.yaml.tftpl, or a
//	             map entry CILIUM_MTU = ... of main.tf) is not the interface
//	             MTU, is written twice, or is not a plain integer (main.tf may
//	             also write tostring(local.node_interface_mtu))
//	default      the slot clusters/_template/bootstrap-kit/01-cilium.yaml does
//	             not set MTU from ${CILIUM_MTU:=<n>} (or :-) with a plain
//	             integer, the chart default MTU of platform/cilium/chart/
//	             values.yaml is not one plain integer, the two differ, or
//	             either is not the interface MTU of a pack that writes no
//	             CILIUM_MTU (that pack's nodes get the default)
//
// Known limits: it reads text, not HCL or YAML. A comment line (# or //) and
// the text after " #" are skipped. A value the module computes elsewhere and
// passes by another name, or a CILIUM_MTU written by a template variable, is
// not followed: such a value is a finding (pack: not a plain integer). An
// overlay under clusters/<sovereign>/ that sets MTU is not read.
//
// Usage, from tools/guards:
//
//	go run ./cmd/cilium-mtu [-root R]   exit 1 on a finding, 2 when a file cannot be read
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	providersDir  = "infra/providers"
	kitPath       = "clusters/_template/bootstrap-kit/01-cilium.yaml"
	chartPath     = "platform/cilium/chart/values.yaml"
	substitutes   = "provider-substitutes.yaml.tftpl"
	localRef      = "local.node_interface_mtu"
	minMTU        = 1280
	maxMTU        = 9000
	kitSubstitute = "CILIUM_MTU"
)

var (
	reInterface  = regexp.MustCompile(`^node_interface_mtu\s*=\s*(.*)$`)
	reRegistry   = regexp.MustCompile(`^mtu\s*=\s*(.*)$`)
	rePackHCL    = regexp.MustCompile(`^"?CILIUM_MTU"?\s*=\s*(.*)$`)
	rePackYAML   = regexp.MustCompile(`^CILIUM_MTU\s*:\s*(.*)$`)
	reYAMLMTU    = regexp.MustCompile(`^MTU\s*:\s*(.*)$`)
	reKitValue   = regexp.MustCompile(`^\$\{CILIUM_MTU:[=-](.*)\}$`)
	rePlainInt   = regexp.MustCompile(`^[0-9]+$`)
	reSubtracted = regexp.MustCompile(`[0-9A-Za-z_)}\]]\s*-\s*[0-9A-Za-z_(]`)
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// statement is one value read from a file, with its place.
type statement struct {
	file  string
	line  int
	value string
}

func (s statement) at() string { return fmt.Sprintf("%s:%d", s.file, s.line) }

// pack is what one provider module states.
type pack struct {
	name     string
	iface    int
	ifaceOK  bool
	cilium   []statement
	registry []statement
	ifaceAt  []statement
}

type report struct {
	out      io.Writer
	findings int
}

func (r *report) fail(rule, at, format string, args ...any) {
	r.findings++
	say(r.out, "FAIL %s %s: %s\n", rule, at, fmt.Sprintf(format, args...))
}

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("cilium-mtu", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(errw, "cilium-mtu: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(string(top))
	}
	tree := os.DirFS(*root)
	packs, err := readPacks(tree)
	if err != nil {
		say(errw, "cilium-mtu: %v\n", err)
		return 2
	}
	kit, err := yamlValues(tree, kitPath, reYAMLMTU)
	if err != nil {
		say(errw, "cilium-mtu: %v\n", err)
		return 2
	}
	chart, err := yamlValues(tree, chartPath, reYAMLMTU)
	if err != nil {
		say(errw, "cilium-mtu: %v\n", err)
		return 2
	}
	r := &report{out: out}
	for i := range packs {
		judgePack(r, &packs[i])
	}
	judgeDefaults(r, packs, kit, chart)
	if r.findings > 0 {
		say(out, "cilium-mtu: %d finding(s). The MTU given to Cilium is the MTU of the node interface of the provider (local.node_interface_mtu); Cilium takes the WireGuard overhead off itself.\n", r.findings)
		return 1
	}
	say(out, "cilium-mtu: every pack, the kit default and the chart default give Cilium the node interface MTU\n")
	return 0
}

// readPacks reads every provider module: infra/providers/<name>/main.tf for
// a name that does not start with "_".
func readPacks(tree fs.FS) ([]pack, error) {
	entries, err := fs.ReadDir(tree, providersDir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", providersDir, err)
	}
	var packs []pack
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		mainTF := path.Join(providersDir, e.Name(), "main.tf")
		text, err := fs.ReadFile(tree, mainTF)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", mainTF, err)
		}
		p := pack{name: e.Name()}
		for i, line := range strings.Split(string(text), "\n") {
			body, ok := codeOf(line)
			if !ok {
				continue
			}
			s := func(m []string) statement {
				return statement{file: mainTF, line: i + 1, value: strings.TrimSpace(m[1])}
			}
			if m := reInterface.FindStringSubmatch(body); m != nil {
				p.ifaceAt = append(p.ifaceAt, s(m))
			}
			if m := reRegistry.FindStringSubmatch(body); m != nil {
				p.registry = append(p.registry, s(m))
			}
			if m := rePackHCL.FindStringSubmatch(body); m != nil {
				p.cilium = append(p.cilium, s(m))
			}
		}
		subs := path.Join(providersDir, e.Name(), substitutes)
		text, err = fs.ReadFile(tree, subs)
		switch {
		case err == nil:
			for i, line := range strings.Split(string(text), "\n") {
				body, ok := codeOf(line)
				if !ok {
					continue
				}
				if m := rePackYAML.FindStringSubmatch(body); m != nil {
					p.cilium = append(p.cilium, statement{file: subs, line: i + 1, value: strings.TrimSpace(m[1])})
				}
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("cannot read %s: %w", subs, err)
		}
		packs = append(packs, p)
	}
	if len(packs) == 0 {
		return nil, fmt.Errorf("no provider module under %s: nothing to judge", providersDir)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].name < packs[j].name })
	return packs, nil
}

// yamlValues returns the values of the lines of file that re matches.
func yamlValues(tree fs.FS, file string, re *regexp.Regexp) ([]statement, error) {
	text, err := fs.ReadFile(tree, file)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", file, err)
	}
	var found []statement
	for i, line := range strings.Split(string(text), "\n") {
		body, ok := codeOf(line)
		if !ok {
			continue
		}
		if m := re.FindStringSubmatch(body); m != nil {
			found = append(found, statement{file: file, line: i + 1, value: strings.TrimSpace(m[1])})
		}
	}
	return found, nil
}

// codeOf returns a line without its indentation and its trailing comment,
// and false for a comment line or an empty one.
func codeOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
		return "", false
	}
	for _, mark := range []string{" #", "\t#", " //"} {
		if i := strings.Index(t, mark); i >= 0 {
			t = strings.TrimSpace(t[:i])
		}
	}
	return t, true
}

// integer reads a plain integer, quoted or not. why says what else it is.
func integer(v string) (n int, why string) {
	u := strings.TrimSpace(v)
	if len(u) >= 2 && (u[0] == '"' || u[0] == '\'') && u[len(u)-1] == u[0] {
		u = strings.TrimSpace(u[1 : len(u)-1])
	}
	if reSubtracted.MatchString(u) {
		return 0, "subtraction"
	}
	if !rePlainInt.MatchString(u) {
		return 0, "not a plain integer"
	}
	n, err := strconv.Atoi(u)
	if err != nil {
		return 0, "not a plain integer"
	}
	return n, ""
}

// value judges one statement that must be a plain integer; ok is false after a finding.
func value(r *report, s statement, what string) (int, bool) {
	n, why := integer(s.value)
	switch why {
	case "subtraction":
		r.fail("subtraction", s.at(), "%s is written as a subtraction (%s); state the node interface MTU itself, Cilium takes the WireGuard overhead off", what, s.value)
		return 0, false
	case "":
		return n, true
	default:
		r.fail("default", s.at(), "%s is %q, not a plain integer", what, s.value)
		return 0, false
	}
}

func judgePack(r *report, p *pack) {
	mainTF := path.Join(providersDir, p.name, "main.tf")
	switch len(p.ifaceAt) {
	case 0:
		r.fail("interface", mainTF, "states no local node_interface_mtu, the MTU of the node interface of the provider")
	case 1:
		s := p.ifaceAt[0]
		n, why := integer(s.value)
		switch {
		case why == "subtraction":
			r.fail("subtraction", s.at(), "node_interface_mtu is written as a subtraction (%s); it is the MTU of the node interface itself", s.value)
		case why != "":
			r.fail("interface", s.at(), "node_interface_mtu is %q, not a plain integer", s.value)
		case n < minMTU || n > maxMTU:
			r.fail("interface", s.at(), "node_interface_mtu %d is not from %d to %d", n, minMTU, maxMTU)
		default:
			p.iface, p.ifaceOK = n, true
		}
	default:
		for _, s := range p.ifaceAt {
			r.fail("interface", s.at(), "node_interface_mtu is stated %d times in the module; state it once", len(p.ifaceAt))
		}
	}
	if len(p.registry) == 0 {
		r.fail("registry", mainTF, "the network registry states no mtu; it is %s", localRef)
	}
	for _, s := range p.registry {
		if s.value == localRef {
			continue
		}
		if _, why := integer(s.value); why == "subtraction" {
			r.fail("subtraction", s.at(), "mtu is written as a subtraction (%s); it is %s", s.value, localRef)
			continue
		}
		r.fail("registry", s.at(), "mtu is %s, not %s (gate check NET-G08 compares it with cilium-config mtu)", s.value, localRef)
	}
	if len(p.cilium) > 1 {
		for _, s := range p.cilium {
			r.fail("pack", s.at(), "the pack writes %s %d times; write it once", kitSubstitute, len(p.cilium))
		}
		return
	}
	for _, s := range p.cilium {
		if strings.HasSuffix(s.file, "main.tf") && (s.value == "tostring("+localRef+")" || s.value == `"${`+localRef+`}"`) {
			continue
		}
		n, why := integer(s.value)
		switch {
		case why == "subtraction":
			r.fail("subtraction", s.at(), "%s is written as a subtraction (%s); it is the node interface MTU itself", kitSubstitute, s.value)
		case why != "":
			r.fail("pack", s.at(), "%s is %q, not a plain integer", kitSubstitute, s.value)
		case p.ifaceOK && n != p.iface:
			r.fail("pack", s.at(), "%s is %d, the node interface MTU of %s is %d", kitSubstitute, n, p.name, p.iface)
		}
	}
	if p.ifaceOK {
		say(r.out, "OK interface %s: node interface MTU %d\n", p.name, p.iface)
	}
}

func judgeDefaults(r *report, packs []pack, kit, chart []statement) {
	kitN, kitOK := 0, false
	switch len(kit) {
	case 0:
		r.fail("default", kitPath, "the slot sets no MTU; it sets MTU: ${%s:=<node interface MTU>}", kitSubstitute)
	case 1:
		s := kit[0]
		m := reKitValue.FindStringSubmatch(s.value)
		if m == nil {
			if _, why := integer(s.value); why == "subtraction" {
				r.fail("subtraction", s.at(), "MTU is written as a subtraction (%s)", s.value)
			} else {
				r.fail("default", s.at(), "MTU is %s, not ${%s:=<n>}: a pack whose node interface differs cannot set it", s.value, kitSubstitute)
			}
			break
		}
		kitN, kitOK = value(r, statement{file: s.file, line: s.line, value: m[1]}, "the default of "+kitSubstitute)
	default:
		for _, s := range kit {
			r.fail("default", s.at(), "the slot sets MTU %d times; set it once", len(kit))
		}
	}
	chartN, chartOK := 0, false
	switch len(chart) {
	case 0:
		r.fail("default", chartPath, "the chart sets no default MTU (0, auto-detect, is the #4467 bug)")
	case 1:
		chartN, chartOK = value(r, chart[0], "the chart default MTU")
	default:
		for _, s := range chart {
			r.fail("default", s.at(), "the chart sets MTU %d times; set it once", len(chart))
		}
	}
	if kitOK && chartOK && kitN != chartN {
		r.fail("default", kit[0].at(), "the default of %s is %d, the chart default MTU (%s) is %d", kitSubstitute, kitN, chart[0].at(), chartN)
	}
	// The defaults are judged only against packs whose interface MTU is
	// known: a pack without it is a finding already, and is not counted as
	// agreeing with the defaults.
	before, judged := r.findings, true
	for _, p := range packs {
		if !p.ifaceOK {
			judged = false
		}
		if len(p.cilium) > 0 || !p.ifaceOK {
			continue
		}
		if kitOK && kitN != p.iface {
			r.fail("default", kit[0].at(), "the default of %s is %d, but %s writes no %s and its node interface MTU is %d", kitSubstitute, kitN, p.name, kitSubstitute, p.iface)
		}
		if chartOK && chartN != p.iface {
			r.fail("default", chart[0].at(), "the chart default MTU is %d, but %s writes no %s and its node interface MTU is %d", chartN, p.name, kitSubstitute, p.iface)
		}
	}
	if kitOK && chartOK && judged && r.findings == before && kitN == chartN {
		say(r.out, "OK default: the slot default and the chart default are %d, the node interface MTU of every pack that writes no %s\n", kitN, kitSubstitute)
	}
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
