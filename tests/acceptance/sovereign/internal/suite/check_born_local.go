package suite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check SOV-G01, Born local: at its root a region reads nothing from outside
// the Sovereign. In every region (primary first) it asserts three clauses; the
// fourth of the design, the catalogue as a verified OCIRepository, is SOV-510's.
//
//  1. Every Node carries the annotation k3s.io/node-args, the arguments k3s
//     started with, and they hold --disable-default-registry-endpoint, not
//     followed by a false value. The registry file
//     /etc/rancher/k3s/registries.yaml of every node, read by the node probe
//     (sec_probe.go, namespace born-local-<run>, one Pod per node), lists
//     https://<LOCAL_REGISTRY_HOST> first for every name, and has the entry
//     "*", so that a name it does not list goes to that registry too.
//     LOCAL_REGISTRY_HOST is the region's, from the ConfigMap
//     flux-system/sovereign-variables. The file is read as JSON, the form the
//     provider modules write; only its mirrors are decoded and recorded, never
//     its configs, which hold a login.
//  2. Flux has a GitRepository, and none outside the Sovereign: the host of
//     every spec.url is a cluster Service (a name ending in .svc or
//     .svc.cluster.local), SOVEREIGN_FQDN of sovereign-variables or a name
//     under it.
//  3. The root Kustomizations (bornLocalRoots; infrastructure-providers is
//     asserted in the primary region only) are Ready and owned by Flux from
//     the Sovereign's own repository: the Flux labels of each (name and
//     namespace) name a Kustomization of flux-system whose source is a
//     GitRepository inside the Sovereign with the path
//     openova/sovereign-regions, and that Kustomization is Ready with a
//     lastAppliedRevision equal to the revision of its source's artifact.
//
// A ConfigMap, key, annotation, file or list the check cannot read fails the
// step that needs it; an absent value is never read as a pass.
const bornLocalID = "SOV-G01"

const (
	bornLocalFlag        = "--disable-default-registry-endpoint"
	bornLocalPrimaryOnly = "infrastructure-providers"
	bornLocalRepository  = "/openova/sovereign-regions"
	bornLocalGit         = "gitrepositories.source.toolkit.fluxcd.io"
	bornLocalKs          = "kustomizations.kustomize.toolkit.fluxcd.io"
)

// bornLocalRoots are RootKustomizations of core/cmd/sovereign-ops/internal/
// seedrepo/seedrepo.go, the Kustomizations that seed-repo commits; a test
// keeps the two lists equal.
var bornLocalRoots = []string{"bootstrap-kit", "bootstrap-kit-crs", "sovereign-tls", "infrastructure-providers"}

// bornLocalScript runs in a probe Pod as `sh -s -- /host born-local <registry
// host>`. It prints the host it was given and the registry file in base64, or
// registries=unreadable; it judges nothing and writes nothing.
const bornLocalScript = `f=$1/etc/rancher/k3s/registries.yaml
echo "own=$3"
if [ -r "$f" ]; then echo "registries=$(base64 < "$f" | tr -d '\n')"; else echo registries=unreadable; fi
`

type bornLocalCheck struct{ kubectl kubectlFunc }

func init() { RegisterCheck(bornLocalCheck{kubectl: kube.Region.Run}) }

func (bornLocalCheck) ID() string { return bornLocalID }

func (c bornLocalCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	n := 0
	for i, region := range cc.Regions {
		vars, varsErr := bornLocalVariables(ctx, region)
		repos, reposErr := region.GetList(ctx, bornLocalGit, "")
		n++
		checkStep(rec, bornLocalID, n, "every node of region "+region.ID+" starts k3s with "+bornLocalFlag, func(ev Evidence) error {
			return bornLocalNodes(ctx, region, ev)
		})
		n++
		checkStep(rec, bornLocalID, n, "no Flux GitRepository of region "+region.ID+" reads from outside the Sovereign", func(ev Evidence) error {
			if err := errors.Join(varsErr, reposErr); err != nil {
				return err
			}
			return bornLocalSources(region, repos, vars["SOVEREIGN_FQDN"], ev)
		})
		n++
		checkStep(rec, bornLocalID, n, "the root Kustomizations of region "+region.ID+" are Ready and owned by Flux from the Sovereign's own repository", func(ev Evidence) error {
			ks, err := region.GetList(ctx, bornLocalKs, "flux-system")
			if err = errors.Join(varsErr, reposErr, err); err != nil {
				return err
			}
			return bornLocalOwned(region, i == 0, ks, repos, vars["SOVEREIGN_FQDN"], ev)
		})
	}
	return secProbe{id: bornLocalID, prefix: "born-local", mode: "born-local", script: bornLocalScript, selector: "kubernetes.io/hostname",
		first: n, kubectl: c.kubectl, what: "the registry file lists the region's own registry first for every name",
		setup: func(ctx context.Context, region kube.Region, _ string) ([]string, error) {
			vars, err := bornLocalVariables(ctx, region)
			if err == nil && vars["LOCAL_REGISTRY_HOST"] == "" {
				err = fmt.Errorf("sovereign-variables of region %s has no LOCAL_REGISTRY_HOST", region.ID)
			}
			return []string{vars["LOCAL_REGISTRY_HOST"]}, err
		},
		judge: judgeBornLocalRegistry}.run(ctx, cc, rec)
}

// bornLocalVariables returns the data of the ConfigMap flux-system/sovereign-variables.
func bornLocalVariables(ctx context.Context, region kube.Region) (map[string]string, error) {
	list, err := region.GetList(ctx, "configmaps", "flux-system")
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		if m.Name() == "sovereign-variables" {
			return sovereignObjectStrings(m.Get("data")), nil
		}
	}
	return nil, fmt.Errorf("region %s has no ConfigMap flux-system/sovereign-variables", region.ID)
}

func bornLocalNodes(ctx context.Context, region kube.Region, ev Evidence) error {
	nodes, err := region.GetList(ctx, "nodes", "")
	if err != nil {
		return err
	}
	names, lacking := []string{}, []string{}
	for _, node := range nodes {
		names = append(names, node.Name())
		var args []string
		if json.Unmarshal([]byte(node.Str("metadata", "annotations", "k3s.io/node-args")), &args) != nil || !bornLocalFlagSet(args) {
			lacking = append(lacking, node.Name())
		}
	}
	ev["nodes"], ev["without_flag"] = names, lacking
	switch {
	case len(nodes) == 0:
		return fmt.Errorf("region %s lists no node", region.ID)
	case len(lacking) > 0:
		return fmt.Errorf("region %s: k3s.io/node-args of the nodes [%s] does not hold %s", region.ID, strings.Join(lacking, " "), bornLocalFlag)
	}
	return nil
}

// bornLocalFlagSet reports whether the arguments set the flag. k3s records
// --name=value as the two words --name and value.
func bornLocalFlagSet(args []string) bool {
	for i, a := range args {
		if a == bornLocalFlag {
			if i+1 < len(args) {
				if b, err := strconv.ParseBool(args[i+1]); err == nil && !b {
					return false
				}
			}
			return true
		}
	}
	return false
}

func bornLocalSources(region kube.Region, repos []kube.Object, fqdn string, ev Evidence) error {
	urls, outside := map[string]string{}, []string{}
	for _, r := range repos {
		id := r.Namespace() + "/" + r.Name()
		urls[id] = r.Str("spec", "url")
		if !bornLocalInside(urls[id], fqdn) {
			outside = append(outside, id+" "+urls[id])
		}
	}
	sort.Strings(outside)
	ev["gitrepositories"] = urls
	switch {
	case fqdn == "":
		return fmt.Errorf("sovereign-variables of region %s has no SOVEREIGN_FQDN to judge the hosts by", region.ID)
	case len(repos) == 0:
		return fmt.Errorf("region %s: Flux has no GitRepository, not even the Sovereign's own repository", region.ID)
	case len(outside) > 0:
		return fmt.Errorf("region %s: GitRepositories read from outside the Sovereign: [%s]", region.ID, strings.Join(outside, ", "))
	}
	return nil
}

// bornLocalInside reports whether the host of a source URL is a cluster
// Service, the Sovereign's FQDN or a name under it.
func bornLocalInside(raw, fqdn string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	h, fqdn := strings.ToLower(u.Hostname()), strings.ToLower(fqdn)
	return strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".svc.cluster.local") || (fqdn != "" && (h == fqdn || strings.HasSuffix(h, "."+fqdn)))
}

func bornLocalOwned(region kube.Region, primary bool, ks, repos []kube.Object, fqdn string, ev Evidence) error {
	byName, sources := map[string]kube.Object{}, map[string]kube.Object{}
	for _, k := range ks {
		byName[k.Name()] = k
	}
	for _, r := range repos {
		sources[r.Namespace()+"/"+r.Name()] = r
	}
	faults, owners, roots := []string{}, map[string][]string{}, Evidence{}
	for _, name := range bornLocalRoots {
		k, ok := byName[name]
		if !ok {
			if primary || name != bornLocalPrimaryOnly {
				faults = append(faults, "no Kustomization flux-system/"+name)
			}
			continue
		}
		if status, _, message, _ := k.Condition("Ready"); status != "True" {
			faults = append(faults, fmt.Sprintf("%s is not Ready (%s)", name, message))
		}
		owner := k.Str("metadata", "labels", "kustomize.toolkit.fluxcd.io/name")
		roots[name] = owner
		if owner == "" || k.Str("metadata", "labels", "kustomize.toolkit.fluxcd.io/namespace") != "flux-system" {
			faults = append(faults, name+" is not owned by a Kustomization of flux-system")
			continue
		}
		owners[owner] = append(owners[owner], name)
	}
	read := Evidence{}
	for _, o := range slices.Sorted(maps.Keys(owners)) {
		source, fault := bornLocalOwner(byName, sources, o, fqdn)
		read[o] = source
		if fault != "" {
			faults = append(faults, fmt.Sprintf("the owner %s of [%s] %s", o, strings.Join(owners[o], " "), fault))
		}
	}
	ev["roots"], ev["owners"] = roots, read
	if len(faults) > 0 {
		return fmt.Errorf("region %s: %s", region.ID, strings.Join(faults, "; "))
	}
	return nil
}

// bornLocalOwner judges the Kustomization that applied a root Kustomization:
// the URL of its source, and what is wrong with it ("" when nothing is).
func bornLocalOwner(byName, sources map[string]kube.Object, name, fqdn string) (string, string) {
	o, ok := byName[name]
	if !ok {
		return "", "is not a Kustomization of flux-system"
	}
	kind, ns := o.Str("spec", "sourceRef", "kind"), o.Str("spec", "sourceRef", "namespace")
	if ns == "" {
		ns = o.Namespace()
	}
	src, ok := sources[ns+"/"+o.Str("spec", "sourceRef", "name")]
	if kind != "GitRepository" || !ok {
		return "", fmt.Sprintf("reads the %s %s/%s, which is no GitRepository of the region", kind, ns, o.Str("spec", "sourceRef", "name"))
	}
	raw := src.Str("spec", "url")
	u, err := url.Parse(raw)
	if err != nil || !bornLocalInside(raw, fqdn) || strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git") != bornLocalRepository {
		return raw, "reads " + raw + ", not the Sovereign's own repository"
	}
	if status, _, message, _ := o.Condition("Ready"); status != "True" {
		return raw, "is not Ready (" + message + ")"
	}
	applied, held := o.Str("status", "lastAppliedRevision"), src.Str("status", "artifact", "revision")
	if applied == "" || applied != held {
		return raw, fmt.Sprintf("applied the revision %q, its source holds %q", applied, held)
	}
	return raw, ""
}

// judgeBornLocalRegistry judges what the probe printed in the mode born-local.
func judgeBornLocalRegistry(v probeValues, ev Evidence) error {
	own := "https://" + v.one("own")
	ev["own_registry"] = own
	switch raw := v["registries"]; {
	case len(raw) == 0:
		return errors.New("the probe did not finish")
	case raw[len(raw)-1] == "unreadable":
		return errors.New("the node has no readable /etc/rancher/k3s/registries.yaml")
	}
	var file struct {
		Mirrors map[string]struct {
			Endpoint []string `json:"endpoint"`
		} `json:"mirrors"`
	}
	b, err := base64.StdEncoding.DecodeString(v.one("registries"))
	if err != nil || json.Unmarshal(b, &file) != nil {
		return errors.New("/etc/rancher/k3s/registries.yaml is not the JSON the provider modules write")
	}
	mirrors, other := map[string][]string{}, []string{}
	for name, m := range file.Mirrors {
		mirrors[name] = m.Endpoint
		if len(m.Endpoint) == 0 || m.Endpoint[0] != own {
			other = append(other, name)
		}
	}
	sort.Strings(other)
	ev["mirrors"] = mirrors
	faults := []string{}
	if len(mirrors) == 0 {
		faults = append(faults, "the registry file lists no name")
	} else if _, ok := file.Mirrors["*"]; !ok {
		faults = append(faults, `the registry file has no entry "*", so a name it does not list does not go to the region's own registry`)
	}
	if len(other) > 0 {
		faults = append(faults, fmt.Sprintf("the first endpoint is not %s for [%s]", own, strings.Join(other, " ")))
	}
	if len(faults) > 0 {
		return errors.New(strings.Join(faults, "; "))
	}
	return nil
}
