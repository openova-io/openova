// Command kube-access holds that every Helm test hook of this repository that runs
// kubectl reaches the API server the way its workload does: through the shared
// template kube-access-lib.in-cluster-kubeconfig (platform/kube-access-lib), which
// writes a kubeconfig from the Pod's ServiceAccount, as the bp-sso-bridge
// reconciler does.
//
// Without it, kubectl uses the in-cluster configuration only while no client flag
// changes its merged configuration; one --request-timeout sends every call to
// http://localhost:8080 (measured with alpine/k8s 1.31.4, 19 November 2026: the
// bp-sso-bridge function test never passed and held the kit's Kustomization).
//
// Rules, each shown to fail by main_test.go:
//
//	K1  the library template exists, defines kube-access-lib.in-cluster-kubeconfig,
//	    exports KUBECONFIG and builds it with kubectl config from the ServiceAccount
//	    token, CA and namespace and from KUBERNETES_SERVICE_HOST/PORT;
//	K2  a template under platform/*/chart/templates or products/*/chart/templates
//	    that is a Helm test hook and runs kubectl (comments not counted) includes the
//	    template before its first kubectl call;
//	K3  the chart of such a template declares the dependency kube-access-lib from
//	    file://../../kube-access-lib/chart.
//
// Usage (from tools/guards): go run ./cmd/kube-access [--root <repository root>]
// Exit 0 when every rule holds, 1 when one does not, 2 when the tree cannot be read.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const (
	// TemplateName is the named template every kubectl test hook includes.
	TemplateName = "kube-access-lib.in-cluster-kubeconfig"
	// LibraryTemplate is the file of the library that defines it.
	LibraryTemplate = "platform/kube-access-lib/chart/templates/_kube_access.tpl"
	// LibraryRepository is the dependency repository a consuming chart declares.
	LibraryRepository = "file://../../kube-access-lib/chart"
)

var (
	templateComment = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	testHook        = regexp.MustCompile(`(?m)["']?helm\.sh/hook["']?\s*:\s*["']?[a-z,-]*\btest\b`)
	kubectlCall     = regexp.MustCompile(`(^|[\s;&|(` + "`" + `$"'=])kubectl(\s|$)`)
	includeCall     = regexp.MustCompile(`include\s+"` + regexp.QuoteMeta(TemplateName) + `"`)
	libraryNeedles  = []string{
		`define "` + TemplateName + `"`,
		"export KUBECONFIG=",
		"kubectl config set-cluster",
		"${KUBERNETES_SERVICE_HOST",
		"${KUBERNETES_SERVICE_PORT",
		"/var/run/secrets/kubernetes.io/serviceaccount",
		"/ca.crt",
		"kubectl config set-credentials",
		"/token",
		"kubectl config set-context",
		"/namespace",
		"kubectl config use-context",
	}
)

// Finding is one broken rule.
type Finding struct {
	Rule string
	Path string
	Why  string
}

func (f Finding) String() string { return fmt.Sprintf("kube-access: %s %s: %s", f.Rule, f.Path, f.Why) }

// Hook is a test hook template that runs kubectl.
type Hook struct {
	Path  string // repository path of the template
	Chart string // repository path of its chart directory
}

// Check judges the tree at root and returns the kubectl test hooks and the findings.
func Check(root string) ([]Hook, []Finding, error) {
	fsys := os.DirFS(root)
	var findings []Finding
	lib, err := fs.ReadFile(fsys, LibraryTemplate)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		findings = append(findings, Finding{"K1", LibraryTemplate, "the shared template is missing"})
	case err != nil:
		return nil, nil, err
	default:
		for _, needle := range libraryNeedles {
			if !strings.Contains(string(lib), needle) {
				findings = append(findings, Finding{"K1", LibraryTemplate, fmt.Sprintf("the shared template no longer holds %q", needle)})
			}
		}
	}

	hooks, err := kubectlHooks(fsys)
	if err != nil {
		return nil, nil, err
	}
	checkedCharts := map[string]bool{}
	for _, h := range hooks {
		text, err := fs.ReadFile(fsys, h.Path)
		if err != nil {
			return nil, nil, err
		}
		code := stripComments(string(text))
		first := firstKubectl(code)
		inc := includeCall.FindStringIndex(code)
		switch {
		case inc == nil:
			findings = append(findings, Finding{"K2", h.Path, "the test hook runs kubectl without including " + TemplateName})
		case inc[0] > first:
			findings = append(findings, Finding{"K2", h.Path, "the test hook runs kubectl before it includes " + TemplateName})
		}
		if checkedCharts[h.Chart] {
			continue
		}
		checkedCharts[h.Chart] = true
		ok, err := declaresLibrary(fsys, h.Chart+"/Chart.yaml")
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			findings = append(findings, Finding{"K3", h.Chart + "/Chart.yaml", "the chart does not declare the dependency kube-access-lib from " + LibraryRepository})
		}
	}
	return hooks, findings, nil
}

// kubectlHooks lists every test hook template of platform/*/chart and products/*/chart
// that runs kubectl outside comments.
func kubectlHooks(fsys fs.FS) ([]Hook, error) {
	var hooks []Hook
	for _, top := range []string{"platform", "products"} {
		charts, err := fs.Glob(fsys, top+"/*/chart")
		if err != nil {
			return nil, err
		}
		for _, chart := range charts {
			templates := chart + "/templates"
			if _, err := fs.Stat(fsys, templates); errors.Is(err, fs.ErrNotExist) {
				continue
			}
			err := fs.WalkDir(fsys, templates, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				text, err := fs.ReadFile(fsys, path)
				if err != nil {
					return err
				}
				code := stripComments(string(text))
				if testHook.MatchString(code) && firstKubectl(code) >= 0 {
					hooks = append(hooks, Hook{Path: path, Chart: chart})
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Path < hooks[j].Path })
	return hooks, nil
}

// stripComments blanks template comments and shell or YAML comment lines, keeping
// every other byte at its offset.
func stripComments(text string) string {
	text = templateComment.ReplaceAllStringFunc(text, func(m string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' {
				return r
			}
			return ' '
		}, m)
	})
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			lines[i] = strings.Repeat(" ", len(strings.TrimRight(line, "\n"))) + line[len(strings.TrimRight(line, "\n")):]
		}
	}
	return strings.Join(lines, "")
}

// firstKubectl returns the offset of the first kubectl call, or -1.
func firstKubectl(code string) int {
	loc := kubectlCall.FindStringIndex(code)
	if loc == nil {
		return -1
	}
	return loc[0]
}

// declaresLibrary reports whether Chart.yaml lists kube-access-lib from the library's
// file repository in its dependencies.
func declaresLibrary(fsys fs.FS, chartYAML string) (bool, error) {
	text, err := fs.ReadFile(fsys, chartYAML)
	if err != nil {
		return false, err
	}
	inDeps, name := false, ""
	for _, line := range strings.Split(string(text), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-") {
			inDeps = strings.HasPrefix(line, "dependencies:")
			name = ""
			continue
		}
		if !inDeps {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		key, value, found := strings.Cut(item, ":")
		if !found {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if strings.HasPrefix(trimmed, "- ") {
			name = ""
		}
		switch strings.TrimSpace(key) {
		case "name":
			name = value
		case "repository":
			if name == "kube-access-lib" && value == LibraryRepository {
				return true, nil
			}
		}
	}
	return false, nil
}

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root with git: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("kube-access", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "repository root (default: the root Git reports)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		r, err := repositoryRoot()
		if err != nil {
			say(stderr, "kube-access: %v\n", err)
			return 2
		}
		*root = r
	}
	hooks, findings, err := Check(*root)
	if err != nil {
		say(stderr, "kube-access: cannot read the tree: %v\n", err)
		return 2
	}
	for _, f := range findings {
		say(stdout, "%s\n", f)
	}
	if len(findings) > 0 {
		say(stdout, "kube-access: FAIL — %d finding(s) over %d test hook(s) that run kubectl\n", len(findings), len(hooks))
		return 1
	}
	for _, h := range hooks {
		say(stdout, "kube-access: ok %s\n", h.Path)
	}
	say(stdout, "kube-access: PASS — %d test hook(s) run kubectl, each sets up its access through %s first\n", len(hooks), TemplateName)
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// say writes one line of the report; a failed write cannot change the verdict, which
// is the exit code.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
