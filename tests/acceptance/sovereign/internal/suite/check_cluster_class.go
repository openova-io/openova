package suite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G17, the conformance profile of a cluster class (ADR-0037 MC6).
// The profile is one probe per capability flag of the schema of the class
// files (cluster_class.go). The check reads the class of the regions of the run
// and runs, in every region, the probe of every flag that the class declares
// true; a declared true that probes false fails the check. A flag that the
// class declares false is not applicable: its probe is not run, and step 1
// names it in its evidence (not_applicable), as the gate's needs field treats
// a check whose needed flag the run does not declare.
//
// The class of every region of the run is clusterClassCheck.class, the reference
// class: the suite drives the regions of the Sovereign's control plane (region
// A Primary, region B Standby), and only a class that declares
// mayHostControlPlane true may run it (ADR-0037 MC5). Step 1 fails when the
// class file breaks the schema or declares mayHostControlPlane false. A region
// of a managed class joins the run with package NET-329.
//
// Per region the check records: the nodes run the class's distribution; the
// probe objects are in place; one step per flag declared true, in the order
// of the schema; no two Pods share an address (two network plug-ins on one
// cluster hand out the same addresses, DR N14); and the probe objects are
// deleted, also when a step failed.
//
// What it writes, per region: a namespace net-class-<run> of its own, one
// CiliumClusterwideNetworkPolicy of the same name, which selects only Pods of
// that namespace, and in the namespace one NetworkPolicy, two
// CiliumNetworkPolicies, four server Pods, two dial Pods (the Dialer of
// check_policy_dials.go) and one Pod on the host network of every node. It
// creates no Service and no DaemonSet. Everything else it only reads, or runs
// with kubectl exec: nc and wget in its own Pods, cilium-dbg status and hubble
// observe in the Cilium agents, and the scripts of check FND-G05 and FND-G09.
const (
	clusterClassID = "NET-G17"
	referenceClass = "reference-k3s-cilium"
	classRunLabel  = "acceptance.openova.io/class-probe" // its value is the run
	classRoleLabel = "acceptance.openova.io/class-role"
	classPort      = 8080
	classWait      = "180s"

	gatewayControllerName = "io.cilium/gateway-controller"
	hostNetworkKey        = "gateway-api-hostnetwork-enabled"
	hostNetworkNodesKey   = "gateway-api-hostnetwork-nodelabelselector"
	kubeProxyHealthPort   = 10256
	hubbleSocket          = "unix:///var/run/cilium/hubble.sock"
	// The names of the DNS-name probe, absolute so that no search domain is tried.
	fqdnAllowed = "kube-dns.kube-system.svc.cluster.local."
	fqdnOther   = "kubernetes.default.svc.cluster.local."
)

// classServerRoles are the server Pods: open has no policy, each other role
// is selected by the policy of its probe.
var classServerRoles = []string{"open", "k8s", "cilium", "l7"}

// classServerLoop answers every TCP connection on classPort with an HTTP 200.
const classServerLoop = `while true; do printf 'HTTP/1.0 200 OK\r\nContent-Length: 3\r\n\r\nok\n' | nc -l -p 8080 >/dev/null 2>&1; done`

// classScript runs in a probe Pod as `sh -s -- dial <host> <port>` or
// `sh -s -- http <host header> <url>`. It prints the exit code and the output
// of busybox nc or wget; it judges nothing.
const classScript = `case "$1" in
dial) out=$(nc -z -vv -w 3 "$2" "$3" 2>&1); echo "rc=$?" ;;
http) out=$(wget -S -q -O /dev/null -T 5 --header "Host: $2" "$3" 2>&1); echo "rc=$?" ;;
esac
echo "out=$(printf '%s' "$out" | tr '\n' ' ')"
`

var httpStatusRE = regexp.MustCompile(`HTTP/1\.[01] ([0-9]{3})`)

type clusterClassCheck struct {
	kubectl kubectlFunc
	class   string
}

func init() { RegisterCheck(clusterClassCheck{kubectl: kube.Region.Run, class: referenceClass}) }

func (clusterClassCheck) ID() string { return clusterClassID }

// classProbe is the probe of one flag. run fills ev and returns the failure.
type classProbe struct {
	flag, what string
	run        func(ctx context.Context, p *classRegion, ev Evidence) error
}

var classProbes = []classProbe{
	{"policy.k8s", "a Kubernetes NetworkPolicy drops a dial that connects to a Pod without one", probePolicyK8s},
	{"policy.cilium", "a CiliumClusterwideNetworkPolicy that admits only the ingress entity drops a dial from a Pod", probePolicyCilium},
	{"policy.l7", "an HTTP rule answers 403 for a denied path and lets the allowed path through", probePolicyL7},
	{"policy.fqdn", "a DNS-name egress rule lets its name connect and drops another name that connects without it", probePolicyFQDN},
	{"dataplane.kubeProxyFree", "no kube-proxy runs and every Cilium agent replaces it", probeKubeProxyFree},
	{"gateway.cilium", "GatewayClass cilium is accepted and its Gateway answers the console route on a node", probeGatewayCilium},
	{"gateway.hostPort", "port 443 answers on the nodes the gateway binds and on no other node", probeGatewayHostPort},
	{"mesh.cilium", "the mesh step D0.10 of the discovery passed", probeMesh},
	{"encryption.wireguard", "every Cilium agent encrypts node to node with WireGuard as the posture declares", probeEncryption},
	{"observability.hubble", "Hubble holds the flow of a dial between two Pods", probeHubble},
	{"node.daemonSets", "the Cilium DaemonSet runs on the host network, Ready on every node", probeDaemonSets},
	{"lb.noNodePort", "no Service holds a node port", probeNoNodePort},
	{"cilium.platformOwned", "the Helm release cilium is installed by a Flux HelmRelease that is Ready", probePlatformOwned},
}

func probeFor(flag string) (classProbe, bool) {
	for _, p := range classProbes {
		if p.flag == flag {
			return p, true
		}
	}
	return classProbe{}, false
}

// classRegion is the state of the probe in one region.
type classRegion struct {
	kubectl      kubectlFunc
	region       kube.Region
	cc           *CheckContext
	ns, run      string
	dialer       *Dialer
	client, fqdn DialSource
	nodes        []kube.Object
	servers      map[string]kube.Object // role -> server Pod
	onNode       map[string]kube.Object // node name -> host-network probe Pod
	podNode      map[string]string      // Pod name of the namespace -> node name
	agents       []kube.Object          // the Cilium agent Pods, read once
	ciliumDS     kube.Object            // DaemonSet kube-system/cilium, read once
}

func (c clusterClassCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	var class *Class
	var flags []string
	ok := checkStep(rec, clusterClassID, 1, "the class "+c.class+" of the regions of the run is a valid class file that may host the control plane", func(ev Evidence) error {
		schema, err := ReadClassSchema(cc.RepoRoot)
		if err != nil {
			return err
		}
		if class, err = ReadClass(cc.RepoRoot, schema, c.class); err != nil {
			return err
		}
		flags = schema.Flags
		probed, na, unknown := []string{}, []string{}, []string{}
		for _, f := range flags {
			_, has := probeFor(f)
			switch {
			case !class.Capabilities[f]:
				na = append(na, f)
			case !has:
				unknown = append(unknown, f)
			default:
				probed = append(probed, f)
			}
		}
		ev["class"], ev["kind"], ev["distribution"], ev["probed"], ev["not_applicable"] = class.Name, class.Kind, class.Distribution, probed, na
		if len(unknown) > 0 {
			return fmt.Errorf("the class declares %v true, and the suite has no probe for them", unknown)
		}
		if !class.MayHostControlPlane {
			return fmt.Errorf("the class %s declares mayHostControlPlane false, and every region of the run runs the Sovereign's control plane", class.Name)
		}
		return nil
	})
	if !ok {
		return nil
	}
	run := strings.ToLower(NewRunID(time.Now()))
	n := 1
	for _, region := range cc.Regions {
		n = c.runRegion(ctx, cc, rec, region, class, flags, run, n)
	}
	return nil
}

func (c clusterClassCheck) runRegion(ctx context.Context, cc *CheckContext, rec StepRecorder, region kube.Region, class *Class, flags []string, run string, n int) int {
	ns := "net-class-" + run
	p := &classRegion{kubectl: c.kubectl, region: region, cc: cc, ns: ns, run: run,
		dialer: &Dialer{Kubectl: c.kubectl, Region: region, Run: run},
		client: DialSource{Namespace: ns, Labels: map[string]string{classRoleLabel: "client"}},
		fqdn:   DialSource{Namespace: ns, Labels: map[string]string{classRoleLabel: "fqdn"}}}
	in := " in region " + region.ID
	n++
	nodesRead := checkStep(rec, clusterClassID, n, "every node"+in+" runs the distribution "+class.Distribution, func(ev Evidence) error {
		var err error
		if p.nodes, err = p.list(ctx, "", "nodes"); err != nil {
			return err
		}
		return judgeDistribution(class.Distribution, p.nodes, ev)
	})
	created := false
	n++
	started := nodesRead && checkStep(rec, clusterClassID, n, "the probe Pods and policies are in place in the namespace "+ns+in, func(ev Evidence) error {
		ev["namespace"] = ns
		if _, err := c.kubectl(region, ctx, nil, "create", "namespace", ns); err != nil {
			return err
		}
		created = true // a set-up that fails half way may leave the cluster-wide policy
		return p.setUp(ctx, ev)
	})
	if started {
		for _, f := range flags {
			probe, _ := probeFor(f)
			if !class.Capabilities[f] {
				continue
			}
			n++
			checkStep(rec, clusterClassID, n, f+": "+probe.what+in, func(ev Evidence) error {
				ev["flag"], ev["region"] = f, region.ID
				return probe.run(ctx, p, ev)
			})
		}
	}
	n++
	checkStep(rec, clusterClassID, n, "no two Pods"+in+" share an address", func(ev Evidence) error {
		pods, err := p.list(ctx, "", "pods", "-A")
		if err != nil {
			return err
		}
		return judgePodAddresses(pods, ev)
	})
	if created {
		n++
		checkStep(rec, clusterClassID, n, "the namespace "+ns+" and the CiliumClusterwideNetworkPolicy "+ns+in+" are deleted", func(Evidence) error {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			var errs []error
			if _, err := c.kubectl(region, cleanup, nil, "delete", "ciliumclusterwidenetworkpolicy", ns, "--wait=false", "--ignore-not-found"); err != nil {
				errs = append(errs, err)
			}
			if _, err := c.kubectl(region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found"); err != nil {
				errs = append(errs, err)
			}
			return errors.Join(errs...)
		})
	}
	return n
}

// distributionRules say how a node shows its distribution.
var distributionRules = map[string]func(node kube.Object) bool{
	"k3s": func(node kube.Object) bool {
		return strings.Contains(node.Str("status", "nodeInfo", "kubeletVersion"), "+k3s")
	},
}

func judgeDistribution(distribution string, nodes []kube.Object, ev Evidence) error {
	rule, ok := distributionRules[distribution]
	if !ok {
		return fmt.Errorf("the suite has no rule to tell distribution %q from a node", distribution)
	}
	versions, other := map[string]string{}, []string{}
	for _, node := range nodes {
		versions[node.Name()] = node.Str("status", "nodeInfo", "kubeletVersion")
		if !rule(node) {
			other = append(other, node.Name())
		}
	}
	ev["kubelet_versions"] = versions
	switch {
	case len(nodes) == 0:
		return errors.New("the region has no node")
	case len(other) > 0:
		return fmt.Errorf("the nodes %v do not run %s", other, distribution)
	}
	return nil
}

// judgePodAddresses fails when two Pods that are not on the host network and
// have not finished hold one address.
func judgePodAddresses(pods []kube.Object, ev Evidence) error {
	holders := map[string][]string{}
	for _, pod := range pods {
		phase := pod.Str("status", "phase")
		if pod.Get("spec", "hostNetwork") == true || phase == "Succeeded" || phase == "Failed" {
			continue
		}
		ips := []string{}
		for _, x := range pod.Objects("status", "podIPs") {
			ips = append(ips, x.Str("ip"))
		}
		if len(ips) == 0 && pod.Str("status", "podIP") != "" {
			ips = append(ips, pod.Str("status", "podIP"))
		}
		for _, ip := range ips {
			holders[ip] = append(holders[ip], pod.Namespace()+"/"+pod.Name())
		}
	}
	shared := map[string][]string{}
	for ip, who := range holders {
		if len(who) > 1 {
			shared[ip] = who
		}
	}
	ev["addresses"], ev["shared"] = len(holders), shared
	switch {
	case len(holders) == 0:
		return errors.New("no Pod address was read")
	case len(shared) > 0:
		return fmt.Errorf("two Pods or more share an address: %v", shared)
	}
	return nil
}

// setUp creates the policies first, so that every probe Pod starts under
// them, then the Pods, and waits until every Pod is Ready.
func (p *classRegion) setUp(ctx context.Context, ev Evidence) error {
	if err := p.create(ctx, classPolicies(p.ns)); err != nil {
		return err
	}
	pods := []any{}
	for _, role := range classServerRoles {
		pods = append(pods, classPod(p.ns, "class-"+role+"-"+p.run, p.run, role, "", []string{"sh", "-c", classServerLoop}))
	}
	for i, node := range p.nodes {
		pods = append(pods, classPod(p.ns, "class-node-"+p.run+"-"+strconv.Itoa(i+1), p.run, "node", node.Name(), []string{"sleep", dialPodLife}))
	}
	if err := p.create(ctx, pods); err != nil {
		return err
	}
	if err := p.dialer.Start(ctx, []DialSource{p.client, p.fqdn}); err != nil {
		return err
	}
	if _, err := p.kubectl(p.region, ctx, nil, "-n", p.ns, "wait", "--for=condition=Ready", "pod", "-l", classRunLabel+"="+p.run, "--timeout="+classWait); err != nil {
		return fmt.Errorf("the probe Pods are not Ready: %v", err)
	}
	all, err := p.list(ctx, p.ns, "pods")
	if err != nil {
		return err
	}
	p.servers, p.onNode, p.podNode = map[string]kube.Object{}, map[string]kube.Object{}, map[string]string{}
	for _, pod := range all {
		p.podNode[pod.Name()] = pod.Str("spec", "nodeName")
		switch role := pod.Str("metadata", "labels", classRoleLabel); {
		case pod.Str("metadata", "labels", classRunLabel) != p.run:
		case role == "node":
			p.onNode[pod.Str("spec", "nodeName")] = pod
		case slices.Contains(classServerRoles, role):
			p.servers[role] = pod
		}
	}
	ev["servers"], ev["node_probes"], ev["dial_pods"] = len(p.servers), len(p.onNode), p.dialer.Pods()
	for _, role := range classServerRoles {
		if p.servers[role].Str("status", "podIP") == "" {
			return fmt.Errorf("the server Pod %s has no address", role)
		}
	}
	for _, node := range p.nodes {
		if p.onNode[node.Name()].Str("status", "hostIP") == "" {
			return fmt.Errorf("no probe Pod with a host address runs on the node %s", node.Name())
		}
	}
	return nil
}

// classPolicies are the policies of the probes, the cluster-wide one last.
func classPolicies(ns string) []any {
	role := func(r string) map[string]any { return map[string]any{"matchLabels": map[string]any{classRoleLabel: r}} }
	port := func(n, proto string) map[string]any { return map[string]any{"port": n, "protocol": proto} }
	return []any{
		map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "class-k8s", "namespace": ns},
			"spec": map[string]any{"podSelector": role("k8s"), "policyTypes": []string{"Ingress"}}},
		map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy", "metadata": map[string]any{"name": "class-l7", "namespace": ns},
			"spec": map[string]any{"endpointSelector": role("l7"), "ingress": []any{map[string]any{
				"fromEndpoints": []any{role("client")},
				"toPorts": []any{map[string]any{"ports": []any{port(strconv.Itoa(classPort), "TCP")},
					"rules": map[string]any{"http": []any{map[string]any{"method": "GET", "path": "/allowed"}}}}}}}}},
		map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumNetworkPolicy", "metadata": map[string]any{"name": "class-fqdn", "namespace": ns},
			"spec": map[string]any{"endpointSelector": role("fqdn"), "egress": []any{map[string]any{
				"toEndpoints": []any{map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": "kube-system", "k8s-app": "kube-dns"}}},
				"toPorts": []any{map[string]any{"ports": []any{port("53", "UDP"), port("53", "TCP")},
					"rules": map[string]any{"dns": []any{map[string]any{"matchName": strings.TrimSuffix(fqdnAllowed, ".")}}}}}}}}},
		map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumClusterwideNetworkPolicy", "metadata": map[string]any{"name": ns},
			"spec": map[string]any{
				"endpointSelector": map[string]any{"matchLabels": map[string]any{"io.kubernetes.pod.namespace": ns, classRoleLabel: "cilium"}},
				"ingress":          []any{map[string]any{"fromEntities": []string{"ingress"}}}}},
	}
}

// classPod is a probe Pod, built like the dial Pod of check_policy_dials.go.
// With a node it runs on the host network of that node and tolerates every
// taint, so that one runs on every node.
func classPod(ns, name, run, role, node string, command []string) map[string]any {
	spec := map[string]any{
		"restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
		"automountServiceAccountToken": false, "enableServiceLinks": false,
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 65534, "runAsGroup": 65534,
			"seccompProfile": map[string]any{"type": "RuntimeDefault"}},
		"containers": []any{map[string]any{
			"name": "probe", "image": secProbeImage, "command": command,
			"securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities": map[string]any{"drop": []string{"ALL"}}},
			"resources": map[string]any{"requests": map[string]any{"cpu": "10m", "memory": "16Mi"},
				"limits": map[string]any{"memory": "64Mi"}},
		}},
	}
	if node != "" {
		spec["nodeName"], spec["hostNetwork"], spec["dnsPolicy"] = node, true, "ClusterFirstWithHostNet"
		spec["tolerations"] = []any{map[string]any{"operator": "Exists"}}
	}
	return map[string]any{"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": map[string]any{classRunLabel: run, classRoleLabel: role}},
		"spec":     spec}
}

func (p *classRegion) create(ctx context.Context, items []any) error {
	b, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	if err != nil {
		return err
	}
	_, err = p.kubectl(p.region, ctx, bytes.NewReader(b), "create", "-f", "-")
	return err
}

// list reads a list with kubectl get -o json; namespace "" reads a cluster-scoped kind or, with -A, every namespace.
func (p *classRegion) list(ctx context.Context, namespace, resource string, extra ...string) ([]kube.Object, error) {
	args := []string{}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	args = append(append(append(args, "get", resource), extra...), "-o", "json")
	out, err := p.kubectl(p.region, ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var l struct {
		Items []kube.Object `json:"items"`
	}
	if err := json.Unmarshal(out, &l); err != nil {
		return nil, fmt.Errorf("[%s] kubectl get %s: not JSON: %v", p.region.ID, resource, err)
	}
	return l.Items, nil
}

// one reads one object with kubectl get -o json; namespace "" reads a cluster-scoped kind.
func (p *classRegion) one(ctx context.Context, namespace, resource, name string) (kube.Object, error) {
	args := []string{"get", resource, name, "-o", "json"}
	if namespace != "" {
		args = append([]string{"-n", namespace}, args...)
	}
	out, err := p.kubectl(p.region, ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	var o kube.Object
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("[%s] kubectl get %s %s: not JSON: %v", p.region.ID, resource, name, err)
	}
	return o, nil
}

// probe runs classScript in a Pod of the namespace and returns its exit code and output.
func (p *classRegion) probe(ctx context.Context, pod, container string, args ...string) (rc, out string, err error) {
	b, err := p.kubectl(p.region, ctx, strings.NewReader(classScript), append([]string{"-n", p.ns, "exec", "-i", pod, "-c", container, "--", "sh", "-s", "--"}, args...)...)
	if err != nil {
		return "", "", fmt.Errorf("run the probe in %s/%s: %v", p.ns, pod, err)
	}
	v := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, val, found := strings.Cut(line, "="); found {
			v[k] = strings.TrimSpace(val)
		}
	}
	return v["rc"], v["out"], nil
}

// httpStatus is the status of an answer of wget -S, or 0 when there was none.
func (p *classRegion) httpStatus(ctx context.Context, pod, container, host, url string) (int, string, error) {
	_, out, err := p.probe(ctx, pod, container, "http", host, url)
	if err != nil {
		return 0, "", err
	}
	if m := httpStatusRE.FindStringSubmatch(out); m != nil {
		code, _ := strconv.Atoi(m[1])
		return code, out, nil
	}
	return 0, out, nil
}

func (p *classRegion) dialPod(s DialSource) string { return p.dialer.pods[s.key()].name }

func (p *classRegion) serverTarget(role string) DialTarget {
	return DialTarget{Address: p.servers[role].Str("status", "podIP"), Port: classPort}
}

// openThenShut dials the server without a policy, which must connect, and the
// server of role, which a policy of the probe selects, which must time out.
func (p *classRegion) openThenShut(ctx context.Context, ev Evidence, role string) error {
	for _, d := range []struct{ role, expect string }{{"open", "allow"}, {role, "deny"}} {
		to := p.serverTarget(d.role)
		res, err := p.dialer.Dial(ctx, p.client, to)
		if err != nil {
			return err
		}
		ev[d.role+"_server"] = res.Outcome
		if err := judgeDial(DialRow{Name: d.role, From: p.client, To: to, Expect: d.expect}, res); err != nil {
			return fmt.Errorf("the %s server: %v", d.role, err)
		}
	}
	return nil
}

func probePolicyK8s(ctx context.Context, p *classRegion, ev Evidence) error {
	return p.openThenShut(ctx, ev, "k8s")
}

func probePolicyCilium(ctx context.Context, p *classRegion, ev Evidence) error {
	return p.openThenShut(ctx, ev, "cilium")
}

func probePolicyL7(ctx context.Context, p *classRegion, ev Evidence) error {
	ip := p.servers["l7"].Str("status", "podIP")
	for _, c := range []struct {
		path string
		want int
	}{{"/allowed", 200}, {"/denied", 403}} {
		code, out, err := p.httpStatus(ctx, p.dialPod(p.client), "dial", ip, "http://"+net.JoinHostPort(ip, strconv.Itoa(classPort))+c.path)
		if err != nil {
			return err
		}
		ev[c.path] = code
		if code != c.want {
			return fmt.Errorf("GET %s answered %d, want %d (%s)", c.path, code, c.want, out)
		}
	}
	return nil
}

// probePolicyFQDN: the DNS-name rule of the policy class-fqdn names only
// fqdnAllowed, so Cilium's DNS proxy answers that name to the Pod it selects
// and refuses every other. The other name connects from the client Pod, which
// no policy selects, so the refusal is the rule's. Only an unresolved name
// passes: the policy opens nothing but kube-dns:53, so a dial to the other
// name times out under the L3/L4 rule even where the DNS rule is ignored.
func probePolicyFQDN(ctx context.Context, p *classRegion, ev Evidence) error {
	for _, c := range []struct {
		from DialSource
		key  string
		ok   func(string) bool
		want string
	}{
		{p.client, "without_rule", func(o string) bool { return o == DialConnected }, "connected"},
		{p.fqdn, "with_rule", func(o string) bool { return o == DialUnresolved }, "unresolved"},
	} {
		res, err := p.dialNames(ctx, c.from, fqdnOther, 443)
		if err != nil {
			return err
		}
		ev[c.key+"_allowed_name"], ev[c.key+"_other_name"] = res.Control, res.Outcome
		if res.Control != DialConnected {
			return fmt.Errorf("from %s the allowed name %s:53 %s (%s), want connected", c.from.key(), fqdnAllowed, res.Control, res.ControlOutput)
		}
		if !c.ok(res.Outcome) {
			return fmt.Errorf("from %s the name %s:443 %s (%s), want %s", c.from.key(), fqdnOther, res.Outcome, res.Output, c.want)
		}
	}
	return nil
}

// dialNames dials fqdnAllowed on port 53, then host, from the dial Pod of s,
// again while the first dial does not connect, as Dialer.Dial does.
func (p *classRegion) dialNames(ctx context.Context, s DialSource, host string, port int) (DialResult, error) {
	deadline := time.Now().Add(dialNetworkWait)
	for {
		b, err := p.kubectl(p.region, ctx, strings.NewReader(dialScript), "-n", p.ns, "exec", "-i", p.dialPod(s), "-c", "dial", "--",
			"sh", "-s", "--", fqdnAllowed, "53", host, strconv.Itoa(port))
		if err != nil {
			return DialResult{}, fmt.Errorf("run the dial in %s/%s: %v", p.ns, p.dialPod(s), err)
		}
		v := map[string]string{}
		for _, line := range strings.Split(string(b), "\n") {
			if k, val, found := strings.Cut(line, "="); found {
				v[k] = strings.TrimSpace(val)
			}
		}
		res := DialResult{Control: dialOutcome(v["control_rc"], v["control_out"]), ControlOutput: v["control_out"],
			Outcome: dialOutcome(v["target_rc"], v["target_out"]), Output: v["target_out"]}
		if res.Control == DialConnected || !time.Now().Before(deadline) {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, nil
		case <-time.After(dialNetworkEvery):
		}
	}
}

// nodeDial dials address:port from the host-network probe Pod of node.
func (p *classRegion) nodeDial(ctx context.Context, node, address string, port int) (string, error) {
	rc, out, err := p.probe(ctx, p.onNode[node].Name(), "probe", "dial", address, strconv.Itoa(port))
	if err != nil {
		return "", err
	}
	return dialOutcome(rc, out), nil
}

func probeKubeProxyFree(ctx context.Context, p *classRegion, ev Evidence) error {
	found := []string{}
	for _, kind := range []string{"pods", "daemonsets"} {
		objs, err := p.list(ctx, "kube-system", kind)
		if err != nil {
			return err
		}
		for _, o := range objs {
			if strings.HasPrefix(o.Name(), "kube-proxy") || o.Str("metadata", "labels", "k8s-app") == "kube-proxy" {
				found = append(found, kind+"/"+o.Name())
			}
		}
	}
	listening := map[string]string{}
	for _, node := range p.nodes {
		outcome, err := p.nodeDial(ctx, node.Name(), "127.0.0.1", kubeProxyHealthPort)
		if err != nil {
			return err
		}
		if outcome != DialRefused {
			listening[node.Name()] = outcome
		}
	}
	agents, err := p.ciliumAgents(ctx)
	if err != nil {
		return err
	}
	notReplacing := map[string]string{}
	for _, a := range agents {
		out, err := p.kubectl(p.region, ctx, nil, "-n", "kube-system", "exec", a.Name(), "-c", "cilium-agent", "--", "cilium-dbg", "status")
		if err != nil {
			return err
		}
		if line := statusLine(string(out), "KubeProxyReplacement:"); !strings.HasPrefix(line, "True") {
			notReplacing[a.Name()] = line
		}
	}
	ev["kube_proxy_objects"], ev["health_port_not_refused"], ev["agents"], ev["agents_not_replacing"] = found, listening, len(agents), notReplacing
	switch {
	case len(found) > 0:
		return fmt.Errorf("kube-proxy runs in kube-system: %v", found)
	case len(listening) > 0:
		return fmt.Errorf("on the nodes %v the kube-proxy health port 127.0.0.1:%d is not refused: kube-proxy runs there", listening, kubeProxyHealthPort)
	case len(notReplacing) > 0:
		return fmt.Errorf("the Cilium agents %v do not report KubeProxyReplacement True", notReplacing)
	}
	return nil
}

// statusLine is what follows key on its line of cilium-dbg status, trimmed.
func statusLine(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), key) {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), key))
		}
	}
	return "(no line " + key + ")"
}

func (p *classRegion) ciliumAgents(ctx context.Context) ([]kube.Object, error) {
	if p.agents != nil {
		return p.agents, nil
	}
	agents, err := p.list(ctx, "kube-system", "pods", "-l", "k8s-app=cilium")
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, errors.New("kube-system runs no Pod labelled k8s-app=cilium")
	}
	p.agents = agents
	return agents, nil
}

// gatewayNodes are the nodes whose host ports the gateway listeners bind, and
// the other nodes, from cilium-config: the host network must be on, and the
// label selector, "k=v,k=v" or empty for every node, chooses the nodes.
func (p *classRegion) gatewayNodes(ctx context.Context, ev Evidence) (chosen, others []string, err error) {
	cm, err := p.one(ctx, "kube-system", "configmap", "cilium-config")
	if err != nil {
		return nil, nil, err
	}
	enabled, selector := cm.Str("data", hostNetworkKey), cm.Str("data", hostNetworkNodesKey)
	ev[hostNetworkKey], ev[hostNetworkNodesKey] = enabled, selector
	if enabled != "true" {
		return nil, nil, fmt.Errorf("cilium-config has %s=%q: the gateway listeners bind no host port", hostNetworkKey, enabled)
	}
	want := map[string]string{}
	for _, pair := range splitList(selector) {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			return nil, nil, fmt.Errorf("cilium-config %s=%q is not k=v,k=v", hostNetworkNodesKey, selector)
		}
		want[k] = v
	}
	for _, node := range p.nodes {
		match := true
		for k, v := range want {
			match = match && node.Str("metadata", "labels", k) == v
		}
		if match {
			chosen = append(chosen, node.Name())
		} else {
			others = append(others, node.Name())
		}
	}
	ev["chosen_nodes"], ev["other_nodes"] = chosen, others
	if len(chosen) == 0 {
		return nil, nil, fmt.Errorf("no node matches the gateway's node selector %q", selector)
	}
	return chosen, others, nil
}

func probeGatewayCilium(ctx context.Context, p *classRegion, ev Evidence) error {
	gc, err := p.one(ctx, "", "gatewayclass", "cilium")
	if err != nil {
		return err
	}
	status, reason, _, _ := gc.Condition("Accepted")
	ev["controller"], ev["accepted"] = gc.Str("spec", "controllerName"), status+" "+reason
	if gc.Str("spec", "controllerName") != gatewayControllerName || status != "True" {
		return fmt.Errorf("GatewayClass cilium has controller %q and Accepted %s, want %s and True", gc.Str("spec", "controllerName"), status, gatewayControllerName)
	}
	fqdn := sovereignFQDN(p.cc.Prior)
	if fqdn == "" {
		return errors.New("the run did not learn the name of the Sovereign: step D0.1 recorded no sovereignFQDN")
	}
	chosen, _, err := p.gatewayNodes(ctx, ev)
	if err != nil {
		return err
	}
	host, probe := "console."+fqdn, p.onNode[chosen[0]]
	code, out, err := p.httpStatus(ctx, probe.Name(), "probe", host, "http://"+net.JoinHostPort(probe.Str("status", "hostIP"), "80")+"/")
	if err != nil {
		return err
	}
	ev["host"], ev["node"], ev["status"] = host, chosen[0], code
	if !slices.Contains(openCodes, code) {
		return fmt.Errorf("GET http://%s/ on port 80 of the node %s answered %d, want one of %v (%s)", host, chosen[0], code, openCodes, out)
	}
	return nil
}

func probeGatewayHostPort(ctx context.Context, p *classRegion, ev Evidence) error {
	chosen, others, err := p.gatewayNodes(ctx, ev)
	if err != nil {
		return err
	}
	wrong := map[string]string{}
	for _, set := range []struct {
		nodes []string
		want  string
	}{{chosen, DialConnected}, {others, DialRefused}} {
		for _, node := range set.nodes {
			outcome, err := p.nodeDial(ctx, node, p.onNode[node].Str("status", "hostIP"), 443)
			if err != nil {
				return err
			}
			if outcome != set.want {
				wrong[node] = outcome + ", want " + set.want
			}
		}
	}
	ev["wrong"] = wrong
	if len(wrong) > 0 {
		return fmt.Errorf("port 443 of the node's own address: %v", wrong)
	}
	return nil
}

func probeMesh(_ context.Context, p *classRegion, ev Evidence) error {
	return priorPassed(p.cc.Prior, func(s *Step) bool { return s.Journey == meshJourney && s.ID == meshStep }, "the mesh step of the discovery", ev)
}

func probeEncryption(ctx context.Context, p *classRegion, ev Evidence) error {
	code, out, err := runRepoScript(ctx, p.cc.RepoRoot, ev, encryptionScript, "--kubeconfig", p.region.Kubeconfig)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("%s exited %d: %s", encryptionScript, code, lastLine(out))
	}
	return nil
}

func probeNoNodePort(ctx context.Context, p *classRegion, ev Evidence) error {
	code, out, err := runRepoScript(ctx, p.cc.RepoRoot, ev, nodePortScript, "--kubeconfig", p.region.Kubeconfig)
	if err != nil {
		return err
	}
	if code != 0 || !strings.Contains(out, nodePortVerdict) {
		return fmt.Errorf("%s exited %d without its verdict line %q: %s", nodePortScript, code, strings.TrimSpace(nodePortVerdict), lastLine(out))
	}
	return nil
}

// probeHubble dials the open server from the client and asks the Hubble of the
// client's node for the flows of the client.
func probeHubble(ctx context.Context, p *classRegion, ev Evidence) error {
	to := p.serverTarget("open")
	res, err := p.dialer.Dial(ctx, p.client, to)
	if err != nil {
		return err
	}
	if err := judgeDial(DialRow{Name: "hubble", From: p.client, To: to, Expect: "allow"}, res); err != nil {
		return err
	}
	client, server := p.dialPod(p.client), p.servers["open"].Name()
	agents, err := p.ciliumAgents(ctx)
	if err != nil {
		return err
	}
	agent := ""
	for _, a := range agents {
		if a.Str("spec", "nodeName") == p.podNode[client] {
			agent = a.Name()
		}
	}
	if agent == "" {
		return fmt.Errorf("no Cilium agent runs on the node %q of the dial Pod %s", p.podNode[client], client)
	}
	out, err := p.kubectl(p.region, ctx, nil, "-n", "kube-system", "exec", agent, "-c", "cilium-agent", "--",
		"hubble", "observe", "--server", hubbleSocket, "--last", "1000", "--pod", p.ns+"/"+client, "-o", "json")
	if err != nil {
		return err
	}
	flows, matched := 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		var f struct {
			Flow struct {
				Source      hubbleEndpoint `json:"source"`
				Destination hubbleEndpoint `json:"destination"`
			} `json:"flow"`
		}
		if json.Unmarshal([]byte(line), &f) != nil {
			continue
		}
		flows++
		src, dst := f.Flow.Source, f.Flow.Destination
		if src.Namespace == p.ns && src.PodName == client && dst.Namespace == p.ns && dst.PodName == server {
			matched++
		}
	}
	ev["agent"], ev["flows"], ev["flows_client_to_server"] = agent, flows, matched
	if matched == 0 {
		return fmt.Errorf("the Hubble of %s holds %d flows of %s, none from it to %s", agent, flows, client, server)
	}
	return nil
}

// hubbleEndpoint is the source or the destination of a flow of hubble observe -o json.
type hubbleEndpoint struct {
	Namespace string `json:"namespace"`
	PodName   string `json:"pod_name"`
}

func (p *classRegion) ciliumDaemonSet(ctx context.Context) (kube.Object, error) {
	if p.ciliumDS != nil {
		return p.ciliumDS, nil
	}
	ds, err := p.one(ctx, "kube-system", "daemonset", "cilium")
	if err == nil {
		p.ciliumDS = ds
	}
	return ds, err
}

func probeDaemonSets(ctx context.Context, p *classRegion, ev Evidence) error {
	ds, err := p.ciliumDaemonSet(ctx)
	if err != nil {
		return err
	}
	host := ds.Get("spec", "template", "spec", "hostNetwork") == true
	desired, ready := ds.Int("status", "desiredNumberScheduled"), ds.Int("status", "numberReady")
	ev["host_network"], ev["desired"], ev["ready"], ev["nodes"] = host, desired, ready, len(p.nodes)
	if !host || desired != len(p.nodes) || ready != desired {
		return fmt.Errorf("DaemonSet kube-system/cilium: hostNetwork %t, %d scheduled, %d Ready, want true and both equal to the %d nodes", host, desired, ready, len(p.nodes))
	}
	return nil
}

// probePlatformOwned: exactly one HelmRelease makes the release cilium of
// kube-system, it is Ready, and the DaemonSet cilium carries the labels Flux
// sets on what its release made and the annotations of that Helm release.
func probePlatformOwned(ctx context.Context, p *classRegion, ev Evidence) error {
	hrs, err := p.list(ctx, "", "helmreleases.helm.toolkit.fluxcd.io", "-A")
	if err != nil {
		return err
	}
	owners := []kube.Object{}
	for _, hr := range hrs {
		release, ns := hr.Str("spec", "releaseName"), hr.Str("spec", "targetNamespace")
		switch {
		case release != "":
		case ns != "":
			release = ns + "-" + hr.Name()
		default:
			release = hr.Name()
		}
		if ns == "" {
			ns = hr.Namespace()
		}
		if release == "cilium" && ns == "kube-system" {
			owners = append(owners, hr)
		}
	}
	names := []string{}
	for _, hr := range owners {
		names = append(names, hr.Namespace()+"/"+hr.Name())
	}
	ev["helmreleases"] = names
	if len(owners) != 1 {
		return fmt.Errorf("%d HelmReleases make the release cilium of kube-system (%v), want 1", len(owners), names)
	}
	hr := owners[0]
	if status, reason, msg, _ := hr.Condition("Ready"); status != "True" {
		return fmt.Errorf("HelmRelease %s is not Ready: %s %s %s", names[0], status, reason, msg)
	}
	ds, err := p.ciliumDaemonSet(ctx)
	if err != nil {
		return err
	}
	got := map[string]string{
		"meta.helm.sh/release-name": ds.Str("metadata", "annotations", "meta.helm.sh/release-name"), "meta.helm.sh/release-namespace": ds.Str("metadata", "annotations", "meta.helm.sh/release-namespace"),
		"helm.toolkit.fluxcd.io/name": ds.Str("metadata", "labels", "helm.toolkit.fluxcd.io/name"), "helm.toolkit.fluxcd.io/namespace": ds.Str("metadata", "labels", "helm.toolkit.fluxcd.io/namespace"),
	}
	want := map[string]string{"meta.helm.sh/release-name": "cilium", "meta.helm.sh/release-namespace": "kube-system",
		"helm.toolkit.fluxcd.io/name": hr.Name(), "helm.toolkit.fluxcd.io/namespace": hr.Namespace()}
	ev["daemonset_owner"] = got
	for k, v := range want {
		if got[k] != v {
			return fmt.Errorf("DaemonSet kube-system/cilium has %s=%q, want %q", k, got[k], v)
		}
	}
	return nil
}
