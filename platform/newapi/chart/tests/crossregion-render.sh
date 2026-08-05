#!/usr/bin/env bash
# bp-newapi — cross-region singleton render audit (#5599).
#
# THE DEFECT THIS GUARDS
# ──────────────────────────────────────────────────────────────────────────
# An OAuth authorization code is SINGLE-USE and stored SERVER-SIDE. Until
# 1.4.151 the bootstrap-kit installed this chart identically on EVERY region's
# control plane, so a 2-region Sovereign ran TWO complete newapi stacks — two
# Deployments AND two INDEPENDENT CNPG `Cluster` objects, not a replicated
# pair — behind ONE `newapi.<fqdn>` hostname whose shared VIP fans a browser's
# parallel connections across both gateways (#5459: a browser can never be
# assumed to pin one backend).
#
# So the flow split: the leg that initiated the exchange wrote its state into
# region-A's Postgres, the `/api/oauth/sovereign` callback landed on region-B,
# whose database had never seen that code, and region-B CORRECTLY rejected it.
# A VALID Keycloak authorization code answered `403 Forbidden` — three times,
# the client retrying and losing the coin flip again — and newapi rendered
# signed-out with `/api/user/self` 401 (UAT row 37).
#
# Measured live on hw292 (dep 1c56518035a83e03), BOTH regions sampled:
#   region-a  deployment.apps/newapi-bp-newapi                        1/1
#             cluster.postgresql.cnpg.io/newapi-bp-newapi-newapi-pg   2 ready
#             httproute newapi-bp-newapi-public -> ["newapi.hw292.omani.works"]
#   region-b  deployment.apps/newapi-bp-newapi                        1/1
#             cluster.postgresql.cnpg.io/newapi-bp-newapi-newapi-pg   2 ready
#             httproute newapi-bp-newapi-public -> ["newapi.hw292.omani.works"]
#
# #5414 already fixed the SESSION_SECRET/CRYPTO_SECRET split at the SECRET
# seam. The DATASTORE split underneath it was never addressed — this guard
# asserts it is now closed.
#
# THE LOAD-BEARING ASSERTION is CASE 3: a `secondary` region must render NO
# Deployment AND NO CNPG Cluster, so both its Services have ZERO local backends
# and `service.cilium.io/affinity: local` falls through the ClusterMesh to the
# primary's singleton. A per-region property has to be asserted PER REGION.
#
# VACUITY — checked in BOTH directions
# ──────────────────────────────────────────────────────────────────────────
# Case 3 FAILS against the pre-fix chart: origin/main has no `crossRegion` seam
# at all, so `--set crossRegion.role=secondary` is inert there and the
# secondary renders a full Deployment + its own CNPG Cluster + annotation-free
# Services, i.e. the two-stack split this issue is about. Run it against a
# checkout of 1.4.150 to see that.
#
# Case 1 is the paired non-regression check that PASSES ON BOTH TREES: the
# default render must carry NO mesh annotations AND must still contain the
# Deployment and the CNPG Cluster. That is what stops the guard from being
# satisfied by blanket suppression or by blanket annotation.
#
# Cases:
#   1. Default (crossRegion absent)   — no mesh annotations; Deployment + CNPG
#                                       Cluster present.   [PASSES ON BOTH]
#   2. enabled + role=primary         — global=true / shared=true / affinity on
#                                       BOTH Services; workloads + CNPG present;
#                                       cross-region CNP present.
#   3. enabled + role=secondary       — NO Deployment, NO CNPG Cluster, NO
#                                       DSN-sync or seed Jobs; BOTH Services
#                                       present with shared=false. [VACUITY-BREAKER]
#   4. enabled + empty peerClusters   — no CiliumNetworkPolicy.
#   5. invalid role                   — render FAILS fast.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

base=(
  --set newapi.enabled=true
  --set sovereignFQDN=smoke.omani.works
  --set auth.adminUI.mode=keycloak
  --set auth.adminUI.keycloak.issuer=https://auth.smoke.omani.works/realms/sovereign
  --set auth.adminUI.keycloak.existingSecret=newapi-oidc
  # The public HTTPRoute is what makes this a 2-region problem at all (one
  # hostname, both gateways), so every case renders it — as slot 80 does live.
  --set ingress.httpRoute.enabled=true
  --api-versions cilium.io/v2
  --api-versions postgresql.cnpg.io/v1
)

# Extract the block a given template produced. Uses herestrings throughout — a
# `grep -q` on a pipe closes it and kills the producer with SIGPIPE 141, which
# `set -o pipefail` turns into a false FAIL (the #5531/#5537 catalog-wide class).
src_block() { # <render> <template-file>
  awk -v pat="^# Source: bp-newapi/templates/$2\$" \
      '$0 ~ pat {f=1; print; next} /^# Source: /{f=0} f' <<<"$1"
}

# Strip YAML comments so the templates' own prose — which names every
# annotation and the words Deployment and Cluster — can never satisfy an
# assertion.
uncomment() { sed 's/[[:space:]]*#.*$//'; }

render() { "$helm" template newapi "$chart_dir" "${base[@]}" "$@" 2>&1; }

fail() { echo "FAIL: $*" >&2; exit 1; }

# ── Case 1: default render is untouched (PASSES ON BOTH TREES) ──────────────
echo "[bp-newapi] Case 1: default (crossRegion off) — no mesh annotations, full stack"
out=$(uncomment <<<"$(render)")
if grep -q "service.cilium.io/" <<<"$out"; then
  fail "default render carries service.cilium.io/* — single-region Sovereigns must be untouched"
fi
grep -q "^kind: Deployment$" <<<"$out" || fail "default render is missing the newapi Deployment"
grep -q "^kind: Cluster$"    <<<"$out" || fail "default render is missing the CNPG Cluster"
echo "[bp-newapi] Case 1: PASS"

# ── Case 2: primary exports its backends and keeps the full stack ───────────
echo "[bp-newapi] Case 2: crossRegion primary — global+shared on BOTH Services, stack present"
raw=$(render --set crossRegion.enabled=true --set crossRegion.role=primary \
  --set 'crossRegion.peerClusters={hw292-me-east-b}')
out=$(uncomment <<<"$raw")
svc=$(uncomment <<<"$(src_block "$raw" service.yaml)")
for want in 'service.cilium.io/global: "true"' 'service.cilium.io/shared: "true"' 'service.cilium.io/affinity: "local"'; do
  # BOTH the main Service and the `-bridge` Service must carry it — the
  # HTTPRoute's Exact `/` and `/login` rules target the bridge, and annotating
  # only one leaves the entry leg free to land in the other region.
  n=$(grep -cF -- "$want" <<<"$svc" || true)
  [ "$n" -eq 2 ] || fail "primary: expected [$want] on BOTH Services, found $n"
done
grep -q "^kind: Deployment$" <<<"$out" || fail "primary region must still run the newapi Deployment"
grep -q "^kind: Cluster$"    <<<"$out" || fail "primary region must still own the CNPG Cluster"
grep -q "kind: CiliumNetworkPolicy" <<<"$out" || fail "primary did not render the cross-region CiliumNetworkPolicy"
cnp=$(uncomment <<<"$(src_block "$raw" crossregion-netpol.yaml)")
# The CNP must name the PEER cluster and the callers' OWN namespaces — a rule
# that omits either is silently narrowed to nothing (#4854 row 67 L2), and it
# must admit remote-node for the peer's hostNetwork cilium-envoy hop.
for want in 'io.cilium.k8s.policy.cluster' '- "hw292-me-east-b"' '- "catalyst-system"' '- remote-node'; do
  grep -qF -- "$want" <<<"$cnp" || fail "cross-region CNP missing [$want]"
done
echo "[bp-newapi] Case 2: PASS"

# ── Case 3: THE LOAD-BEARING ONE — secondary renders no stack ───────────────
echo "[bp-newapi] Case 3: crossRegion secondary — zero local backends, zero local datastore"
raw=$(render --set crossRegion.enabled=true --set crossRegion.role=secondary \
  --set 'crossRegion.peerClusters={hw292-mesh}')
out=$(uncomment <<<"$raw")

# (a) NO Deployment.
if grep -q "^kind: Deployment$" <<<"$out"; then
  echo "FAIL: the secondary region rendered a Deployment. Its newapi Services" >&2
  echo "      must have ZERO local backends, otherwise the shared VIP again" >&2
  echo "      splits one browser's OAuth exchange across two regions and a" >&2
  echo "      VALID authorization code comes back 403 (#5599)." >&2
  grep -B4 -A2 "^kind: Deployment$" <<<"$out" | head -20 >&2
  exit 1
fi
# (b) NO second datastore — this is the piece #5414 never addressed.
if grep -q "^kind: Cluster$" <<<"$out"; then
  echo "FAIL: the secondary region rendered its OWN CNPG Cluster. An OAuth" >&2
  echo "      authorization code is single-use and stored SERVER-SIDE, so the" >&2
  echo "      Sovereign may hold exactly ONE newapi datastore (#5599)." >&2
  exit 1
fi
# (c) NO hook Job that would poll a datastore that does not exist and wedge the
#     release before the mesh stubs ever install.
if grep -q "^kind: Job$" <<<"$out"; then
  echo "FAIL: the secondary rendered a Job. The DSN-sync / seed hooks poll the" >&2
  echo "      CNPG '-app' Secret and rollout-restart the Deployment — neither" >&2
  echo "      exists here, so the post-install hook would fail the release." >&2
  grep -B4 -A2 "^kind: Job$" <<<"$out" | head -20 >&2
  exit 1
fi
# (d) but BOTH Services MUST exist — they are the mesh stubs the peer merges
#     with, and this region's envoy still answers for newapi.<fqdn>.
svc=$(uncomment <<<"$(src_block "$raw" service.yaml)")
[ -n "$svc" ] || fail "the secondary must still render the newapi Services — Cilium merges global Services by name+namespace"
n=$(grep -cE "^  name: newapi-bp-newapi(-bridge)?$" <<<"$svc" || true)
[ "$n" -eq 2 ] || fail "secondary: expected BOTH the main and the -bridge Service, found $n"
for want in 'service.cilium.io/global: "true"' 'service.cilium.io/shared: "false"'; do
  n=$(grep -cF -- "$want" <<<"$svc" || true)
  [ "$n" -eq 2 ] || fail "secondary: expected [$want] on BOTH Services, found $n"
done
# (e) and the HTTPRoute MUST still render — the shared VIP fans TCP across both
#     regions' gateways, so this region has to answer for the hostname.
grep -q "^kind: HTTPRoute$" <<<"$out" || fail "the secondary must still render the HTTPRoute — the shared VIP fans TCP across BOTH gateways"
echo "[bp-newapi] Case 3: PASS"

# ── Case 4: no peers → no CNP (single-region byte-identity) ─────────────────
echo "[bp-newapi] Case 4: empty peerClusters — no CiliumNetworkPolicy"
out=$(uncomment <<<"$(render --set crossRegion.enabled=true --set crossRegion.role=primary)")
grep -q "^kind: CiliumNetworkPolicy$" <<<"$(src_block "$out" crossregion-netpol.yaml)" \
  && fail "rendered a cross-region CNP with an empty peerClusters list"
echo "[bp-newapi] Case 4: PASS"

# ── Case 5: an unknown role must fail the render, never render silently ─────
echo "[bp-newapi] Case 5: invalid crossRegion.role fails fast"
if "$helm" template newapi "$chart_dir" "${base[@]}" \
    --set crossRegion.enabled=true --set crossRegion.role=standby >/dev/null 2>&1; then
  fail "crossRegion.role=standby rendered instead of failing — a typo'd role would silently fall back to a full second newapi stack"
fi
echo "[bp-newapi] Case 5: PASS"

echo "[bp-newapi] cross-region render audit: ALL PASS"
