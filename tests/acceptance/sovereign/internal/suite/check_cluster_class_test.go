package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// classFake is a cluster for check NET-G17: two nodes, and the answers of a
// healthy reference class unless fault names one thing that is wrong.
type classFake struct {
	t       *testing.T
	fault   string
	pods    map[string]map[string]any // "<region>/<name>" -> Pod, as created
	order   []string                  // the kinds and names created, in order, "<region>/<kind>/<name>"
	deleted []string                  // "<region>/<kind>/<name>"
	ns      string
}

func newClassFake(t *testing.T, fault string) *classFake {
	return &classFake{t: t, fault: fault, pods: map[string]map[string]any{}}
}

func (f *classFake) nodes(region string) []obj {
	out := []obj{}
	for i, name := range []string{"node-1", "node-2"} {
		version := "v1.31.4+k3s1"
		if f.fault == "distribution" && i == 1 {
			version = "v1.32.9-r20"
		}
		labels := obj{"kubernetes.io/hostname": name}
		if i == 0 {
			labels["role"] = "edge"
		}
		out = append(out, obj{"metadata": obj{"name": name, "labels": labels}, "status": obj{"nodeInfo": obj{"kubeletVersion": version},
			"addresses": []obj{{"type": "InternalIP", "address": f.nodeIP(region, i)}}}})
	}
	return out
}

func (f *classFake) nodeIP(region string, i int) string {
	return fmt.Sprintf("192.168.%d.%d", strings.Count(region, "b"), i+1)
}

func (f *classFake) list(items ...obj) []byte { return []byte(kubeList(items...)) }

// podsOf are the Pods created in the namespace of the check, with their status.
func (f *classFake) podsOf(region string) []obj {
	out := []obj{}
	i := 0
	for _, k := range classPodKeys(f.pods) {
		if !strings.HasPrefix(k, region+"/") {
			continue
		}
		i++
		pod := f.pods[k]
		spec, _ := pod["spec"].(map[string]any)
		node, _ := spec["nodeName"].(string)
		if node == "" {
			node = "node-" + fmt.Sprint(1+i%2)
		}
		spec["nodeName"] = node
		host := f.nodeIP(region, map[string]int{"node-1": 0, "node-2": 1}[node])
		ip := fmt.Sprintf("10.%d.0.%d", strings.Count(region, "b"), i)
		if spec["hostNetwork"] == true {
			ip = host
		}
		out = append(out, obj{"metadata": pod["metadata"], "spec": spec, "status": obj{"phase": "Running", "podIP": ip, "hostIP": host}})
	}
	return out
}

func classPodKeys(m map[string]map[string]any) []string {
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// role is the class role of the Pod of region that has name or address ip.
func (f *classFake) role(region, nameOrIP string) string {
	for _, p := range f.podsOf(region) {
		m := kube.Object(p)
		if m.Name() == nameOrIP || m.Str("status", "podIP") == nameOrIP {
			return m.Str("metadata", "labels", classRoleLabel)
		}
	}
	return ""
}

// outcome is what nc gives from the Pod named from to host:port.
func (f *classFake) outcome(region, from, host, port string) string {
	src := f.role(region, from)
	switch {
	case strings.HasPrefix(host, "kube-dns.kube-system"):
		return DialConnected
	case host == fqdnOther && src == "fqdn" && f.fault == "fqdn-open":
		return DialConnected
	case host == fqdnOther && src == "fqdn" && f.fault == "fqdn-l4-only": // the name resolves, L4 drops the dial
		return DialTimedOut
	case host == fqdnOther && src == "fqdn":
		return DialUnresolved
	case host == fqdnOther:
		return map[bool]string{true: DialTimedOut, false: DialConnected}[f.fault == "fqdn-name-down"]
	case host == "127.0.0.1" && port == "10256":
		return map[bool]string{true: DialConnected, false: DialRefused}[f.fault == "kube-proxy" && strings.HasSuffix(from, "-2")]
	case port == "443":
		edge := strings.HasSuffix(host, ".1")
		selected := !strings.HasPrefix(f.fault, "selector") || edge
		return map[bool]string{true: DialConnected, false: DialRefused}[selected || f.fault == "selector-leak"]
	}
	switch f.role(region, host) {
	case "open":
		return DialConnected
	case "k8s":
		return map[bool]string{true: DialConnected, false: DialTimedOut}[f.fault == "k8s-open"]
	case "cilium":
		return map[bool]string{true: DialConnected, false: DialTimedOut}[f.fault == "cilium-open"]
	}
	return DialRefused
}

func ncLine(outcome string) (string, string) {
	switch outcome {
	case DialConnected:
		return "0", "x (x:1) open"
	case DialTimedOut:
		return "1", "nc: x (x:1): Connection timed out"
	case DialUnresolved:
		return "1", "nc: bad address 'x'"
	}
	return "1", "nc: x (x:1): Connection refused"
}

func (f *classFake) exec(region string, args []string, stdin io.Reader) ([]byte, error) {
	pod, i := args[4], slices.Index(args, "--")
	rest := args[i+1:]
	script, _ := io.ReadAll(stdin)
	switch {
	case string(script) == dialScript: // sh -s -- <control host> <port> <host> <port>
		crc, cout := ncLine(f.outcome(region, pod, rest[3], rest[4]))
		trc, tout := ncLine(f.outcome(region, pod, rest[5], rest[6]))
		return []byte(fmt.Sprintf("control_rc=%s\ncontrol_out=%s\ntarget_rc=%s\ntarget_out=%s\n", crc, cout, trc, tout)), nil
	case string(script) == classScript && rest[3] == "dial":
		rc, out := ncLine(f.outcome(region, pod, rest[4], rest[5]))
		return []byte("rc=" + rc + "\nout=" + out + "\n"), nil
	case string(script) == classScript && rest[3] == "http":
		code := 200
		switch url := rest[5]; {
		case strings.HasSuffix(url, "/denied") && f.fault != "l7-open":
			code = 403
		case strings.HasSuffix(url, ":80/") && f.fault == "console-404":
			code = 404
		case strings.HasSuffix(url, ":80/") && rest[4] != "console.t1.omani.works":
			code = 404
		}
		return []byte(fmt.Sprintf("rc=0\nout=  HTTP/1.1 %d X  Content-Length: 3\n", code)), nil
	}
	f.t.Errorf("unexpected exec: %v", args)
	return nil, errors.New("unexpected exec")
}

func (f *classFake) agentExec(region string, args []string) ([]byte, error) {
	switch cmd := args[7:]; { // -n kube-system exec <agent> -c cilium-agent -- <cmd>

	case cmd[0] == "cilium-dbg" && cmd[1] == "status":
		value := "True   [eth0   192.168.0.1 (Direct Routing)]"
		if f.fault == "kpr-false" && args[3] == "cilium-node-2" {
			value = "False"
		}
		return []byte("KVStore:  Disabled\nKubeProxyReplacement:    " + value + "\nHost firewall:  Disabled\n"), nil
	case cmd[0] == "hubble" && cmd[1] == "observe":
		pod := strings.TrimPrefix(cmd[slices.Index(cmd, "--pod")+1], f.ns+"/")
		dst := "class-open-x"
		for _, p := range f.podsOf(region) {
			if m := kube.Object(p); m.Str("metadata", "labels", classRoleLabel) == "open" {
				dst = m.Name()
			}
		}
		if f.fault == "hubble-empty" {
			dst = "elsewhere"
		}
		flow, _ := json.Marshal(obj{"flow": obj{"source": obj{"namespace": f.ns, "pod_name": pod}, "destination": obj{"namespace": f.ns, "pod_name": dst}}})
		return []byte("not json\n" + string(flow) + "\n"), nil
	}
	f.t.Errorf("unexpected agent exec: %v", args)
	return nil, errors.New("unexpected agent exec")
}

func (f *classFake) create(region string, stdin io.Reader) ([]byte, error) {
	var doc map[string]any
	if err := json.NewDecoder(stdin).Decode(&doc); err != nil {
		return nil, err
	}
	items := []any{doc}
	if doc["kind"] == "List" {
		items = doc["items"].([]any)
	}
	for _, x := range items {
		o := kube.Object(x.(map[string]any))
		f.order = append(f.order, region+"/"+o.Str("kind")+"/"+o.Name())
		if o.Str("kind") == "Pod" {
			if f.fault == "create-pod" {
				return nil, errors.New("admission refused the Pod")
			}
			f.pods[region+"/"+o.Name()] = o
		}
	}
	return nil, nil
}

func (f *classFake) kubectl(r kube.Region, ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	region, a := r.ID, strings.Join(args, " ")
	switch {
	case a == "get nodes -o json":
		return f.list(f.nodes(region)...), nil
	case len(args) == 3 && args[0] == "create" && args[1] == "namespace":
		f.ns = args[2]
		f.order = append(f.order, region+"/Namespace/"+args[2])
		return nil, nil
	case a == "create -f -" || strings.HasSuffix(a, " create -f -"):
		return f.create(region, stdin)
	case args[0] == "delete":
		if !strings.HasSuffix(a, "--wait=false --ignore-not-found") {
			f.t.Errorf("unexpected delete: %v", args)
		}
		f.deleted = append(f.deleted, region+"/"+args[1]+"/"+args[2])
		return nil, nil
	case len(args) > 3 && args[2] == "wait":
		if f.fault == "not-ready" && slices.Contains(args, "-l") {
			return nil, errors.New("timed out waiting for the condition")
		}
		return nil, nil
	case a == "-n "+f.ns+" get pods -o json":
		return f.list(f.podsOf(region)...), nil
	case len(args) > 4 && args[0] == "-n" && args[1] == f.ns && args[2] == "exec":
		return f.exec(region, args, stdin)
	case len(args) > 6 && args[1] == "kube-system" && args[2] == "exec":
		return f.agentExec(region, args)
	case a == "-n kube-system get pods -l k8s-app=cilium -o json":
		agents := []obj{}
		for i, n := range []string{"node-1", "node-2"} {
			agents = append(agents, obj{"metadata": obj{"name": "cilium-" + n, "namespace": "kube-system"}, "spec": obj{"nodeName": n, "hostNetwork": true},
				"status": obj{"phase": "Running", "podIP": f.nodeIP(region, i)}})
		}
		return f.list(agents...), nil
	case a == "-n kube-system get pods -o json":
		pods := []obj{{"metadata": obj{"name": "coredns-1", "labels": obj{"k8s-app": "kube-dns"}}}}
		if f.fault == "kube-proxy-pod" {
			pods = append(pods, obj{"metadata": obj{"name": "kube-proxy-abcde", "labels": obj{"k8s-app": "kube-proxy"}}})
		}
		return f.list(pods...), nil
	case a == "-n kube-system get daemonsets -o json":
		return f.list(obj{"metadata": obj{"name": "cilium"}}), nil
	case a == "-n kube-system get daemonset cilium -o json":
		return json.Marshal(f.daemonSet())
	case a == "get helmreleases.helm.toolkit.fluxcd.io -A -o json":
		return f.list(f.helmReleases()...), nil
	case a == "get gatewayclass cilium -o json":
		accepted := map[bool]string{true: "False", false: "True"}[f.fault == "gateway-class"]
		return json.Marshal(obj{"spec": obj{"controllerName": gatewayControllerName}, "status": obj{"conditions": kubeConditions("Accepted", accepted)}})
	case a == "-n kube-system get configmap cilium-config -o json":
		data := obj{hostNetworkKey: "true", hostNetworkNodesKey: ""}
		switch {
		case f.fault == "hostnetwork-off":
			data[hostNetworkKey] = "false"
		case strings.HasPrefix(f.fault, "selector"):
			data[hostNetworkNodesKey] = "role=edge"
		}
		return json.Marshal(obj{"data": data})
	case a == "get pods -A -o json":
		pods := f.podsOf(region)
		pods = append(pods, obj{"metadata": obj{"name": "cilium-node-1", "namespace": "kube-system"}, "spec": obj{"hostNetwork": true},
			"status": obj{"phase": "Running", "podIP": f.nodeIP(region, 0)}})
		pods = append(pods, obj{"metadata": obj{"name": "done", "namespace": "x"}, "spec": obj{}, "status": obj{"phase": "Succeeded", "podIP": "10.9.9.9"}})
		pods = append(pods, obj{"metadata": obj{"name": "dual", "namespace": "x"}, "spec": obj{}, "status": obj{"phase": "Running",
			"podIPs": []obj{{"ip": "10.9.9.8"}, {"ip": "fd00::8"}}}})
		if f.fault == "dup-ip" {
			pods = append(pods, obj{"metadata": obj{"name": "twin", "namespace": "y"}, "spec": obj{}, "status": obj{"phase": "Pending", "podIP": "10.9.9.8"}})
		}
		return f.list(pods...), nil
	}
	f.t.Errorf("unexpected kubectl %v", args)
	return nil, errors.New("unexpected kubectl call")
}

func (f *classFake) daemonSet() obj {
	ready := 2
	if f.fault == "ds-not-ready" {
		ready = 1
	}
	labels := obj{"helm.toolkit.fluxcd.io/name": "cilium", "helm.toolkit.fluxcd.io/namespace": "flux-system"}
	if f.fault == "ds-unlabelled" {
		labels = obj{}
	}
	return obj{"metadata": obj{"name": "cilium", "namespace": "kube-system", "labels": labels,
		"annotations": obj{"meta.helm.sh/release-name": "cilium", "meta.helm.sh/release-namespace": "kube-system"}},
		"spec": obj{"template": obj{"spec": obj{"hostNetwork": true}}}, "status": obj{"desiredNumberScheduled": 2, "numberReady": ready}}
}

func (f *classFake) helmReleases() []obj {
	ready := map[bool]string{true: "False", false: "True"}[f.fault == "hr-not-ready"]
	out := []obj{
		{"metadata": obj{"name": "cilium", "namespace": "flux-system"}, "spec": obj{"releaseName": "cilium", "targetNamespace": "kube-system"},
			"status": obj{"conditions": kubeConditions("Ready", ready)}},
		// release kube-system-bp-other: not the release cilium
		{"metadata": obj{"name": "bp-other", "namespace": "flux-system"}, "spec": obj{"targetNamespace": "kube-system"}},
		// release cilium of the namespace cilium-test: not the one of kube-system
		{"metadata": obj{"name": "cilium", "namespace": "cilium-test"}, "spec": obj{}},
	}
	if f.fault == "hr-two" {
		out = append(out, obj{"metadata": obj{"name": "cilium-copy", "namespace": "kube-system"}, "spec": obj{"releaseName": "cilium"}})
	}
	return out
}

// classPrior are the discovery steps the check reads: D0.1 with the name of
// the Sovereign, and the mesh step D0.10.
func classPrior(meshStatus string) []*Step {
	return []*Step{
		{Journey: "D0", ID: "D0.1", Status: Pass, Evidence: Evidence{"sovereignFQDN": "t1.omani.works"}},
		{Journey: meshJourney, ID: meshStep, Status: meshStatus, Error: "a peer is missing"},
	}
}

// classRun runs check NET-G17 on two regions the way the runner does, with the
// class files of root and stub scripts of FND-G05 and FND-G09.
func classRun(t *testing.T, f *classFake, root, class string, prior []*Step) *runner {
	t.Helper()
	nodePorts, encryption := stubAnswer{out: nodePortClean}, stubAnswer{out: "OK: every agent encrypts as declared"}
	switch f.fault {
	case "nodeport":
		nodePorts = stubAnswer{out: "WARN: cannot read Services"}
	case "encryption":
		encryption = stubAnswer{out: "FAIL: node-2 OptedOut", code: 1}
	}
	stubScript(t, root, nodePortScript, map[string]stubAnswer{"a": nodePorts, "b": nodePorts})
	stubScript(t, root, encryptionScript, map[string]stubAnswer{"a": encryption, "b": encryption})
	// The runner hands the check the steps recorded before it as Prior.
	r := &runner{out: io.Discard, run: &Run{Mode: "run", Steps: prior}}
	c := clusterClassCheck{kubectl: f.kubectl, class: class}
	regions := []kube.Region{{ID: "region-a", Kubeconfig: "a"}, {ID: "region-b", Kubeconfig: "b"}}
	r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(c.ID(), StateRequired)}}, []Check{c},
		&CheckContext{Regions: regions, RepoRoot: root}, true)
	return r
}

// classSteps are the steps of check NET-G17.
func classSteps(r *runner) []*Step {
	out := []*Step{}
	for _, s := range r.run.Steps {
		if s.Journey == clusterClassID {
			out = append(out, s)
		}
	}
	return out
}

func referenceRoot(t *testing.T, edits ...string) string {
	t.Helper()
	body := repoClassFile(t, referenceClass)
	for i := 0; i+1 < len(edits); i += 2 {
		body = strings.Replace(body, edits[i], edits[i+1], 1)
	}
	return classTempRoot(t, map[string]string{referenceClass + ".yaml": body})
}

// failingSteps are the names of the failed steps.
func failingSteps(r *runner) []string {
	out := []string{}
	for _, s := range classSteps(r) {
		if s.Status == Fail {
			out = append(out, s.Name+": "+s.Error)
		}
	}
	return out
}

// TestClusterClassCheck_Profile: on a healthy reference class every probe
// passes; each fault fails the probe of its flag, in both regions, and only
// it; the namespace and the cluster-wide policy are deleted in both regions.
func TestClusterClassCheck_Profile(t *testing.T) {
	if _, ok := registry[clusterClassID]; !ok || clusterClassID != "NET-G17" {
		t.Fatal("check NET-G17 is not registered")
	}
	for fault, want := range map[string]string{
		"":               "",
		"selector":       "",
		"k8s-open":       "policy.k8s: ",
		"cilium-open":    "policy.cilium: ",
		"l7-open":        "policy.l7: ",
		"fqdn-open":      "policy.fqdn: ",
		"fqdn-name-down": "policy.fqdn: ",
		"fqdn-l4-only":   "policy.fqdn: ",
		"kube-proxy":     "dataplane.kubeProxyFree: ",
		"kube-proxy-pod": "dataplane.kubeProxyFree: ",
		"kpr-false":      "dataplane.kubeProxyFree: ",
		"gateway-class":  "gateway.cilium: ",
		"console-404":    "gateway.cilium: ",
		"selector-leak":  "gateway.hostPort: ",
		"encryption":     "encryption.wireguard: ",
		"hubble-empty":   "observability.hubble: ",
		"ds-not-ready":   "node.daemonSets: ",
		"nodeport":       "lb.noNodePort: ",
		"hr-not-ready":   "cilium.platformOwned: ",
		"hr-two":         "cilium.platformOwned: ",
		"ds-unlabelled":  "cilium.platformOwned: ",
		"dup-ip":         "no two Pods in region",
	} {
		t.Run("fault "+fault, func(t *testing.T) {
			f := newClassFake(t, fault)
			r := classRun(t, f, referenceRoot(t), referenceClass, classPrior(Pass))
			failed := failingSteps(r)
			res := r.run.Checks[0]
			if want == "" {
				if res.Status != Pass || len(failed) > 0 {
					t.Fatalf("status %s, failed steps %v", res.Status, failed)
				}
			} else if res.Status != Fail || len(failed) != 2 || !strings.HasPrefix(failed[0], want) || !strings.HasPrefix(failed[1], want) {
				t.Fatalf("want the step %q to fail in both regions and nothing else, got %s %v", want, res.Status, failed)
			}
			// step 1, then per region: distribution, setup, 13 probes, addresses, cleanup
			if len(classSteps(r)) != 1+2*17 {
				t.Errorf("%d steps, want 35", len(classSteps(r)))
			}
			for _, region := range []string{"region-a", "region-b"} {
				for _, kind := range []string{"namespace", "ciliumclusterwidenetworkpolicy"} {
					if !slices.Contains(f.deleted, region+"/"+kind+"/"+f.ns) {
						t.Errorf("%s: the %s %s was not deleted: %v", region, kind, f.ns, f.deleted)
					}
				}
			}
		})
	}
}

// TestClusterClassCheck_WhatItCreates: the policies come before the Pods, the
// namespace is the check's own, a probe Pod runs on the host network of each
// node, and the cluster-wide policy selects only Pods of that namespace.
func TestClusterClassCheck_WhatItCreates(t *testing.T) {
	f := newClassFake(t, "")
	classRun(t, f, referenceRoot(t), referenceClass, classPrior(Pass))
	if !strings.HasPrefix(f.ns, "net-class-") {
		t.Fatalf("namespace %q", f.ns)
	}
	a := []string{}
	for _, o := range f.order {
		if strings.HasPrefix(o, "region-a/") {
			a = append(a, strings.SplitN(strings.TrimPrefix(o, "region-a/"), "/", 2)[0])
		}
	}
	want := []string{"Namespace", "NetworkPolicy", "CiliumNetworkPolicy", "CiliumNetworkPolicy", "CiliumClusterwideNetworkPolicy", "Pod", "Pod", "Pod", "Pod", "Pod", "Pod", "Pod", "Pod"}
	if !slices.Equal(a, want) {
		t.Errorf("created in region a: %v, want %v", a, want)
	}
	ccnp := kube.Object(classPolicies(f.ns)[3].(map[string]any))
	if ccnp.Str("spec", "endpointSelector", "matchLabels", "io.kubernetes.pod.namespace") != f.ns || ccnp.Str("metadata", "name") != f.ns {
		t.Errorf("the cluster-wide policy %v", ccnp)
	}
	host := 0
	for _, k := range classPodKeys(f.pods) {
		p := kube.Object(f.pods[k])
		if p.Get("spec", "hostNetwork") == true {
			host++
			if p.Str("spec", "nodeName") == "" || p.Str("metadata", "labels", classRoleLabel) != "node" {
				t.Errorf("host-network Pod %v", p)
			}
		}
	}
	if host != 4 {
		t.Errorf("%d host-network Pods, want one per node in each region (4)", host)
	}
}

// TestClusterClassCheck_NotApplicable: a flag the class declares false is not
// probed, so a fault there changes nothing, and step 1 names it.
func TestClusterClassCheck_NotApplicable(t *testing.T) {
	root := referenceRoot(t, "mesh.cilium: true", "mesh.cilium: false", "encryption.wireguard: true", "encryption.wireguard: false")
	f := newClassFake(t, "encryption")
	r := classRun(t, f, root, referenceClass, classPrior(Fail))
	if res := r.run.Checks[0]; res.Status != Pass {
		t.Fatalf("status %s %v", res.Status, failingSteps(r))
	}
	if na := classSteps(r)[0].Evidence["not_applicable"]; fmt.Sprint(na) != "[mesh.cilium encryption.wireguard]" {
		t.Errorf("not_applicable %v", na)
	}
	if len(classSteps(r)) != 1+2*15 {
		t.Errorf("%d steps, want 31", len(classSteps(r)))
	}
	for _, s := range classSteps(r) {
		if strings.HasPrefix(s.Name, "mesh.cilium") || strings.HasPrefix(s.Name, "encryption.wireguard") {
			t.Errorf("the flag declared false was probed: %s", s.Name)
		}
	}
	// The control: the same faults with the flags declared true fail both probes.
	f = newClassFake(t, "encryption")
	r = classRun(t, f, referenceRoot(t), referenceClass, classPrior(Fail))
	if failed := failingSteps(r); len(failed) != 4 {
		t.Errorf("want mesh.cilium and encryption.wireguard to fail in both regions, got %v", failed)
	}
}

// TestClusterClassCheck_Refusals: a class that may not host the control plane,
// a broken class file, a region whose nodes run another distribution, a
// probe Pod that is not Ready or refused, and a missing name of the Sovereign.
func TestClusterClassCheck_Refusals(t *testing.T) {
	managed := repoManagedClass(t)
	managedRoot := classTempRoot(t, map[string]string{repoClassPath(t, managed): repoClassFile(t, managed)})
	for name, c := range map[string]struct {
		root, class, fault string
		prior              []*Step
		want               string
		steps              int
	}{
		"managed class":   {managedRoot, managed, "", classPrior(Pass), "declares mayHostControlPlane false", 1},
		"no class file":   {managedRoot, referenceClass, "", classPrior(Pass), "no class file reference-k3s-cilium.yaml", 1},
		"a broken file":   {referenceRoot(t, "kind: reference", "kind: other"), referenceClass, "", classPrior(Pass), "kind: other is not one of", 1},
		"distribution":    {referenceRoot(t), referenceClass, "distribution", classPrior(Pass), "the nodes [node-2] do not run k3s", 1 + 2*2},
		"not ready":       {referenceRoot(t), referenceClass, "not-ready", classPrior(Pass), "the probe Pods are not Ready", 1 + 2*4},
		"refused Pod":     {referenceRoot(t), referenceClass, "create-pod", classPrior(Pass), "admission refused the Pod", 1 + 2*4},
		"no Sovereign":    {referenceRoot(t), referenceClass, "", classPrior(Pass)[1:], "recorded no sovereignFQDN", 1 + 2*17},
		"hostnetwork off": {referenceRoot(t), referenceClass, "hostnetwork-off", classPrior(Pass), "the gateway listeners bind no host port", 1 + 2*17},
	} {
		t.Run(name, func(t *testing.T) {
			f := newClassFake(t, c.fault)
			r := classRun(t, f, c.root, c.class, c.prior)
			res := r.run.Checks[0]
			if res.Status != Fail || !strings.Contains(res.Reason, c.want) || len(classSteps(r)) != c.steps {
				t.Fatalf("status %s, reason %q, %d steps; want FAIL with %q and %d steps", res.Status, res.Reason, len(classSteps(r)), c.want, c.steps)
			}
			created := 0
			for _, o := range f.order {
				if strings.Contains(o, "/Namespace/") {
					created++
				}
			}
			deleted := 0
			for _, d := range f.deleted {
				if strings.Contains(d, "/namespace/") {
					deleted++
				}
			}
			if created != deleted {
				t.Errorf("%d namespaces created, %d deleted", created, deleted)
			}
		})
	}
}
