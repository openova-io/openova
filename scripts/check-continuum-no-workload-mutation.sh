#!/usr/bin/env bash
# check-continuum-no-workload-mutation.sh — ADR-0015 D8 guard.
#
# Continuum moves a Primary ONLY by committing to Git; the native operators
# (Flux, CNPG, the chart's own Services/routes, PowerDNS records) carry the
# change out. The pre-D8 sequencer instead patched CNPG Clusters, HTTPRoutes
# and PowerDNS directly, exec'd into pods and deleted them. This guard keeps
# that from coming back, at both layers:
#
#   1. Go source (core/controllers/continuum, non-test): no Kubernetes write
#      verb except a status-subresource Patch on Continuum / Application; no
#      pod exec; no non-GET HTTP request (Git writes go through
#      core/controllers/pkg/gitea, outside this tree).
#   2. Chart RBAC (products/continuum/chart/templates/rbac.yaml): every rule
#      granting a verb beyond get/list/watch must be on an allowed resource
#      (continuums/status, applications/status, leases for in-region leader
#      election, events).
#
# Usage:
#   scripts/check-continuum-no-workload-mutation.sh              # scan the tree
#   scripts/check-continuum-no-workload-mutation.sh --self-test  # prove both directions
set -euo pipefail

scan() {
  local src="$1" rbac="$2"
  python3 - "$src" "$rbac" <<'PY'
import os, re, sys
src, rbac = sys.argv[1], sys.argv[2]
bad = []
WRITE = re.compile(r'\.(Create|Update|UpdateStatus|Delete|DeleteCollection|Apply|ApplyStatus)\(\s*ctx')
PATCH = re.compile(r'\.Patch\(')
STATUS_PATCH = re.compile(r'Resource\((ContinuumGVR|observe\.ApplicationGVR|ApplicationGVR)\).*\.Patch\(.*"status"\)')
FORBIDDEN = [
    (re.compile(r'"k8s\.io/client-go/tools/remotecommand"'), 'pod exec (remotecommand)'),
    (re.compile(r'pods/exec|SubResource\("exec"\)'), 'pod exec'),
    (re.compile(r'http\.Method(Post|Put|Patch|Delete)\b'), 'non-GET HTTP request (Git writes belong in pkg/gitea)'),
    (re.compile(r'"(POST|PUT|PATCH|DELETE)"'), 'non-GET HTTP request literal'),
]
for root, _, files in os.walk(src):
    for f in files:
        if not f.endswith('.go') or f.endswith('_test.go'):
            continue
        p = os.path.join(root, f)
        for n, line in enumerate(open(p, encoding='utf-8'), 1):
            s = line.strip()
            if s.startswith('//'):
                continue
            if WRITE.search(s):
                bad.append(f'{p}:{n}: Kubernetes write verb: {s}')
            if PATCH.search(s) and not STATUS_PATCH.search(s):
                bad.append(f'{p}:{n}: Patch outside the Continuum/Application status subresource: {s}')
            for rx, why in FORBIDDEN:
                if rx.search(s):
                    bad.append(f'{p}:{n}: {why}: {s}')
READ = {'get', 'list', 'watch'}
ALLOWED = {'continuums/status', 'applications/status', 'leases', 'events'}
text = open(rbac, encoding='utf-8').read()
for m in re.finditer(r'resources:\s*\[([^\]]*)\]\s*\n\s*verbs:\s*\[([^\]]*)\]', text):
    res = {x.strip().strip('"\'') for x in m.group(1).split(',') if x.strip()}
    verbs = {x.strip().strip('"\'') for x in m.group(2).split(',') if x.strip()}
    if verbs - READ and not res <= ALLOWED:
        bad.append(f'{rbac}: write verbs {sorted(verbs - READ)} on {sorted(res - ALLOWED)} — Continuum may write only {sorted(ALLOWED)}')
if bad:
    print('check-continuum-no-workload-mutation: FAIL — Continuum must act through Git only (ADR-0015 D8):')
    for b in bad:
        print('  ' + b)
    sys.exit(1)
print(f'check-continuum-no-workload-mutation: OK ({src}, {rbac})')
PY
}

if [[ "${1:-}" == "--self-test" ]]; then
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT
  mkdir -p "$tmp/clean" "$tmp/dirty"
  cat >"$tmp/clean/ok.go" <<'GO'
package x
func f() {
	_, _ = c.Dyn.Resource(ContinuumGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{}, "status")
	_, _ = c.Dyn.Resource(observe.HelmReleaseGVR).Namespace("").List(ctx, metav1.ListOptions{})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
}
GO
  cat >"$tmp/clean/rbac.yaml" <<'YAML'
rules:
  - apiGroups: ["postgresql.cnpg.io"]
    resources: ["clusters"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["dr.openova.io"]
    resources: ["continuums/status"]
    verbs: ["get", "patch"]
YAML
  cat >"$tmp/dirty/bad.go" <<'GO'
package x
func f() {
	_, _ = c.Dyn.Resource(CNPGClusterGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{})
	_ = c.Dyn.Resource(PodGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, pdnsURL, nil)
}
GO
  cat >"$tmp/dirty/rbac.yaml" <<'YAML'
rules:
  - apiGroups: ["postgresql.cnpg.io"]
    resources: ["clusters"]
    verbs: ["get", "list", "watch", "patch"]
YAML
  if ! scan "$tmp/clean" "$tmp/clean/rbac.yaml" >/dev/null; then
    echo "self-test FAIL: clean fixture rejected"; exit 1
  fi
  out="$(scan "$tmp/dirty" "$tmp/dirty/rbac.yaml" || true)"
  for want in "Patch outside" "Kubernetes write verb" "non-GET HTTP" "write verbs ['patch'] on ['clusters']"; do
    if ! grep -qF "$want" <<<"$out"; then
      echo "self-test FAIL: dirty fixture not caught: $want"; echo "$out"; exit 1
    fi
  done
  echo "check-continuum-no-workload-mutation: self-test OK (clean passes; CNPG patch, pod delete, PowerDNS POST and RBAC write all caught; no cluster contacted)"
  exit 0
fi

ROOT="${ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
scan "$ROOT/core/controllers/continuum" "$ROOT/products/continuum/chart/templates/rbac.yaml"
