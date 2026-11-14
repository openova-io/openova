#!/usr/bin/env bash
# bp-openbao — HTTPRoute render audit (Wave 5.85 #2425).
#
# Verifies the chart renders a Cilium Gateway HTTPRoute when the
# bootstrap-kit overlay flips `gateway.enabled: true` + sets
# `gateway.host`. The default-values CI smoke does NOT exercise this
# path because the chart defaults `gateway.enabled` to false (correct
# for standalone-install on non-Catalyst clusters). Without this guard
# a regression in `templates/httproute.yaml` (gate condition, hostname
# templating, parentRef typo) ships fresh-prov NXDOMAIN to every
# Sovereign + only surfaces on operator walk — exactly the #2421
# symptom that drove Wave 5.84 for bp-newapi.
#
# Cases:
#   1. Overlay-enabled render — HTTPRoute kind present with correct
#      hostname
#   2. parentRefs cite canonical Cilium Gateway
#      (`cilium-gateway` in `kube-system`)
#   3. Default-off path — no HTTPRoute kind in render output
#      (silent-skip behaviour preserved for standalone-install
#      contexts on non-Catalyst clusters)
#   4. Operator host override propagates correctly

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# ── Case 1: HTTPRoute renders with overlay-enabled values ─────────────
echo "[bp-openbao] Case 1: overlay-enabled render — HTTPRoute present + correct hostname"
out=$("$helm" template smoke "$chart_dir" \
        --set gateway.enabled=true \
        --set gateway.host=openbao.smoke.omani.works 2>&1)

route_block=$(echo "$out" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if [ -z "$route_block" ]; then
  echo "FAIL: HTTPRoute did not render with gateway.enabled=true + gateway.host"
  echo "(would surface on fresh Sovereign prov as NXDOMAIN for openbao.<fqdn>)"
  exit 1
fi
if ! grep -q "openbao.smoke.omani.works" <<<"$route_block"; then
  echo "FAIL: HTTPRoute hostname does not match gateway.host"
  echo "$route_block"
  exit 1
fi
echo "[bp-openbao] Case 1: PASS"

# ── Case 2: parentRefs cite canonical Cilium Gateway ──────────────────
echo "[bp-openbao] Case 2: parentRefs cite 'cilium-gateway' in 'kube-system'"
if ! grep -q "name: \"cilium-gateway\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite name=cilium-gateway"
  exit 1
fi
if ! grep -q "namespace: \"kube-system\"" <<<"$route_block"; then
  echo "FAIL: HTTPRoute parentRefs do not cite namespace=kube-system"
  exit 1
fi
echo "[bp-openbao] Case 2: PASS"

# ── Case 3: default-off path — HTTPRoute absent ───────────────────────
echo "[bp-openbao] Case 3: default-off path — HTTPRoute not rendered"
out_default=$("$helm" template smoke "$chart_dir" 2>&1)
if grep -q "^kind: HTTPRoute$" <<<"$out_default"; then
  echo "FAIL: HTTPRoute should NOT render when gateway.enabled is unset (default)"
  exit 1
fi
echo "[bp-openbao] Case 3: PASS"

# ── Case 4: operator host override propagates ─────────────────────────
echo "[bp-openbao] Case 4: operator override of gateway.host"
out_override=$("$helm" template smoke "$chart_dir" \
        --set gateway.enabled=true \
        --set gateway.host=custom.smoke.example 2>&1)

route_block_override=$(echo "$out_override" | awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f')
if ! grep -q "custom.smoke.example" <<<"$route_block_override"; then
  echo "FAIL: explicit gateway.host override not propagated to HTTPRoute hostnames"
  exit 1
fi
if grep -q "openbao.smoke.omani.works" <<<"$route_block_override"; then
  echo "FAIL: explicit override leaked the case-1 hostname"
  exit 1
fi
echo "[bp-openbao] Case 4: PASS"

# ── Case 5: bootstrap Application CR placement is canonical ────────────
# #3375 / #3768 / #3786 — ONE canonical vocabulary on the wire. When the
# bootstrap-kit slot opts the chart into self-registering its Application
# CR (bootstrapOwned.enabled=true), spec.placement MUST emit the canonical
# token `singleton` — the read path (endpoint_handler.go readTopology)
# serves it VERBATIM to the Topology tab + the Instances-table chip, so the
# banned legacy `single-region` spelling would render raw to the operator
# (exactly the hw159 #3375 finding).
echo "[bp-openbao] Case 5: bootstrap Application CR placement is canonical 'singleton'"
appcr=$("$helm" template smoke "$chart_dir" \
        --set bootstrapOwned.enabled=true \
        --set bootstrapOwned.helmRelease.name=bp-openbao \
        --api-versions apps.openova.io/v1 2>&1)
if ! grep -qE '^  placement: singleton$' <<<"$appcr"; then
  echo "FAIL: Application CR placement must be canonical 'singleton' (not banned 'single-region')"
  echo "$appcr" | grep -E '^  placement:' || true
  exit 1
fi
if grep -qE '^  placement: single-region$' <<<"$appcr"; then
  echo "FAIL: #3375 REGRESSION — Application CR re-introduced the banned 'single-region' placement"
  exit 1
fi
echo "[bp-openbao] Case 5: PASS"

# ── Case 6: OpenBao system paths are not served on the public route ────
# SEC-010. The route answers each of these 15 path prefixes with a 302 to
# /ui/ on the same host, and sends none of them to a backend. This holds
# with the bare-URL sign-in on and off. The catch-all rule of gateway.path
# stays the last rule, so the rest of /v1/ still reaches OpenBao. The
# assertions use bash substring matching only (no pipe into grep -q), for
# the broken-pipe reason given in sso-landing-render.sh.
echo "[bp-openbao] Case 6: the 15 OpenBao system path prefixes redirect and reach no backend"
denied_prefixes=(
  /v1/sys/generate-root /v1/sys/generate-recovery-token /v1/sys/unseal
  /v1/sys/seal /v1/sys/init /v1/sys/rekey /v1/sys/rekey-recovery-key
  /v1/sys/step-down /v1/sys/raw /v1/sys/storage /v1/sys/rotate
  /v1/sys/pprof /v1/sys/audit /v1/sys/config /v1/sys/replication
)
# route_rules prints one line per rule of the rendered HTTPRoute: the lines
# of the rule joined by "|", with a "|" at the end.
route_rules() {
  awk '/^---$/{f=0} /^kind: HTTPRoute$/{f=1} f' <<<"$1" |
    awk '/^    - matches:$/{if (r != "") print r "|"; r = $0; next} r != "" {r = r "|" $0} END{if (r != "") print r "|"}'
}
check_denied() {   # $1: label of the render; $2: the render
  local label="$1" rules rule p n_rules=0 n_redirect=0 n_value=0 last=""
  rules="$(route_rules "$2")"
  while IFS= read -r rule; do
    [ -n "$rule" ] || continue
    n_rules=$((n_rules + 1))
    last="$rule"
    case "$rule" in *'value: "/v1/sys/'*) ;; *) continue ;; esac
    n_redirect=$((n_redirect + 1))
    case "$rule" in *backendRefs:*)
      echo "FAIL ($label): a rule of the system paths has backendRefs: $rule"; exit 1 ;;
    esac
    for want in '|        - type: RequestRedirect|' '|            hostname: "openbao.smoke.omani.works"|' \
                '|            port: 443|' '|              type: ReplaceFullPath|' \
                '|              replaceFullPath: "/ui/"|' '|            statusCode: 302|'; do
      case "$rule" in *"$want"*) ;; *)
        echo "FAIL ($label): a rule of the system paths lacks '${want//|/}': $rule"; exit 1 ;;
      esac
    done
  done <<<"$rules"
  if [ "$n_redirect" -ne 2 ]; then
    echo "FAIL ($label): $n_redirect rules carry the system paths, want 2"; exit 1
  fi
  for p in "${denied_prefixes[@]}"; do
    case "$rules" in *"|            type: PathPrefix|            value: \"$p\"|"*) ;; *)
      echo "FAIL ($label): no PathPrefix match for $p"; exit 1 ;;
    esac
  done
  n_value=$(awk -v s='value: "/v1/sys/' '{n += gsub(s, "")} END{print n + 0}' <<<"$rules")
  if [ "$n_value" -ne "${#denied_prefixes[@]}" ]; then
    echo "FAIL ($label): $n_value system path matches, want ${#denied_prefixes[@]}"; exit 1
  fi
  case "$last" in *'|            value: "/"|      backendRefs:|'*) ;; *)
    echo "FAIL ($label): the last rule is not the catch-all of gateway.path: $last"; exit 1 ;;
  esac
  if [ "$n_rules" -gt 16 ]; then
    echo "FAIL ($label): the route has $n_rules rules; Gateway API allows 16"; exit 1
  fi
}
check_denied "bare-URL sign-in on" "$out"
check_denied "bare-URL sign-in off" "$("$helm" template smoke "$chart_dir" \
        --set gateway.enabled=true \
        --set gateway.host=openbao.smoke.omani.works \
        --set sso.bareURL.enabled=false 2>&1)"
echo "[bp-openbao] Case 6: PASS"

echo "[bp-openbao] All HTTPRoute render cases PASS"
