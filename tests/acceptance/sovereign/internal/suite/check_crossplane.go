package suite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check CLD-G06, Crossplane upgrade leaves Workspaces and packages intact. It
// asserts what one run can see.
//
// The adoption seam is the primary region's alone: the provisioner applies one
// CloudAdoption per cloud resource of every region onto the primary region,
// from the one OpenTofu state, and only the primary's control-plane user data
// creates the Flux Kustomizations that install provider-opentofu and its
// configuration (infra/providers/_shared/cloudinit-control-plane.tftpl, the
// block `if sovereign_region_role == "primary"`). A follower region therefore
// holds no claim and serves no Workspace kind. So, in the primary region (the
// first of the run):
//   - the region has at least one CloudAdoption claim;
//   - every CloudAdoption claim is Synced=True and Ready=True;
//   - every claim composes exactly one Workspace: one Workspace carries the
//     label crossplane.io/composite with the value of the claim's
//     spec.resourceRef.name. A claim without spec.resourceRef.name composes
//     none;
//   - that Workspace is Synced=True and Ready=True.
//
// In every other region: no CloudAdoption claim exists (a region that does not
// serve the kind holds none). A claim there would observe a second time what
// the primary observes.
//
// In the primary region the OpenTofu CLI configuration of the provider pod
// (SOV-342: the key .tofurc of the ConfigMap crossplane-system/opentofu-cli-config,
// with which every tofu init of a Workspace installs its providers) is judged
// (SOV-343): it names at least one OCI mirror (repository_template) and no other
// installation method (direct, network_mirror, filesystem_mirror, dev_overrides);
// when the ConfigMap catalyst/self-sovereign-cutover-status says
// cutoverComplete=true every mirror is the Sovereign's registry (registry. or
// harbor. before SOVEREIGN_FQDN of flux-system/sovereign-variables, then
// /openova-io/tofu-providers/), before it the origin
// (ghcr.io/openova-io/tofu-providers/). A configuration that is absent is a
// finding: tofu init then takes the implied configuration, which installs from
// the public registry.
//
// The package reference (spec.package) of every Crossplane provider and
// function is recorded as evidence and not judged: the hand-over rewrites the
// package references, so one run cannot say which registry is right. The check
// only reads: it runs `kubectl get` and nothing else.
const crossplaneID = "CLD-G06"

// The OpenTofu CLI configuration of the provider pod, and the mirror paths it
// may name.
const (
	crossplaneCLINamespace  = "crossplane-system"
	crossplaneCLIConfigMap  = "opentofu-cli-config"
	crossplaneCLIKey        = ".tofurc"
	crossplaneTofuOrigin    = "ghcr.io/openova-io/tofu-providers/"
	crossplaneTofuLocalPath = "/openova-io/tofu-providers/"
)

// crossplaneCLIMethods are the installation methods of an OpenTofu CLI
// configuration other than oci_mirror: each can fetch a provider from a source
// the Sovereign does not hold.
var crossplaneCLIMethods = []string{"direct", "network_mirror", "filesystem_mirror", "dev_overrides"}

// The kinds the check reads, by their full resource names.
const (
	crossplaneClaimResource     = "cloudadoptions.compose.openova.io"
	crossplaneWorkspaceResource = "workspaces.opentofu.upbound.io"
	crossplaneProviderResource  = "providers.pkg.crossplane.io"
	crossplaneFunctionResource  = "functions.pkg.crossplane.io"
)

// crossplaneCompositeLabel is the label that Crossplane puts on every
// resource it composes. Its value is the name of the composite resource.
const crossplaneCompositeLabel = "crossplane.io/composite"

type crossplaneCheck struct{}

func init() { RegisterCheck(crossplaneCheck{}) }

func (crossplaneCheck) ID() string { return crossplaneID }

func (crossplaneCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, region := range cc.Regions {
		id := crossplaneID + "." + strconv.Itoa(i+1)
		if i > 0 {
			rec.Step(id, "follower region "+region.ID+" holds no CloudAdoption claim: the adoption seam is the primary region's", func(ev Evidence) error {
				crossplanePackages(ctx, region, ev)
				claims, err := region.GetList(ctx, crossplaneClaimResource, "")
				if err != nil && strings.Contains(err.Error(), notServed) {
					ev["claims"], ev["claims_served"] = 0, false
					return nil
				}
				if err != nil {
					return err
				}
				names := make([]string, 0, len(claims))
				for _, claim := range claims {
					names = append(names, claim.Namespace()+"/"+claim.Name())
				}
				sort.Strings(names)
				ev["claims"], ev["claim_names"] = len(claims), names
				if len(claims) > 0 {
					return fmt.Errorf("follower region %s holds %d CloudAdoption claims (%s): the adoption seam is the primary region's alone",
						region.ID, len(claims), strings.Join(names, ", "))
				}
				return nil
			})
			continue
		}
		rec.Step(id, "every CloudAdoption claim is Synced and Ready and composes exactly one Synced and Ready Workspace in the primary region "+region.ID, func(ev Evidence) error {
			crossplanePackages(ctx, region, ev)
			claims, err := region.GetList(ctx, crossplaneClaimResource, "")
			if err != nil {
				return err
			}
			workspaces, err := region.GetList(ctx, crossplaneWorkspaceResource, "")
			if err != nil {
				return err
			}
			byComposite := map[string][]kube.Object{}
			for _, w := range workspaces {
				if composite := w.Str("metadata", "labels", crossplaneCompositeLabel); composite != "" {
					byComposite[composite] = append(byComposite[composite], w)
				}
			}
			composed, bad := map[string]string{}, []string{}
			for _, claim := range claims {
				name := claim.Namespace() + "/" + claim.Name()
				if why := crossplaneSyncedReady(claim); why != "" {
					bad = append(bad, "claim "+name+" "+why)
				}
				composite := claim.Str("spec", "resourceRef", "name")
				if composite == "" {
					bad = append(bad, "claim "+name+" has no spec.resourceRef.name")
					continue
				}
				own := byComposite[composite]
				if len(own) != 1 {
					bad = append(bad, fmt.Sprintf("claim %s composes %d Workspaces with %s=%s, want 1", name, len(own), crossplaneCompositeLabel, composite))
					continue
				}
				composed[name] = own[0].Name()
				if why := crossplaneSyncedReady(own[0]); why != "" {
					bad = append(bad, "Workspace "+own[0].Name()+" of claim "+name+" "+why)
				}
			}
			bad = append(bad, crossplaneCLIConfig(ctx, region, ev)...)
			sort.Strings(bad)
			ev["claims"], ev["workspaces"], ev["composed"], ev["not_ready"] = len(claims), len(workspaces), composed, bad
			if len(claims) == 0 {
				return errors.New("no CloudAdoption claim was found: nothing was measured")
			}
			if len(bad) > 0 {
				return fmt.Errorf("%d problems with the %d CloudAdoption claims: %s", len(bad), len(claims), strings.Join(bad, "; "))
			}
			return nil
		})
	}
	return nil
}

// crossplaneSyncedReady returns "" when the object is Synced=True and
// Ready=True, and else what is wrong with it.
func crossplaneSyncedReady(o kube.Object) string {
	why := []string{}
	for _, t := range []string{"Synced", "Ready"} {
		status, reason, _, found := o.Condition(t)
		switch {
		case !found:
			why = append(why, "has no "+t+" condition")
		case status != "True":
			why = append(why, "is "+t+"="+status+" ("+reason+")")
		}
	}
	return strings.Join(why, " and ")
}

// crossplaneCLIConfig judges the OpenTofu CLI configuration of the region: one
// line per problem. It records the cutover state, the mirrors and the other
// installation methods it read as evidence.
func crossplaneCLIConfig(ctx context.Context, region kube.Region, ev Evidence) []string {
	const where = "the OpenTofu CLI configuration " + crossplaneCLINamespace + "/" + crossplaneCLIConfigMap
	status, _, err := getNamed(ctx, region, "configmaps", "catalyst", "self-sovereign-cutover-status")
	if err != nil {
		return []string{"the cutover state cannot be read, so " + where + " cannot be judged: " + err.Error()}
	}
	cut := stringValues(status.Get("data"))["cutoverComplete"] == "true"
	ev["cutover_complete"] = cut
	want := []string{crossplaneTofuOrigin}
	if cut {
		vars, _, err := sovereignVariables(ctx, region)
		if err != nil {
			return []string{"the Sovereign's registry cannot be named, so " + where + " cannot be judged: " + err.Error()}
		}
		fqdn := vars["SOVEREIGN_FQDN"]
		if fqdn == "" {
			return []string{"SOVEREIGN_FQDN is empty: the Sovereign's registry cannot be named, so " + where + " cannot be judged"}
		}
		want = []string{"registry." + fqdn + crossplaneTofuLocalPath, "harbor." + fqdn + crossplaneTofuLocalPath}
	}
	cm, found, err := getNamed(ctx, region, "configmaps", crossplaneCLINamespace, crossplaneCLIConfigMap)
	if err != nil {
		return []string{where + " cannot be read: " + err.Error()}
	}
	text := stringValues(cm.Get("data"))[crossplaneCLIKey]
	if !found || strings.TrimSpace(text) == "" {
		return []string{where + " (key " + crossplaneCLIKey + ") is absent: tofu init takes the implied configuration, which installs from registry.opentofu.org"}
	}
	mirrors, methods := crossplaneCLIParse(text)
	ev["tofu_mirrors"], ev["tofu_methods"] = mirrors, methods
	var bad []string
	for _, m := range methods {
		bad = append(bad, where+" names the installation method "+m+": a provider can come from outside the Sovereign's registry")
	}
	if len(mirrors) == 0 {
		bad = append(bad, where+" names no OCI mirror (repository_template)")
	}
	for _, m := range mirrors {
		ok := false
		for _, w := range want {
			ok = ok || strings.HasPrefix(m, w)
		}
		if !ok {
			bad = append(bad, fmt.Sprintf("%s names the mirror %s; with cutoverComplete=%t it may name only %s", where, m, cut, strings.Join(want, " or ")))
		}
	}
	return bad
}

// crossplaneCLIParse returns the repository_template values of an OpenTofu CLI
// configuration and the other installation methods it opens, in their order.
// Comment lines (# or //) are not read.
func crossplaneCLIParse(text string) (mirrors, methods []string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "repository_template"); ok {
			if v, ok := strings.CutPrefix(strings.TrimSpace(rest), "="); ok {
				mirrors = append(mirrors, strings.Trim(strings.TrimSpace(v), `"`))
			}
			continue
		}
		word := strings.TrimSpace(strings.SplitN(line, "{", 2)[0])
		for _, m := range crossplaneCLIMethods {
			if word == m {
				methods = append(methods, m)
			}
		}
	}
	return mirrors, methods
}

// crossplanePackages records the package reference of every Crossplane
// provider and function of the region as evidence. Nothing here is judged:
// a list that cannot be read is recorded with its error.
func crossplanePackages(ctx context.Context, region kube.Region, ev Evidence) {
	packages, unread := map[string]string{}, []string{}
	for _, kind := range []struct{ prefix, resource string }{
		{"provider/", crossplaneProviderResource},
		{"function/", crossplaneFunctionResource},
	} {
		list, err := region.GetList(ctx, kind.resource, "")
		if err != nil {
			unread = append(unread, err.Error())
			continue
		}
		for _, p := range list {
			packages[kind.prefix+p.Name()] = p.Str("spec", "package")
		}
	}
	ev["packages"] = packages
	if len(unread) > 0 {
		ev["packages_unread"] = unread
	}
}
