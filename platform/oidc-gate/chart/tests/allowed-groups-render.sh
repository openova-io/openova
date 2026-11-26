#!/usr/bin/env bash
# bp-oidc-gate — allowedGroups render audit (ACC-010).
#
# Per instance, `allowedGroups` lists the Keycloak groups the gate admits: one
# `--allowed-group=<group>` argument per entry, directly after
# `--email-domain=*`. Without it (or with an empty list) the instance renders
# no such argument and admits every user of the realm, as before. The three
# instances render together, so a value that leaks between them fails too.
set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/values.yaml" <<'YAML'
sovereignFQDN: smoke.example.com
instances:
  - name: grp
    upstream: http://grp.default.svc.cluster.local:80
    allowedGroups: [a, b]
  - name: open
    upstream: http://open.default.svc.cluster.local:80
  - name: emptylist
    upstream: http://emptylist.default.svc.cluster.local:80
    allowedGroups: []
YAML
"${HELM_BIN:-helm}" template oidc-gate "$chart_dir" -f "$tmp/values.yaml" >"$tmp/render.yaml"

# args <deployment>: the args of its one container (oauth2-proxy), one per line.
args() {
  awk -v want="$1" '
    function flush() { if (dep && hit) printf "%s", buf; buf = ""; dep = 0; hit = 0 }
    /^---/ { flush(); next }
    { buf = buf $0 "\n" }
    /^kind: Deployment$/ { dep = 1 }
    /^  name: / { n = $2; gsub(/"/, "", n); if (n == want) hit = 1 }
    END { flush() }
  ' "$tmp/render.yaml" | sed -n 's/^ *- \(--.*\)$/\1/p'
}

fails=0
check() { if [ "$2" = ok ]; then echo "  PASS $1"; else echo "  FAIL $1"; fails=$((fails + 1)); fi; }

names="$(awk '/^---/ { d = 0 } /^kind: Deployment$/ { d = 1 } d && /^  name: / { n = $2; gsub(/"/, "", n); print n }' "$tmp/render.yaml" | sort | tr '\n' ' ')"
[ "$names" = "oidc-gate-emptylist oidc-gate-grp oidc-gate-open " ] && r=ok || r=no
check "the three gate Deployments render (got: ${names})" "$r"

grp="$(args oidc-gate-grp)"
after="$(grep -x -F -A 2 -- '--email-domain=*' <<<"$grp" | tail -n +2 | tr '\n' ' ')"
count="$(grep -c -- '^--allowed-group' <<<"$grp" || true)"
[ "$after" = "--allowed-group=a --allowed-group=b " ] && [ "$count" = 2 ] && r=ok || r=no
check "grp: exactly --allowed-group=a and --allowed-group=b, right after --email-domain=*" "$r"

for name in open emptylist; do
  a="$(args "oidc-gate-$name")"
  n="$(grep -c -- '^--allowed-group' <<<"$a" || true)"
  grep -q -x -F -- '--email-domain=*' <<<"$a" && [ "$n" = 0 ] && r=ok || r=no
  check "$name: no --allowed-group argument" "$r"
done

if [ "$fails" -ne 0 ]; then
  echo "[bp-oidc-gate ACC-010 allowedGroups] FAIL"
  exit 1
fi
echo "[bp-oidc-gate ACC-010 allowedGroups] All cases PASS"
