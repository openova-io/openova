// Package seedrepo is the subcommand seed-repo of sovereign-ops.
//
// seed-repo commits the root of one region into the Sovereign's own Git
// repository, once. It reads from the region's cluster the ConfigMap
// flux-system/sovereign-variables and each Kustomization of
// RootKustomizations, and writes them with a kustomization.yaml that lists
// them into clusters/<fqdn>/regions/<region>/platform/root/, in one commit
// of the commit library gitwrite (contract C4, scope RegionPlatform, module
// root). <fqdn> is the value SOVEREIGN_FQDN of the ConfigMap. As contract C4
// asks, each file holds one object, is named <metadata.name>.yaml and carries
// the label app.kubernetes.io/managed-by and the annotation
// catalyst.openova.io/change-id.
//
// Git wins once the root is there: when platform/root/kustomization.yaml
// exists, seed-repo commits nothing, so a commit made after the seed is
// never overwritten. When the folder holds files but no kustomization.yaml,
// it fails and names them. It also fails, and commits nothing, when a root
// Kustomization carries an inline postBuild.substitute map or a key of the
// ConfigMap names a secret value: no value reaches Git except through the
// ConfigMap, and no secret at all.
package seedrepo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"sigs.k8s.io/yaml"
)

// RootKustomizations are the Kustomizations in flux-system that the first
// boot creates. One that is absent (infrastructure-providers exists in the
// primary region only) is skipped.
var RootKustomizations = []string{"bootstrap-kit", "bootstrap-kit-crs", "sovereign-tls", "infrastructure-providers"}

// secretSuffixes and secretNameExceptions are rule 5 of contract C5: a key
// whose name ends in one of the suffixes names a secret value, except the
// listed names. They copy secretSuffixes and SecretNameExceptions of
// core/controllers/pkg/upstream, which this module does not import (it would
// bring the controller runtime into the image); TestRule5MatchesUpstream
// fails when the two differ.
var secretSuffixes = []string{"_KEY", "_TOKEN", "_PASSWORD", "_SECRET_VALUE"}
var secretNameExceptions = []string{"RELEASE_PUBLIC_KEY_B64", "SOVEREIGN_REGION_KEY"}

// dropMetadata are the metadata fields and annotations that the API server
// or a client writes. They never go into Git.
var dropMetadata = []string{"managedFields", "resourceVersion", "uid", "creationTimestamp", "generation",
	"selfLink", "finalizers", "ownerReferences", "deletionTimestamp", "deletionGracePeriodSeconds"}
var dropAnnotations = []string{"kubectl.kubernetes.io/last-applied-configuration", "reconcile.fluxcd.io/requestedAt"}

// Cluster reads one object of the region's cluster. It returns nil and no
// error when the object does not exist, and an error that wraps
// ops.ErrTransient when the cluster did not answer.
type Cluster interface {
	Get(ctx context.Context, apiVersion, kind, namespace, name string) (map[string]interface{}, error)
}

// Seed runs seed-repo for one region. newWriter returns the commit library
// client for the Sovereign with the given FQDN. The evidence names the
// commit and each file written, or the file that shows the root is seeded.
func Seed(ctx context.Context, c Cluster, newWriter func(fqdn string) (gitwrite.Writer, error), region string, repo gitwrite.Repo) ([]string, error) {
	vars, err := c.Get(ctx, "v1", "ConfigMap", "flux-system", "sovereign-variables")
	if err != nil {
		return nil, fmt.Errorf("read ConfigMap flux-system/sovereign-variables: %w", err)
	}
	data, _ := vars["data"].(map[string]interface{})
	fqdn, _ := data["SOVEREIGN_FQDN"].(string)
	if vars == nil || fqdn == "" {
		return nil, errors.New("ConfigMap flux-system/sovereign-variables does not exist or has no value SOVEREIGN_FQDN")
	}
	scope := gitwrite.RegionPlatform(fqdn, []string{"root"}, []string{region})
	if len(scope.Prefixes) != 1 {
		return nil, fmt.Errorf("%q and %q do not name one region folder", fqdn, region)
	}
	dir := scope.Prefixes[0]
	w, err := newWriter(fqdn)
	if err != nil {
		return nil, err
	}
	if _, sha, err := w.Read(ctx, scope, repo, dir+"kustomization.yaml"); err == nil {
		return []string{"seeded already: " + dir + "kustomization.yaml is blob " + sha}, nil
	} else if !errors.Is(err, gitwrite.ErrNotFound) {
		return nil, gitError("read "+dir+"kustomization.yaml", err)
	}
	entries, err := w.List(ctx, scope, repo, dir)
	if err != nil {
		return nil, gitError("list "+dir, err)
	}
	if len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Path)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("%s holds %s but no kustomization.yaml; seed-repo writes only into an empty folder", dir, strings.Join(names, ", "))
	}
	changeID := gitwrite.NewChangeID()
	ch := gitwrite.Change{Scope: scope, Repo: repo, Summary: "seed-repo: root of region " + region, ChangeID: changeID, Via: gitwrite.ViaController}
	var resources []interface{}
	add := func(name string, obj map[string]interface{}) error {
		b, err := yaml.Marshal(obj)
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		ch.Files = append(ch.Files, gitwrite.FileChange{Path: dir + name, Op: gitwrite.OpUpsert, Content: b, IfMatch: gitwrite.AbsentSHA})
		resources = append(resources, name)
		return nil
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if namesSecret(k) {
			return nil, fmt.Errorf("the key %s of ConfigMap flux-system/sovereign-variables names a secret value; nothing is committed", k)
		}
	}
	cm := clean(vars, changeID)
	delete(cm, "binaryData")
	cm["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})["kustomize.toolkit.fluxcd.io/substitute"] = "disabled"
	if err := add("sovereign-variables.yaml", cm); err != nil {
		return nil, err
	}
	for _, name := range RootKustomizations {
		obj, err := c.Get(ctx, "kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", name)
		if err != nil {
			return nil, fmt.Errorf("read Kustomization flux-system/%s: %w", name, err)
		}
		if obj == nil {
			continue
		}
		spec, _ := obj["spec"].(map[string]interface{})
		postBuild, _ := spec["postBuild"].(map[string]interface{})
		if _, inline := postBuild["substitute"]; inline {
			return nil, fmt.Errorf("the Kustomization flux-system/%s carries an inline postBuild.substitute map; nothing is committed", name)
		}
		if err := add(name+".yaml", clean(obj, changeID)); err != nil {
			return nil, err
		}
	}
	if err := add("kustomization.yaml", map[string]interface{}{"apiVersion": "kustomize.config.k8s.io/v1beta1",
		"kind": "Kustomization", "resources": resources}); err != nil {
		return nil, err
	}
	res, err := w.Commit(ctx, ch)
	if err != nil {
		return nil, gitError("commit "+dir, err)
	}
	evidence := []string{"commit " + res.Commit}
	for _, f := range ch.Files {
		evidence = append(evidence, "wrote "+f.Path)
	}
	return evidence, nil
}

// namesSecret reports whether rule 5 of contract C5 refuses the key name.
func namesSecret(key string) bool {
	for _, e := range secretNameExceptions {
		if key == e {
			return false
		}
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(key, s) {
			return true
		}
	}
	return false
}

// clean returns a copy of obj without status and the fields of
// dropMetadata and dropAnnotations, with the label
// app.kubernetes.io/managed-by flux and the annotation
// catalyst.openova.io/change-id that every committed object carries.
func clean(obj map[string]interface{}, changeID string) map[string]interface{} {
	out, meta, labels, annotations := map[string]interface{}{}, map[string]interface{}{}, map[string]interface{}{}, map[string]interface{}{}
	for k, v := range obj {
		out[k] = v
	}
	delete(out, "status")
	m, _ := obj["metadata"].(map[string]interface{})
	for k, v := range m {
		meta[k] = v
	}
	for _, k := range dropMetadata {
		delete(meta, k)
	}
	l, _ := m["labels"].(map[string]interface{})
	for k, v := range l {
		labels[k] = v
	}
	a, _ := m["annotations"].(map[string]interface{})
	for k, v := range a {
		annotations[k] = v
	}
	for _, k := range dropAnnotations {
		delete(annotations, k)
	}
	labels["app.kubernetes.io/managed-by"] = "flux"
	annotations["catalyst.openova.io/change-id"] = changeID
	meta["labels"], meta["annotations"] = labels, annotations
	out["metadata"] = meta
	return out
}

// gitError describes a failed call to the commit library. A Git server that
// did not answer, and a file another writer created first, may be retried.
func gitError(what string, err error) error {
	err = fmt.Errorf("%s: %w", what, err)
	if errors.Is(err, gitwrite.ErrUnavailable) || gitwrite.IsConflict(err) {
		return ops.Transient(err)
	}
	return err
}
