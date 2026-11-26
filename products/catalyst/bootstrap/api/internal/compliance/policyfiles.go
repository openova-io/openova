package compliance

// The policy files of every region (contract C4, section 2.2). The policy
// values document, and later the decisions and engine exceptions, live in
// the same files in every region folder of the fleet repository:
//
//	clusters/<fqdn>/regions/<region-id>/platform/policy/
//
// Each region's sovereign-region Kustomization applies its own folder. This
// file is the one writer of that folder: Regions lists the regions,
// WriteAll writes one file into all of them in one commit, Serialize gives
// the bytes of an object, and EnsurePolicyFiles brings the regions back to
// the same files. None of them writes a Flux object or a kustomization.yaml,
// and none writes outside platform/policy/.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
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
	// ApplyAnnotation with ApplyIfNotPresent makes Flux create the object
	// when the cluster has none and never apply it to one that exists.
	ApplyAnnotation   = "kustomize.toolkit.fluxcd.io/ssa"
	ApplyIfNotPresent = "IfNotPresent"
	// AlignSummary is the commit summary of EnsurePolicyFiles.
	AlignSummary = "Align policy files across regions"
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

// regionPolicyDir is the policy folder of one region in the fleet
// repository, with a trailing slash.
func regionPolicyDir(sovereignFQDN, region string) string {
	return "clusters/" + sovereignFQDN + "/regions/" + region + "/" + policyDir
}

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
// intent file is (intent.Serialize). It removes ApplyAnnotation, so the
// file it gives always replaces the object in the cluster, also when obj
// was read from the empty document that EnsurePolicyFiles writes. obj is
// not changed.
func Serialize(obj *unstructured.Unstructured, changeID string) ([]byte, error) {
	return serialize(obj, changeID, false)
}

// serialize is Serialize; seed sets ApplyAnnotation to ApplyIfNotPresent
// instead of removing it.
func serialize(obj *unstructured.Unstructured, changeID string, seed bool) ([]byte, error) {
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
	delete(ann, ApplyAnnotation)
	if seed {
		ann[ApplyAnnotation] = ApplyIfNotPresent
	}
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

// policyTree returns the files below one region's policy folder, as path
// relative to that folder -> blob SHA. A missing folder gives an empty map.
func policyTree(ctx context.Context, w gitwrite.Writer, s gitwrite.Scope, root string) (map[string]string, error) {
	files := map[string]string{}
	dirs := []string{strings.TrimSuffix(root, "/")}
	for len(dirs) > 0 {
		dir := dirs[0]
		dirs = dirs[1:]
		entries, err := w.List(ctx, s, FleetRepo, dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !strings.HasPrefix(e.Path, root) {
				return nil, fmt.Errorf("compliance: the listing of %s holds %s", dir, e.Path)
			}
			if e.IsDir {
				dirs = append(dirs, e.Path)
				continue
			}
			files[strings.TrimPrefix(e.Path, root)] = e.BlobSHA
		}
	}
	return files, nil
}

// EnsurePolicyFiles brings the policy folders of every region of the
// Sovereign to the same files, in one commit with Via controller and the
// summary AlignSummary.
//
// The reference region is the first region by name whose policy folder
// holds ValuesFile. A region folder that appears later, whatever its name,
// therefore receives the files instead of becoming an empty reference. When
// no region holds ValuesFile, the reference is the first region by name,
// and the empty document (EmptyValuesDocument) is written to every region
// with ApplyAnnotation set to ApplyIfNotPresent: Flux creates it in a region
// whose cluster has no ValuesConfigMap and never replaces one there, such
// as the local provider pack's own document, rendered once the same way
// (clusters/_template/providers/local/kit/policy-values.yaml). A document
// that a route writes through Serialize replaces either. Each file of the
// reference that another region lacks, or holds with other content, is
// written there from the reference, with compare-and-swap on that
// region's copy: a concurrent writer makes it fail with
// *gitwrite.ConflictError rather than be overwritten. Nothing is deleted:
// a file that only a non-reference region holds stays. When every region
// already matches, nothing is committed and the result has Committed false.
// A Sovereign with no region folder returns ErrNoRegion and commits nothing.
func EnsurePolicyFiles(ctx context.Context, w gitwrite.Writer, sovereignFQDN string) (gitwrite.Result, error) {
	regions, err := Regions(ctx, w, sovereignFQDN)
	if err != nil {
		return gitwrite.Result{}, err
	}
	scope := PolicyScope(sovereignFQDN, regions)
	trees := make(map[string]map[string]string, len(regions))
	for _, r := range regions {
		t, err := policyTree(ctx, w, scope, regionPolicyDir(sovereignFQDN, r))
		if err != nil {
			return gitwrite.Result{}, err
		}
		trees[r] = t
	}
	ref := regions[0]
	for _, r := range regions {
		if _, ok := trees[r][ValuesFile]; ok {
			ref = r
			break
		}
	}

	changeID := gitwrite.NewChangeID()
	var files []gitwrite.FileChange
	if _, ok := trees[ref][ValuesFile]; !ok {
		doc, err := serialize(EmptyValuesDocument(), changeID, true)
		if err != nil {
			return gitwrite.Result{}, err
		}
		for _, r := range regions {
			files = append(files, gitwrite.FileChange{Path: regionPolicyDir(sovereignFQDN, r) + ValuesFile, Op: gitwrite.OpUpsert, Content: doc, IfMatch: gitwrite.AbsentSHA})
		}
	}
	rels := make([]string, 0, len(trees[ref]))
	for rel := range trees[ref] {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		want := trees[ref][rel]
		var content []byte
		read := false
		for _, r := range regions {
			have, ok := trees[r][rel]
			if r == ref || (ok && have == want) {
				continue
			}
			if !read {
				src := regionPolicyDir(sovereignFQDN, ref) + rel
				b, sha, err := w.Read(ctx, scope, FleetRepo, src)
				if err != nil {
					return gitwrite.Result{}, err
				}
				if sha != want {
					return gitwrite.Result{}, &gitwrite.ConflictError{Path: src, Want: want, Current: sha}
				}
				content, read = b, true
			}
			ifMatch := gitwrite.AbsentSHA
			if ok {
				ifMatch = have
			}
			files = append(files, gitwrite.FileChange{Path: regionPolicyDir(sovereignFQDN, r) + rel, Op: gitwrite.OpUpsert, Content: content, IfMatch: ifMatch})
		}
	}
	if len(files) == 0 {
		return gitwrite.Result{}, nil
	}
	return w.Commit(ctx, gitwrite.Change{
		Scope:    scope,
		Repo:     FleetRepo,
		Files:    files,
		Summary:  AlignSummary,
		ChangeID: changeID,
		Via:      gitwrite.ViaController,
	})
}
