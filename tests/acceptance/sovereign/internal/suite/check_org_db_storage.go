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

// Check CLD-G09, per-Organization database volumes use the Sovereign's
// storage class: every volume of a per-Organization database is Bound, with
// the storage class that the Sovereign was installed with.
//
// The provisioning service installs the database of an Organization with an
// active-hot-standby database as two HelmReleases of the chart bp-cnpg-pair,
// one per side. Both carry the label catalyst.openova.io/component=cnpg-pair
// and the label catalyst.openova.io/cnpg-pair-side. The release of
// bootstrap-kit slot 16b carries the first label only; it is the platform's
// own and is not judged. Each release installs into its spec.targetNamespace,
// the namespace of the Organization: the primary side in the first region, the
// replica side in the second. CNPG puts the label cnpg.io/cluster on every
// PVC of its Clusters.
//
// The storage class of a region is the value of the kit key
// SOVEREIGN_CNPG_STORAGE_CLASS in the bootstrap variables of that region (the
// ConfigMap flux-system/sovereign-variables, or spec.postBuild.substitute of
// the Kustomization flux-system/bootstrap-kit on a region installed before
// it). The provisioning service names that class on the volumes it asks for.
//
// Steps:
//
//   - CLD-G09.1: the per-Organization database releases of every region are
//     listed. None in any region fails: nothing would be measured.
//   - CLD-G09.2 and on, one per region: the storage class of the region is
//     read, then every PVC with the label cnpg.io/cluster in each namespace of
//     those releases. Each must be in phase Bound with spec.storageClassName
//     equal to the class. A missing or empty class fails.
//   - the last step: at least one volume was judged in some region.
//
// The check only reads: `kubectl get`.
const orgDBStorageID = "CLD-G09"

const (
	orgDBComponentLabel = "catalyst.openova.io/component"
	orgDBComponent      = "cnpg-pair"
	orgDBSideLabel      = "catalyst.openova.io/cnpg-pair-side"
	orgDBClusterLabel   = "cnpg.io/cluster"
	orgDBClassKey       = "SOVEREIGN_CNPG_STORAGE_CLASS"
	orgDBVolumes        = "persistentvolumeclaims"
)

type orgDBStorageCheck struct{}

func init() { RegisterCheck(orgDBStorageCheck{}) }

func (orgDBStorageCheck) ID() string { return orgDBStorageID }

// orgDBHasLabel says whether the object carries the label key, and its value.
func orgDBHasLabel(o kube.Object, key string) (string, bool) {
	labels, _ := o.Get("metadata", "labels").(map[string]any)
	v, ok := labels[key]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

func (orgDBStorageCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	n := 0
	next := func() string { n++; return orgDBStorageID + "." + strconv.Itoa(n) }

	namespaces := []string{}
	listed := false
	rec.Step(next(), "the per-Organization database releases are found", func(ev Evidence) error {
		seen := map[string]bool{}
		releases := []string{}
		for _, region := range cc.Regions {
			hrs, err := region.GetList(ctx, hrResource, "", "-l", orgDBComponentLabel+"="+orgDBComponent+","+orgDBSideLabel)
			if err != nil {
				return err
			}
			for _, hr := range hrs {
				if v, _ := orgDBHasLabel(hr, orgDBComponentLabel); v != orgDBComponent {
					continue
				}
				if _, side := orgDBHasLabel(hr, orgDBSideLabel); !side {
					continue
				}
				ns := nonEmpty(hr.Str("spec", "targetNamespace"), hr.Namespace())
				releases = append(releases, region.ID+" "+hr.Namespace()+"/"+hr.Name()+" -> "+ns)
				if !seen[ns] {
					seen[ns] = true
					namespaces = append(namespaces, ns)
				}
			}
		}
		sort.Strings(releases)
		sort.Strings(namespaces)
		ev["releases"], ev["namespaces"] = releases, namespaces
		if len(namespaces) == 0 {
			return fmt.Errorf("no HelmRelease with the labels %s=%s and %s in any region: no per-Organization database exists, so nothing was measured",
				orgDBComponentLabel, orgDBComponent, orgDBSideLabel)
		}
		listed = true
		return nil
	})
	if !listed {
		return nil
	}

	judged := 0
	for _, region := range cc.Regions {
		rec.Step(next(), "per-Organization database volumes are Bound with the storage class of region "+region.ID, func(ev Evidence) error {
			vars, source, err := sovereignVariables(ctx, region)
			if err != nil {
				return err
			}
			class := strings.TrimSpace(vars[orgDBClassKey])
			ev["class"], ev["class_source"] = class, source
			if class == "" {
				return fmt.Errorf("the bootstrap variables of region %s (%s) have no value for %s", region.ID, source, orgDBClassKey)
			}
			volumes, wrong := []string{}, []string{}
			for _, ns := range namespaces {
				pvcs, err := region.GetList(ctx, orgDBVolumes, ns, "-l", orgDBClusterLabel)
				if err != nil {
					return err
				}
				for _, pvc := range pvcs {
					if _, ok := orgDBHasLabel(pvc, orgDBClusterLabel); !ok {
						continue
					}
					phase, got := pvc.Str("status", "phase"), pvc.Str("spec", "storageClassName")
					line := ns + "/" + pvc.Name() + " " + phase + " " + got
					volumes = append(volumes, line)
					if phase != "Bound" || got != class {
						wrong = append(wrong, line)
					}
				}
			}
			sort.Strings(volumes)
			sort.Strings(wrong)
			judged += len(volumes)
			ev["volumes"], ev["wrong"] = volumes, wrong
			if len(wrong) > 0 {
				return fmt.Errorf("%d per-Organization database volumes in region %s are not Bound with the storage class %s: %s",
					len(wrong), region.ID, class, strings.Join(wrong, "; "))
			}
			return nil
		})
	}
	rec.Step(next(), "at least one per-Organization database volume was judged", func(ev Evidence) error {
		ev["judged"] = judged
		if judged == 0 {
			return fmt.Errorf("the namespaces %s hold no PVC with the label %s in any region: nothing was measured",
				strings.Join(namespaces, ", "), orgDBClusterLabel)
		}
		return nil
	})
	return nil
}
