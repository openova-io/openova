package suite

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// Check NET-G12, Region separation stated: every region's Sovereign object
// states the region separation of the Sovereign, the record carries it as
// provision.separation with its summary line, and a run whose regions share
// one failure domain makes no disaster-recovery claim.
//
// The rule of the provider modules: site when every region has its own
// cloudRegion; zone when the cloud regions repeat but every region has its
// own pair of cloudRegion and az; none otherwise, and always for fewer than
// two regions.
//
// For each region the check reads the Sovereign objects of the cluster
// (exactly one must exist). A region fails when the object has no
// status.network; when status.regionSeparation or
// status.network.regionSeparation differs from the value that the rule gives
// for the regions of status.network; or when the condition RegionSeparation
// does not state that value: status True with the reason SeparateZones for
// zone or SeparateSites for site, status False with the reason
// SameFailureDomain for none. These are the values that the writer of the
// object, core/cmd/sovereign-ops/internal/sovereignobject, writes.
//
// The last step fails when a region failed or the regions state different
// values, and when the value is none while the run declares the capability
// disaster-recovery. It hands the value and the summary line, such as "two
// clusters, one failure domain", to the record. A gate check that needs
// disaster-recovery is not run in a run that does not declare it: the record
// shows it as SKIP, never as PASS. The check only runs `kubectl get`.
const regionSeparationID = "NET-G12"

const (
	// regionSeparationClaim is the capability flag of a disaster-recovery claim.
	regionSeparationClaim = "disaster-recovery"
	// regionSeparationCondition is the condition type that states the value.
	regionSeparationCondition = "RegionSeparation"
)

type regionSeparationCheck struct{}

func init() { RegisterCheck(regionSeparationCheck{}) }

func (regionSeparationCheck) ID() string { return regionSeparationID }

func (regionSeparationCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	values := make([]string, len(cc.Regions))
	counts := make([]int, len(cc.Regions))
	for i, region := range cc.Regions {
		rec.Step(regionSeparationID+"."+strconv.Itoa(i+1), "the Sovereign object of region "+region.ID+" states the region separation", func(ev Evidence) error {
			value, count, err := regionSeparationRegion(ctx, region, ev)
			if err == nil {
				values[i], counts[i] = value, count
			}
			return err
		})
	}
	rec.Step(regionSeparationID+"."+strconv.Itoa(len(cc.Regions)+1), "the region separation goes into the record", func(ev Evidence) error {
		ev["values"] = values
		if len(values) == 0 {
			return fmt.Errorf("the run has no region")
		}
		for i, v := range values {
			if v == "" {
				return fmt.Errorf("region %s states no region separation that the check could confirm (step %s.%d)", cc.Regions[i].ID, regionSeparationID, i+1)
			}
			if v != values[0] {
				return fmt.Errorf("the regions state different region separations: %s", strings.Join(values, ", "))
			}
		}
		sep := &Separation{Value: values[0], Summary: regionSeparationSummary(values[0], counts[0])}
		ev["summary"] = sep.Summary
		ev[SeparationKey] = sep
		if sep.Value == "none" && cc.Caps[regionSeparationClaim] {
			return fmt.Errorf("the run declares %s, but its regions share one failure domain: %s", regionSeparationClaim, sep.Summary)
		}
		return nil
	})
	return nil
}

// regionSeparationRegion returns the value that the Sovereign object of one
// region states and the number of regions of its registry.
func regionSeparationRegion(ctx context.Context, region kube.Region, ev Evidence) (string, int, error) {
	objects, err := region.GetList(ctx, networkRegistryKind, "")
	if err != nil {
		return "", 0, err
	}
	if len(objects) != 1 {
		return "", 0, fmt.Errorf("region %s has %d Sovereign objects, want exactly 1", region.ID, len(objects))
	}
	o := objects[0]
	network, ok := o.Get("status", "network").(map[string]any)
	if !ok {
		return "", 0, fmt.Errorf("region %s: the Sovereign object %s has no status.network", region.ID, o.Name())
	}
	registry := kube.Object(network)
	regions := registry.Objects("regions")
	rule := regionSeparationRule(regions)
	stated := o.Str("status", "regionSeparation")
	status, reason, _, found := o.Condition(regionSeparationCondition)
	ev["stated"], ev["rule"], ev["registry"], ev["regions"] = stated, rule, registry.Str("regionSeparation"), len(regions)
	ev["condition"] = strings.TrimSpace(status + " " + reason)

	failures := []string{}
	if stated != rule {
		failures = append(failures, fmt.Sprintf("status.regionSeparation is %q, the regions of status.network give %s", stated, rule))
	}
	if r := registry.Str("regionSeparation"); r != rule {
		failures = append(failures, fmt.Sprintf("status.network.regionSeparation is %q, the regions of status.network give %s", r, rule))
	}
	wantStatus, wantReason := "False", "SameFailureDomain"
	switch rule {
	case "zone":
		wantStatus, wantReason = "True", "SeparateZones"
	case "site":
		wantStatus, wantReason = "True", "SeparateSites"
	}
	switch {
	case !found:
		failures = append(failures, "the condition "+regionSeparationCondition+" is absent")
	case status != wantStatus || reason != wantReason:
		failures = append(failures, fmt.Sprintf("the condition %s is %s %s, want %s %s", regionSeparationCondition, status, reason, wantStatus, wantReason))
	}
	if len(failures) > 0 {
		return "", 0, fmt.Errorf("region %s: %s", region.ID, strings.Join(failures, "; "))
	}
	return stated, len(regions), nil
}

// regionSeparationRule is the region separation of the regions of a registry.
func regionSeparationRule(regions []kube.Object) string {
	clouds, pairs := map[string]bool{}, map[string]bool{}
	for _, r := range regions {
		clouds[r.Str("cloudRegion")] = true
		pairs[r.Str("cloudRegion")+"/"+r.Str("az")] = true
	}
	switch n := len(regions); {
	case n < 2:
		return "none"
	case len(clouds) == n:
		return "site"
	case len(pairs) == n:
		return "zone"
	}
	return "none"
}

// regionSeparationSummary is the summary line of the record, for example
// "two clusters, one failure domain" or "two clusters in two zones".
func regionSeparationSummary(value string, regions int) string {
	words := []string{"no", "one", "two", "three", "four", "five", "six"}
	n := strconv.Itoa(regions)
	if regions >= 0 && regions < len(words) {
		n = words[regions]
	}
	clusters := n + " clusters"
	if regions == 1 {
		clusters = "one cluster"
	}
	switch value {
	case "zone":
		return clusters + " in " + n + " zones"
	case "site":
		return clusters + " in " + n + " sites"
	}
	return clusters + ", one failure domain"
}
