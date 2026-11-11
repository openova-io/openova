package controller

// region_continuum.go — ADR-0015 D8: the Application's Continuum is delivered
// from Git, in every region, and a primary move is Continuum's ordered commit
// series — never one commit to every region.
//
// Single source of the desired primary: Application.spec.placement (D3). The
// application-controller renders it into the Continuum document it adds to
// EVERY region file of a governed Application (spec.desiredPrimaryRegion).
// Region B has no Application CR and no API path to region A, but it has the
// file — so each region's Continuum reads the same desired state from Git.
// The former API path (create/patch the CR in region A, patch
// desiredPrimaryRegion on a placement edit) is gone for these Applications.
//
// Field ownership in a governed Application's region files: this controller
// writes everything EXCEPT `openova.topology.primary` and
// `database.topology.promotionToken` of the HelmReleases once a file exists —
// those are committed by Continuum, phase by phase. A file being created for
// the first time takes the primary every existing file already names (or, on
// first install, the placement primary). Writes are compare-and-swapped
// (gitea.PutFileIfMatch), so a render based on a stale read never reverts a
// Continuum commit.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	topo "github.com/openova-io/openova/core/controllers/application/internal/render"
	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// transitionGovernance describes how a governed Application's Continuum is
// rendered into its region files.
type transitionGovernance struct {
	plan      continuumPlan
	repoOwner string
	repoName  string
	branch    string
	dependsOn []dependsOnRef
	// layout is where the region files and the lease live in the repo
	// (regionfile.OrgLayout for an Org Application, PlatformLayout for a
	// platform one).
	layout regionfile.Layout
	// helmRelease ("<ns>/<name>") — the bootstrap-kit slot HelmRelease a
	// platform Application's Continuum observes; empty for Org Applications.
	helmRelease string
	// scope is the openova.io/scope label: application | platform.
	scope string
}

// renderRegionContinuum builds the Continuum document committed into every
// region file. desired / regions are read from the rendered files themselves
// (the placement primary the renderer stamped, and the regions that have a
// file), so the Continuum can never disagree with the HelmReleases beside it.
func renderRegionContinuum(appName, namespace, desired string, regions []string, g transitionGovernance) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetAPIVersion(ContinuumGroup + "/" + ContinuumVersion)
	cr.SetKind(ContinuumKind)
	cr.SetName(ContinuumNameFor(appName))
	cr.SetNamespace(namespace)
	labels := map[string]string{}
	for k, v := range g.plan.OwnerLabels {
		labels[k] = v
	}
	labels[topo.LabelApp] = appName
	scope := g.scope
	if scope == "" {
		scope = "application"
	}
	labels["openova.io/scope"] = scope
	cr.SetLabels(labels)

	rs := make([]interface{}, 0, len(regions))
	for _, r := range regions {
		rs = append(rs, r)
	}
	deps := make([]interface{}, 0, len(g.dependsOn))
	for _, d := range g.dependsOn {
		name := ContinuumNameFor(d.Name)
		if d.Namespace != "" && d.Namespace != namespace {
			name = d.Namespace + "/" + name
		}
		deps = append(deps, name)
	}
	repo := map[string]interface{}{
		"owner":  g.repoOwner,
		"name":   g.repoName,
		"branch": g.branch,
	}
	if g.layout.Root != "" {
		repo["path"] = g.layout.Root
	}
	if g.layout.Folder != "" {
		repo["folder"] = g.layout.Folder
	}
	tr := map[string]interface{}{
		"repo":     repo,
		"app":      appName,
		"stateful": g.plan.Mechanism == "cnpg-pair",
	}
	if ns, name, ok := strings.Cut(g.helmRelease, "/"); ok && name != "" {
		tr["helmRelease"] = map[string]interface{}{"name": name, "namespace": ns}
	}
	if len(deps) > 0 {
		tr["dependsOn"] = deps
	}
	spec := map[string]interface{}{
		"applicationRef":       namespace + "/" + appName,
		"desiredPrimaryRegion": desired,
		"regions":              rs,
		"transition":           tr,
	}
	if g.plan.RTOSeconds > 0 {
		spec["rto"] = fmt.Sprintf("%ds", g.plan.RTOSeconds)
	}
	if g.plan.RPOSeconds > 0 {
		spec["rpo"] = fmt.Sprintf("%ds", g.plan.RPOSeconds)
	}
	cr.Object["spec"] = spec
	return cr
}

// withRegionContinuum appends the Continuum document to every region file.
// The desired primary is the topology primary the renderer stamped into the
// files (all files agree: they were rendered from one placement).
func withRegionContinuum(files map[string][]byte, regionOrder []string, appName, namespace string, g transitionGovernance) (map[string][]byte, error) {
	regions := make([]string, 0, len(files))
	for _, r := range regionOrder {
		if _, ok := files[r]; ok {
			regions = append(regions, r)
		}
	}
	extra := []string{}
	for r := range files {
		if !containsString(regions, r) {
			extra = append(extra, r)
		}
	}
	sort.Strings(extra)
	regions = append(regions, extra...)

	desired := ""
	parsed := map[string][]*unstructured.Unstructured{}
	for _, r := range regions {
		objs, err := regionfile.Parse(files[r])
		if err != nil {
			return nil, err
		}
		parsed[r] = objs
		f, ok, err := regionfile.ReadTransitionFields(objs)
		if err != nil {
			return nil, err
		}
		if ok && desired == "" {
			desired = f.Primary
		}
	}
	if desired == "" {
		// No HelmRelease carries the topology contract: nothing for a
		// transition to move.
		return files, nil
	}
	out := make(map[string][]byte, len(files))
	for _, r := range regions {
		// The Continuum rendered here IS the promotion actor; declare it on
		// the HelmReleases so the chart's #5623 fail-closed check is
		// satisfied by the truth, not by a default (ADR-0015 D8).
		if _, err := regionfile.DeclarePromotionMechanism(parsed[r], "continuum"); err != nil {
			return nil, err
		}
		cr := renderRegionContinuum(appName, namespace, desired, regions, g)
		l := cr.GetLabels()
		l[topo.LabelRegion] = r
		cr.SetLabels(l)
		b, err := regionfile.Marshal(append(parsed[r], cr))
		if err != nil {
			return nil, err
		}
		out[r] = b
	}
	return out, nil
}

// errRegionFileMoved — a compare-and-swap lost against a concurrent commit
// (Continuum's). The reconcile requeues and re-reads.
var errRegionFileMoved = errors.New("region file changed during the write (a topology transition committed); retrying")

// commitGovernedRegionFiles writes a governed Application's files keeping the
// transition fields Continuum owns. Same region set semantics as
// commitRegionFiles: a region with no file in `files` has its file removed.
// layout locates the files (OrgLayout, or PlatformLayout for a platform
// Application in the Sovereign region repo).
func (r *Reconciler) commitGovernedRegionFiles(
	ctx context.Context,
	app *unstructured.Unstructured,
	org, repo, branch string,
	layout regionfile.Layout,
	files map[string][]byte,
) error {
	for region := range files {
		if !r.Cfg.Regions.Contains(region) {
			return fmt.Errorf("region %q is not in the Sovereign region registry %v", region, r.Cfg.Regions.IDs())
		}
	}
	type existing struct {
		file   gitea.File
		data   []byte
		fields regionfile.TransitionFields
		has    bool
	}
	cur := map[string]existing{}
	for _, region := range r.Cfg.Regions.IDs() {
		path := layout.AppPath(region, app.GetName())
		f, err := r.Gitea.GetFile(ctx, org, repo, branch, path)
		if err != nil {
			if errors.Is(err, gitea.ErrFileNotFound) || (r.GiteaErrors != nil && r.GiteaErrors.IsNotFound(err)) {
				continue
			}
			return fmt.Errorf("GetFile %s: %w", path, err)
		}
		data, err := f.Decoded()
		if err != nil {
			return err
		}
		e := existing{file: f, data: data}
		if objs, perr := regionfile.Parse(data); perr == nil {
			e.fields, e.has, _ = regionfile.ReadTransitionFields(objs)
		}
		cur[region] = e
	}
	// The primary the existing files converge on — what a newly created
	// file must start from, so adding a region never moves the writer.
	committed := ""
	for _, e := range cur {
		if e.has && e.fields.Primary != "" {
			committed = e.fields.Primary
			break
		}
	}

	msg := fmt.Sprintf("application-controller: placement of %s/%s (gen %d)",
		app.GetNamespace(), app.GetName(), app.GetGeneration())
	for _, region := range r.Cfg.Regions.IDs() {
		path := layout.AppPath(region, app.GetName())
		data, want := files[region]
		e, have := cur[region]
		if !want {
			if have {
				if err := r.deleteRepoFile(ctx, org, repo, branch, path, msg); err != nil {
					return err
				}
			}
			continue
		}
		objs, err := regionfile.Parse(data)
		if err != nil {
			return err
		}
		switch {
		case have && e.has:
			if _, err := regionfile.SetTransitionFields(objs, e.fields); err != nil {
				return err
			}
		case committed != "":
			if _, err := regionfile.SetTransitionFields(objs, regionfile.TransitionFields{Primary: committed}); err != nil {
				return err
			}
		}
		out, err := regionfile.Marshal(objs)
		if err != nil {
			return err
		}
		if have && string(out) == string(e.data) {
			continue
		}
		sha := ""
		if have {
			sha = e.file.SHA
		}
		if _, err := r.Gitea.PutFileIfMatch(ctx, org, repo, branch, path, out, msg, sha); err != nil {
			if errors.Is(err, gitea.ErrConflict) {
				return errRegionFileMoved
			}
			return fmt.Errorf("PutFileIfMatch %s: %w", path, err)
		}
	}
	return nil
}
