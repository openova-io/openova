// Command no-hand-marks keeps the platform's own automation from leaving the
// marks of a hand on the objects that check FND-G02, Zero touch
// (tests/acceptance/sovereign/internal/suite/check_zero_touch.go), tracks.
//
// FND-G02 fails when a HelmRelease, or a Deployment or DaemonSet of
// catalyst-system, cnpg-system or kube-system, carries a field manager named
// kubectl or kubectl-<anything>, or when a Deployment, DaemonSet or StatefulSet
// of those namespaces or of flux-system carries the annotation
// kubectl.kubernetes.io/restartedAt on its pod template. Those are the marks a
// hand leaves, so the product never makes them: it writes with its own field
// manager (--field-manager=catalyst-<component>, one stable name per
// component) and restarts a workload by a patch that sets
// catalyst.openova.io/restartedAt and catalyst.openova.io/restartedBy on the
// pod template.
//
// It reads the files Git lists (tracked, and new files Git does not ignore)
// that hold the product's automation, each a template (.tftpl .tpl), Terraform
// (.tf), shell (.sh) or YAML (.yaml .yml):
//
//	node   a file under infra/providers/ outside a tests/ folder: the
//	       start-up data of the nodes
//	chart  a file of a chart (platform/<x>/chart/, products/<x>/chart/,
//	       products/<x>/charts/<y>/) outside the chart's own tests/ folder,
//	       other than its Chart.yaml (metadata that nothing runs): hooks,
//	       Jobs, the scripts of ConfigMaps
//
// and fails on
//
//	restart  a kubectl rollout restart, whatever its object;
//	manager  a kubectl apply, patch, annotate, label, scale or set that can
//	         reach an object FND-G02 tracks and carries no
//	         --field-manager=catalyst-..., given as the flag or as a variable
//	         that the same file assigns the flag. It can reach one when its
//	         object is a HelmRelease (hr, helmrelease, helmreleases, with or
//	         without the group), in any namespace; when its object is a
//	         Deployment, DaemonSet or StatefulSet (deploy, ds, sts and their
//	         long names) and its namespace is not written out as another
//	         namespace than the four (-n "$NS" and no -n can be one of them);
//	         and when it is a kubectl apply into one of the four written out.
//
// The four namespaces are catalyst-system, cnpg-system, kube-system and
// flux-system. A write of another kind of object (a ConfigMap, a Secret, a
// GitRepository, a Kustomization) leaves no mark FND-G02 reads. A kubectl with
// --local or a dry run writes nothing. A comment holds nothing: a line that
// starts with # or //, the rest of a line from a # that starts a word outside
// quotes, and a Go template comment {{/* */}}; nor does a quoted message given
// to echo, printf or a function that prints one (log, warn, die, fail, say,
// info, err). Lines that end in a backslash are read as one line. A command
// ends at ;, |, && or || outside quotes, or at the next kubectl.
//
// Known limits: an object whose kind only a variable names (kubectl patch
// "$KIND" "$NAME"), a file or a here-document (kubectl apply -f x.yaml) is not
// seen, nor a kubectl that a program calls by a list of arguments
// (["kubectl", ...]). Review holds those, and FND-G02 on an installed
// Sovereign holds them all.
//
// Usage, from tools/guards:
//
//	go run ./cmd/no-hand-marks          exit 1 on a finding, 2 when it cannot read
//	go run ./cmd/no-hand-marks --list   also prints every kubectl write it read
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
)

// The namespaces FND-G02 tracks (zeroTouchRestartNamespaces of the check).
var trackedNamespaces = map[string]bool{
	"catalyst-system": true, "cnpg-system": true, "kube-system": true, "flux-system": true,
}

// The kubectl verbs that write an object and so leave a field manager.
var writeVerbs = map[string]bool{
	"apply": true, "patch": true, "annotate": true, "label": true, "scale": true, "set": true,
}

// Flags of kubectl whose value is the next word when it is not given with =.
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "--kubeconfig": true, "--context": true, "--cluster": true,
	"--user": true, "-s": true, "--server": true, "--token": true, "--as": true, "--as-group": true,
	"--request-timeout": true, "-p": true, "--patch": true, "-f": true, "--filename": true,
	"--type": true, "-l": true, "--selector": true, "-o": true, "--output": true,
	"--timeout": true, "--replicas": true, "--subresource": true, "--containers": true, "-c": true,
}

const managerFlag = "--field-manager=catalyst-"

var (
	chartRoot   = regexp.MustCompile(`^(platform/[^/]+/chart|products/[^/]+/chart|products/[^/]+/charts/[^/]+)/`)
	tplComment  = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	kubectlWord = regexp.MustCompile("(?:^|[\\s(;|&`\"'{/=])kubectl[ \\t]")
	managerVar  = regexp.MustCompile(`(?m)^\s*(?:export\s+|local\s+|readonly\s+)?([A-Za-z_][A-Za-z0-9_]*)=["']?--field-manager=catalyst-[A-Za-z0-9.-]`)
	varRef      = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	redirect    = regexp.MustCompile(`^[0-9]*[<>]`)
	// The text before the quote that opens a message: the quote is an
	// argument of echo, printf or a function that prints a message.
	textCommand = regexp.MustCompile(`(?:^|[;&|({]|\bthen|\bdo|\belse)\s*(?:echo|printf|log|warn|die|fail|say|info|err)\b[^;&|(]*$`)
)

// A finding is one kubectl command of a file. Rule is "restart" or "manager"
// when it breaks a rule, and "" for a write that breaks none (--list).
type finding struct {
	Line int
	Rule string
	Text string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("no-hand-marks", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	list := flags.Bool("list", false, "also print every kubectl write that breaks no rule")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := git(".", "rev-parse", "--show-toplevel")
		if err != nil {
			say(errw, "no-hand-marks: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(top)
	}
	listed, err := git(*root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		say(errw, "no-hand-marks: cannot list the files: %v\n", err)
		return 2
	}
	tree := os.DirFS(*root)
	read, bad := 0, 0
	for _, p := range strings.Split(listed, "\x00") {
		if !inScope(p) {
			continue
		}
		b, err := fs.ReadFile(tree, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			say(errw, "no-hand-marks: %v\n", err)
			return 2
		}
		read++
		for _, f := range scan(string(b)) {
			switch {
			case f.Rule != "":
				bad++
				say(out, "FAIL %s %s:%d: %s\n", f.Rule, p, f.Line, f.Text)
			case *list:
				say(out, "ok   write %s:%d: %s\n", p, f.Line, f.Text)
			}
		}
	}
	if read == 0 {
		say(errw, "no-hand-marks: no file of the start-up data or of a chart was read: nothing was measured\n")
		return 2
	}
	if bad > 0 {
		say(out, "no-hand-marks: %d findings in %d files read. The product's automation restarts a workload by a patch with --field-manager=catalyst-<component> that sets catalyst.openova.io/restartedAt and catalyst.openova.io/restartedBy, never kubectl rollout restart; it writes an object FND-G02 tracks (a HelmRelease; a Deployment, DaemonSet or StatefulSet of catalyst-system, cnpg-system, kube-system or flux-system) only with --field-manager=catalyst-<component>. FND-G02 reads anything else as a hand.\n", bad, read)
		return 1
	}
	say(out, "no-hand-marks: OK, %d files read, no kubectl rollout restart and no write of a tracked object without --field-manager=catalyst-\n", read)
	return 0
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

func inScope(p string) bool {
	switch path.Ext(p) {
	case ".tftpl", ".tpl", ".tf", ".sh", ".yaml", ".yml":
	default:
		return false
	}
	if strings.HasPrefix(p, "infra/providers/") {
		return !strings.Contains(p, "/tests/")
	}
	m := chartRoot.FindString(p)
	// Chart.yaml is the chart's metadata and description: nothing runs it.
	return m != "" && !strings.HasPrefix(p[len(m):], "tests/") && path.Base(p) != "Chart.yaml"
}

// scan returns the kubectl writes and restarts of one file.
func scan(text string) []finding {
	text = tplComment.ReplaceAllStringFunc(text, func(s string) string {
		return strings.Repeat("\n", strings.Count(s, "\n"))
	})
	vars := map[string]bool{}
	for _, m := range managerVar.FindAllStringSubmatch(text, -1) {
		vars[m[1]] = true
	}
	var out []finding
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		start := i + 1
		if isComment(lines[i]) {
			continue
		}
		logical := stripComment(lines[i])
		for strings.HasSuffix(strings.TrimRight(logical, " \t"), `\`) && i+1 < len(lines) && !isComment(lines[i+1]) {
			i++
			logical = strings.TrimSuffix(strings.TrimRight(logical, " \t"), `\`) + " " + strings.TrimSpace(stripComment(lines[i]))
		}
		out = append(out, scanLine(logical, start, vars)...)
	}
	return out
}

func isComment(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")
}

// stripComment cuts a line at a # that starts a word outside quotes.
func stripComment(line string) string {
	single, double := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && !single:
			i++
		case c == '\'' && !double:
			single = !single
		case c == '"' && !single:
			double = !double
		case c == '#' && !single && !double && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return line[:i]
		}
	}
	return line
}

func scanLine(logical string, line int, vars map[string]bool) []finding {
	var out []finding
	locs := kubectlWord.FindAllStringIndex(logical, -1)
	for k, loc := range locs {
		end := len(logical)
		if k+1 < len(locs) {
			end = locs[k+1][0] + 1
		}
		if inText(logical, loc[1]-len("kubectl ")) {
			continue
		}
		region := logical[loc[1]:end]
		write, rule := judge(words(region), region, vars)
		if !write {
			continue
		}
		text := strings.TrimSpace(logical[loc[0]:end])
		text = strings.TrimLeft(text, "(;|&`\"'{/=")
		if len(text) > 200 {
			text = text[:200] + "..."
		}
		out = append(out, finding{Line: line, Rule: rule, Text: text})
	}
	return out
}

// judge reads the words that follow kubectl. write says whether the command
// writes an object; rule names the rule it breaks, or is "".
func judge(toks []string, region string, vars map[string]bool) (write bool, rule string) {
	var verb, sub, ns string
	var objects []string
	local := false // --local or a dry run: nothing reaches the cluster
	manager := strings.Contains(region, managerFlag)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if m := varRef.FindStringSubmatch(t); m != nil && vars[m[1]] {
			manager = true
			continue
		}
		if redirect.MatchString(t) {
			continue
		}
		if strings.HasPrefix(t, "-") {
			name, val, has := strings.Cut(t, "=")
			next := func() string {
				if has {
					return val
				}
				if i+1 < len(toks) {
					i++
					return toks[i]
				}
				return ""
			}
			switch {
			case name == "-n" || name == "--namespace":
				ns = next()
			case strings.HasPrefix(name, "-n") && !strings.HasPrefix(name, "--"):
				ns = t[2:]
			case name == "--field-manager":
				if strings.HasPrefix(next(), "catalyst-") {
					manager = true
				}
			case name == "--local" || (name == "--dry-run" && val != "none"):
				local = true
			case valueFlags[name]:
				next()
			}
			continue
		}
		switch {
		case verb == "":
			verb = t
		case verb == "rollout" && sub == "":
			sub = t
		default:
			objects = append(objects, t)
		}
	}
	if verb == "rollout" {
		return sub == "restart", map[bool]string{true: "restart"}[sub == "restart"]
	}
	if (!writeVerbs[verb] && verb != "create" && verb != "replace") || local {
		return false, ""
	}
	if writeVerbs[verb] && !manager && tracked(verb, ns, objects) {
		return true, "manager"
	}
	return true, ""
}

// tracked says whether a write can reach an object FND-G02 tracks: a
// HelmRelease of any namespace; a Deployment, DaemonSet or StatefulSet unless
// its namespace is written out and is none of the four; anything a kubectl
// apply writes into one of the four namespaces written out.
func tracked(verb, ns string, objects []string) bool {
	literal := ns != "" && !strings.ContainsAny(ns, "${<")
	for _, o := range objects {
		kind, _, _ := strings.Cut(strings.ToLower(o), "/")
		kind, _, _ = strings.Cut(kind, ".")
		switch kind {
		case "hr", "helmrelease", "helmreleases":
			return true
		case "deploy", "deployment", "deployments", "ds", "daemonset", "daemonsets", "sts", "statefulset", "statefulsets":
			if !literal || trackedNamespaces[ns] {
				return true
			}
		}
	}
	return verb == "apply" && literal && trackedNamespaces[ns]
}

// inText says whether position at of a line is inside a quoted string that is
// an argument of echo or printf: a message, not a command.
func inText(line string, at int) bool {
	type open struct {
		quote byte
		at    int
	}
	var stack []open
	for i := 0; i < at && i < len(line); i++ {
		c := line[i]
		top := byte(0)
		if len(stack) > 0 {
			top = stack[len(stack)-1].quote
		}
		switch {
		case top == '\'':
			if c == '\'' {
				stack = stack[:len(stack)-1]
			}
		case c == '\\':
			i++
		case top == '"' && c == '"':
			stack = stack[:len(stack)-1]
		case strings.HasPrefix(line[i:], "$("):
			stack = append(stack, open{'(', i})
			i++
		case top == '"':
		case c == '"' || c == '\'':
			stack = append(stack, open{c, i})
		case c == '(':
			stack = append(stack, open{'(', i})
		case c == ')' && top == '(':
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) == 0 || stack[len(stack)-1].quote == '(' {
		return false
	}
	return textCommand.MatchString(line[:stack[len(stack)-1].at])
}

// words splits the text after kubectl into the words of one shell command. It
// drops quotes, keeps ${...}, {{...}} and %{...} whole, and stops at ;, |, &&,
// || or a ) outside quotes that closes nothing it opened.
func words(s string) []string {
	var toks []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], "{{"):
			j := strings.Index(s[i:], "}}")
			if j < 0 {
				j = len(s) - i - 2
			}
			cur.WriteString(s[i : i+j+2])
			i += j + 1
		case strings.HasPrefix(s[i:], "${") || strings.HasPrefix(s[i:], "%{"):
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				j = len(s) - i - 1
			}
			cur.WriteString(s[i : i+j+1])
			i += j
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(s[i+1])
			i++
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				j = len(s) - i - 1
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			j := closeDouble(s, i+1)
			cur.WriteString(s[i+1 : j])
			i = j
		case c == '(':
			depth++
			cur.WriteByte(c)
		case c == ')' && depth == 0:
			flush()
			return toks
		case c == ')':
			depth--
			cur.WriteByte(c)
		case depth == 0 && (c == ';' || c == '|' || (c == '&' && (i == 0 || s[i-1] != '>'))):
			flush()
			return toks
		case depth == 0 && (c == ' ' || c == '\t'):
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return toks
}

// closeDouble returns the index of the quote that closes a double-quoted
// string whose text starts at i, or len(s).
func closeDouble(s string, i int) int {
	for ; i < len(s); i++ {
		switch {
		case s[i] == '\\':
			i++
		case s[i] == '"':
			return i
		case strings.HasPrefix(s[i:], "$("):
			i = closeParen(s, i+2)
		}
	}
	return len(s)
}

// closeParen returns the index of the ) that closes a $( whose text starts at
// i, or len(s).
func closeParen(s string, i int) int {
	for ; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			i = closeDouble(s, i+1)
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return len(s)
			}
			i += j + 1
		case '(':
			i = closeParen(s, i+1)
		case ')':
			return i
		}
	}
	return len(s)
}
