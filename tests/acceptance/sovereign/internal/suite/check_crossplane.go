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
// asserts what one run can see, in every region:
//   - the region has at least one CloudAdoption claim;
//   - every CloudAdoption claim is Synced=True and Ready=True;
//   - every claim composes exactly one Workspace: one Workspace carries the
//     label crossplane.io/composite with the value of the claim's
//     spec.resourceRef.name. A claim without spec.resourceRef.name composes
//     none;
//   - that Workspace is Synced=True and Ready=True.
//
// The package reference (spec.package) of every Crossplane provider and
// function is recorded as evidence and not judged: the hand-over rewrites the
// package references, so one run cannot say which registry is right. The check
// only reads: it runs `kubectl get` and nothing else.
const crossplaneID = "CLD-G06"

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
		rec.Step(id, "every CloudAdoption claim is Synced and Ready and composes exactly one Synced and Ready Workspace in region "+region.ID, func(ev Evidence) error {
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
