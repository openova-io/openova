#!/usr/bin/env bash
# chargeback-secondary-edge-6827.sh — #6827.
#
# bp-chargeback is SECONDARY_HR_SUSPEND on a secondary CP, so region-b has no
# local chargeback route — while the SHARED cilium-gateway VIP still targets
# region-b's envoy. Measured on hw307 2026-09-03, same VIP:
#
#     chargeback  404 200 404 200 404 200 200 404 200 404   5/10
#     registry    200 200 200 200 200 200                   6/6   (both regions)
#     region-a envoy chargeback=11 · region-b envoy chargeback=0
#
# Pins the secondary-edge render AND the default-off gate, because a stub that
# rendered unconditionally would create an empty chargeback namespace and a
# route to nothing on every single-region Sovereign.
set -euo pipefail
chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
HOST="chargeback.t99.omani.works"
fail() { echo "FAIL: $*" >&2; exit 1; }

off="$("$helm" template er "$chart_dir" 2>/dev/null)"
on="$("$helm" template er "$chart_dir" --set ingress.hosts.chargeback.host="$HOST" 2>/dev/null)"

# 1. vacuity control — the render must produce something at all
[ -n "$on" ] || fail "the chart rendered NOTHING with the host set; this test would pass on a broken chart"

# 2. default-off
if grep -q 'chargeback' <<<"$off"; then
  fail "#6827: chargeback objects render by DEFAULT — a single-region Sovereign would get an empty chargeback namespace and a route to nothing"
fi

# 3. the three objects the secondary needs
for want in "kind: Namespace" "kind: Service" "kind: HTTPRoute"; do
  grep -q "$want" <<<"$on" || fail "#6827: '$want' missing from the secondary-edge render"
done

# 4. the mesh contract — a stub with a local backend would defeat the purpose
grep -q 'service.cilium.io/global: "true"' <<<"$on" \
  || fail "#6827: the stub Service is not a ClusterMesh global Service — it cannot fall through to the primary"

# 5. the supplied hostname, on the SHARED gateway
grep -q -- "- \"$HOST\"" <<<"$on" || fail "#6827: the route does not carry the supplied hostname"
grep -q 'name: "cilium-gateway"' <<<"$on" \
  || fail "#6827: the route must attach to the SHARED cilium-gateway — the same parent products/chargeback uses; another gateway answers on a listener the primary never uses"

echo "PASS: #6827 chargeback secondary-edge — default-off, and host-set renders Namespace + global Service + HTTPRoute on the shared gateway"
