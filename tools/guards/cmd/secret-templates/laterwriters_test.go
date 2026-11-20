package main

import (
	"strings"
	"testing"
)

// ghcrPull builds a template of the Secret ghcr-pull in the given namespace,
// with the given lines under metadata after its namespace.
func ghcrPull(ns, meta string) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: ghcr-pull\n  namespace: " + ns + "\n" + meta +
		"type: kubernetes.io/dockerconfigjson\ndata:\n  .dockerconfigjson: \"${A}\"\n"
}

const (
	renderOnceAnnotation = "  annotations:\n    kustomize.toolkit.fluxcd.io/ssa: IfNotPresent\n    reflector.v1.k8s.emberstack.com/reflection-allowed: \"true\"\n"
	pullWriters          = "platform/self-sovereign-cutover/chart/templates/06-helmrepository-patches-job.yaml, platform/harbor/chart/templates/pull-robot-job.yaml, scripts/local-pack.sh"
)

// laterWriterTree is a repository whose folder clusters/a, rendered from a
// Secret source, holds the given template, with every listed writer file but
// the one named by missing.
func laterWriterTree(t *testing.T, template, missing string) string {
	t.Helper()
	files := map[string]string{"clusters/root/a.yaml": flux("a", "./clusters/a", seedOnly), "clusters/a/secret.yaml": template}
	for _, writers := range laterWriters {
		for _, w := range writers {
			if w != missing {
				files[w] = "# a writer of the Secret after first boot\n"
			}
		}
	}
	return sourceTree(t, files)
}

func wantListed(t *testing.T, root string) {
	t.Helper()
	if out, errw := runOn(t, root); out != "clusters/a/secret.yaml\n" || strings.Contains(errw, "not a template") {
		t.Errorf("listed %q, stderr %q", out, errw)
	}
}

// A template of a listed Secret that Flux would apply again at every
// interval: no annotation, only other annotations, another apply policy, the
// policy as a label, or the Secret as the second document of its file.
func TestLaterWriterTemplateWithoutRenderOnceIsAFinding(t *testing.T) {
	reason := "has writers after first boot (" + pullWriters + "), and its template does not carry the annotation kustomize.toolkit.fluxcd.io/ssa: IfNotPresent"
	for what, c := range map[string]struct{ file, reason string }{
		"no annotation":         {ghcrPull("flux-system", ""), "the Secret flux-system/ghcr-pull (line 2) " + reason},
		"only other ones":       {ghcrPull("flux-system", "  annotations:\n    reflector.v1.k8s.emberstack.com/reflection-allowed: \"true\"\n"), "the Secret flux-system/ghcr-pull (line 2) " + reason},
		"the policy Override":   {ghcrPull("flux-system", strings.Replace(renderOnceAnnotation, "IfNotPresent", "Override", 1)), "the Secret flux-system/ghcr-pull (line 2) " + reason},
		"the policy as a label": {ghcrPull("flux-system", "  labels:\n    kustomize.toolkit.fluxcd.io/ssa: IfNotPresent\n"), "the Secret flux-system/ghcr-pull (line 2) " + reason},
		"the second document":   {secret("data:\n  b: ${A}\n") + "---\n" + ghcrPull("flux-system", ""), "the Secret flux-system/ghcr-pull (line 11) " + reason},
	} {
		t.Run(what, func(t *testing.T) { wantFinding(t, laterWriterTree(t, c.file, ""), c.reason) })
	}
}

// The template of a listed Secret that carries the annotation passes, plain or
// quoted, alone or beside a Secret that is not listed and does not carry it.
func TestLaterWriterTemplateRenderedOncePasses(t *testing.T) {
	for what, file := range map[string]string{
		"the annotation":                  ghcrPull("flux-system", renderOnceAnnotation),
		"the annotation quoted":           ghcrPull("flux-system", "  annotations:\n    \"kustomize.toolkit.fluxcd.io/ssa\": \"IfNotPresent\"\n"),
		"beside a Secret not listed":      secret("data:\n  b: ${A}\n") + "---\n" + ghcrPull("flux-system", renderOnceAnnotation),
		"a Secret not listed, without it": secret("data:\n  token: \"${A}\"\n"),
		"a Secret not listed, another policy": strings.Replace(secret("data:\n  token: \"${A}\"\n"), "  namespace: flux-system\n",
			"  namespace: flux-system\n  annotations:\n    kustomize.toolkit.fluxcd.io/ssa: Merge\n", 1),
	} {
		t.Run(what, func(t *testing.T) { wantListed(t, laterWriterTree(t, file, "")) })
	}
}

// The annotation on a Secret that is not listed: another name, or the listed
// name in another namespace. Flux would never correct its drift.
func TestRenderOnceWithoutALaterWriterIsAFinding(t *testing.T) {
	reason := "carries the annotation kustomize.toolkit.fluxcd.io/ssa: IfNotPresent, and laterwriters.go lists no writer after first boot for it"
	for what, c := range map[string]struct{ file, reason string }{
		"another name": {strings.Replace(secret("data:\n  token: \"${A}\"\n"), "  namespace: flux-system\n",
			"  namespace: flux-system\n  annotations:\n    kustomize.toolkit.fluxcd.io/ssa: IfNotPresent\n", 1), "the Secret flux-system/x (line 2) " + reason},
		"another namespace": {ghcrPull("gitea", renderOnceAnnotation), "the Secret gitea/ghcr-pull (line 2) " + reason},
	} {
		t.Run(what, func(t *testing.T) { wantFinding(t, laterWriterTree(t, c.file, ""), c.reason) })
	}
}

// A listed writer that is not a file of the tree: the template that carries
// the annotation for it is a finding that names the missing file.
func TestLaterWriterFileMissingIsAFinding(t *testing.T) {
	for _, writers := range laterWriters {
		for _, w := range writers {
			t.Run(w, func(t *testing.T) {
				wantFinding(t, laterWriterTree(t, ghcrPull("flux-system", renderOnceAnnotation), w),
					"the Secret flux-system/ghcr-pull (line 2) lists the writer "+w+", which is not a file of the tree")
			})
		}
	}
}
