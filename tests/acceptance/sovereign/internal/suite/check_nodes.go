package suite

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Check FND-G03, Nodes: the Ready nodes of each region equal the number that
// the deployment request asked for. More nodes fail as fewer do: "at least"
// is not accepted. The requested numbers come from --nodes-requested; without
// them the check fails, it never assumes a number.
const nodesID = "FND-G03"

type nodesCheck struct{}

func init() { RegisterCheck(nodesCheck{}) }

func (nodesCheck) ID() string { return nodesID }

func (nodesCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	var requested map[string]int
	if cc.Deploy != nil {
		requested = cc.Deploy.NodesRequested
	}
	n := 0
	next := func() string { n++; return nodesID + "." + strconv.Itoa(n) }
	reached := map[string]bool{}
	for _, region := range cc.Regions {
		reached[region.ID] = true
		rec.Step(next(), "ready nodes equal requested nodes in region "+region.ID, func(ev Evidence) error {
			nodes, err := region.GetList(ctx, "nodes", "")
			if err != nil {
				return err
			}
			ready, notReady := 0, []string{}
			for _, node := range nodes {
				if status, _, _, _ := node.Condition("Ready"); status == "True" {
					ready++
				} else {
					notReady = append(notReady, node.Name())
				}
			}
			sort.Strings(notReady)
			ev["nodes"], ev["ready"], ev["not_ready"] = len(nodes), ready, notReady
			want, said := requested[region.ID]
			if !said {
				return fmt.Errorf("the run did not say how many nodes region %s was asked for (--nodes-requested)", region.ID)
			}
			ev["requested"] = want
			if ready != want {
				return fmt.Errorf("%d nodes are Ready in region %s and %d were requested", ready, region.ID, want)
			}
			return nil
		})
	}
	// A requested region that this run does not reach is a failure, never a skip.
	missing := []string{}
	for id := range requested {
		if !reached[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	for _, id := range missing {
		rec.Step(next(), "the run reaches requested region "+id, func(ev Evidence) error {
			ev["requested"] = requested[id]
			return fmt.Errorf("region %s was requested with %d nodes and the run has no kubeconfig for it", id, requested[id])
		})
	}
	return nil
}

// parseNodesRequested reads "<region id>=<nodes>,<region id>=<nodes>".
// An empty string gives nil: the run did not say.
func parseNodesRequested(s string) (map[string]int, error) {
	parts := splitList(s)
	if len(parts) == 0 {
		return nil, nil
	}
	out := map[string]int{}
	for _, part := range parts {
		id, number, found := strings.Cut(part, "=")
		id = strings.TrimSpace(id)
		nodes, err := strconv.Atoi(strings.TrimSpace(number))
		if !found || id == "" || err != nil || nodes < 1 {
			return nil, fmt.Errorf("--nodes-requested: %q is not <region id>=<nodes>", part)
		}
		if _, twice := out[id]; twice {
			return nil, fmt.Errorf("--nodes-requested: region %s is named twice", id)
		}
		out[id] = nodes
	}
	return out, nil
}
