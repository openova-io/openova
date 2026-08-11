#!/usr/bin/env bash
# bp-keycloak — the catalyst-pin backchannel must name a host THIS CHART
# makes resolve (#6172).
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────────────────────────────────────────────────────
# Keycloak validates an identity provider's URLs when it CREATES it.
# org.keycloak.common.util.UriUtils.checkUrl, verbatim from 26.3.3:
#
#     if (!"https".equals(protocol) && sslRequired.isRequired(parsed.getHost()))
#         throw new IllegalArgumentException(
#             "The url [" + name + "] requires secure connections");
#
# and SslRequired.EXTERNAL.isRequired(host) is !isLocal(host), where isLocal
# is InetAddress.getByName(host) → loopback / site-local / link-local / ULA.
# The sovereign realm ships sslRequired: external.
#
# A PLAINTEXT backchannel leg is therefore legal only while its host
# RESOLVES. #6089 pointed those legs at catalyst-api.catalyst-system, which
# bp-catalyst-platform creates at bootstrap-kit slot 13 — behind bp-gitea,
# behind THIS chart at slot 09. On fresh prov hw294 the name was NXDOMAIN at
# import time (measured inside keycloak-0: `getent hosts
# catalyst-api.catalyst-system.svc.cluster.local` → rc=2, while
# kubernetes.default.svc.cluster.local answered 10.96.0.1), Keycloak replied
#
#     HTTP/1.1 400 Bad Request
#     {"errorMessage":"The url [token_url] requires secure connections"}
#
# and Phase 1 stalled at 51/67 with 16 HelmReleases — cutover, the per-Org
# workspace and every SSO consumer — parked behind bp-keycloak.
#
# The invariant: the realm may name an in-cluster PLAINTEXT backchannel ONLY
# when this chart also renders the Service that makes that exact host
# resolve. A ClusterIP is allocated at create time, so it answers DNS with a
# site-local address before catalyst-api's Pods exist.
#
# Cases:
#   1. the realm's plaintext backchannel host is rendered as a Service here
#   2. that Service is ClusterIP — a headless Service with zero endpoints
#      does NOT resolve, so it would satisfy Case 1 and still wedge
#   3. that Service is NOT named `catalyst-api` — Helm refuses to install a
#      resource another release owns, so the name bp-catalyst-platform uses
#      would move the wedge to slot 13
#   4. the Service SELECTS catalyst-api's Pods, so it routes once slot 13 lands
#   5. the #6106 egress CNP selects the catalyst-api WORKLOAD label, not the
#      anchor Service name
#   6. CONTROL — authorizationUrl and issuer, URLs in the SAME IdP under the
#      SAME validator, stay PUBLIC https (#6087's split is not undone)
#   7. CONTROL — an all-public backchannel renders NO anchor and NO plaintext
#      leg: the anchor is gated on the need, not always-on
#   8. VACUITY — the render actually contains a catalyst-pin IdP with a
#      backchannel, so Cases 1-6 cannot pass on an empty document

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fqdn="smoke.omani.works"
render() {
  "$helm" template keycloak "$chart_dir" \
    --namespace keycloak \
    --api-versions cilium.io/v2 \
    --set sovereignFQDN="$fqdn" \
    --set gateway.host="auth.${fqdn}" \
    "$@"
}

out=$(render)

# ── VACUITY (Case 8) — run FIRST: every later case is read off this block ──
echo "[bp-keycloak] Case 8 (vacuity): the render carries a catalyst-pin IdP"
if ! grep -q '"alias": "catalyst-pin"' <<<"$out"; then
  echo "FAIL: no catalyst-pin identity provider in the render."
  echo "(Cases 1-6 assert properties OF that block. Without it they would all"
  echo " pass vacuously and this guard would be decorative.)"
  exit 1
fi
token_url=$(grep -oE '"tokenUrl": "[^"]+"' <<<"$out" | head -1 | sed 's/.*: "//;s/"$//')
if [[ -z "$token_url" ]]; then
  echo "FAIL: the catalyst-pin IdP rendered no tokenUrl — nothing to validate."
  exit 1
fi
echo "[bp-keycloak] Case 8: PASS (tokenUrl = ${token_url})"

# ── Derive the host the realm actually names ──────────────────────────────
scheme="${token_url%%://*}"
authority="${token_url#*://}"; authority="${authority%%/*}"
host="${authority%%:*}"
svc="${host%%.*}"
rest="${host#*.}"; ns="${rest%%.*}"

# ── Case 1: a plaintext in-cluster leg MUST have its Service rendered here ─
echo "[bp-keycloak] Case 1: plaintext backchannel host is anchored by a Service"
if [[ "$scheme" == "http" && "$host" == *.* ]]; then
  svc_block=$(awk -v RS='---' -v n="$svc" -v ns="$ns" '
    $0 ~ /kind: Service/ && $0 ~ ("name: \"?" n "\"?\n") && $0 ~ ("namespace: \"?" ns "\"?\n") { print }
  ' <<<"$out" || true)
  if [[ -z "$svc_block" ]]; then
    echo "FAIL: the realm names PLAINTEXT ${token_url}, but this chart renders no"
    echo "      Service ${svc} in namespace ${ns}."
    echo
    echo "Keycloak accepts an http identity-provider URL only while its host"
    echo "resolves to a private address (UriUtils.checkUrl → SslRequired.EXTERNAL"
    echo "→ InetAddress.getByName). bp-catalyst-platform creates catalyst-api at"
    echo "bootstrap-kit slot 13, behind bp-gitea, behind THIS chart at slot 09 —"
    echo "so at import time the name is NXDOMAIN and Keycloak answers"
    echo '      400 {"errorMessage":"The url [token_url] requires secure connections"}'
    echo "That is #6172: hw294 Phase 1 stalled at 51/67, 16 HelmReleases behind it."
    exit 1
  fi
else
  echo "      (backchannel is ${scheme}:// — checkUrl exempts https, no anchor needed)"
  svc_block=""
fi
echo "[bp-keycloak] Case 1: PASS"

if [[ -n "$svc_block" ]]; then
  # ── Case 2: ClusterIP, not headless ────────────────────────────────────
  echo "[bp-keycloak] Case 2: the anchor Service is ClusterIP, not headless"
  if grep -qE '^\s*clusterIP:\s*None' <<<"$svc_block"; then
    echo "FAIL: the anchor Service is headless. CoreDNS answers a headless name"
    echo "      with its ENDPOINT addresses, so with zero endpoints — the state"
    echo "      it is in until slot 13 lands — it returns NXDOMAIN and the import"
    echo "      400s exactly as before. The anchor must carry a ClusterIP."
    echo "$svc_block"
    exit 1
  fi
  echo "[bp-keycloak] Case 2: PASS"

  # ── Case 3: distinct from bp-catalyst-platform's own Service name ──────
  echo "[bp-keycloak] Case 3: the anchor does not steal bp-catalyst-platform's name"
  if [[ "$svc" == "catalyst-api" ]]; then
    echo "FAIL: the anchor Service is named 'catalyst-api' in ${ns} — the name"
    echo "      products/catalyst/chart/templates/api-service.yaml owns. Helm"
    echo "      refuses a resource another release owns ('exists and cannot be"
    echo "      imported into the current release'), so this would unwedge slot"
    echo "      09 by wedging slot 13 instead."
    exit 1
  fi
  echo "[bp-keycloak] Case 3: PASS"

  # ── Case 4: it routes, not just resolves ──────────────────────────────
  echo "[bp-keycloak] Case 4: the anchor selects catalyst-api's Pods"
  if ! grep -qE '^\s*app\.kubernetes\.io/name:\s*"?catalyst-api"?\s*$' <<<"$svc_block"; then
    echo "FAIL: the anchor Service does not select app.kubernetes.io/name=catalyst-api."
    echo "      A Service that resolves but selects nothing turns the import wedge"
    echo "      into a silent brokered-login failure — strictly harder to find."
    echo "$svc_block"
    exit 1
  fi
  echo "[bp-keycloak] Case 4: PASS"
fi

# ── Case 5: the #6106 egress policy follows the WORKLOAD, not the Service ──
echo "[bp-keycloak] Case 5: the egress CNP selects the catalyst-api workload"
cnp_block=$(awk -v RS='---' '/kind: CiliumNetworkPolicy/ && /keycloak-allow-catalyst-api-backchannel-egress/ { print }' <<<"$out" || true)
if [[ -z "$cnp_block" ]]; then
  echo "FAIL: #6106's keycloak-allow-catalyst-api-backchannel-egress CNP is gone."
  echo "(Without it the in-cluster backchannel is denied by the catalyst-system"
  echo " baseline default-deny and every brokered login fails.)"
  exit 1
fi
if ! grep -qE 'k8s:app\.kubernetes\.io/name:\s*"?catalyst-api"?\s*$' <<<"$cnp_block"; then
  echo "FAIL: the egress CNP does not select app.kubernetes.io/name=catalyst-api."
  echo "      It used to derive that label from the backchannel URL's first DNS"
  echo "      label, which was correct only while the Service name and the Pod"
  echo "      label were the same string. A CNP that selects nothing renders"
  echo "      exactly as green as one that selects the right endpoints."
  echo "$cnp_block"
  exit 1
fi
echo "[bp-keycloak] Case 5: PASS"

# ── Case 6: CONTROL — the frontchannel legs stay PUBLIC ───────────────────
echo "[bp-keycloak] Case 6 (control): authorizationUrl + issuer stay public https"
auth_url=$(grep -oE '"authorizationUrl": "[^"]+"' <<<"$out" | head -1 | sed 's/.*: "//;s/"$//')
issuer=$(grep -oE '"issuer": "[^"]+"' <<<"$out" | head -1 | sed 's/.*: "//;s/"$//')
for pair in "authorizationUrl=${auth_url}" "issuer=${issuer}"; do
  name="${pair%%=*}"; val="${pair#*=}"
  if [[ "$val" != "https://api.${fqdn}"* ]]; then
    echo "FAIL: ${name} = ${val}, expected the public https://api.${fqdn} form."
    echo "      These two are URLs in the SAME identity provider under the SAME"
    echo "      validator as tokenUrl — they share the suspect property. They must"
    echo "      stay PUBLIC: authorizationUrl is a BROWSER redirect, and issuer is"
    echo "      compared against the iss claim catalyst-api mints from its own"
    echo "      SOVEREIGN_FQDN. Pointing either inward trades a connectivity"
    echo "      failure for a validation failure (#6087)."
    exit 1
  fi
done
echo "[bp-keycloak] Case 6: PASS"

# ── Case 7: CONTROL — all-public restore needs no anchor ──────────────────
echo "[bp-keycloak] Case 7 (control): an all-public backchannel renders no anchor"
pub_out=$(render --set catalystAPIInternalURL="https://api.${fqdn}")
if grep -qE '^\s*catalyst\.openova\.io/component:\s*catalyst-pin-backchannel-anchor' <<<"$pub_out"; then
  echo "FAIL: the anchor Service rendered even though every backchannel leg is"
  echo "      https. checkUrl never consults sslRequired for an https URL, so"
  echo "      there is nothing to anchor — an always-on Service here would be a"
  echo "      second owner of catalyst-api traffic on a hairpin-capable EIP."
  exit 1
fi
if grep -qE '"tokenUrl": "http://' <<<"$pub_out"; then
  echo "FAIL: catalystAPIInternalURL was set to an https endpoint and the realm"
  echo "      still rendered a plaintext tokenUrl."
  exit 1
fi
echo "[bp-keycloak] Case 7: PASS"

echo "[bp-keycloak] catalyst-pin backchannel resolvability (#6172): ALL CASES PASS"
