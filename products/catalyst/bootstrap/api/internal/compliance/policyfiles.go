package compliance

// The policy files of every region (contract C4, section 2.2). The policy
// values document, and later the decisions and engine exceptions, live in
// the same files in every region folder of the fleet repository:
//
//	clusters/<fqdn>/regions/<region-id>/platform/policy/
//
// Each region's sovereign-region Kustomization applies its own folder. This
// file is the one writer of that folder: Regions lists the regions,
// WriteAll writes one file into all of them in one commit, and Serialize
// gives the bytes of an object. None of them writes a Flux object or a
// kustomization.yaml, and none writes outside platform/policy/.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/intent"
)

const (
	// PolicyModule is the module of contract C4 that holds the policy files.
	PolicyModule = "policy"
	// ValuesFile is the policy values document, relative to the policy folder.
	ValuesFile = "policy-values.yaml"
	// ValuesConfigMap, ValuesNamespace and ValuesKey name what slot 27a reads
	// through spec.valuesFrom of the HelmRelease bp-kyverno-policies.
	ValuesConfigMap = "compliance-policy-values"
	ValuesNamespace = "flux-system"
	ValuesKey       = "values.yaml"
	// EmptyValues is the content of ValuesKey in the empty document: it sets
	// no value, so the chart defaults apply.
	EmptyValues = "{}\n"
	// SubstituteAnnotation with SubstituteDisabled stops the region
	// Kustomization from substituting variables in the object.
	SubstituteAnnotation = "kustomize.toolkit.fluxcd.io/substitute"
	SubstituteDisabled   = "disabled"
	// WatchLabel with WatchEnabled asks helm-controller to reconcile the
	// HelmRelease that reads the document when the document changes.
	WatchLabel   = "reconcile.fluxcd.io/watch"
	WatchEnabled = "Enabled"
)

// FleetRepo is the fleet repository of contract C4, section 2.1.
var FleetRepo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions", Branch: "main"}

// ErrNoRegion is returned when the fleet repository holds no region folder
// for the Sovereign: there is no region to write to and no reference region.
var ErrNoRegion = errors.New("compliance: the fleet repository holds no region folder")

// PolicyScope is the scope of the policy files: module "policy" of every
// listed region (gitwrite.RegionPlatform). An empty list refuses every commit.
func PolicyScope(sovereignFQDN string, regionIDs []string) gitwrite.Scope {
	return gitwrite.RegionPlatform(sovereignFQDN, []string{PolicyModule}, regionIDs)
}

// policyDir is the policy folder of one region, relative to that region's
// folder, with a trailing slash.
const policyDir = "platform/" + PolicyModule + "/"

// Regions returns the region IDs of a Sovereign: the folders directly under
// clusters/<fqdn>/regions/ of the fleet repository, sorted by name. A
// Sovereign with no region folder returns ErrNoRegion.
func Regions(ctx context.Context, w gitwrite.Writer, sovereignFQDN string) ([]string, error) {
	ids, err := gitwrite.Regions(ctx, w, FleetRepo, sovereignFQDN)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: clusters/%s/regions/", ErrNoRegion, sovereignFQDN)
	}
	return ids, nil
}

// checkRel refuses a path that is not a file path below the policy folder,
// and an index file: no kustomization.yaml is written (C4, rule 9).
func checkRel(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return fmt.Errorf("%w: %q is not a file path below %s", gitwrite.ErrInvalid, rel, policyDir)
	}
	for _, el := range strings.Split(rel, "/") {
		if el == "" || el == "." || el == ".." {
			return fmt.Errorf("%w: %q is not a file path below %s", gitwrite.ErrInvalid, rel, policyDir)
		}
	}
	switch path.Base(rel) {
	case "kustomization.yaml", "kustomization.yml", "Kustomization":
		return fmt.Errorf("%w: %q is an index file, which no writer commits", gitwrite.ErrInvalid, rel)
	}
	return nil
}

// WriteAll writes content to the path rel below platform/policy/ of every
// region of the Sovereign, in one commit. A nil content deletes the file in
// every region. ifMatch is the compare-and-swap value of the copy in the
// first region by name ("" for none, gitwrite.AbsentSHA for "must not
// exist"); the other copies carry none, so the caller aligns the regions
// first (EnsurePolicyFiles).
//
// WriteAll sets c.Scope (PolicyScope of every region), c.Repo (FleetRepo)
// and c.Files; a c that already carries one of them is refused with
// gitwrite.ErrInvalid. The caller sets Summary, ChangeID, Via, and
// optionally Author, Trailers and Mode. The result lists one file per
// region, regions in name order.
func WriteAll(ctx context.Context, w gitwrite.Writer, sovereignFQDN, rel string, content []byte, ifMatch string, c gitwrite.Change) (gitwrite.Result, error) {
	if err := checkRel(rel); err != nil {
		return gitwrite.Result{}, err
	}
	if len(c.Files) > 0 || c.Repo != (gitwrite.Repo{}) || c.Scope.Name != "" || c.Scope.Robot != "" || len(c.Scope.Prefixes) > 0 {
		return gitwrite.Result{}, fmt.Errorf("%w: WriteAll sets the scope, the repository and the files of the change", gitwrite.ErrInvalid)
	}
	regions, err := Regions(ctx, w, sovereignFQDN)
	if err != nil {
		return gitwrite.Result{}, err
	}
	f := gitwrite.FileChange{Path: policyDir + rel, Op: gitwrite.OpUpsert, Content: content, IfMatch: ifMatch}
	if content == nil {
		f.Op = gitwrite.OpDelete
	}
	c.Scope = PolicyScope(sovereignFQDN, regions)
	c.Repo = FleetRepo
	c.Files = []gitwrite.FileChange{f}
	return gitwrite.WriteAllRegions(ctx, w, sovereignFQDN, regions, c)
}

// Serialize returns the bytes of a policy file holding obj: obj with the
// label app.kubernetes.io/managed-by=flux, the annotation
// catalyst.openova.io/change-id=changeID and the annotation
// kustomize.toolkit.fluxcd.io/substitute=disabled, serialised as every
// intent file is (intent.Serialize). obj is not changed.
func Serialize(obj *unstructured.Unstructured, changeID string) ([]byte, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: no object to serialise", gitwrite.ErrInvalid)
	}
	if changeID == "" {
		return nil, fmt.Errorf("%w: %s %s needs a change identifier", gitwrite.ErrInvalid, obj.GetKind(), obj.GetName())
	}
	c := obj.DeepCopy()
	labels := c.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[intent.ManagedByLabel] = intent.ManagedByValue
	c.SetLabels(labels)
	ann := c.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[intent.ChangeIDAnnotation] = changeID
	ann[SubstituteAnnotation] = SubstituteDisabled
	c.SetAnnotations(ann)
	return intent.Serialize(c)
}

// EmptyValuesDocument is the policy values document that sets no value:
// the ConfigMap compliance-policy-values in flux-system with values.yaml
// "{}\n" and the watch label. Serialize adds the rest of its metadata.
func EmptyValuesDocument() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]interface{}{
			"name":      ValuesConfigMap,
			"namespace": ValuesNamespace,
			"labels":    map[string]interface{}{WatchLabel: WatchEnabled},
		},
		"data": map[string]interface{}{ValuesKey: EmptyValues},
	}}
}
