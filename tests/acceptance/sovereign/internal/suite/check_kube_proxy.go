package suite

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G03, kube-proxy absent: Cilium is the only Service proxy on every
// node of every region.
//
// k3s runs kube-proxy inside its own process, so the name of a process proves
// nothing. What kube-proxy leaves on a node is its iptables chains: each
// Service it programs gets a chain whose name starts with KUBE-SVC- in the
// nat table. The check reads the node through its Cilium agent, which runs
// in the network namespace of the node.
//
// For each region:
//
//   - every node runs a Cilium agent pod (label k8s-app=cilium in namespace
//     kube-system) in phase Running. A node without one fails: its state
//     cannot be read;
//   - `cilium-dbg status` in the container cilium-agent prints the line
//     "KubeProxyReplacement:" with the value True. The exit code of the
//     command is not judged: an agent that reports another problem still
//     prints the line;
//   - the nat table of the node holds no KUBE-SVC- chain. The table is listed
//     with each iptables program of the agent image (iptables-legacy,
//     iptables-nft, iptables), because kube-proxy may have used either back
//     end. At least one of them must list the table; a node whose table
//     cannot be read fails.
//
// A region without a node fails. The check only reads: `kubectl get` and
// `kubectl exec` of the two commands above.
const kubeProxyID = "NET-G03"

const (
	kubeProxyNamespace = "kube-system"
	kubeProxySelector  = "k8s-app=cilium"
	kubeProxyContainer = "cilium-agent"
	kubeProxyChain     = "KUBE-SVC-"
	kubeProxyListed    = "#listed "
)

// kubeProxyNatScript lists the nat table with each iptables program. Before
// each listing that succeeds it prints "#listed <program>". A program that is
// missing, or that fails, prints nothing.
const kubeProxyNatScript = `for b in iptables-legacy iptables-nft iptables; do
  if out="$("$b" -t nat -S 2>/dev/null)"; then printf '#listed %s\n%s\n' "$b" "$out"; fi
done`

// kubeProxyExec runs a command in the Cilium agent container of a pod. The
// tests replace it.
var kubeProxyExec = func(ctx context.Context, region kube.Region, pod string, cmd ...string) ([]byte, error) {
	return region.Exec(ctx, kubeProxyNamespace, pod, kubeProxyContainer, nil, cmd...)
}

type kubeProxyCheck struct{}

func init() { RegisterCheck(kubeProxyCheck{}) }

func (kubeProxyCheck) ID() string { return kubeProxyID }

func (kubeProxyCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	next := func() string { n++; return kubeProxyID + "." + strconv.Itoa(n) }
	for _, region := range cc.Regions {
		rec.Step(next(), "every node of region "+region.ID+" has kube-proxy replacement True and no KUBE-SVC chain", func(ev Evidence) error {
			return kubeProxyRegion(ctx, region, ev)
		})
	}
	return nil
}

func kubeProxyRegion(ctx context.Context, region kube.Region, ev Evidence) error {
	nodes, err := region.GetList(ctx, "nodes", "")
	if err != nil {
		return err
	}
	pods, err := region.GetList(ctx, "pods", kubeProxyNamespace, "-l", kubeProxySelector)
	if err != nil {
		return err
	}
	agents := map[string][]string{}
	for _, p := range pods {
		if p.Str("status", "phase") == "Running" {
			node := p.Str("spec", "nodeName")
			agents[node] = append(agents[node], p.Name())
		}
	}
	names := make([]string, 0, len(nodes))
	for _, node := range nodes {
		names = append(names, node.Name())
	}
	sort.Strings(names)
	ev["nodes"] = names
	if len(names) == 0 {
		return fmt.Errorf("region %s has no node", region.ID)
	}
	results := map[string]string{}
	failures := []string{}
	for _, node := range names {
		if len(agents[node]) == 0 {
			results[node] = "no Running Cilium agent"
			failures = append(failures, "node "+node+" has no Running Cilium agent")
			continue
		}
		sort.Strings(agents[node])
		for _, pod := range agents[node] {
			problem := kubeProxyNode(ctx, region, pod)
			if problem == "" {
				results[node+"/"+pod] = "ok"
				continue
			}
			results[node+"/"+pod] = problem
			failures = append(failures, "node "+node+" (agent "+pod+"): "+problem)
		}
	}
	ev["results"] = results
	if len(failures) > 0 {
		return fmt.Errorf("region %s: %s", region.ID, strings.Join(failures, "; "))
	}
	return nil
}

// kubeProxyNode reads one agent and returns "" when its node is clean, or what is wrong.
func kubeProxyNode(ctx context.Context, region kube.Region, pod string) string {
	status, err := kubeProxyExec(ctx, region, pod, "cilium-dbg", "status")
	value := kubeProxyReplacement(string(status))
	switch {
	case value == "" && err != nil:
		return "cilium-dbg status failed: " + err.Error()
	case value != "True":
		return "KubeProxyReplacement is " + strconv.Quote(value) + ", want True"
	}
	nat, err := kubeProxyExec(ctx, region, pod, "sh", "-c", kubeProxyNatScript)
	listed, chains := kubeProxyNat(string(nat))
	switch {
	case len(listed) == 0 && err != nil:
		return "listing the nat table failed: " + err.Error()
	case len(listed) == 0:
		return "no iptables program could list the nat table"
	}
	if chains > 0 {
		return fmt.Sprintf("the nat table names a %s chain in %d line(s) (listed with %s): kube-proxy runs or ran on this node", kubeProxyChain, chains, strings.Join(listed, ", "))
	}
	return ""
}

// kubeProxyReplacement returns the value of the line "KubeProxyReplacement:"
// of `cilium-dbg status`, or "" when the line is missing.
func kubeProxyReplacement(status string) string {
	for _, line := range strings.Split(status, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "KubeProxyReplacement:" {
			return f[1]
		}
	}
	return ""
}

// kubeProxyNat reads the output of kubeProxyNatScript: the programs that
// listed the table, and the number of lines that name a KUBE-SVC- chain.
func kubeProxyNat(out string) (listed []string, chains int) {
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, kubeProxyListed):
			listed = append(listed, strings.TrimSpace(strings.TrimPrefix(line, kubeProxyListed)))
		case strings.Contains(line, kubeProxyChain):
			chains++
		}
	}
	return listed, chains
}
