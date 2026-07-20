#!/usr/bin/env bash
# §-guard (regression #5290 → #5292, 2026-07-20): the mothership `catalyst-platform`
# Flux Kustomization raw-kustomize-builds `products/catalyst/chart/templates` via that
# directory's kustomization.yaml `resources:` list. A **bare** Helm `{{ }}` directive
# (an unquoted structural `{{- if }}` / `{{- end }}` etc., not one inside a comment or
# a quoted string) in a resources[] file makes `kustomize build` fail with
# `MalformedYAMLError` → the mothership kustomization wedges at the last-good revision →
# the catalyst-api image roll stalls → fresh-prov preflight 4b (DEPLOYED != PINNED)
# never clears → NO prov can fire.
#
# Ground truth = the exact build the mothership runs. If it succeeds, the raw-consumed
# templates are kustomize-parseable; if it fails, a resources[] file broke it.
#
# Fix when it fails: keep the offending template plain YAML, or move the Helm-templated
# content to a separate plain `*-kustomize.yaml` variant (like ui-deployment-kustomize.yaml)
# and leave the Helm file OUT of the resources[] list.
set -euo pipefail
DIR="products/catalyst/chart/templates"
[ -f "$DIR/kustomization.yaml" ] || { echo "SKIP: $DIR/kustomization.yaml not found (run from repo root)"; exit 0; }
BIN=""
if command -v kustomize >/dev/null 2>&1; then BIN="kustomize build"; fi
if command -v kubectl   >/dev/null 2>&1; then BIN="kubectl kustomize"; fi
[ -n "$BIN" ] || { echo "SKIP: neither kustomize nor kubectl available"; exit 0; }
if $BIN "$DIR" >/dev/null 2>/tmp/mk-kustomize.err; then
  echo "PASS: mothership raw-kustomize build of $DIR succeeds — no resources[] template carries a bare Helm {{ }} that would wedge the mothership GitOps (#5290/#5292)."
  exit 0
fi
echo "FAIL: '$BIN $DIR' errored — a resources[] template likely contains a bare Helm {{ }} directive kustomize cannot parse (regression class #5290/#5292):"
sed 's/^/  /' /tmp/mk-kustomize.err | head -6
echo "Fix: keep the mothership-consumed template plain YAML, or move Helm-templated content to a plain *-kustomize.yaml variant that is in resources[] while the Helm file is not."
exit 1
