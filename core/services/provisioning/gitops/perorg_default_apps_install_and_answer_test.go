package gitops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The applications a new Organization is given install and answer on their
// hostname (gate run of 21 Nov 2026 on hw307, check BSS-G01 step 6, both
// runs; FND-G04 then failed on the two stalled releases).
//
// The customer bought the smallest plan with openclaw. The funnel installed
// bp-openclaw and the bp-newapi it implies INTO the Organization's vCluster,
// and there:
//
//   - bp-newapi's hooks mounted `newapi-bp-newapi-newapi-pg-app`, a Secret
//     only the CNPG operator writes, for a Cluster the chart renders only
//     where postgresql.cnpg.io/v1 is served. The vCluster serves no such API:
//     no Cluster, no Secret, "failed post-install: timed out waiting for the
//     condition".
//   - both charts' HTTPRoutes existed only inside the vCluster, which copies
//     no HTTPRoute to the host (#4993): the host gateway had the listener of
//     openclaw.<org>.<pool> and no route, 404 on every request for 30 minutes.
//
// The two tests below fail on that tree. They follow the objects, not the
// file names: the first finds who produces the Secret the release waits for
// and whether that producer's API is served where the release installs; the
// second finds, for every hostname the releases declare, a route the HOST
// gateway sees.
//
// Region scope: the per-Org `vcluster/host-apps/` tree is reconciled by the
// Organization's home region only (core/controllers/organization/internal/
// gitops/region_local.go, "no split brain"), so these tests prove the home
// region's entry point. The other region's entry point is not served by this
// tree at all (see the pull request's list of what remains).

const (
	// defaultAppsSlug / defaultAppsParent are the Organization of the gate run.
	defaultAppsSlug   = "g01-w3ojsz0a"
	defaultAppsParent = "omani.rest"

	newapiTemplatesDir = "../../../../platform/newapi/chart/templates"
	// orgControllerVClusterValues holds the vCluster values the
	// org-controller renders for every Organization (vclusterTemplate).
	orgControllerVClusterValues = "../../../controllers/organization/internal/gitops/manifests.go"
	// cnpgBootstrapSlot installs the CNPG operator and its CRDs on the host.
	cnpgBootstrapSlot = "../../../../clusters/_template/bootstrap-kit/16-cnpg.yaml"
)

// defaultAppsCart is what BSS-G01 buys: the smallest package with openclaw.
var defaultAppsCart = []string{"openclaw"}

// renderedDoc is one YAML document of a rendered file.
type renderedDoc map[string]any

func (d renderedDoc) str(path ...string) string {
	v, _ := d.get(path...).(string)
	return v
}

func (d renderedDoc) get(path ...string) any {
	var cur any = map[string]any(d)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

func parseDocs(t *testing.T, name, body string) []renderedDoc {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(body))
	var out []renderedDoc
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("%s does not parse as YAML: %v\n%s", name, err, body)
		}
		if d != nil {
			out = append(out, renderedDoc(d))
		}
	}
	return out
}

// newOrgTree is what the funnel commits to the new Organization's repo: the
// apps tree, the host-apps tree with the HelmRelease files, and the
// host-native routes, exactly as applyTenantChangePerOrg assembles them.
func newOrgTree(t *testing.T) (files map[string]string, hostIndex string) {
	t.Helper()
	g := NewManifestGenerator(testBasePath)
	g.ParentDomain = defaultAppsParent
	files, _ = g.GeneratePerOrgAppsTree(defaultAppsSlug, "s", defaultAppsCart, "pw")
	routeFiles, routeDocs := g.GeneratePerOrgHostAppRoutes(defaultAppsSlug, defaultAppsCart)
	for p, c := range routeFiles {
		files[p] = c
	}
	hostDocs := append(routeDocs, PerOrgHostHelmReleaseAppDocs(defaultAppsCart)...)
	hostIndex = MergePerOrgHostAppsKustomization("", nil, hostDocs)
	return files, hostIndex
}

// helmRelease returns the HelmRelease document of a host-apps file.
func helmRelease(t *testing.T, files map[string]string, file string) (renderedDoc, []renderedDoc) {
	t.Helper()
	body, ok := files[PerOrgHostAppsDir+"/"+file]
	if !ok {
		t.Fatalf("the new Organization's tree has no %s/%s (files: %v)", PerOrgHostAppsDir, file, keys(files))
	}
	docs := parseDocs(t, file, body)
	for _, d := range docs {
		if d.str("kind") == "HelmRelease" {
			return d, docs
		}
	}
	t.Fatalf("%s holds no HelmRelease", file)
	return nil, nil
}

// installsInVCluster: a HelmRelease with spec.kubeConfig is installed by the
// host helm-controller INTO the cluster that kubeconfig names, the
// Organization's vCluster; without one it installs on the host.
func installsInVCluster(hr renderedDoc) bool {
	return hr.get("spec", "kubeConfig") != nil
}

// vclusterServedGroups reads, from the vCluster values the org-controller
// renders, the API groups the vCluster adds to the core ones: the CRDs it
// deploys into itself (experimental.deploy.vcluster.manifests). Read from the
// source so that a CRD added there later moves this test with it.
func vclusterServedGroups(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(orgControllerVClusterValues)
	if err != nil {
		t.Fatalf("read the org-controller's vCluster values %s: %v", orgControllerVClusterValues, err)
	}
	s := string(src)
	i := strings.Index(s, "    experimental:\n      deploy:\n        vcluster:\n          manifests: |")
	if i < 0 {
		t.Fatalf("%s: no experimental.deploy.vcluster.manifests block — re-point this reader", orgControllerVClusterValues)
	}
	block := s[i:]
	if j := strings.Index(block, "\n`"); j >= 0 {
		block = block[:j]
	}
	groups := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+group:\s*(\S+)\s*$`).FindAllStringSubmatch(block, -1) {
		groups[m[1]] = true
	}
	// Vacuity: the one CRD the vCluster is known to carry must be found, or
	// the reader above is reading nothing.
	if !groups["gateway.networking.k8s.io"] {
		t.Fatalf("read no gateway.networking.k8s.io CRD from %s — the reader is broken, not the tree", orgControllerVClusterValues)
	}
	return groups
}

// TestNewOrganizationDefaultApps_DatabaseConnectionHasAProducer: the Secret
// bp-newapi's install waits for has a producer where the release installs.
func TestNewOrganizationDefaultApps_DatabaseConnectionHasAProducer(t *testing.T) {
	files, _ := newOrgTree(t)
	hr, _ := helmRelease(t, files, "app-newapi.yaml")

	// The release's object names follow the chart's fullname rule: the
	// release name when it already contains the chart name, else
	// <release>-<chart> (bp-newapi.fullname).
	release, chart := hr.str("spec", "releaseName"), hr.str("spec", "chart", "spec", "chart")
	if release == "" || chart != "bp-newapi" {
		t.Fatalf("app-newapi.yaml HelmRelease: releaseName %q, chart %q", release, chart)
	}
	fullname := release + "-" + chart
	if strings.Contains(release, chart) {
		fullname = release
	}

	// What the chart does with the database (read from the chart, so a
	// change there moves this test):
	cnpgTmpl, err := os.ReadFile(newapiTemplatesDir + "/cnpg-cluster.yaml")
	if err != nil {
		t.Fatalf("read bp-newapi cnpg-cluster.yaml: %v", err)
	}
	syncTmpl, err := os.ReadFile(newapiTemplatesDir + "/database-secret-sync-job.yaml")
	if err != nil {
		t.Fatalf("read bp-newapi database-secret-sync-job.yaml: %v", err)
	}
	if !strings.Contains(string(cnpgTmpl), `$clusterName := printf "%s-newapi-pg" (include "bp-newapi.fullname" .)`) {
		t.Fatalf("bp-newapi no longer names its Cluster <fullname>-newapi-pg — re-point this test")
	}
	if !strings.Contains(string(syncTmpl), `printf "%s-app" $clusterName`) {
		t.Fatalf("bp-newapi's db-dsn-sync hook no longer reads <cluster>-app — re-point this test")
	}
	gate := regexp.MustCompile(`\.Capabilities\.APIVersions\.Has "([^"/]+)/[^"]+"\s*-?\}\}\s*\napiVersion: postgresql\.cnpg\.io/v1\nkind: Cluster`).FindStringSubmatch(string(cnpgTmpl))
	if gate == nil {
		t.Fatalf("bp-newapi's Cluster is no longer behind a capability gate on its API — re-point this test")
	}
	producerGroup := gate[1]
	cluster := fullname + "-newapi-pg"
	secret := cluster + "-app"

	// Where that API is served. On the host: bp-cnpg installs the operator
	// and its CRDs (bootstrap-kit slot 16). In the vCluster: only the core
	// groups and the CRDs the org-controller deploys into it.
	slot, err := os.ReadFile(cnpgBootstrapSlot)
	if err != nil || !strings.Contains(string(slot), "chart: bp-cnpg") {
		t.Fatalf("%s does not install bp-cnpg on the host (err %v) — re-point this test", cnpgBootstrapSlot, err)
	}
	where, served := "the host namespace "+defaultAppsSlug+" (bp-cnpg, "+cnpgBootstrapSlot+")", true
	if installsInVCluster(hr) {
		where, served = "the Organization's vCluster", vclusterServedGroups(t)[producerGroup]
	}
	if !served {
		t.Fatalf("bp-newapi installs into %s, which serves no %s API. Its db-dsn-sync hook and admin-promote "+
			"CronJob mount Secret %q, written only by the CNPG operator for Cluster %q, which the chart renders "+
			"only where %s is served. Nothing produces the Secret, so the install waits for it until "+
			"\"failed post-install: timed out waiting for the condition\" (hw307, 21 Nov 2026).",
			where, producerGroup, secret, cluster, producerGroup)
	}
	// That the operator can also read the instances in the Organization
	// namespace is the boundary policy's part: core/controllers/organization/
	// internal/gitops cnpg_operator_probe_test.go.
}

// releaseHostnames are the hostnames a HelmRelease's values give its chart's
// HTTPRoute: bp-openclaw `httpRoute.hostnames`, bp-newapi
// `ingress.httpRoute.host`, each with its parentRef.
func releaseHostnames(hr renderedDoc) (hosts []string, parentName, parentNS string) {
	values := renderedDoc{}
	if v, ok := hr.get("spec", "values").(map[string]any); ok {
		values = renderedDoc(v)
	}
	for _, base := range [][]string{{"httpRoute"}, {"ingress", "httpRoute"}} {
		if on, _ := values.get(append(base, "enabled")...).(bool); !on {
			continue
		}
		if h := values.str(append(base, "host")...); h != "" {
			hosts = append(hosts, h)
		}
		if hs, ok := values.get(append(base, "hostnames")...).([]any); ok {
			for _, h := range hs {
				if s, _ := h.(string); s != "" {
					hosts = append(hosts, s)
				}
			}
		}
		parentName = values.str(append(base, "parentRef", "name")...)
		parentNS = values.str(append(base, "parentRef", "namespace")...)
	}
	return hosts, parentName, parentNS
}

// TestNewOrganizationDefaultApps_EveryHostnameHasARouteOnTheHostGateway: every
// hostname the given releases declare has an HTTPRoute the host gateway sees.
func TestNewOrganizationDefaultApps_EveryHostnameHasARouteOnTheHostGateway(t *testing.T) {
	files, hostIndex := newOrgTree(t)

	// Routes that are host objects by construction: HTTPRoute documents of
	// the host-apps tree, parented to the gateway.
	hostRouted := map[string]bool{}
	for path, body := range files {
		if !strings.HasPrefix(path, PerOrgHostAppsDir+"/") {
			continue
		}
		for _, d := range parseDocs(t, path, body) {
			if d.str("kind") != "HTTPRoute" {
				continue
			}
			parents, _ := d.get("spec", "parentRefs").([]any)
			onGateway := false
			for _, p := range parents {
				pm, _ := p.(map[string]any)
				if pm["name"] == appsGatewayName && pm["namespace"] == appsGatewayNamespace {
					onGateway = true
				}
			}
			hs, _ := d.get("spec", "hostnames").([]any)
			for _, h := range hs {
				if s, _ := h.(string); s != "" && onGateway {
					hostRouted[s] = true
				}
			}
		}
	}

	want := map[string]bool{
		fmt.Sprintf("openclaw.%s.%s", defaultAppsSlug, defaultAppsParent): false,
		fmt.Sprintf("api.%s.%s", defaultAppsSlug, defaultAppsParent):      false,
	}
	var names []string
	for path := range files {
		if strings.HasPrefix(path, PerOrgHostAppsDir+"/app-") {
			names = append(names, strings.TrimPrefix(path, PerOrgHostAppsDir+"/"))
		}
	}
	sort.Strings(names)
	for _, name := range names {
		docs := parseDocs(t, name, files[PerOrgHostAppsDir+"/"+name])
		for _, hr := range docs {
			if hr.str("kind") != "HelmRelease" {
				continue
			}
			if !strings.Contains(hostIndex, "- "+name+"\n") {
				t.Errorf("%s is not in the host-apps index, so Flux never applies it:\n%s", name, hostIndex)
			}
			hosts, parentName, parentNS := releaseHostnames(hr)
			for _, h := range hosts {
				if _, tracked := want[h]; tracked {
					want[h] = true
				}
				if _, ok := hostRouted[h]; ok {
					continue
				}
				if installsInVCluster(hr) {
					t.Errorf("%s: the route of %s is rendered by its chart INSIDE the Organization's vCluster, which "+
						"copies no HTTPRoute to the host (loft vcluster 0.33.4, #4993), and no host route carries the "+
						"hostname: the host gateway has the listener and no route, every request answers 404",
						name, h)
					continue
				}
				if parentName != appsGatewayName || parentNS != appsGatewayNamespace {
					t.Errorf("%s: the route of %s is parented to %s/%s, not the host gateway %s/%s",
						name, h, parentNS, parentName, appsGatewayNamespace, appsGatewayName)
				}
			}
		}
	}
	for h, seen := range want {
		if !seen {
			t.Errorf("no release of the new Organization declares %s — the check has nothing to route (vacuity)", h)
		}
	}
}
