package suite

import (
	"context"
	"sort"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// dbVolumesKey is the evidence key of step J1.4 that holds, per region id, the
// volumes of the Application's CNPG Cluster in that region, each written
// "<namespace>/<name> <phase> <storage class>". J1's two-region database lives
// in the Organization's namespace, so it is a per-Organization database that
// the run creates itself; journey J5 deletes it before the checks run, so
// check CLD-G09 can judge it only from what J1.4 recorded while it existed.
// A volume list that cannot be read is recorded under dbVolumesKey+"_unread"
// and does not fail J1.4, which judges the database, not its volumes.
const dbVolumesKey = "db_volumes"

// recordDBVolumes writes dbVolumesKey into ev: every PVC that carries the
// label cnpg.io/cluster with the name of a CNPG Cluster of the Application.
func (r *runner) recordDBVolumes(ctx context.Context, ev Evidence) {
	volumes, unread := map[string][]string{}, []string{}
	for _, reg := range []kube.Region{r.A, r.B} {
		clusters, err := r.cnpgFor(ctx, reg)
		if err != nil {
			unread = append(unread, err.Error())
			continue
		}
		for _, c := range clusters {
			pvcs, err := reg.GetList(ctx, orgDBVolumes, c.Namespace(), "-l", orgDBClusterLabel+"="+c.Name())
			if err != nil {
				unread = append(unread, err.Error())
				continue
			}
			for _, p := range pvcs {
				volumes[reg.ID] = append(volumes[reg.ID],
					c.Namespace()+"/"+p.Name()+" "+p.Str("status", "phase")+" "+p.Str("spec", "storageClassName"))
			}
		}
		sort.Strings(volumes[reg.ID])
	}
	ev[dbVolumesKey] = volumes
	if len(unread) > 0 {
		ev[dbVolumesKey+"_unread"] = unread
	}
}

// runDBVolumes returns what a passed step J1.4 of the run recorded under
// dbVolumesKey, per region id, regions without a volume left out. It reads the
// in-memory form (map[string][]string) and the form of a record read back from
// JSON (map[string]any of []any).
func runDBVolumes(prior []*Step) map[string][]string {
	out := map[string][]string{}
	for _, s := range prior {
		if s == nil || s.Journey != "J1" || s.ID != "J1.4" || s.Status != Pass {
			continue
		}
		switch v := s.Evidence[dbVolumesKey].(type) {
		case map[string][]string:
			for region, lines := range v {
				out[region] = append(out[region], lines...)
			}
		case map[string]any:
			for region, list := range v {
				items, _ := list.([]any)
				for _, item := range items {
					if line, ok := item.(string); ok {
						out[region] = append(out[region], line)
					}
				}
			}
		}
	}
	for region, lines := range out {
		if len(lines) == 0 {
			delete(out, region)
		}
	}
	return out
}
