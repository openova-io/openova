package transition

import (
	"fmt"
	"sort"
	"strings"
)

// Group is a set of members that move together: the connected component of
// the spec.dependsOn graph. Members are ordered BOTTOM-UP (every member after
// the members it depends on), so state moves bottom-up and traffic stops
// top-down (the reverse order).
type Group struct {
	ID      string
	Members []Member
}

// GroupError is a group that cannot be ordered (a dependency cycle).
type GroupError struct {
	Members []Member
	Err     error
}

func (e GroupError) Error() string { return e.Err.Error() }

// Groups partitions members into dependency groups. A dependsOn entry naming
// a member that is not in the set is ignored (that backing service is not
// governed by a Continuum — it is single-region or region-agnostic and has no
// primary to move). A dependency cycle is an error for the members involved.
func Groups(members []Member) ([]Group, []GroupError) {
	byName := map[string]Member{}
	for _, m := range members {
		byName[m.Namespace+"/"+m.Name] = m
	}
	ref := func(m Member, dep string) string {
		if strings.Contains(dep, "/") {
			return dep
		}
		return m.Namespace + "/" + dep
	}
	// Union-find over undirected dependency edges.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	for k := range byName {
		find(k)
	}
	for k, m := range byName {
		for _, d := range m.DependsOn {
			if _, ok := byName[ref(m, d)]; ok {
				a, b := find(k), find(ref(m, d))
				if a != b {
					parent[a] = b
				}
			}
		}
	}
	comps := map[string][]Member{}
	for k, m := range byName {
		comps[find(k)] = append(comps[find(k)], m)
	}
	var out []Group
	var errs []GroupError
	for _, ms := range comps {
		sort.Slice(ms, func(i, j int) bool {
			return ms[i].Namespace+"/"+ms[i].Name < ms[j].Namespace+"/"+ms[j].Name
		})
		ordered, err := orderBottomUp(ms, ref)
		ids := make([]string, 0, len(ms))
		for _, m := range ms {
			ids = append(ids, m.Name)
		}
		sort.Strings(ids)
		id := ms[0].Namespace + "--" + strings.Join(ids, "_")
		if err != nil {
			errs = append(errs, GroupError{Members: ms, Err: fmt.Errorf("group %s: %w", id, err)})
			continue
		}
		out = append(out, Group{ID: id, Members: ordered})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, errs
}

// orderBottomUp topologically sorts ms so every member follows its
// dependencies; ties break by name for determinism.
func orderBottomUp(ms []Member, ref func(Member, string) string) ([]Member, error) {
	in := map[string]Member{}
	for _, m := range ms {
		in[m.Namespace+"/"+m.Name] = m
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var out []Member
	var visit func(k string, path []string) error
	visit = func(k string, path []string) error {
		switch color[k] {
		case grey:
			return fmt.Errorf("dependency cycle %s -> %s", strings.Join(path, " -> "), k)
		case black:
			return nil
		}
		color[k] = grey
		m := in[k]
		deps := make([]string, 0, len(m.DependsOn))
		for _, d := range m.DependsOn {
			if _, ok := in[ref(m, d)]; ok {
				deps = append(deps, ref(m, d))
			}
		}
		sort.Strings(deps)
		for _, d := range deps {
			if err := visit(d, append(path, k)); err != nil {
				return err
			}
		}
		color[k] = black
		out = append(out, m)
		return nil
	}
	for _, k := range keys {
		if err := visit(k, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
