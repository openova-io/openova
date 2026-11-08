package suite

// Pure decision helpers — everything here is unit-tested in match_test.go so
// the assertions the live run makes are themselves proven able to FAIL.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// OrgItem is one row of GET /api/v1/organizations (the console's directory feed).
type OrgItem struct {
	Subdomain       string `json:"subdomain"`
	Kind            string `json:"kind"`
	TenantNamespace string `json:"tenant_namespace"`
	State           string `json:"state"`
}

// PickOwnOrg returns the Sovereign's own Organization: the single
// kind=internal Organization. When several exist, the one whose slug is the
// dashed Sovereign FQDN wins (that is how the org-controller names the
// self-org); anything else is ambiguous and must be passed explicitly.
func PickOwnOrg(items []OrgItem, explicit, fqdnDashed string) (OrgItem, error) {
	if explicit != "" {
		for _, o := range items {
			if o.Subdomain == explicit {
				return o, nil
			}
		}
		return OrgItem{}, fmt.Errorf("organization %q is not in GET /api/v1/organizations (have %s)", explicit, orgSlugs(items))
	}
	var internal []OrgItem
	for _, o := range items {
		if o.Kind == "internal" {
			internal = append(internal, o)
		}
	}
	if len(internal) == 1 {
		return internal[0], nil
	}
	for _, o := range internal {
		if o.Subdomain == fqdnDashed {
			return o, nil
		}
	}
	return OrgItem{}, fmt.Errorf("cannot identify the Sovereign's own Organization: %d kind=internal orgs (%s); pass --org", len(internal), orgSlugs(internal))
}

func orgSlugs(items []OrgItem) string {
	s := make([]string, 0, len(items))
	for _, o := range items {
		s = append(s, o.Subdomain)
	}
	return strings.Join(s, ",")
}

// EnvInfo is the subset of an Environment the suite needs.
type EnvInfo struct {
	Name      string
	Org       string
	Placement string
	Regions   int
}

// PickEnvironment returns the Org's Environment for a two-region install:
// the explicit one if given (it must belong to the Org), else the Org's ONLY
// Environment spanning >= 2 regions.
func PickEnvironment(envs []EnvInfo, org, explicit string) (EnvInfo, error) {
	var mine []EnvInfo
	for _, e := range envs {
		if e.Org == org {
			mine = append(mine, e)
		}
	}
	if explicit != "" {
		for _, e := range mine {
			if e.Name == explicit {
				return e, nil
			}
		}
		return EnvInfo{}, fmt.Errorf("environment %q does not exist for organization %q (have %s)", explicit, org, envNames(mine))
	}
	var multi []EnvInfo
	for _, e := range mine {
		if e.Regions >= 2 || e.Placement == "multi-region" {
			multi = append(multi, e)
		}
	}
	if len(multi) == 1 {
		return multi[0], nil
	}
	return EnvInfo{}, fmt.Errorf("organization %q has %d multi-region Environments (%s of %s); pass --environment", org, len(multi), envNames(multi), envNames(mine))
}

func envNames(e []EnvInfo) string {
	s := make([]string, 0, len(e))
	for _, x := range e {
		s = append(s, x.Name)
	}
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ",")
}

// ParseRegionIPs parses "regionA=1.2.3.4,5.6.7.8;regionB=9.9.9.9".
func ParseRegionIPs(s string) (map[string][]string, error) {
	out := map[string][]string{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out, nil
	}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || strings.TrimSpace(kv[0]) == "" {
			return nil, fmt.Errorf("region-ips: bad entry %q (want region=ip,ip)", part)
		}
		for _, ip := range strings.Split(kv[1], ",") {
			if ip = strings.TrimSpace(ip); ip != "" {
				out[strings.TrimSpace(kv[0])] = append(out[strings.TrimSpace(kv[0])], ip)
			}
		}
	}
	return out, nil
}

// DNSServesRegion asserts every resolved answer belongs to `want` and none to
// any other region. Overlapping region IP sets make the assertion
// undecidable, which is reported as a failure, never as a pass.
func DNSServesRegion(answers []string, regionIPs map[string][]string, want string) error {
	if len(answers) == 0 {
		return errors.New("no DNS answers")
	}
	wantSet := map[string]bool{}
	for _, ip := range regionIPs[want] {
		wantSet[ip] = true
	}
	if len(wantSet) == 0 {
		return fmt.Errorf("no public IPs known for region %s (set ACCEPT_REGION_IPS)", want)
	}
	for r, ips := range regionIPs {
		if r == want {
			continue
		}
		for _, ip := range ips {
			if wantSet[ip] {
				return fmt.Errorf("region %s and %s share public IP %s — DNS cannot show which region serves", want, r, ip)
			}
		}
	}
	for _, a := range answers {
		if !wantSet[a] {
			owner := "no region"
			for r, ips := range regionIPs {
				for _, ip := range ips {
					if ip == a {
						owner = r
					}
				}
			}
			return fmt.Errorf("answer %s belongs to %s, want %s (%v)", a, owner, want, regionIPs[want])
		}
	}
	return nil
}

// ClusterHealthy reports whether a CNPG Cluster has all instances ready.
func ClusterHealthy(c kube.Object) (bool, string) {
	inst := c.Int("spec", "instances")
	ready := c.Int("status", "readyInstances")
	phase := c.Str("status", "phase")
	d := fmt.Sprintf("%s instances=%d ready=%d phase=%q primary=%s", c.Name(), inst, ready, phase, c.Str("status", "currentPrimary"))
	return inst > 0 && ready == inst && strings.Contains(strings.ToLower(phase), "healthy") && c.Str("status", "currentPrimary") != "", d
}

// DeclaredWriter reports whether the Cluster object DECLARES itself the
// writer: CNPG distributed topology (spec.replica.primary == spec.replica.self)
// or no replica section at all. Recorded as evidence only — the assertion is
// pg_is_in_recovery() on the instance, which is what actually takes writes.
func DeclaredWriter(c kube.Object) bool {
	p, s := c.Str("spec", "replica", "primary"), c.Str("spec", "replica", "self")
	if p != "" && s != "" {
		return p == s
	}
	en, ok := c.Get("spec", "replica", "enabled").(bool)
	return !ok || !en
}

// Target is one placement target (PlacementEditor wire shape).
type Target struct {
	Region      string `json:"region"`
	Cluster     string `json:"cluster"`
	VCluster    string `json:"vcluster,omitempty"`
	Role        string `json:"role"`
	StandbyType string `json:"standbyType,omitempty"`
}

// MovePrimary returns the targets the PlacementEditor would Save when the
// operator makes `to` the Primary: `to` becomes Primary, every other target
// becomes a Hot Standby. Fails unless `to` is already a target — the editor
// cannot move a writer to a region the app is not placed in.
func MovePrimary(ts []Target, to string) ([]Target, error) {
	found := false
	out := make([]Target, 0, len(ts))
	for _, t := range ts {
		n := t
		if t.Region == to {
			found = true
			n.Role, n.StandbyType = "Primary", ""
		} else {
			n.Role, n.StandbyType = "Standby", "Hot"
		}
		out = append(out, n)
	}
	if !found {
		return nil, fmt.Errorf("region %s is not a placement target (targets=%v)", to, ts)
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("need >=2 targets to move the Primary, have %d", len(out))
	}
	return out, nil
}

// Job is the subset of the Jobs API row the suite reads.
type Job struct {
	ID          string   `json:"id"`
	JobName     string   `json:"jobName"`
	DisplayName string   `json:"displayName"`
	Type        string   `json:"type"`
	Kind        string   `json:"kind"`
	AppID       string   `json:"appId"`
	Region      string   `json:"region"`
	ParentID    string   `json:"parentId"`
	ChildIDs    []string `json:"childIds"`
	Status      string   `json:"status"`
	StartedAt   *string  `json:"startedAt"`
	FinishedAt  *string  `json:"finishedAt"`
}

// TransitionKind is the ADR-0015 D8 Jobs type of a topology transition.
const TransitionKind = "topology-transition"

func isTransition(j Job) bool {
	return j.Kind == TransitionKind || j.Type == TransitionKind || strings.HasPrefix(j.JobName, TransitionKind)
}

func mentionsApp(j Job, app string) bool {
	return j.AppID == app || strings.HasSuffix(j.AppID, ":"+app) || strings.HasSuffix(j.AppID, "/"+app) ||
		strings.Contains(j.JobName, app) || strings.Contains(j.DisplayName, app)
}

// TransitionVerdict is what FindTransition concluded.
type TransitionVerdict struct {
	Done   bool     // a transition for app, started after since, with every step succeeded
	Parent string   // job id
	Steps  []string // "name=status"
	Detail string
}

// FindTransition looks for the topology-transition record of `app` that
// started at or after `since` (the Save), and reports whether it completed
// with EVERY step succeeded. A record with zero steps is not complete — "a
// complete Jobs record" means its phases are visible.
func FindTransition(jobs []Job, app string, since time.Time) TransitionVerdict {
	byID := map[string]Job{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	var cands []Job
	for _, j := range jobs {
		if !isTransition(j) || !mentionsApp(j, app) {
			continue
		}
		if p, ok := byID[j.ParentID]; ok && isTransition(p) {
			continue // a step; we want the record (root of the transition)
		}
		if j.StartedAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *j.StartedAt); err == nil && t.Before(since.Add(-30*time.Second)) {
				continue // a previous move
			}
		}
		cands = append(cands, j)
	}
	if len(cands) == 0 {
		n := 0
		for _, j := range jobs {
			if isTransition(j) {
				n++
			}
		}
		return TransitionVerdict{Detail: fmt.Sprintf("no %s job for %s since %s (%d %s jobs total, %d jobs)", TransitionKind, app, since.Format(time.RFC3339), n, TransitionKind, len(jobs))}
	}
	sort.Slice(cands, func(i, k int) bool { return deref(cands[i].StartedAt) > deref(cands[k].StartedAt) })
	root := cands[0]
	var steps []Job
	for _, j := range jobs {
		if j.ParentID == root.ID {
			steps = append(steps, j)
		}
	}
	v := TransitionVerdict{Parent: root.ID}
	allOK := len(steps) > 0
	for _, s := range steps {
		v.Steps = append(v.Steps, fmt.Sprintf("%s=%s", nonEmpty(s.DisplayName, s.JobName), s.Status))
		if s.Status != "succeeded" {
			allOK = false
		}
	}
	sort.Strings(v.Steps)
	v.Done = allOK && root.Status == "succeeded"
	v.Detail = fmt.Sprintf("record %s status=%s steps=%d", root.ID, root.Status, len(steps))
	if len(steps) == 0 {
		v.Detail += " (no step rows — not a complete record)"
	}
	return v
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
