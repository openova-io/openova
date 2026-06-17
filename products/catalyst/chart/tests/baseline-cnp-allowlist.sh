#!/usr/bin/env bash
# bp-catalyst-platform — baseline-CNP allow-list contract gate.
#
# Verifies that `templates/network-policies/baseline-catalyst-system.yaml`
# continues to allow the load-bearing east-west and egress surfaces that
# customer journey and Sovereign handover depend on. This gate was
# created in response to the 3-PR regression cascade in May 2026:
#
#   - #1785 (chart 1.4.171) introduced the baseline CNP for
#     catalyst-system with WORLD egress restricted to TCP/443 ONLY and
#     NO `catalyst`/`flux-system`/`kube-system` namespace ingress
#     allow-list.
#   - #1803 (chart 1.4.177) re-added SMTP egress (587/465/25 TCP) after
#     catalyst-api PIN-issue 502'd on every fresh onboarding.
#   - #1847 (chart 1.4.178) re-added ingress from `catalyst` namespace
#     (cutover Pods → catalyst-api on TCP/8080) after t24 fresh-prov
#     handover hung at WAIT_TIMEOUT_SECONDS=1500s.
#
# The cascade shipped because nothing in CI rendered the chart and
# asserted on the rendered CNP shape. This script fills that gap so
# any future narrowing of the allow-list — accidental or otherwise —
# fails Blueprint Release publish BEFORE the OCI artifact reaches a
# Sovereign. See issue #1850.
#
# Test framework: pure `helm template` + grep/awk on rendered YAML,
# matching the established `platform/self-sovereign-cutover/chart/
# tests/cutover-contract.sh` pattern. Picked up automatically by
# `.github/workflows/blueprint-release.yaml` (the "Run chart
# integration tests (chart/tests/*.sh)" step at line 384).
#
# Usage: bash tests/baseline-cnp-allowlist.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

CNP_TEMPLATE="templates/network-policies/baseline-catalyst-system.yaml"

echo "[baseline-cnp] Case 1: chart renders the baseline-catalyst-system CNP with defaults"
helm template smoke . --show-only "${CNP_TEMPLATE}" > "$TMP/cnp.yaml"
if ! grep -q "name: baseline-default-deny" "$TMP/cnp.yaml"; then
  echo "FAIL: baseline-default-deny CNP missing from default render — chart broken or template renamed" >&2
  exit 1
fi
if ! grep -q "namespace: catalyst-system" "$TMP/cnp.yaml"; then
  echo "FAIL: baseline CNP not scoped to catalyst-system namespace" >&2
  exit 1
fi
if ! grep -q "kind: CiliumNetworkPolicy" "$TMP/cnp.yaml"; then
  echo "FAIL: baseline CNP is not a CiliumNetworkPolicy (regressed to CCNP or NetworkPolicy?)" >&2
  exit 1
fi
echo "  PASS (baseline-default-deny CNP rendered, namespaced to catalyst-system)"

echo "[baseline-cnp] Case 2: egress allow-list includes SMTP submission (TCP/587) — #1803 regression guard"
# The catalyst-api PIN-issue handler calls mail.openova.io:587. Without
# 587 in the world-egress allow-list, /api/v1/auth/pin-request returns
# 502 and every fresh onboarding is blocked. This was caught LIVE on
# t22 2026-05-18 after chart 1.4.171 rolled out. Re-narrowing this
# block — even "for security hardening" — must fail CI here.
if ! grep -E '^\s*-\s+port:\s+"587"' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 587/TCP missing — PIN-issue email delivery will 502 (#1803 regression)" >&2
  exit 1
fi
# Confirm 587 is on a toEntities:[world] rule, not accidentally on a
# kube-dns toEndpoints block (which would be a different bug).
if ! awk '/toEntities:/,/toPorts:/{block=block"\n"$0} /port: "587"/{if (block ~ /world/) {print "ok"; exit 0}; exit 1}' "$TMP/cnp.yaml" | grep -q ok; then
  # Looser fallback: render the egress section and confirm world + 587 co-occur within 30 lines.
  if ! awk '/egress:/,0' "$TMP/cnp.yaml" | grep -B30 'port: "587"' | grep -q '\- world'; then
    echo "FAIL: port 587/TCP exists but not scoped to toEntities:[world] (#1803)" >&2
    exit 1
  fi
fi
echo "  PASS (egress TCP/587 SMTP submission allowed to world)"

echo "[baseline-cnp] Case 3: egress allow-list includes SMTPS (TCP/465) — #1803 regression guard"
# 465 is the SMTPS fallback path. Stalwart accepts both 587 and 465;
# the chart-1.4.177 fix added BOTH so a future Stalwart deployment that
# disables 587 doesn't silently rebreak PIN-issue.
if ! grep -E '^\s*-\s+port:\s+"465"' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 465/TCP missing — SMTPS fallback unavailable (#1803 regression)" >&2
  exit 1
fi
echo "  PASS (egress TCP/465 SMTPS allowed)"

echo "[baseline-cnp] Case 4: egress allow-list includes legacy SMTP (TCP/25) — #1803 regression guard"
# 25 is the legacy SMTP fallback path. Same reasoning as 465.
if ! grep -E '^\s*-\s+port:\s+"25"' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 25/TCP missing — legacy SMTP fallback unavailable (#1803 regression)" >&2
  exit 1
fi
echo "  PASS (egress TCP/25 legacy SMTP allowed)"

echo "[baseline-cnp] Case 5: egress includes HTTPS (TCP/443) to world"
# 443 is the original allow from chart 1.4.171 — Dynadot, S3, GitHub,
# ghcr/harbor pulls. Belt-and-braces guard.
if ! grep -E '^\s*-\s+port:\s+"443"' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 443/TCP missing — Dynadot/S3/GitHub/registry pulls broken" >&2
  exit 1
fi
echo "  PASS (egress TCP/443 HTTPS allowed)"

echo "[baseline-cnp] Case 5b: egress allow-list includes kube-apiserver (TCP/6443) to world — #1908 regression guard"
# catalyst-api fans out to SECONDARY-REGION kube-apiservers on TCP/6443
# (D5 nodes endpoint, D16 dashboard treemap, D20 per-region jobs queries).
# A152 diagnostic on t31 isolated the symptom:
#   kubectl -n catalyst-system exec deploy/catalyst-api -- \
#     nc -zvw 3 49.12.210.78 6443
#   nc: connect to 49.12.210.78 port 6443 (tcp) timed out
# while the SAME endpoint from the bastion connected. The baseline
# CNP world-egress block only allowed 443/587/465/25 — secondary CP
# api-server List() calls timed out silently and fan-out returned
# primary-only. Re-narrowing this block — even "for security
# hardening" — must fail CI here.
if ! grep -E '^\s*-\s+port:\s+"6443"' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 6443/TCP missing — multi-region fan-out (D5/D16/D20) will silently return primary-only (#1908 regression)" >&2
  exit 1
fi
# Confirm 6443 is on a toEntities:[world] rule (the secondary CPs are
# reachable only via public LB IPs per ClusterMesh model), not on a
# kube-dns or kube-apiserver-entity block.
if ! awk '/egress:/,0' "$TMP/cnp.yaml" | grep -B30 'port: "6443"' | grep -q '\- world'; then
  echo "FAIL: port 6443/TCP exists but not scoped to toEntities:[world] (#1908)" >&2
  exit 1
fi
echo "  PASS (egress TCP/6443 to world allowed — secondary CP fan-out unblocked)"

echo "[baseline-cnp] Case 5c: egress allow-list includes 'org-services' namespace — #1917/#1920 regression guard (TBD-A38/A43; #3383 renamed 'sme' → 'org-services')"
# catalyst-ui (catalyst-system) → gateway.org-services.svc:8080 carries every
# tenant-facing Organization action (admin, auth, billing, catalog, console,
# domain, marketplace). #3383 eradicated the legacy 'sme'-named subsystem,
# renaming the namespace + its gateway to 'org-services' (chart
# templates/org-services/, values.yaml security.baselineCnp.allowedPlatformNamespaces).
# Without `org-services` in the egress allow-list, the baseline-default-deny CNP
# drops the traffic and the Console returns 503 `context deadline exceeded`.
# Caught LIVE on t32 fresh-prov walk 2026-05-19 (then under the 'sme' name);
# any re-narrowing must fail here.
if ! awk '/egress:/,0' "$TMP/cnp.yaml" | grep -q '"org-services"'; then
  echo "FAIL: egress allow-list missing 'org-services' namespace — Console → gateway.org-services.svc will 503 (#1920/#3383 regression)" >&2
  exit 1
fi
echo "  PASS (egress to 'org-services' namespace allowed — Console → gateway.org-services.svc unblocked)"

echo "[baseline-cnp] Case 5d: egress allow-list includes 'newapi' namespace — #1920 regression guard (TBD-A43)"
# catalyst-system controllers reach the NewAPI v2 service plane in the
# `newapi` namespace for the customer-journey D29 flows. Without
# `newapi` in the egress allow-list, every catalyst-system → NewAPI v2
# call times out.
if ! awk '/egress:/,0' "$TMP/cnp.yaml" | grep -q '"newapi"'; then
  echo "FAIL: egress allow-list missing 'newapi' namespace — catalyst-system → NewAPI v2 will time out (#1920 regression)" >&2
  exit 1
fi
echo "  PASS (egress to 'newapi' namespace allowed — NewAPI v2 plane reachable)"

echo "[baseline-cnp] Case 6: egress allow-list includes DNS (53/UDP + 53/TCP) to kube-dns"
# Without DNS, every name lookup in catalyst-system fails. The CNP
# explicitly allows kube-dns endpoints in kube-system on 53/UDP +
# 53/TCP. The presence of BOTH protocols is asserted because TCP/53
# is required for DNS-over-TCP fallback whenever a UDP response would
# exceed 512 bytes (every AAAA/A combo for a multi-region host).
if ! grep -E '^\s*-\s+port:\s+"53"\s*$' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress port 53 missing — DNS resolution will fail cluster-wide" >&2
  exit 1
fi
if ! grep -E 'protocol:\s+UDP' "$TMP/cnp.yaml" >/dev/null; then
  echo "FAIL: egress to port 53 has no UDP protocol entry — DNS will fail" >&2
  exit 1
fi
# Both 53/UDP AND 53/TCP must be present. Collapse the rendered DNS
# block and check the protocol set.
dns_block=$(awk '/k8s-app: kube-dns/,/toPorts/{print}; /toPorts/,/^\s{4}-/{print}' "$TMP/cnp.yaml" | head -60)
udp_53=$(printf '%s\n' "$dns_block" | grep -c "protocol: UDP" || true)
tcp_53=$(printf '%s\n' "$dns_block" | grep -c "protocol: TCP" || true)
if [ "${udp_53}" -lt 1 ] || [ "${tcp_53}" -lt 1 ]; then
  echo "FAIL: kube-dns toPorts must include BOTH UDP and TCP on port 53 (got UDP=${udp_53} TCP=${tcp_53})" >&2
  exit 1
fi
echo "  PASS (egress 53/UDP + 53/TCP to kube-dns allowed)"

echo "[baseline-cnp] Case 7: ingress allow-list includes the 'catalyst' namespace — #1847 regression guard"
# bp-self-sovereign-cutover stamps Jobs into the `catalyst` namespace.
# The 10-auto-trigger Pod (platform/self-sovereign-cutover/chart/
# templates/10-auto-trigger-job.yaml) curls
#   http://catalyst-api.catalyst-system.svc.cluster.local:8080/api/v1/internal/cutover/trigger
# at chart install time. Without `catalyst` in the ingress allow-list
# the auto-trigger Pod times out at WAIT_TIMEOUT_SECONDS=1500s,
# handoverFiredAt stays null, the operator is stuck on mothership
# /jobs forever (D0 auto-redirect never fires). Caught LIVE on t24
# fresh-prov verification 2026-05-18.
if ! grep -q '"catalyst"' "$TMP/cnp.yaml"; then
  echo "FAIL: ingress allow-list missing 'catalyst' namespace — cutover auto-trigger will time out, handover broken (#1847 regression)" >&2
  exit 1
fi
echo "  PASS (ingress from 'catalyst' namespace allowed — cutover Pods can reach catalyst-api:8080)"

echo "[baseline-cnp] Case 8: ingress allow-list includes 'flux-system' (HelmRelease readiness probes)"
# Helm Controller, Kustomize Controller, and Source Controller probe
# backend Services in catalyst-system for HelmRelease/Kustomization
# health rollups. Without this, every HelmRelease that targets
# catalyst-system shows Ready=Unknown indefinitely on the mothership
# dashboard.
if ! grep -q '"flux-system"' "$TMP/cnp.yaml"; then
  echo "FAIL: ingress allow-list missing 'flux-system' — HelmRelease health rollups will stall" >&2
  exit 1
fi
echo "  PASS (ingress from 'flux-system' allowed)"

echo "[baseline-cnp] Case 9: ingress allow-list includes 'kube-system' (operator + ccm + CoreDNS)"
# kube-system Pods (cilium-operator, hcloud-ccm, coredns) need cluster
# introspection into catalyst-system. The reserved.ingress gateway
# endpoint also in kube-system is matched separately via the
# `reserved.ingress: ""` selector.
if ! grep -q '"kube-system"' "$TMP/cnp.yaml"; then
  echo "FAIL: ingress allow-list missing 'kube-system' — Cilium operator + ccm + CoreDNS introspection broken" >&2
  exit 1
fi
echo "  PASS (ingress from 'kube-system' allowed)"

echo "[baseline-cnp] Case 10: ingress allow-list is namespace-scoped (NOT reserved/world/cluster wildcard)"
# The baseline CNP MUST stay namespace-scoped per issue #1746 design:
# only `reserved.ingress: ""` (which is a SPECIFIC label selector for
# the cilium-gateway endpoint, not a wildcard) AND `matchExpressions`
# In-listed namespaces are allowed. A future "simplify" refactor that
# replaces the namespace allow-list with `fromEntities: [cluster]` or
# `fromEntities: [world]` would render the default-deny CNP toothless
# and must fail here.
ingress_block=$(awk '/^  ingress:/,/^  egress:/' "$TMP/cnp.yaml")
if printf '%s\n' "$ingress_block" | grep -qE '^\s*-\s*cluster\s*$'; then
  echo "FAIL: ingress fromEntities includes 'cluster' (toothless wildcard) — must use namespace allow-list" >&2
  exit 1
fi
if printf '%s\n' "$ingress_block" | grep -qE '^\s*-\s*world\s*$'; then
  echo "FAIL: ingress fromEntities includes 'world' — the catalyst-system CNP must not allow direct internet ingress" >&2
  exit 1
fi
if printf '%s\n' "$ingress_block" | grep -qE '^\s*-\s*all\s*$'; then
  echo "FAIL: ingress fromEntities includes 'all' — toothless wildcard" >&2
  exit 1
fi
echo "  PASS (ingress is namespace-scoped, no wildcard reserved/world/cluster/all)"

echo "[baseline-cnp] Case 11: catalyst-system api-service contract — auto-trigger Pod hits port 8080"
# Cross-check: the api-service.yaml in this same chart MUST expose port
# 8080 (the port the catalyst ns cutover Pods connect to). If a future
# refactor moves catalyst-api to a non-8080 port, the ingress allow we
# just asserted would still pass but the cutover Pod would still time
# out. This guard catches that drift.
helm template smoke . --show-only templates/api-service.yaml > "$TMP/api-service.yaml" 2>/dev/null || true
if [ -s "$TMP/api-service.yaml" ]; then
  if ! grep -E '^\s*-\s+port:\s+8080' "$TMP/api-service.yaml" >/dev/null; then
    echo "FAIL: catalyst-api Service no longer exposes port 8080 — cutover auto-trigger contract broken" >&2
    exit 1
  fi
  echo "  PASS (catalyst-api Service exposes port 8080)"
else
  # api-service.yaml didn't render — fall back to a textual check
  # against the template source (template content is what matters; the
  # path may be relocated by a future refactor).
  if ! grep -RE 'port:\s+8080' templates/api-service.yaml >/dev/null 2>&1; then
    echo "FAIL: catalyst-api template no longer references port 8080" >&2
    exit 1
  fi
  echo "  PASS (catalyst-api template references port 8080)"
fi

echo "[baseline-cnp] Case 12: CNP toggles off cleanly when baselineCnp.enabled=false"
# Operator escape hatch — the chart MUST not render any baseline CNP
# when the toggle is flipped. Catches future regressions that hardcode
# the policy outside the conditional.
helm template smoke-disabled . --set security.baselineCnp.enabled=false --show-only "${CNP_TEMPLATE}" > "$TMP/cnp-off.yaml" 2>&1 || true
if grep -q "name: baseline-default-deny" "$TMP/cnp-off.yaml"; then
  echo "FAIL: baseline CNP still rendered with security.baselineCnp.enabled=false — toggle broken" >&2
  exit 1
fi
echo "  PASS (CNP toggles off when security.baselineCnp.enabled=false)"

echo "[baseline-cnp] Case 13: allow-list is operator-tunable via values"
# Operators must be able to extend the ingress allow-list (e.g. to add
# a custom tenant ns) without forking the chart. This test asserts
# .Values.security.baselineCnp.allowedIngressNamespaces propagates.
helm template smoke-extra . \
  --set 'security.baselineCnp.allowedIngressNamespaces[0]=catalyst' \
  --set 'security.baselineCnp.allowedIngressNamespaces[1]=flux-system' \
  --set 'security.baselineCnp.allowedIngressNamespaces[2]=kube-system' \
  --set 'security.baselineCnp.allowedIngressNamespaces[3]=tenant-acme' \
  --show-only "${CNP_TEMPLATE}" > "$TMP/cnp-extra.yaml"
if ! grep -q '"tenant-acme"' "$TMP/cnp-extra.yaml"; then
  echo "FAIL: custom ingress namespace 'tenant-acme' did not propagate via --set — operator-tunability broken" >&2
  exit 1
fi
echo "  PASS (allowedIngressNamespaces is operator-tunable)"

echo "[baseline-cnp] All gates green."
