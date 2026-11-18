package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// The release scripts run as the cutover chart's runner ServiceAccount, and the
// kubectl stub the other release tests use cannot refuse a call. So the fifth
// delivery to a Sovereign was the first to learn that compose created the
// delivery Job by server-side apply, a PATCH, while the runner may only create
// Jobs (#7938): every Sovereign refused every delivery at that line, and every
// test passed.
//
// This file closes that class. It replays every path of release-run.sh through
// the stub, turns each kubectl call the stub recorded into the API requests
// kubectl makes for it, and checks each request against the RBAC the cutover
// chart renders for the account that makes it. A call shape the model below
// does not know fails the test, and so does a kubectl call site of
// release-run.sh that no replayed path reached: no call goes unchecked. The
// route's own calls (the catalyst-api account) are checked by
// TestReleaseRouteVerbsAreGranted.

// apiRequest is one request kubectl sends to the API server.
type apiRequest struct {
	verb, group, resource, namespace, name string
	anyUser                                bool // allowed to every authenticated user (system:basic-user)
}

func (q apiRequest) String() string {
	r := q.resource
	if q.group != "" {
		r += "." + q.group
	}
	where := "cluster"
	if q.namespace != "" {
		where = "ns " + q.namespace
	}
	return fmt.Sprintf("%s %s (%s)", q.verb, r, where)
}

// kubectlCall is one recorded kubectl invocation: its arguments and the kind
// and namespace of the object it read on stdin (-f -).
type kubectlCall struct {
	args         []string
	kind, kindNS string
}

func (c kubectlCall) String() string { return "kubectl " + strings.Join(c.args, " ") }

// peer: a call with --kubeconfig reaches a follower region as its peer account.
func (c kubectlCall) peer() bool { _, fl := kubectlArgs(c.args); return fl["--kubeconfig"] != "" }

var kubectlValueFlags = map[string]bool{"-n": true, "--namespace": true, "-o": true, "--output": true, "-l": true,
	"--selector": true, "--type": true, "-p": true, "--patch": true, "-f": true, "--filename": true, "--raw": true,
	"--subresource": true, "--kubeconfig": true, "--field-manager": true, "--tail": true, "--from-file": true,
	"--from-literal": true}

// kubectlArgs splits kubectl arguments into the positional words and the flags.
func kubectlArgs(args []string) (pos []string, flags map[string]string) {
	flags = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-" || !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		case strings.Contains(a, "="):
			k, v, _ := strings.Cut(a, "=")
			flags[k] = v
		case kubectlValueFlags[a] && i+1 < len(args):
			flags[a] = args[i+1]
			i++
		default:
			flags[a] = "true"
		}
	}
	for short, long := range map[string]string{"-n": "--namespace", "-f": "--filename", "-o": "--output", "-A": "--all-namespaces"} {
		if v, ok := flags[long]; ok {
			flags[short] = v
		}
	}
	return pos, flags
}

// The resources the release scripts name, as kubectl takes them.
var kubectlResources = map[string][2]string{
	"configmap": {"", "configmaps"}, "configmaps": {"", "configmaps"}, "cm": {"", "configmaps"},
	"secret": {"", "secrets"}, "secrets": {"", "secrets"},
	"pod": {"", "pods"}, "pods": {"", "pods"},
	"job": {"batch", "jobs"}, "jobs": {"batch", "jobs"},
	"helmrelease": {"helm.toolkit.fluxcd.io", "helmreleases"}, "helmreleases": {"helm.toolkit.fluxcd.io", "helmreleases"},
	"helmreleases.helm.toolkit.fluxcd.io": {"helm.toolkit.fluxcd.io", "helmreleases"},
}

var kubectlKinds = map[string][2]string{"ConfigMap": {"", "configmaps"}, "Secret": {"", "secrets"}, "Job": {"batch", "jobs"}}

// kubectlResource reads "<resource> [<name>]" or "<resource>/<name>".
func kubectlResource(rest []string) (gr [2]string, name string, err error) {
	if len(rest) == 0 {
		return gr, "", fmt.Errorf("names no resource")
	}
	r := rest[0]
	if k, n, ok := strings.Cut(r, "/"); ok {
		r, name = k, n
	} else if len(rest) > 1 {
		name = rest[1]
	}
	gr, ok := kubectlResources[r]
	if !ok {
		return gr, "", fmt.Errorf("names resource %q, which the model does not know: add it to kubectlResources", r)
	}
	return gr, name, nil
}

func (c kubectlCall) stdinObject(fl map[string]string) ([2]string, error) {
	if fl["-f"] != "-" {
		return [2]string{}, fmt.Errorf("reads %q; the model knows -f - only", fl["-f"])
	}
	gr, ok := kubectlKinds[c.kind]
	if !ok {
		return gr, fmt.Errorf("reads a %q on stdin, which the model does not know: add it to kubectlKinds", c.kind)
	}
	return gr, nil
}

var nodeProxyPath = regexp.MustCompile(`^/api/v1/nodes/([^/]+)/proxy(/|$)`)

// requests are the API requests kubectl sends for the call.
func (c kubectlCall) requests() ([]apiRequest, error) {
	pos, fl := kubectlArgs(c.args)
	if len(pos) == 0 {
		return nil, fmt.Errorf("names no subcommand")
	}
	sub, rest, ns := pos[0], pos[1:], fl["-n"]
	of := func(gr [2]string, ns, name string, verbs ...string) []apiRequest {
		out := make([]apiRequest, 0, len(verbs))
		for _, v := range verbs {
			n := name
			if v == "create" || v == "list" || v == "watch" {
				n = "" // these requests carry no object name for RBAC
			}
			out = append(out, apiRequest{verb: v, group: gr[0], resource: gr[1], namespace: ns, name: n})
		}
		return out
	}
	switch sub {
	case "get":
		if raw := fl["--raw"]; raw != "" {
			m := nodeProxyPath.FindStringSubmatch(raw)
			if m == nil {
				return nil, fmt.Errorf("reads the raw path %q, which the model does not know", raw)
			}
			return []apiRequest{{verb: "get", resource: "nodes/proxy", name: m[1]}}, nil
		}
		gr, name, err := kubectlResource(rest)
		if err != nil {
			return nil, err
		}
		if fl["-A"] == "true" {
			ns = ""
		}
		if name == "" {
			return of(gr, ns, "", "list"), nil
		}
		return of(gr, ns, name, "get"), nil
	case "patch", "annotate", "label":
		gr, name, err := kubectlResource(rest)
		if err != nil || name == "" {
			return nil, fmt.Errorf("%s of no named object (%v)", sub, err)
		}
		return of(gr, ns, name, "get", "patch"), nil
	case "create":
		if fl["--dry-run"] == "client" {
			return nil, nil // built locally; nothing reaches the API server
		}
		if fl["-f"] != "" {
			gr, err := c.stdinObject(fl)
			if err != nil {
				return nil, err
			}
			if ns == "" {
				ns = c.kindNS
			}
			return of(gr, ns, "", "create"), nil
		}
		gr, _, err := kubectlResource(rest)
		if err != nil {
			return nil, err
		}
		return of(gr, ns, "", "create"), nil
	case "apply":
		gr, err := c.stdinObject(fl)
		if err != nil {
			return nil, err
		}
		if ns == "" {
			ns = c.kindNS
		}
		if fl["--server-side"] == "true" {
			// A server-side apply is a PATCH; of an absent object the API server
			// also checks create.
			return of(gr, ns, "", "patch", "create"), nil
		}
		return of(gr, ns, "", "get", "create", "patch"), nil
	case "delete":
		gr, name, err := kubectlResource(rest)
		if err != nil || name == "" {
			return nil, fmt.Errorf("delete of no named object (%v)", err)
		}
		if fl["--wait"] == "false" {
			return of(gr, ns, name, "delete"), nil
		}
		return of(gr, ns, name, "delete", "get", "list", "watch"), nil // --wait (the default) watches the object go
	case "logs":
		if len(rest) == 0 {
			return nil, fmt.Errorf("logs of nothing")
		}
		kind, name, ok := strings.Cut(rest[0], "/")
		if !ok {
			return []apiRequest{{verb: "get", resource: "pods/log", namespace: ns, name: rest[0]}}, nil
		}
		if gr := kubectlResources[kind]; gr[1] != "jobs" {
			return nil, fmt.Errorf("logs of %q, which the model does not know", rest[0])
		}
		// The Job, for its selector; its Pods, waited for; their logs.
		return append(append(of([2]string{"batch", "jobs"}, ns, name, "get"), of([2]string{"", "pods"}, ns, "", "list", "watch")...),
			apiRequest{verb: "get", resource: "pods/log", namespace: ns}), nil
	case "auth":
		if len(rest) > 0 && rest[0] == "can-i" {
			return []apiRequest{{verb: "create", group: "authorization.k8s.io", resource: "selfsubjectaccessreviews", anyUser: true}}, nil
		}
	}
	return nil, fmt.Errorf("the model knows no kubectl %s: add it to requests", sub)
}

// callKeys name a call by its account, subcommand and resource, the way a call
// site of the script is named, so that the sites can be matched to the calls.
// A site that passes "$@" may be either account.
func callKeys(args []string) ([]string, error) {
	whom := []string{}
	var rest []string
	for _, a := range args {
		if a == "$@" {
			whom = []string{"runner", "peer"}
			continue
		}
		rest = append(rest, a)
	}
	pos, fl := kubectlArgs(rest)
	if len(pos) == 0 {
		return nil, fmt.Errorf("names no subcommand")
	}
	if len(whom) == 0 {
		whom = []string{"runner"}
		if fl["--kubeconfig"] != "" {
			whom = []string{"peer"}
		}
	}
	res := ""
	switch {
	case fl["-f"] != "":
		res = "stdin"
	case fl["--raw"] != "":
		res = "raw"
	case len(pos) > 1:
		gr, _, err := kubectlResource(pos[1:])
		if err != nil {
			return nil, err
		}
		res = gr[1]
	}
	out := make([]string, 0, len(whom))
	for _, w := range whom {
		out = append(out, w+" kubectl "+pos[0]+" "+res)
	}
	return out, nil
}

// recordedCalls reads STUB_DIR/calls.rec: per call its arguments (each ended by
// \037), \035, the stdin object, \036.
func recordedCalls(t *testing.T, stub string) []kubectlCall {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(stub + "/calls.rec"))
	if err != nil {
		t.Fatalf("the stub recorded no call: %v", err)
	}
	var out []kubectlCall
	for _, rec := range strings.Split(string(b), "\x1e") {
		if rec == "" {
			continue
		}
		argv, obj, _ := strings.Cut(rec, "\x1d")
		kind, ns, _ := strings.Cut(strings.TrimSpace(obj), " ")
		out = append(out, kubectlCall{args: strings.Split(strings.TrimSuffix(argv, "\x1f"), "\x1f"), kind: kind, kindNS: ns})
	}
	return out
}

// shellWords are the words of a shell command up to its first operator, quotes
// removed.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	in, q := false, byte(0)
loop:
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			} else {
				cur.WriteByte(c)
			}
		case q == '"':
			switch {
			case c == '\\' && i+1 < len(s):
				i++
				cur.WriteByte(s[i])
			case c == '"':
				q = 0
			default:
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q, in = c, true
		case c == ' ' || c == '\t':
			if in {
				words = append(words, cur.String())
				cur.Reset()
				in = false
			}
		case strings.IndexByte("|;&<>)}", c) >= 0, c == '2' && !in && i+1 < len(s) && s[i+1] == '>':
			break loop
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		words = append(words, cur.String())
	}
	return words
}

type kubectlSite struct {
	line int
	text string
	keys []string
}

var kubectlWord = regexp.MustCompile(`(^|[\s(|;&{])kubectl\s`)

// kubectlSites are the kubectl call sites of a shell script, comments skipped
// and continued lines joined.
func kubectlSites(t *testing.T, path string) []kubectlSite {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	var out []kubectlSite
	for i := 0; i < len(lines); i++ {
		start, l := i+1, lines[i]
		for strings.HasSuffix(l, "\\") && i+1 < len(lines) {
			i++
			l = strings.TrimSuffix(l, "\\") + " " + lines[i]
		}
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		for _, m := range kubectlWord.FindAllStringIndex(l, -1) {
			words := shellWords(l[m[1]:])
			keys, err := callKeys(words)
			if err != nil {
				t.Errorf("%s:%d: kubectl %s: %v", filepath.Base(path), start, strings.Join(words, " "), err)
				continue
			}
			out = append(out, kubectlSite{line: start, text: "kubectl " + strings.Join(words, " "), keys: keys})
		}
	}
	return out
}

// runnerRBAC renders the cutover chart and returns the account the bootstrap
// Job runs as (step 01's: the route copies its pod) and the delivery Job runs
// as (step 03's: compose copies its pod), and the rules ClusterRoleBindings
// grant it. Rules of a RoleBinding are left out, so a namespaced grant can
// only make this test stricter.
func runnerRBAC(t *testing.T) (string, []rbacv1.PolicyRule) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatalf("helm renders the cutover chart whose RBAC the release runs with: %v", err)
	}
	out, err := exec.Command(helm, "template", "self-sovereign-cutover", filepath.Join(repoRoot(t), cutoverChartPath), //nolint:gosec // the chart of this repository
		"--namespace", testNS, "--set", "sovereign.fqdn=t01.omani.works").Output()
	if err != nil {
		t.Fatalf("helm template %s: %v", cutoverChartPath, err)
	}
	roles := map[string][]rbacv1.PolicyRule{}
	var bindings []rbacv1.ClusterRoleBinding
	podSA := map[string]string{}
	for _, part := range regexp.MustCompile(`(?m)^---\s*$`).Split(string(out), -1) {
		var head struct {
			Kind     string            `json:"kind"`
			Metadata metav1ObjectMeta  `json:"metadata"`
			Data     map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(part), &head); err != nil {
			t.Fatalf("the render does not parse: %v", err)
		}
		switch head.Kind {
		case "ClusterRole":
			var r rbacv1.ClusterRole
			if err := yaml.Unmarshal([]byte(part), &r); err != nil {
				t.Fatal(err)
			}
			roles[r.Name] = r.Rules
		case "ClusterRoleBinding":
			var b rbacv1.ClusterRoleBinding
			if err := yaml.Unmarshal([]byte(part), &b); err != nil {
				t.Fatal(err)
			}
			bindings = append(bindings, b)
		case "ConfigMap":
			if head.Metadata.Name != mirrorStepConfigMap && head.Metadata.Name != step03ConfigMap {
				continue
			}
			var ps corev1.PodSpec
			if err := yaml.Unmarshal([]byte(head.Data["podSpec"]), &ps); err != nil {
				t.Fatalf("%s: the podSpec does not parse: %v", head.Metadata.Name, err)
			}
			podSA[head.Metadata.Name] = ps.ServiceAccountName
		}
	}
	sa := podSA[mirrorStepConfigMap]
	if sa == "" || podSA[step03ConfigMap] != sa {
		t.Fatalf("the bootstrap Job runs as step 01's account %q and the delivery Job as step 03's %q: this test checks one account", sa, podSA[step03ConfigMap])
	}
	var rules []rbacv1.PolicyRule
	for _, b := range bindings {
		for _, s := range b.Subjects {
			if s.Kind == rbacv1.ServiceAccountKind && s.Name == sa && s.Namespace == testNS && b.RoleRef.Kind == "ClusterRole" {
				rules = append(rules, roles[b.RoleRef.Name]...)
			}
		}
	}
	if len(rules) == 0 {
		t.Fatalf("the render binds no ClusterRole rule to %s/%s", testNS, sa)
	}
	return sa, rules
}

type metav1ObjectMeta struct {
	Name string `json:"name"`
}

const step03ConfigMap = "cutover-step-03-harbor-prewarm"

// A follower region is reached with its peer kubeconfig (SOV-141), whose
// account the region's first boot binds to cluster-admin.
func peerIsClusterAdmin(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(t), "infra/providers/_shared/cloudinit-control-plane.tftpl")))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "N=handover-peer")
	if i < 0 || !strings.Contains(s[i:min(len(s), i+1200)], "kind: ClusterRoleBinding") ||
		!strings.Contains(s[i:min(len(s), i+1200)], "kind: ClusterRole, name: cluster-admin}") {
		t.Fatal("the peer account of a follower region (kube-system/handover-peer) is no longer bound to cluster-admin: model its role here")
	}
}

// replayEveryReleasePath runs every path of release-run.sh through the stub and
// returns the calls it made.
func replayEveryReleasePath(t *testing.T) []kubectlCall {
	t.Helper()
	var calls []kubectlCall
	run := func(r *runFixture, phase string, req releaseRequest, ok bool) {
		t.Helper()
		if out, code := r.run(phase, req); (code == 0) != ok {
			t.Fatalf("%s: exit %d\n%s\n%s", phase, code, out, r.jobLog(req))
		}
	}
	// A delivery to two regions with the pull credential: stage, then deliver
	// (compose, the delivery Job's prewarm and promote).
	r, req := credentialFixture(t, false, false)
	run(r, "stage", req, true)
	run(r, "deliver", req, true)
	calls = append(calls, recordedCalls(t, r.stub)...)
	// The prewarm fails: deliver reads the state and removes the credential.
	r, req = credentialFixture(t, false, true)
	run(r, "stage", req, true)
	run(r, "deliver", req, false)
	calls = append(calls, recordedCalls(t, r.stub)...)
	// A follower's stage conflicts: the credential is removed.
	r, req = credentialFixture(t, true, false)
	run(r, "stage", req, false)
	calls = append(calls, recordedCalls(t, r.stub)...)
	// A dry run whose candidate's step 03 does not cover the followers: the
	// stage reads where every region's registry stores.
	f := newFixture(t)
	id, _ := f.candidate(1, f.candidateCutoverRelease(step03Marker(candidateCutover)))
	req = releaseRequest{Candidate: id, DryRun: true}
	r = newRunFixtureFrom(t, f, req, false)
	f.write(r.stub, "registry-primary.yml", registryConfig("sovereign-registry"))
	f.write(r.stub, "registry-region-b.yml", registryConfig("sovereign-registry"))
	run(r, "stage", req, true)
	return append(calls, recordedCalls(t, r.stub)...)
}

func allowed(rules []rbacv1.PolicyRule, q apiRequest) bool {
	return q.anyUser || granted(rules, q.verb, q.group, q.resource, q.name)
}

// TestReleaseScriptCallsAreGrantedToTheirAccount: every kubectl call of every
// path of a delivery, against the RBAC of the account that makes it.
func TestReleaseScriptCallsAreGrantedToTheirAccount(t *testing.T) {
	sa, rules := runnerRBAC(t)
	peerIsClusterAdmin(t)
	calls := replayEveryReleasePath(t)
	if len(calls) < 40 {
		t.Fatalf("only %d kubectl calls were recorded: the replay did not run the scripts", len(calls))
	}

	verdicts := map[string]string{}
	seen := map[string]bool{}
	for _, c := range calls {
		keys, err := callKeys(c.args)
		if err != nil {
			t.Errorf("%s: %v", c, err)
			continue
		}
		for _, k := range keys {
			seen[k] = true
		}
		reqs, err := c.requests()
		if err != nil {
			t.Errorf("%s: %v", c, err)
			continue
		}
		for _, q := range reqs {
			switch {
			case c.peer():
				verdicts["peer (cluster-admin)  "+q.String()] = "granted"
			case allowed(rules, q):
				verdicts[sa+"  "+q.String()] = "granted"
			default:
				verdicts[sa+"  "+q.String()] = "MISSING"
				t.Errorf("%s sends %s, which the cutover chart does not grant %s/%s", c, q, testNS, sa)
			}
		}
	}

	// Every call site of release-run.sh was replayed, so its calls were checked.
	script := filepath.Join(repoRoot(t), cutoverChartPath, "files/release/release-run.sh")
	sites := kubectlSites(t, script)
	if len(sites) < 20 {
		t.Fatalf("only %d kubectl call sites found in %s: the scan does not read the script", len(sites), script)
	}
	for _, s := range sites {
		for _, k := range s.keys {
			if !seen[k] {
				t.Errorf("release-run.sh:%d `%s` (%s) was not reached by any replayed path, so its RBAC is unchecked: add the path to replayEveryReleasePath", s.line, s.text, k)
			}
		}
	}

	lines := make([]string, 0, len(verdicts))
	for k, v := range verdicts {
		lines = append(lines, fmt.Sprintf("%-8s %s", v, k))
	}
	sort.Strings(lines)
	t.Logf("%d calls from %d call sites of release-run.sh, %d distinct requests:\n%s", len(calls), len(sites), len(lines), strings.Join(lines, "\n"))

	// Mutation control: without the rule that lets the runner delete Secrets,
	// the replay must be refused (the credential is deleted in every region).
	secretsDelete := func(r rbacv1.PolicyRule) bool {
		return len(r.Resources) == 1 && r.Resources[0] == "secrets" && len(r.Verbs) == 1 && r.Verbs[0] == "delete"
	}
	var less []rbacv1.PolicyRule
	for _, r := range rules {
		if !secretsDelete(r) {
			less = append(less, r)
		}
	}
	refused := 0
	for _, c := range calls {
		reqs, _ := c.requests()
		for _, q := range reqs {
			if !c.peer() && !allowed(less, q) {
				refused++
			}
		}
	}
	if len(less) == len(rules) || refused == 0 {
		t.Fatalf("mutation control: with the secrets delete rule removed (%d of %d rules left) no replayed call is refused: the check cannot fail", len(less), len(rules))
	}
}

// The call the fifth delivery to a Sovereign was refused at, against the grant
// the runner had: Jobs create, get, list, watch. The model must refuse it, and
// must allow the create that replaces it.
func TestReleaseRBACModelRefusesTheServerSideApplyOfAJob(t *testing.T) {
	attempt5 := []rbacv1.PolicyRule{
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"create"}},
		{APIGroups: []string{"batch"}, Resources: []string{"jobs"}, Verbs: []string{"get", "list", "watch"}},
	}
	verdict := func(args ...string) []string {
		reqs, err := kubectlCall{args: args, kind: "Job", kindNS: testNS}.requests()
		if err != nil {
			t.Fatal(err)
		}
		var no []string
		for _, q := range reqs {
			if !allowed(attempt5, q) {
				no = append(no, q.String())
			}
		}
		return no
	}
	if no := verdict("apply", "--server-side", "--force-conflicts", "--field-manager=sovereign-release", "-f", "-"); len(no) != 1 || !strings.HasPrefix(no[0], "patch jobs.batch") {
		t.Fatalf("the server-side apply of a Job under create-only RBAC: refused %v, want exactly the patch", no)
	}
	if no := verdict("create", "-f", "-"); len(no) != 0 {
		t.Fatalf("the create of a Job under the same RBAC is refused: %v", no)
	}
}
