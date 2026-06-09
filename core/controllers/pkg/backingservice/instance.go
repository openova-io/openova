package backingservice

import "sort"

// InstancePlan is the per-data-instance aggregate the catalyst layer
// writes into ONE bp-postgres instance HR. When N consumers bind to the
// same instance (the shared case), their DatabaseBindings collect here —
// this is exactly the `databases[]` list the bp-postgres chart renders
// into N Database CRs + N managed.roles on ONE Cluster (the reuse proof).
type InstancePlan struct {
	// InstanceName — the bp-postgres data-instance (CNPG Cluster) name.
	InstanceName string
	// Databases — one entry per consumer bound to this instance, sorted
	// by database name for deterministic IaC output (stable diffs).
	Databases []DatabaseBinding
}

// AggregateByInstance groups bindings by their target data-instance so
// the catalyst layer can render ONE bp-postgres HR per instance carrying
// every consumer's Database binding. Two consumers on one instance =>
// one InstancePlan with two Databases (sharing).
//
// Output is sorted by instance name, and each plan's Databases sorted by
// name, so the generated IaC is deterministic (Inviolable Principle: no
// non-deterministic diffs in committed gitops).
func AggregateByInstance(bindings []Binding) []InstancePlan {
	byInstance := map[string]*InstancePlan{}
	for _, b := range bindings {
		p, ok := byInstance[b.InstanceName]
		if !ok {
			p = &InstancePlan{InstanceName: b.InstanceName}
			byInstance[b.InstanceName] = p
		}
		p.Databases = append(p.Databases, b.Database)
	}

	plans := make([]InstancePlan, 0, len(byInstance))
	for _, p := range byInstance {
		sort.Slice(p.Databases, func(i, j int) bool {
			return p.Databases[i].Name < p.Databases[j].Name
		})
		plans = append(plans, *p)
	}
	sort.Slice(plans, func(i, j int) bool {
		return plans[i].InstanceName < plans[j].InstanceName
	})
	return plans
}

// InstanceValues marshals an InstancePlan into the `databases[]` values
// fragment the bp-postgres chart consumes. The returned map merges into
// the instance HR's Helm values under the top-level key. Keys mirror
// platform/postgres/chart/values.yaml verbatim.
func (p InstancePlan) InstanceValues() map[string]any {
	dbs := make([]map[string]any, 0, len(p.Databases))
	for _, d := range p.Databases {
		dbs = append(dbs, map[string]any{
			"name":  d.Name,
			"owner": d.Owner,
			"consumer": map[string]any{
				"blueprint": d.Consumer.Blueprint,
				"mode":      d.Consumer.Mode,
			},
			"reflect": map[string]any{
				"secretName": d.Reflect.SecretName,
				"namespaces": toAnySlice(d.Reflect.Namespaces),
			},
		})
	}
	return map[string]any{
		"instance": map[string]any{
			"name": p.InstanceName,
		},
		"databases": dbs,
	}
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
