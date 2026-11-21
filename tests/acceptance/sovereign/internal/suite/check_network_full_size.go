package suite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G18, full-size transfers between workers over the node network.
//
// Between two workers every packet goes through node-to-node encryption, and
// the receiving host hands the decrypted packet to cilium_host. When the MTU
// that Cilium runs with is below the MTU of the node's interface, every
// packet larger than it is dropped there without a word: small requests pass,
// a transfer of more than one full-size packet never ends. The control plane
// opts out of node encryption, so a transfer from it passes either way.
//
// In every region the check chooses two workers: Ready, schedulable, without
// the label node-role.kubernetes.io/control-plane, the first two by name;
// fewer than two fail the region as not measured. In a namespace
// net-path-<run> of its own it starts one probe Pod on each, on the host
// network, as user 65534 with a read-only root, no capability, no volume and
// no token; it creates no Service. Then, with `kubectl exec`:
//
//	(a) on each worker the interface that holds the node's InternalIP and
//	    its MTU, and the MTU of cilium_host, are read from /sys/class/net;
//	    they must be equal (cilium_wg0 and cilium-config's mtu are evidence);
//	(b) each worker fetches http://<InternalIP>:9965/metrics, the Hubble
//	    metrics listener that the Cilium agent serves on the host network of
//	    every node (hubble.metrics.port, the default of Cilium 1.19.3 in its
//	    install/kubernetes/cilium/values.yaml line 1442), of the other
//	    worker with busybox wget under timeout 5: the GET passes with exit
//	    code 0 and at least 16384 bytes. Before it, the same GET of the first
//	    Ready control-plane node by name of the destination's region must
//	    pass, else the pair fails as not measured and the worker is not
//	    fetched;
//	(c) the first chosen worker of every region does the same with the first
//	    chosen worker of every other region.
//
// The namespace is deleted at the end, also when a step failed. Nothing of
// the install changes.
const (
	fullSizeID       = "NET-G18"
	fullSizeRole     = "node-role.kubernetes.io/control-plane"
	fullSizePort     = 9965
	fullSizeMinBytes = 16384
	fullSizeSeconds  = 5
	fullSizeNet      = "/sys/class/net"
)

// fullSizePod is a probe Pod as JSON: its name, namespace, node and image.
const fullSizePod = `{"apiVersion": "v1", "kind": "Pod",
  "metadata": {"name": %q, "namespace": %q, "labels": {"app.kubernetes.io/name": "net-path-probe"}},
  "spec": {"nodeName": %q, "hostNetwork": true, "restartPolicy": "Never", "terminationGracePeriodSeconds": 1,
    "automountServiceAccountToken": false, "enableServiceLinks": false, "tolerations": [{"operator": "Exists"}],
    "containers": [{"name": "probe", "image": %q, "command": ["sleep", "900"],
      "securityContext": {"runAsUser": 65534, "runAsGroup": 65534, "runAsNonRoot": true, "allowPrivilegeEscalation": false,
        "readOnlyRootFilesystem": true, "capabilities": {"drop": ["ALL"]}},
      "resources": {"requests": {"cpu": "50m", "memory": "32Mi"}, "limits": {"memory": "64Mi"}}}]}}`

// fullSizeScript runs as `sh -s -- <sys/class/net> mtu <address>` or
// `sh -s -- <sys/class/net> get <address> <port> <seconds>`, with busybox of
// the image and alike with sh of a CI runner. It prints name=value words and
// writes nothing.
const fullSizeScript = `export LC_ALL=C
net=$1
if [ "$2" = mtu ]; then
	iface=none
	addrs=$(ip -o -4 addr show)
	while read -r _ dev _ cidr _; do
		[ "${cidr%/*}" = "$3" ] && { iface=$dev; break; }
	done <<EOF
$addrs
EOF
	mtu() { cat "$net/$1/mtu" 2>/dev/null || echo none; }
	echo "iface=$iface" "iface_mtu=$(mtu "$iface")" "cilium_host_mtu=$(mtu cilium_host)" "cilium_wg0_mtu=$(mtu cilium_wg0)"
	exit 0
fi
read -r t0 _ < /proc/uptime
body=$(timeout "$5" wget -q -O - "http://$3:$4/metrics" 2>/dev/null; echo "x$?")
read -r t1 _ < /proc/uptime
code=${body##*x}
body=${body%x*}
echo "exit=$code" "bytes=${#body}" "start=$t0" "end=$t1"
`

type fullSizeCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(fullSizeCheck{kubectl: kube.Region.Run}) }

func (fullSizeCheck) ID() string { return fullSizeID }

// fullSizeNode is a node the check uses: its name and InternalIP.
type fullSizeNode struct{ name, address string }

// fullSizeRegion is one region of a run: probe-<i> runs on workers[i].
type fullSizeRegion struct {
	region           kube.Region
	ns, configMTU    string
	control          fullSizeNode
	workers          []fullSizeNode
	created, started bool
}

func (c fullSizeCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	ns := "net-path-" + strings.ToLower(NewRunID(time.Now()))
	n, regions := 0, []*fullSizeRegion{}
	step := func(name string, fn func(ev Evidence) error) bool {
		n++
		return checkStep(rec, fullSizeID, n, name, fn)
	}
	for _, region := range cc.Regions {
		r := &fullSizeRegion{region: region, ns: ns}
		regions = append(regions, r)
		r.started = step("two workers of region "+region.ID+" are chosen and a probe Pod on the host network is Ready on each", func(ev Evidence) error {
			return c.start(ctx, r, ev)
		})
	}
	for _, r := range regions {
		if !r.started {
			continue
		}
		for i, w := range r.workers {
			step("the MTU of cilium_host equals the MTU of the interface of node "+w.name+" of region "+r.region.ID, func(ev Evidence) error {
				return c.mtu(ctx, r, i, ev)
			})
		}
		for i := range r.workers {
			step(fullSizeName(r, i, r, 1-i), func(ev Evidence) error { return c.transfer(ctx, r, i, r, 1-i, ev) })
		}
	}
	for _, a := range regions {
		for _, b := range regions {
			if a != b && a.started && b.started {
				step(fullSizeName(a, 0, b, 0), func(ev Evidence) error { return c.transfer(ctx, a, 0, b, 0, ev) })
			}
		}
	}
	for _, r := range regions {
		if r.created {
			step("the namespace "+ns+" of region "+r.region.ID+" is deleted", func(Evidence) error {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancel()
				_, err := c.kubectl(r.region, cleanup, nil, "delete", "namespace", ns, "--wait=false", "--ignore-not-found")
				return err
			})
		}
	}
	return nil
}

func fullSizeName(a *fullSizeRegion, i int, b *fullSizeRegion, j int) string {
	return fmt.Sprintf("node %s of region %s fetches at least %d bytes from node %s of region %s within %d seconds",
		a.workers[i].name, a.region.ID, fullSizeMinBytes, b.workers[j].name, b.region.ID, fullSizeSeconds)
}

// start chooses the workers and the control-plane node of a region and
// starts the probe Pods.
func (c fullSizeCheck) start(ctx context.Context, r *fullSizeRegion, ev Evidence) error {
	out, err := c.kubectl(r.region, ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	var list kube.List
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("the nodes of region %s are not JSON: %v", r.region.ID, err)
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name() < list.Items[j].Name() })
	var workers, controls []fullSizeNode
	for _, o := range list.Items {
		node := fullSizeNode{name: o.Name()}
		for _, a := range o.Objects("status", "addresses") {
			if ip := net.ParseIP(a.Str("address")); a.Str("type") == "InternalIP" && ip != nil && ip.To4() != nil && node.address == "" {
				node.address = a.Str("address")
			}
		}
		ready, _, _, _ := o.Condition("Ready")
		labels, _ := o.Get("metadata", "labels").(map[string]any)
		_, controlPlane := labels[fullSizeRole]
		cordoned, _ := o.Get("spec", "unschedulable").(bool)
		switch {
		case ready != "True" || node.address == "":
		case controlPlane:
			controls = append(controls, node)
		case !cordoned:
			workers = append(workers, node)
		}
	}
	names := []string{}
	for _, w := range workers {
		names = append(names, w.name)
	}
	ev["workers"], ev["namespace"] = names, r.ns
	if len(workers) < 2 {
		return fmt.Errorf("region %s has %d Ready, schedulable worker(s) with an InternalIP, the check needs 2: not measured", r.region.ID, len(workers))
	}
	if len(controls) == 0 {
		return fmt.Errorf("region %s has no Ready node with the label %s: no control transfer can be made", r.region.ID, fullSizeRole)
	}
	r.workers, r.control = workers[:2], controls[0]
	ev["chosen"], ev["control_plane"] = []string{r.workers[0].name, r.workers[1].name}, r.control.name
	r.configMTU = "unread"
	if b, err := c.kubectl(r.region, ctx, nil, "-n", "kube-system", "get", "configmap", "cilium-config", "-o", "jsonpath={.data.mtu}"); err == nil {
		r.configMTU = strings.TrimSpace(string(b))
	}
	if _, err := c.kubectl(r.region, ctx, nil, "create", "namespace", r.ns); err != nil {
		return err
	}
	r.created = true
	for i, w := range r.workers {
		pod := fmt.Sprintf(fullSizePod, "probe-"+strconv.Itoa(i), r.ns, w.name, secProbeImage)
		if _, err := c.kubectl(r.region, ctx, strings.NewReader(pod), "-n", r.ns, "create", "-f", "-"); err != nil {
			return err
		}
	}
	_, err = c.kubectl(r.region, ctx, nil, "-n", r.ns, "wait", "--for=condition=Ready", "pod", "--all", "--timeout="+secProbeWait)
	return err
}

// probe runs the script in probe-<i> of a region.
func (c fullSizeCheck) probe(ctx context.Context, r *fullSizeRegion, i int, args ...string) (probeValues, error) {
	argv := append([]string{"-n", r.ns, "exec", "-i", "probe-" + strconv.Itoa(i), "-c", "probe", "--", "sh", "-s", "--", fullSizeNet}, args...)
	out, err := c.kubectl(r.region, ctx, strings.NewReader(fullSizeScript), argv...)
	return parseProbe(out), err
}

func (c fullSizeCheck) mtu(ctx context.Context, r *fullSizeRegion, i int, ev Evidence) error {
	w := r.workers[i]
	v, err := c.probe(ctx, r, i, "mtu", w.address)
	if err != nil {
		return err
	}
	ev["node"], ev["address"], ev["interface"] = w.name, w.address, v.one("iface")
	ev["interface_mtu"], ev["cilium_host_mtu"], ev["cilium_wg0_mtu"], ev["cilium_config_mtu"] = v.one("iface_mtu"), v.one("cilium_host_mtu"), v.one("cilium_wg0_mtu"), r.configMTU
	if v.one("iface") == "none" || v.one("iface") == "" {
		return fmt.Errorf("node %s of region %s: no interface holds the node's address %s", w.name, r.region.ID, w.address)
	}
	if host, link := v.one("cilium_host_mtu"), v.one("iface_mtu"); host != link || host == "none" {
		return fmt.Errorf("node %s of region %s: cilium_host has MTU %s and the interface %s that holds %s has MTU %s: Cilium must run with the MTU of the node's interface",
			w.name, r.region.ID, host, v.one("iface"), w.address, link)
	}
	return nil
}

// get is one GET from probe-<i> of a region to an address; it returns why it failed.
func (c fullSizeCheck) get(ctx context.Context, r *fullSizeRegion, i int, to fullSizeNode) (Evidence, error) {
	v, err := c.probe(ctx, r, i, "get", to.address, strconv.Itoa(fullSizePort), strconv.Itoa(fullSizeSeconds))
	start, _ := strconv.ParseFloat(v.one("start"), 64)
	end, _ := strconv.ParseFloat(v.one("end"), 64)
	bytes, _ := strconv.Atoi(v.one("bytes"))
	ev := Evidence{"source": r.workers[i].name, "destination": to.name, "address": fmt.Sprintf("%s:%d", to.address, fullSizePort),
		"exit": v.one("exit"), "bytes": bytes, "seconds": strconv.FormatFloat(end-start, 'f', 2, 64)}
	switch {
	case err != nil:
		return ev, err
	case v.one("exit") != "0" || bytes < fullSizeMinBytes:
		return ev, fmt.Errorf("the GET from node %s of http://%s/metrics (node %s) ended with exit code %s and %d bytes in %s seconds: want exit code 0 and at least %d bytes within %d seconds",
			r.workers[i].name, ev["address"], to.name, ev["exit"], bytes, ev["seconds"], fullSizeMinBytes, fullSizeSeconds)
	}
	return ev, nil
}

// transfer is the control GET of the control-plane node of b's region, then
// the GET of worker j of b, both from worker i of a.
func (c fullSizeCheck) transfer(ctx context.Context, a *fullSizeRegion, i int, b *fullSizeRegion, j int, ev Evidence) error {
	control, err := c.get(ctx, a, i, b.control)
	ev["control"] = control
	if err != nil {
		return fmt.Errorf("not measured: the control transfer from the control-plane node %s of region %s failed: %v", b.control.name, b.region.ID, err)
	}
	peer, err := c.get(ctx, a, i, b.workers[j])
	ev["transfer"] = peer
	return err
}
