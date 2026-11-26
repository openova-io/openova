package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G06, default deny: step block POL. In every region the check
// dials every row of the dial table policyDialsTable from a Pod in the row's
// source namespace, with the row's labels, and compares what happened with the
// row's expectation. A row expects allow (the connection is made) or deny (the
// connection is dropped: the dial times out). A refused connection reached the
// destination and found no listener, so it proves neither and fails both.
//
// Every dial Pod first dials the cluster DNS service on TCP port 53, which
// every namespace may reach, also under default deny. When that dial does not
// connect, the Pod has no working network and the row was not measured: it
// fails. So a Pod without a network never passes a deny row. A deny row to a
// Service also needs a ready endpoint of that Service (its EndpointSlices, read
// with `get`): without one nothing listens, a timeout proves nothing, and the
// row fails.
//
// Per region the check records one step that starts the dial Pods, one step
// per row, and one step that deletes the Pods it created, also when a step
// failed. It creates Pods only, one per source (namespace and labels), named
// pol-dial-<run>-<n>, and deletes them; it creates no namespace, because the
// policies select pods by their namespace.
//
// The Dialer is exported for the checks of other areas that dial from a Pod:
// Start, Dial, ReadyEndpoints and Stop, with the image of sec_probe.go.
const (
	policyDialsID = "NET-G06"
	// policyDialsTable is the dial table, relative to the checkout at RepoRoot.
	policyDialsTable = "tests/acceptance/sovereign/policy-dials.yaml"
	// PolicyDialsSchema is the schema string of the dial table.
	PolicyDialsSchema = "openova.io/policy-dials/v1"
	// DialLabel is the label that marks a dial Pod; its value is the run.
	DialLabel = "acceptance.openova.io/policy-dial"

	dialWait        = "180s" // the longest wait for a dial Pod to be Ready
	dialPodLife     = "3600" // seconds a dial Pod lives if Stop never runs: longer than the dials of a large table
	dialSeconds     = "3"    // nc -w: the longest wait for a connection
	dialControlHost = "kube-dns.kube-system.svc.cluster.local"
	dialControlPort = 53
)

// The outcomes of one dial.
const (
	DialConnected  = "connected"
	DialTimedOut   = "timed-out"
	DialRefused    = "refused"
	DialUnresolved = "unresolved"
	DialError      = "error"
)

// DialSource is where a dial starts: the namespace of the dial Pod and the
// labels it carries besides DialLabel.
type DialSource struct {
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// key is the identity of a source: its namespace and its labels in order.
func (s DialSource) key() string {
	keys := make([]string, 0, len(s.Labels))
	for k := range s.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := strings.Builder{}
	b.WriteString(s.Namespace)
	for _, k := range keys {
		b.WriteString("," + k + "=" + s.Labels[k])
	}
	return b.String()
}

// DialTarget is what a dial reaches: the Service of a namespace, or an
// address, and a TCP port.
type DialTarget struct {
	Namespace string `json:"namespace,omitempty"`
	Service   string `json:"service,omitempty"`
	Address   string `json:"address,omitempty"`
	Port      int    `json:"port"`
}

// Host is the name or the address that the dial connects to.
func (t DialTarget) Host() string {
	if t.Address != "" {
		return t.Address
	}
	return t.Service + "." + t.Namespace + ".svc.cluster.local"
}

func (t DialTarget) String() string {
	return net.JoinHostPort(t.Host(), strconv.Itoa(t.Port))
}

// DialRow is one row of the dial table.
type DialRow struct {
	Name   string     `json:"name"`
	From   DialSource `json:"from"`
	To     DialTarget `json:"to"`
	Expect string     `json:"expect"` // allow or deny
}

// DialResult is what one dial gave: the outcome of the control dial to the
// cluster DNS service, the outcome of the dial of the target, and what nc
// printed for each.
type DialResult struct {
	Control, ControlOutput string
	Outcome, Output        string
}

var (
	dialNameRE  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	labelPartRE = regexp.MustCompile(`^([A-Za-z0-9][-A-Za-z0-9_.]{0,61})?[A-Za-z0-9]$`)
)

// ReadDialTable reads the dial table and checks every row. The file is YAML in
// flow style that is JSON once its comment lines are left out: a line whose
// first character other than a space is # is a comment.
func ReadDialTable(data []byte) ([]DialRow, error) {
	var kept bytes.Buffer
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			kept.WriteString(line + "\n")
		}
	}
	dec := json.NewDecoder(&kept)
	dec.DisallowUnknownFields()
	var t struct {
		Schema string    `json:"schema"`
		Dials  []DialRow `json:"dials"`
	}
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("the dial table is not JSON once its comment lines are left out: %v", err)
	}
	if dec.More() {
		return nil, errors.New("the dial table holds more than one document")
	}
	if t.Schema != PolicyDialsSchema {
		return nil, fmt.Errorf("the dial table has schema %q, want %q", t.Schema, PolicyDialsSchema)
	}
	if len(t.Dials) == 0 {
		return nil, errors.New("the dial table has no row")
	}
	seen := map[string]bool{}
	for i, r := range t.Dials {
		if err := checkDialRow(r); err != nil {
			return nil, fmt.Errorf("row %d (%q): %v", i+1, r.Name, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("row %d: the name %q is used twice", i+1, r.Name)
		}
		seen[r.Name] = true
	}
	return t.Dials, nil
}

func checkDialRow(r DialRow) error {
	switch {
	case !dialNameRE.MatchString(r.Name):
		return errors.New("the name is not a lower-case name of letters, digits and dashes")
	case !dialNameRE.MatchString(r.From.Namespace):
		return fmt.Errorf("from.namespace %q is not a namespace name", r.From.Namespace)
	case r.Expect != "allow" && r.Expect != "deny":
		return fmt.Errorf("expect is %q, want allow or deny", r.Expect)
	case r.To.Port < 1 || r.To.Port > 65535:
		return fmt.Errorf("to.port %d is not a port", r.To.Port)
	}
	for k, v := range r.From.Labels {
		if k == DialLabel {
			return fmt.Errorf("from.labels may not set %s, the label of the dial Pod", DialLabel)
		}
		name := k
		if i := strings.LastIndex(k, "/"); i >= 0 {
			name = k[i+1:]
		}
		if !labelPartRE.MatchString(name) || (v != "" && !labelPartRE.MatchString(v)) {
			return fmt.Errorf("from.labels %q: %q is not a label", k, v)
		}
	}
	byService := r.To.Namespace != "" || r.To.Service != ""
	switch {
	case byService && r.To.Address != "":
		return errors.New("to names a Service and an address; name one")
	case byService && (!dialNameRE.MatchString(r.To.Namespace) || !dialNameRE.MatchString(r.To.Service)):
		return fmt.Errorf("to.namespace %q and to.service %q must both be names", r.To.Namespace, r.To.Service)
	case !byService && net.ParseIP(r.To.Address) == nil:
		return fmt.Errorf("to.address %q is not an IP address, and to names no Service", r.To.Address)
	}
	return nil
}

// dialOutcome classifies the exit code and the output of busybox
// `nc -z -vv -w <seconds> <host> <port>`: exit code 0 is a connection; else the
// output says why (measured on alpine 3.20, BusyBox 1.36.1).
func dialOutcome(rc, out string) string {
	switch {
	case rc == "0":
		return DialConnected
	case strings.Contains(out, "timed out"):
		return DialTimedOut
	case strings.Contains(out, "Connection refused"):
		return DialRefused
	case strings.Contains(out, "bad address"):
		return DialUnresolved
	}
	return DialError
}

// dialScript runs in a dial Pod as `sh -s -- <control host> <control port>
// <host> <port>`. It prints, per dial, the exit code of nc and its output on
// one line; it judges nothing.
const dialScript = `dial() {
	out=$(nc -z -vv -w ` + dialSeconds + ` "$2" "$3" 2>&1)
	rc=$?
	echo "$1_rc=$rc"
	echo "$1_out=$(printf '%s' "$out" | tr '\n' ' ')"
}
dial control "$1" "$2"
dial target "$3" "$4"
`

// Dialer starts dial Pods in one region, dials from them and deletes them.
type Dialer struct {
	Kubectl kubectlFunc
	Region  kube.Region
	Run     string // lower-case identifier of the run; part of every Pod name and the value of DialLabel
	pods    map[string]dialPod
	order   []string

	// HostNetwork starts the dial Pods on the host network of their node, with
	// the cluster DNS: they dial as the node, not as a Pod of their namespace.
	HostNetwork bool
}

type dialPod struct{ namespace, name string }

// Start creates one dial Pod for each source that has none yet and waits until
// every Pod it created is Ready. A Pod it created is deleted by Stop, also when
// Start fails.
func (d *Dialer) Start(ctx context.Context, sources []DialSource) error {
	if d.pods == nil {
		d.pods = map[string]dialPod{}
	}
	created := []dialPod{}
	for _, s := range sources {
		if _, ok := d.pods[s.key()]; ok {
			continue
		}
		p := dialPod{namespace: s.Namespace, name: "pol-dial-" + d.Run + "-" + strconv.Itoa(len(d.order)+1)}
		pod, err := dialPodObject(p, s, d.Run, d.HostNetwork)
		if err != nil {
			return err
		}
		if _, err := d.Kubectl(d.Region, ctx, bytes.NewReader(pod), "-n", p.namespace, "create", "-f", "-"); err != nil {
			return fmt.Errorf("create the dial Pod %s/%s: %v", p.namespace, p.name, err)
		}
		d.pods[s.key()] = p
		d.order = append(d.order, s.key())
		created = append(created, p)
	}
	for _, p := range created {
		if _, err := d.Kubectl(d.Region, ctx, nil, "-n", p.namespace, "wait", "--for=condition=Ready", "pod/"+p.name, "--timeout="+dialWait); err != nil {
			return fmt.Errorf("the dial Pod %s/%s is not Ready: %v", p.namespace, p.name, err)
		}
	}
	return nil
}

// ReadyEndpoints counts the ready endpoints of the Service that to names, from
// its EndpointSlices; an endpoint whose ready condition is not set counts as
// ready, as the API defines it. It only reads.
func (d *Dialer) ReadyEndpoints(ctx context.Context, to DialTarget) (int, error) {
	out, err := d.Kubectl(d.Region, ctx, nil, "-n", to.Namespace, "get", "endpointslices",
		"-l", "kubernetes.io/service-name="+to.Service, "-o", "json")
	if err != nil {
		return 0, fmt.Errorf("read the EndpointSlices of the Service %s/%s: %v", to.Namespace, to.Service, err)
	}
	var list struct {
		Items []struct {
			Endpoints []struct {
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return 0, fmt.Errorf("the EndpointSlices of the Service %s/%s are not JSON: %v", to.Namespace, to.Service, err)
	}
	n := 0
	for _, s := range list.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready == nil || *e.Conditions.Ready {
				n++
			}
		}
	}
	return n, nil
}

// Pods names the dial Pods that Start created, as <namespace>/<name>.
func (d *Dialer) Pods() []string {
	out := []string{}
	for _, k := range d.order {
		out = append(out, d.pods[k].namespace+"/"+d.pods[k].name)
	}
	return out
}

// dialNetworkWait is how long Dial dials again while the control dial does not
// connect, and dialNetworkEvery the pause between two dials.
var dialNetworkWait, dialNetworkEvery = 30 * time.Second, 2 * time.Second

// Dial dials to from the Pod of the source from, after the control dial.
//
// Ready says only that the container of a dial Pod started, not that its
// network works yet: on hw307 (23 November 2026, NET-G10 in region b) a dial
// Pod of a new namespace could not resolve the cluster DNS name in its first
// 10 seconds after Ready, while its twin, created with it and dialed 10
// seconds later, connected; CoreDNS ran without an error and no Cilium agent
// restarted. A dial whose control did not connect measured nothing, so Dial
// dials again, every dialNetworkEvery, until the control connects or
// dialNetworkWait has passed, and returns the last result. A dial whose
// control connected is never repeated.
func (d *Dialer) Dial(ctx context.Context, from DialSource, to DialTarget) (DialResult, error) {
	deadline := time.Now().Add(dialNetworkWait)
	for {
		res, err := d.dialOnce(ctx, from, to)
		if err != nil || res.Control == DialConnected || !time.Now().Before(deadline) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, nil
		case <-time.After(dialNetworkEvery):
		}
	}
}

// dialOnce runs the control dial and the dial to to once in the Pod of from.
func (d *Dialer) dialOnce(ctx context.Context, from DialSource, to DialTarget) (DialResult, error) {
	p, ok := d.pods[from.key()]
	if !ok {
		return DialResult{}, fmt.Errorf("no dial Pod was started for the source %s", from.key())
	}
	out, err := d.Kubectl(d.Region, ctx, strings.NewReader(dialScript), "-n", p.namespace, "exec", "-i", p.name, "-c", "dial", "--",
		"sh", "-s", "--", dialControlHost, strconv.Itoa(dialControlPort), to.Host(), strconv.Itoa(to.Port))
	if err != nil {
		return DialResult{}, fmt.Errorf("run the dial in %s/%s: %v", p.namespace, p.name, err)
	}
	v := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, val, found := strings.Cut(line, "="); found {
			v[k] = strings.TrimSpace(val)
		}
	}
	return DialResult{Control: dialOutcome(v["control_rc"], v["control_out"]), ControlOutput: v["control_out"],
		Outcome: dialOutcome(v["target_rc"], v["target_out"]), Output: v["target_out"]}, nil
}

// Stop deletes the dial Pods that Start created. It runs also when ctx is done.
func (d *Dialer) Stop(ctx context.Context) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	var errs []error
	for _, k := range d.order {
		p := d.pods[k]
		if _, err := d.Kubectl(d.Region, cleanup, nil, "-n", p.namespace, "delete", "pod", p.name, "--wait=false", "--ignore-not-found"); err != nil {
			errs = append(errs, fmt.Errorf("delete the dial Pod %s/%s: %v", p.namespace, p.name, err))
		}
	}
	return errors.Join(errs...)
}

// dialPodObject is the dial Pod as JSON. It runs on the Pod network, as an
// unprivileged user, with the image of the probe of sec_probe.go, and mounts
// no token: the policies judge it as any Pod of its namespace. With
// hostNetwork it runs on the host network of its node instead, resolving
// through the cluster DNS (ClusterFirstWithHostNet), and the policies judge it
// as that node.
func dialPodObject(p dialPod, s DialSource, run string, hostNetwork bool) ([]byte, error) {
	labels := map[string]string{DialLabel: run}
	for k, v := range s.Labels {
		labels[k] = v
	}
	spec := map[string]any{
		"restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
		"automountServiceAccountToken": false, "enableServiceLinks": false,
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65534, "runAsGroup": 65534,
			"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers": []any{map[string]any{
			"name": "dial", "image": secProbeImage, "command": []string{"sleep", dialPodLife},
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities": map[string]any{"drop": []string{"ALL"}}},
			"resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"},
				"limits": map[string]any{"memory": "64Mi"}},
		}},
	}
	if hostNetwork {
		spec["hostNetwork"], spec["dnsPolicy"] = true, "ClusterFirstWithHostNet"
	}
	return json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": p.name, "namespace": p.namespace, "labels": labels},
		"spec":     spec,
	})
}

// judgeDial judges one row from what its dial gave.
func judgeDial(r DialRow, res DialResult) error {
	if res.Control != DialConnected {
		return fmt.Errorf("the dial Pod in %s did not connect to %s:%d (%s: %s): it has no working network, so the row was not measured",
			r.From.Namespace, dialControlHost, dialControlPort, res.Control, res.ControlOutput)
	}
	switch {
	case r.Expect == "allow" && res.Outcome == DialConnected, r.Expect == "deny" && res.Outcome == DialTimedOut:
		return nil
	case res.Outcome == DialRefused:
		return fmt.Errorf("want %s; the dial of %s was refused: it reached the destination and no listener answered, so the row proves nothing", r.Expect, r.To)
	case res.Outcome == DialUnresolved:
		return fmt.Errorf("want %s; the name %s does not resolve", r.Expect, r.To.Host())
	}
	return fmt.Errorf("want %s; the dial of %s %s (%s)", r.Expect, r.To, res.Outcome, res.Output)
}

type policyDialsCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(policyDialsCheck{kubectl: kube.Region.Run}) }

func (policyDialsCheck) ID() string { return policyDialsID }

func (c policyDialsCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if cc.RepoRoot == "" {
		return errors.New("the dial table is read from the checkout of --repo-root, and the run has none")
	}
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	data, err := os.ReadFile(filepath.Join(cc.RepoRoot, filepath.FromSlash(policyDialsTable)))
	if err != nil {
		return err
	}
	rows, err := ReadDialTable(data)
	if err != nil {
		return fmt.Errorf("%s: %v", policyDialsTable, err)
	}
	sources := make([]DialSource, 0, len(rows))
	for _, r := range rows {
		sources = append(sources, r.From)
	}
	run := strings.ToLower(NewRunID(time.Now()))
	n := 0
	for _, region := range cc.Regions {
		d := &Dialer{Kubectl: c.kubectl, Region: region, Run: run}
		n++
		started := checkStep(rec, policyDialsID, n, "POL: a dial Pod is Ready for every source of the dial table in region "+region.ID, func(ev Evidence) error {
			err := d.Start(ctx, sources)
			ev["block"], ev["region"], ev["rows"], ev["pods"] = "POL", region.ID, len(rows), d.Pods()
			return err
		})
		for _, r := range rows {
			if !started {
				break // no row is dialed when a Pod is not Ready; the step above failed
			}
			n++
			name := fmt.Sprintf("POL %s in region %s: %s to %s is %s", r.Name, region.ID, r.From.Namespace, r.To, r.Expect)
			checkStep(rec, policyDialsID, n, name, func(ev Evidence) error {
				ev["block"], ev["row"], ev["region"], ev["from"], ev["to"], ev["expect"] = "POL", r.Name, region.ID, r.From.key(), r.To.String(), r.Expect
				if r.Expect == "deny" && r.To.Service != "" {
					ready, err := d.ReadyEndpoints(ctx, r.To)
					if err != nil {
						return err
					}
					ev["ready_endpoints"] = ready
					if ready == 0 {
						return fmt.Errorf("the Service %s/%s has no ready endpoint: nothing listens, so a timeout would prove nothing", r.To.Namespace, r.To.Service)
					}
				}
				res, err := d.Dial(ctx, r.From, r.To)
				if err != nil {
					return err
				}
				ev["control"], ev["observed"], ev["output"] = res.Control, res.Outcome, res.Output
				return judgeDial(r, res)
			})
		}
		if len(d.order) > 0 {
			n++
			checkStep(rec, policyDialsID, n, "POL: the dial Pods of region "+region.ID+" are deleted", func(ev Evidence) error {
				ev["pods"] = d.Pods()
				return d.Stop(ctx)
			})
		}
	}
	return nil
}
