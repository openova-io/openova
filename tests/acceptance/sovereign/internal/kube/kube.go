// Package kube shells out to kubectl against ONE region's kubeconfig.
//
// The suite reads cluster state to prove WHERE an object actually lives: the
// region is the kubeconfig that returned it, never a label on the object. The
// verbs it uses are `get` and `exec` (psql SELECT, WordPress pod setup), and
// `create`, `apply`, `wait` and `delete` in gate checks CMP-G01, SEC-G01,
// SEC-G02, SEC-G07 and CTL-G04 only. It never patches — every other mutation of the
// Sovereign goes through the console API the way a sovereign-admin would.
// SEC-G07 (suite/check_sec_scope.go) stores nothing: its `create` and `apply`
// are server-side dry runs, its `create token` returns a token of 10 minutes
// that is not stored, and its one `exec` in an OpenBao server pod logs in,
// reads two paths with the output thrown away and revokes its token. Four gate checks
// write to a cluster. CTL-G04 (suite/check_ctl_lock.go) tries to create, in a namespace
// of its own, a copy of an object that the intent lock must refuse, and deletes
// both; its `auth can-i` only asks. CMP-G01 (suite/check_compliance_push.go) creates a
// namespace of its own, applies one Deployment with zero replicas in it, and
// deletes that namespace. SEC-G01 and SEC-G02 (suite/sec_probe.go) create a
// namespace of their own in every region and one probe Pod per control-plane
// node in it, SEC-G01 also one Secret; they run a script that only reads in
// each Pod with `exec`, and delete that namespace.
package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Region is one cluster of the Sovereign.
type Region struct {
	ID         string // cluster region id, e.g. hw-me-east-215-a-rtz-prod
	Kubeconfig string
	Kubectl    string // binary, default "kubectl"
}

func (r Region) bin() string {
	if r.Kubectl != "" {
		return r.Kubectl
	}
	return "kubectl"
}

// Run executes kubectl with args (stdin optional) and returns stdout.
func (r Region) Run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"--kubeconfig", r.Kubeconfig}, args...)
	cmd := exec.CommandContext(ctx, r.bin(), full...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, &errb, stdin
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("[%s] kubectl %s: %v: %s", r.ID, redactArgs(args), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// redactArgs keeps the verb + resource in error text and drops anything after
// `--` (exec payloads may carry generated passwords).
func redactArgs(args []string) string {
	for i, a := range args {
		if a == "--" {
			return strings.Join(args[:i], " ") + " -- <redacted>"
		}
	}
	return strings.Join(args, " ")
}

// List is the decoded shape of `kubectl get -o json` for a list.
type List struct {
	Items []Object `json:"items"`
}

// Object is a loosely typed Kubernetes object.
type Object map[string]any

// GetList runs `kubectl get <resource> [-n ns|-A] -o json`.
func (r Region) GetList(ctx context.Context, resource, namespace string, extra ...string) ([]Object, error) {
	args := []string{"get", resource, "-o", "json"}
	if namespace == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", namespace)
	}
	args = append(args, extra...)
	out, err := r.Run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var l List
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] decode %s list: %w", r.ID, resource, err)
	}
	return l.Items, nil
}

// Exists reports whether a namespace exists in this region.
func (r Region) NamespaceExists(ctx context.Context, ns string) (bool, error) {
	out, err := r.Run(ctx, nil, "get", "namespace", ns, "--ignore-not-found", "-o", "name")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Exec runs a command in a pod container, script on stdin.
func (r Region) Exec(ctx context.Context, ns, pod, container string, stdin io.Reader, cmd ...string) ([]byte, error) {
	args := []string{"exec", "-n", ns, pod}
	if stdin != nil {
		args = append(args, "-i")
	}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--")
	args = append(args, cmd...)
	return r.Run(ctx, stdin, args...)
}

// PSQLSelect runs ONE read-only statement on a CNPG instance pod as the local
// postgres superuser over the unix socket. It refuses anything that is not a
// SELECT, and runs inside a READ ONLY transaction so even a SELECT calling a
// volatile function cannot write.
func (r Region) PSQLSelect(ctx context.Context, ns, pod, db, sql string) (string, error) {
	s := strings.TrimSpace(sql)
	if !strings.HasPrefix(strings.ToUpper(s), "SELECT ") || strings.Contains(s, ";") {
		return "", fmt.Errorf("PSQLSelect: only a single SELECT is allowed, got %q", s)
	}
	script := "BEGIN READ ONLY;\n" + s + ";\nROLLBACK;\n"
	out, err := r.Exec(ctx, ns, pod, "postgres", strings.NewReader(script),
		"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-d", db)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// --- unstructured helpers ------------------------------------------------

// Str walks a path of map keys and returns the string at the end ("" if absent).
func (o Object) Str(path ...string) string {
	v := o.Get(path...)
	s, _ := v.(string)
	return s
}

// Get walks a path of map keys.
func (o Object) Get(path ...string) any {
	var cur any = map[string]any(o)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// Int returns a numeric field (JSON numbers decode as float64).
func (o Object) Int(path ...string) int {
	switch v := o.Get(path...).(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// Objects walks a path of map keys and returns the objects of the list at the
// end. A missing path, or a value that is not a list, gives an empty list.
func (o Object) Objects(path ...string) []Object {
	list, _ := o.Get(path...).([]any)
	out := make([]Object, 0, len(list))
	for _, x := range list {
		if m, ok := x.(map[string]any); ok {
			out = append(out, Object(m))
		}
	}
	return out
}

// OrganizationLabel is the label on the host namespace of an Organization.
// A namespace without it is a platform namespace.
const OrganizationLabel = "openova.io/organization"

// PlatformPods returns the pods of every platform namespace of this region:
// every namespace that does not carry OrganizationLabel.
func (r Region) PlatformPods(ctx context.Context) ([]Object, error) {
	orgs, err := r.GetList(ctx, "namespaces", "", "-l", OrganizationLabel)
	if err != nil {
		return nil, err
	}
	organization := map[string]bool{}
	for _, ns := range orgs {
		organization[ns.Name()] = true
	}
	pods, err := r.GetList(ctx, "pods", "")
	if err != nil {
		return nil, err
	}
	out := make([]Object, 0, len(pods))
	for _, p := range pods {
		if !organization[p.Namespace()] {
			out = append(out, p)
		}
	}
	return out, nil
}

// Name / Namespace shortcuts.
func (o Object) Name() string      { return o.Str("metadata", "name") }
func (o Object) Namespace() string { return o.Str("metadata", "namespace") }

// Condition returns status/reason/message of status.conditions[type=t].
func (o Object) Condition(t string) (status, reason, message string, ok bool) {
	cs, _ := o.Get("status", "conditions").([]any)
	for _, c := range cs {
		m, _ := c.(map[string]any)
		if m["type"] == t {
			s, _ := m["status"].(string)
			rs, _ := m["reason"].(string)
			ms, _ := m["message"].(string)
			return s, rs, ms, true
		}
	}
	return "", "", "", false
}

// Mentions reports whether name appears in the object's name, labels or
// annotations — how the suite ties a per-region object back to an Application
// whose name is unique to this run (accept-<ts>), without trusting any one
// label key that another slice may rename.
func (o Object) Mentions(name string) bool {
	if strings.Contains(o.Name(), name) {
		return true
	}
	for _, k := range []string{"labels", "annotations"} {
		m, _ := o.Get("metadata", k).(map[string]any)
		for _, v := range m {
			if s, _ := v.(string); s == name {
				return true
			}
		}
	}
	return false
}
