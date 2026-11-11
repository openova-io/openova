package suite

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// TEMPORARY marks a discovery value read from cluster state because the
// console has no list endpoint for it yet (ADR-0015 D3/D4 add them). Every
// such fallback is named in the step evidence so a record shows exactly which
// inputs were not taken from the API.
const TEMPORARY = "TEMPORARY-fallback"

// discover resolves every reference the journeys use FROM EXISTING OBJECTS —
// the suite never fabricates an Org, Environment or Region id.
func (r *runner) discover(ctx context.Context) bool {
	ok := r.step("D0", "D0.1", "session authenticates as owner (GET /api/v1/whoami)", func(ev Evidence) error {
		var who struct {
			Tier          string `json:"tier"`
			DeploymentID  string `json:"deploymentId"`
			SovereignFQDN string `json:"sovereignFQDN"`
			Mode          string `json:"mode"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/whoami", &who); err != nil {
			return err
		}
		ev["tier"], ev["deploymentId"], ev["sovereignFQDN"], ev["mode"] = who.Tier, who.DeploymentID, who.SovereignFQDN, who.Mode
		if who.Tier != "owner" {
			return fmt.Errorf("session tier=%q, need owner (installs and placement edits are owner/sovereign-admin actions)", who.Tier)
		}
		r.dep = nonEmpty(r.cfg.DeploymentID, who.DeploymentID)
		r.fqdn = who.SovereignFQDN
		if r.dep == "" {
			return fmt.Errorf("no deployment id (whoami returned none; pass --deployment)")
		}
		if r.cfg.DeploymentID != "" && who.DeploymentID != "" && r.cfg.DeploymentID != who.DeploymentID {
			return fmt.Errorf("--deployment %s but the console serves deployment %s", r.cfg.DeploymentID, who.DeploymentID)
		}
		r.run.DeploymentID = r.dep
		return nil
	})
	if !ok {
		return false
	}

	ok = r.step("D0", "D0.2", "regions: each kubeconfig IS the region it claims; ids from the region registry", func(ev Evidence) error {
		keyA, ipsA, err := regionIdentity(ctx, r.A)
		if err != nil {
			return err
		}
		keyB, ipsB, err := regionIdentity(ctx, r.B)
		if err != nil {
			return err
		}
		if keyA == keyB {
			return fmt.Errorf("both kubeconfigs report region %s — they point at the same cluster", keyA)
		}
		if r.cfg.RegionA != "" && r.cfg.RegionA != keyA {
			return fmt.Errorf("--region-a %s but kubeconfig-a is region %s", r.cfg.RegionA, keyA)
		}
		if r.cfg.RegionB != "" && r.cfg.RegionB != keyB {
			return fmt.Errorf("--region-b %s but kubeconfig-b is region %s", r.cfg.RegionB, keyB)
		}
		r.A.ID, r.B.ID = keyA, keyB
		ev["regionA"], ev["regionB"] = keyA, keyB
		ev["identity_source"] = TEMPORARY + ": bootstrap-kit postBuild SOVEREIGN_REGION_KEY per kubeconfig"

		// D3 target: the Sovereign's region list is the registry.
		res, err := r.api.Do(ctx, "GET", "/api/v1/sovereigns/"+url.PathEscape(r.dep)+"/regions", nil)
		if err != nil {
			return err
		}
		switch {
		case res.Status == 200:
			ids := regionIDsFromRegistry(res.Body)
			ev["registry"] = ids
			for _, k := range []string{keyA, keyB} {
				if !contains(ids, k) {
					return fmt.Errorf("region %s is not in the Sovereign's region registry %v", k, ids)
				}
			}
		case res.Status == 404:
			ev["registry"] = "absent (GET /api/v1/sovereigns/{id}/regions 404 — ADR-0015 D3 pending)"
		default:
			return fmt.Errorf("GET regions registry: HTTP %d", res.Status)
		}
		// The console's own topology source must list both regions.
		var topo struct {
			Topology struct {
				Regions []struct {
					Name string `json:"name"`
				} `json:"regions"`
			} `json:"topology"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/deployments/"+url.PathEscape(r.dep)+"/infrastructure/topology", &topo); err != nil {
			return err
		}
		names := []string{}
		for _, x := range topo.Topology.Regions {
			names = append(names, x.Name)
		}
		ev["console_topology_regions"] = names
		if len(names) < 2 {
			return fmt.Errorf("console topology lists %d region(s) — an active-hot-standby journey needs 2", len(names))
		}

		m, err := ParseRegionIPs(r.cfg.RegionIPs)
		if err != nil {
			return err
		}
		if len(m) == 0 {
			m = map[string][]string{keyA: ipsA, keyB: ipsB}
			ev["region_ips_source"] = TEMPORARY + ": bootstrap-kit CILIUM_LB_IPAM_CIDRS per region (override: ACCEPT_REGION_IPS)"
		} else {
			ev["region_ips_source"] = "ACCEPT_REGION_IPS"
		}
		r.regionIPs = m
		ev["region_ips"] = m
		// The Sovereign's SHARED ingress (the console/gateway load
		// balancers that front every region and forward over the mesh to
		// wherever the app's primary runs). A DNS answer that is one of these
		// serves whichever region holds the primary; J3.6 then proves the
		// routing by reading the content back (Refs #7067).
		if out, err := r.A.Run(ctx, nil, "get", "configmap", "sovereign-fqdn", "-n", "catalyst-system",
			"-o", "jsonpath={.data.lbIP} {.data.consoleLBIP}"); err == nil {
			for _, ip := range strings.Fields(string(out)) {
				r.sharedIngressIPs = append(r.sharedIngressIPs, ip)
			}
		}
		ev["shared_ingress_ips"] = r.sharedIngressIPs
		r.run.Subject["regions"] = map[string]string{"A": keyA, "B": keyB}
		return nil
	})
	if !ok {
		return false
	}

	ok = r.step("D0", "D0.3", "Organization = the Sovereign's own (GET /api/v1/organizations)", func(ev Evidence) error {
		var orgs struct {
			Items []OrgItem `json:"items"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/organizations", &orgs); err != nil {
			return err
		}
		o, err := PickOwnOrg(orgs.Items, r.cfg.Org, strings.ReplaceAll(r.fqdn, ".", "-"))
		if err != nil {
			return err
		}
		r.org = o
		ev["org"], ev["kind"], ev["namespace"], ev["state"] = o.Subdomain, o.Kind, o.TenantNamespace, o.State
		r.run.Subject["org"] = o.Subdomain
		return nil
	})
	if !ok {
		return false
	}

	ok = r.step("D0", "D0.4", "Environment of that Org spanning both regions", func(ev Evidence) error {
		envs, src, err := r.listEnvironments(ctx)
		if err != nil {
			return err
		}
		ev["source"] = src
		e, err := PickEnvironment(envs, r.org.Subdomain, r.cfg.Environment)
		if err != nil {
			return err
		}
		r.env = e
		ev["environment"], ev["placement"], ev["regions"] = e.Name, e.Placement, e.Regions
		r.run.Subject["environment"] = e.Name
		return nil
	})
	if !ok {
		return false
	}

	okBP := r.step("D0", "D0.5", "Blueprint exists and declares active-hot-standby", func(ev Evidence) error {
		bp := strings.TrimPrefix(r.cfg.Blueprint, "bp-")
		var item struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/catalog/"+url.PathEscape(bp), &item); err != nil {
			return err
		}
		var full struct {
			Raw struct {
				Spec struct {
					Topology struct {
						Supported []string `json:"supported"`
					} `json:"topology"`
				} `json:"spec"`
			} `json:"raw"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/catalog/"+url.PathEscape(bp)+"/versions/"+url.PathEscape(item.Version), &full); err != nil {
			return err
		}
		sup := full.Raw.Spec.Topology.Supported
		ev["blueprint"], ev["version"], ev["supported"] = item.Name, item.Version, sup
		r.run.Subject["blueprint"] = item.Name + "@" + item.Version
		if len(sup) > 0 && !contains(sup, "active-hot-standby") {
			return fmt.Errorf("%s@%s supports %v — not active-hot-standby (the console disables that option)", item.Name, item.Version, sup)
		}
		return nil
	})

	// D0.6 runs even when the Blueprint check failed: the record must always
	// say WHICH delivery was measured, pass or fail.
	okImgs := r.step("D0", "D0.6", "delivered control-plane images (what this run certifies)", func(ev Evidence) error {
		imgs := map[string]map[string]string{}
		for _, reg := range []kube.Region{r.A, r.B} {
			deps, err := reg.GetList(ctx, "deployments", "catalyst-system")
			if err != nil {
				return err
			}
			m := map[string]string{}
			for _, d := range deps {
				cs, _ := d.Get("spec", "template", "spec", "containers").([]any)
				if len(cs) > 0 {
					c, _ := cs[0].(map[string]any)
					img, _ := c["image"].(string)
					m[d.Name()] = img
				}
			}
			imgs[reg.ID] = m
		}
		ev["images"] = imgs
		r.run.Subject["delivered_images"] = imgs
		return nil
	})
	return okBP && okImgs
}

// regionIdentity reads the region key + gateway VIPs a cluster was
// bootstrapped with (bootstrap-kit Kustomization postBuild substitutions).
func regionIdentity(ctx context.Context, reg kube.Region) (string, []string, error) {
	out, err := reg.Run(ctx, nil, "-n", "flux-system", "get", "kustomization", "bootstrap-kit", "-o", "json")
	if err != nil {
		return "", nil, err
	}
	var k kube.Object
	if err := json.Unmarshal(out, &k); err != nil {
		return "", nil, err
	}
	sub, _ := k.Get("spec", "postBuild", "substitute").(map[string]any)
	key, _ := sub["SOVEREIGN_REGION_KEY"].(string)
	if key == "" {
		return "", nil, fmt.Errorf("kubeconfig %s: bootstrap-kit has no SOVEREIGN_REGION_KEY", reg.Kubeconfig)
	}
	var ips []string
	cidrs, _ := sub["CILIUM_LB_IPAM_CIDRS"].(string)
	for _, c := range strings.Split(cidrs, ",") {
		if c = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c), "/32")); c != "" && !strings.Contains(c, "/") {
			ips = append(ips, c)
		}
	}
	return key, ips, nil
}

func regionIDsFromRegistry(b []byte) []string {
	var asList []string
	if json.Unmarshal(b, &asList) == nil && len(asList) > 0 {
		return asList
	}
	var env struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
		Regions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"regions"`
	}
	_ = json.Unmarshal(b, &env)
	var ids []string
	for _, x := range append(env.Items, env.Regions...) {
		ids = append(ids, nonEmpty(x.ID, x.Name))
	}
	sort.Strings(ids)
	return ids
}

// listEnvironments reads the Environments from the console API (ADR-0015 D4
// target), falling back to the CRs in region A when that endpoint is absent.
func (r *runner) listEnvironments(ctx context.Context) ([]EnvInfo, string, error) {
	res, err := r.api.Do(ctx, "GET", "/api/v1/sovereigns/"+url.PathEscape(r.dep)+"/environments", nil)
	if err != nil {
		return nil, "", err
	}
	if res.Status == 200 {
		var body struct {
			Items []struct {
				Name         string   `json:"name"`
				Organization string   `json:"organization"`
				OrgRef       string   `json:"organizationRef"`
				Placement    string   `json:"placement"`
				Regions      []any    `json:"regions"`
				RegionIDs    []string `json:"regionIds"`
			} `json:"items"`
		}
		if err := res.JSON(&body); err != nil {
			return nil, "", err
		}
		var out []EnvInfo
		for _, e := range body.Items {
			n := len(e.Regions)
			if len(e.RegionIDs) > n {
				n = len(e.RegionIDs)
			}
			out = append(out, EnvInfo{Name: e.Name, Org: nonEmpty(e.Organization, e.OrgRef), Placement: e.Placement, Regions: n})
		}
		return out, "GET /api/v1/sovereigns/{id}/environments", nil
	}
	if res.Status != 404 {
		return nil, "", fmt.Errorf("GET environments: HTTP %d", res.Status)
	}
	items, err := r.A.GetList(ctx, "environments.catalyst.openova.io", "")
	if err != nil {
		return nil, "", err
	}
	var out []EnvInfo
	for _, e := range items {
		regs, _ := e.Get("spec", "regions").([]any)
		out = append(out, EnvInfo{Name: e.Name(), Org: e.Str("spec", "organizationRef"), Placement: e.Str("spec", "placement"), Regions: len(regs)})
	}
	return out, TEMPORARY + ": kubectl get environments (region A) — console list endpoint 404 (ADR-0015 D4 pending)", nil
}

// probeReadOnly checks, without writing anything, that every endpoint the
// journeys call answers in the shape the suite parses. The create/PUT/DELETE
// calls themselves are NOT made; their read-side twins are.
func (r *runner) probeReadOnly(ctx context.Context) {
	r.step("D0", "D0.7", "Jobs API answers in the Job shape (GET /deployments/{id}/jobs)", func(ev Evidence) error {
		var jobs struct {
			Jobs []Job `json:"jobs"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/deployments/"+url.PathEscape(r.dep)+"/jobs", &jobs); err != nil {
			return err
		}
		kinds := map[string]int{}
		for _, j := range jobs.Jobs {
			kinds[j.Kind]++
		}
		ev["jobs"], ev["kinds"] = len(jobs.Jobs), kinds
		ev[TransitionKind+"_jobs"] = kinds[TransitionKind]
		return nil
	})
	r.step("D0", "D0.8", "Application list/get/placement answer for an existing multi-region app", func(ev Evidence) error {
		var list struct {
			Items []struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				Placement string `json:"placement"`
			} `json:"items"`
		}
		if _, err := r.api.Get(ctx, "/api/v1/sovereigns/"+url.PathEscape(r.dep)+"/applications", &list); err != nil {
			return err
		}
		ev["applications"] = len(list.Items)
		for _, a := range list.Items {
			if a.Placement != "active-hot-standby" {
				continue
			}
			q := "?namespace=" + url.QueryEscape(a.Namespace)
			var got appView
			if _, err := r.api.Get(ctx, r.appPath(a.Name)+q, &got); err != nil {
				return err
			}
			var pl struct {
				Targets []Target `json:"targets"`
			}
			if _, err := r.api.Get(ctx, r.appPath(a.Name)+"/placement"+q, &pl); err != nil {
				return err
			}
			ev["sample"] = a.Namespace + "/" + a.Name
			ev["sample_primary"] = got.primary()
			ev["sample_targets"] = pl.Targets
			return nil
		}
		return fmt.Errorf("no active-hot-standby Application to sample the get/placement shape from")
	})
	r.step("D0", "D0.9", "create rejects an unknown Organization — NOT probed in discover (it is a POST)", func(ev Evidence) error {
		ev["note"] = "J1.0 asserts 422 + no namespace; discover mode never POSTs"
		return nil
	})
}
