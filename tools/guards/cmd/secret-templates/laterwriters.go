package main

// The Secrets with a writer after first boot (SEC-154, revision 4).
//
// The Flux Kustomization that renders a template applies it again at every
// interval and, under the default apply policy (Override), writes the
// template's value back over the change of any other writer. A Secret that a
// component writes after first boot is therefore rendered once: its template
// carries, under metadata.annotations, kustomize.toolkit.fluxcd.io/ssa:
// IfNotPresent, so that Flux creates it from the seed when it is absent and
// never applies it again. (kustomize-controller v1.4.0, the one Flux v2.4.0
// installs and the kit pins: docs/spec/v1/kustomizations.md, "Controlling
// the apply behavior of resources"; the skipped object stays in the
// Kustomization's inventory, so prune still deletes it once its template
// leaves the folder.)
//
// laterWriters lists those Secrets, each with the files of its writers. Each
// of these is a finding, and a file with a finding is not a template (G1
// counts its line kind: Secret):
//
//	a template of a listed Secret (namespace and name) without that
//	annotation (the finding names the Secret and its writers);
//	a template that carries that annotation for a Secret not listed: Flux
//	would never correct its drift, and nothing names why;
//	a template of a listed Secret whose listed writer is not a file of the
//	tree: the reason for the annotation is not known to hold.
//
// A writer of a key that the template does not render is not listed: Flux's
// server-side apply sets only the fields of its manifest.

import (
	"fmt"
	"io/fs"
	"strings"
)

const (
	applyPolicy  = "kustomize.toolkit.fluxcd.io/ssa"
	renderedOnce = "IfNotPresent"
)

// laterWriters maps namespace/name of a Secret written after first boot to
// the files of its writers, from the repository root.
var laterWriters = map[string][]string{
	"flux-system/ghcr-pull": {
		// The cutover's step 06: phase 0 merges the Sovereign's own registry
		// login, the part for a secondary region applies region A's bytes,
		// phase 3b removes the parent's logins.
		"platform/self-sovereign-cutover/chart/templates/06-helmrepository-patches-job.yaml",
		// bp-harbor's pull-robot Job adds the local registry's robot login.
		"platform/harbor/chart/templates/pull-robot-job.yaml",
		// The local pack (pin_flux) puts the CI runner's pull login.
		"scripts/local-pack.sh",
	},
}

// laterWriterFinding judges every Secret of a template file against
// laterWriters, the writer files in tree: "" when none is a finding,
// otherwise the first reason.
func laterWriterFinding(tree fs.FS, text string) string {
	lines := strings.Split(text, "\n")
	start := 0
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) && !separator.MatchString(lines[i]) {
			continue
		}
		doc, first := lines[start:i], start
		start = i + 1
		kind, at := kindOf(doc)
		if kind != "Secret" {
			continue
		}
		line := first + at + 1
		n, err := parseYAML(doc)
		if err != nil {
			return fmt.Sprintf("the Secret of line %d cannot be read to learn whether a component writes it after first boot: %v", line, err)
		}
		meta := n.get("metadata")
		id := meta.get("namespace").scalar + "/" + meta.get("name").scalar
		once := meta.get("annotations").get(applyPolicy).scalar == renderedOnce
		writers, listed := laterWriters[id]
		switch {
		case listed && !once:
			return fmt.Sprintf("the Secret %s (line %d) has writers after first boot (%s), and its template does not carry the annotation %s: %s, so Flux would write the seed's value back over theirs",
				id, line, strings.Join(writers, ", "), applyPolicy, renderedOnce)
		case !listed && once:
			return fmt.Sprintf("the Secret %s (line %d) carries the annotation %s: %s, and laterwriters.go lists no writer after first boot for it, so Flux would never correct its drift and nothing names why",
				id, line, applyPolicy, renderedOnce)
		}
		for _, w := range writers {
			if _, err := fs.Stat(tree, w); err != nil {
				return fmt.Sprintf("the Secret %s (line %d) lists the writer %s, which is not a file of the tree (%v), so the reason for its annotation %s: %s is not known to hold",
					id, line, w, err, applyPolicy, renderedOnce)
			}
		}
	}
	return ""
}
