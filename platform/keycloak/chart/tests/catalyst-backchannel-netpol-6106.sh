#!/usr/bin/env bash
# bp-keycloak — catalyst-pin OIDC BACKCHANNEL egress policy gate (#6106).
#
# WHAT THIS GUARDS
# ────────────────────────────────────────────────────────────────────────
# #6087/#6089 repointed the sovereign realm's `catalyst-pin` identity
# provider so its server-side legs stop hairpinning through the Sovereign's
# own public EIP:
#
#   authorizationUrl  https://api.<sovereignFQDN>/oidc/auth   (browser  — PUBLIC)
#   tokenUrl / userInfoUrl / jwksUrl                          (KC server — INTERNAL)
#
# The realm ConfigMap carrying that split was re-imported on hw293 at
# 2026-08-10T23:43:49Z and every catalyst-pin brokered login went dark —
# grafana, gitea, openbao, guacamole, newapi, hubble and the Keycloak admin
# console — with keycloak-0 logging, verbatim:
#
#   ERROR [org.keycloak.broker.oidc.AbstractOAuth2IdentityProvider]
#   Failed to make identity provider oauth callback:
#   org.apache.http.conn.ConnectTimeoutException: Connect to
#   catalyst-api.catalyst-system.svc.cluster.local:8080 [10.96.1.11] failed:
#   Connection timed out
#
# The dial moved from `world:443` (already permitted) to an in-cluster
# endpoint on :8080, and no policy granted the new hop. UAT rows
# 30/31/33/34/35/37/39/219 all terminate there.
#
# This gate asserts the chart that MOVED the URL also ships the policy that
# permits it, and — crucially — that the policy is DERIVED from that same
# value rather than restated, so the two can never drift apart again.
#
# Cases:
#   1. the egress CNP renders, with the destination + port DERIVED from
#      catalystAPIInternalURL (asserted on VALUES, not on key presence)
#   2. PAIRING — the realm's tokenUrl/userInfoUrl/jwksUrl name exactly the
#      host:port the policy permits
#   3. CONTROL — authorizationUrl + issuer stay PUBLIC and unchanged; they
#      share the suspect property (catalyst-pin IdP URLs) and must NOT move
#   4. FORM — toEndpoints + io.cilium.k8s.policy.cluster Exists, never CIDR
#   5. VACUITY — catalystAPIInternalURL="" removes the policy AND restores
#      the public backchannel (proves case 1 can fail)
#   6. VACUITY — no cilium.io/v2 capability removes the policy (proves the
#      Capabilities gate is real, not decorative)
#   7. DERIVATION — a different URL moves namespace + Pod label + port with
#      it (proves case 1's assertions are not tautologies over hardcoded text)
#
# Usage: bash tests/catalyst-backchannel-netpol-6106.sh [CHART_DIR]

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fqdn="smoke.omani.works"
tpl="templates/catalyst-backchannel-egress-cnp.yaml"
cnp_name="keycloak-allow-catalyst-api-backchannel-egress"

render() {
  # $1.. = extra helm args. Cilium CRDs are declared so the Capabilities gate
  # is satisfied — helm-controller discovers them server-side on a live
  # Sovereign, `helm template` needs them stated.
  "$helm" template keycloak "$chart_dir" \
    --namespace keycloak \
    --api-versions cilium.io/v2 \
    --set sovereignFQDN="$fqdn" \
    "$@" 2>/dev/null
}

# ── Case 1: the egress CNP renders with derived VALUES ────────────────────
echo "[bp-keycloak/6106] Case 1: catalyst-pin backchannel egress CNP renders with derived values"
cnp="$(render --show-only "$tpl" || true)"
if [ -z "$cnp" ]; then
  echo "FAIL: $tpl did not render. Without it a Keycloak Pod cannot reach catalyst-api's OIDC backchannel and every brokered SSO login ConnectTimeouts (#6106)." >&2
  exit 1
fi
if ! grep -q "name: ${cnp_name}" <<<"$cnp"; then
  echo "FAIL: CNP ${cnp_name} missing from the render (#6106)." >&2
  echo "$cnp" >&2
  exit 1
fi
if ! grep -q '^kind: CiliumNetworkPolicy$' <<<"$cnp"; then
  echo "FAIL: the backchannel allow is not a CiliumNetworkPolicy — a vanilla NetworkPolicy cannot express the ClusterMesh cluster key (#6106)." >&2
  exit 1
fi
# Slice the egress block so the assertions below cannot be satisfied by text
# living somewhere else in the document (the comment block names the same
# service and port).
#
# Comment-only lines are dropped first: this template documents the CIDR trap
# it avoids, and a form check that greps the raw text would fail on its own
# rationale. Every assertion below therefore runs against real YAML only.
strip_comments() { grep -v '^[[:space:]]*#' || true; }
egress_block="$(awk '/^  egress:/,0' <<<"$cnp" | strip_comments)"
if [ -z "$egress_block" ]; then
  echo "FAIL: could not slice the egress block out of ${cnp_name} — the awk range is broken, not the policy." >&2
  exit 1
fi
# Assert on the VALUE of each selector key. `k8s:io.kubernetes.pod.namespace: ""`
# would carry the key and match nothing.
if ! grep -q 'k8s:io.kubernetes.pod.namespace: "catalyst-system"' <<<"$egress_block"; then
  echo "FAIL: the egress destination namespace is not catalyst-system (#6106)." >&2
  echo "$egress_block" >&2
  exit 1
fi
if ! grep -q 'k8s:app.kubernetes.io/name: "catalyst-api"' <<<"$egress_block"; then
  echo "FAIL: the egress destination is not scoped to the catalyst-api Pods — a namespace-only destination would grant keycloak the whole control-plane namespace (#6106)." >&2
  echo "$egress_block" >&2
  exit 1
fi
if ! grep -q 'port: "8080"' <<<"$egress_block"; then
  echo "FAIL: the egress allow does not name TCP/8080 — the port keycloak-0 timed out on (#6106)." >&2
  echo "$egress_block" >&2
  exit 1
fi
if ! grep -q 'protocol: TCP' <<<"$egress_block"; then
  echo "FAIL: the egress port rule carries no TCP protocol (#6106)." >&2
  exit 1
fi
echo "[bp-keycloak/6106] Case 1: PASS (egress -> catalyst-system/catalyst-api on TCP/8080)"

# ── Case 2: PAIRING — realm backchannel URLs match what the policy permits ─
echo "[bp-keycloak/6106] Case 2: the realm's backchannel URLs name exactly the host:port the policy permits"
realm="$(render --show-only templates/configmap-sovereign-realm.yaml || true)"
if [ -z "$realm" ]; then
  echo "FAIL: the sovereign realm ConfigMap did not render — this pairing check would be vacuous." >&2
  exit 1
fi
backchannel="http://catalyst-api.catalyst-system.svc.cluster.local:8080"
for leg in tokenUrl userInfoUrl jwksUrl; do
  if ! grep -q "\"${leg}\": \\\\\"${backchannel}/oidc/" <<<"$realm"; then
    # The realm JSON is embedded, so quoting is escaped; fall back to a plain
    # substring check that still asserts the VALUE, not the key.
    if ! grep -q "${leg}" <<<"$realm" || ! grep -q "${backchannel}" <<<"$realm"; then
      echo "FAIL: catalyst-pin ${leg} does not point at ${backchannel} — the policy this chart ships permits a destination the realm no longer dials (#6106)." >&2
      exit 1
    fi
  fi
done
echo "[bp-keycloak/6106] Case 2: PASS (tokenUrl/userInfoUrl/jwksUrl -> ${backchannel})"

# ── Case 3: CONTROL — the frontchannel must NOT have moved ────────────────
# authorizationUrl and issuer share the suspect property — they are
# catalyst-pin IdP URLs in the same realm block — and they must stay PUBLIC.
# authorizationUrl is a BROWSER redirect (an internal URL is unresolvable from
# the User's machine) and issuer is compared against the `iss` claim
# catalyst-api mints from its own SOVEREIGN_FQDN (an internal value would
# trade a connectivity failure for a validation failure). If the #6106 diff
# had loosened rather than added, this control is what catches it.
echo "[bp-keycloak/6106] Case 3: CONTROL — authorizationUrl + issuer stay PUBLIC"
if ! grep -q "\"authorizationUrl\"" <<<"$realm"; then
  echo "FAIL: authorizationUrl absent from the realm — the control has no subject." >&2
  exit 1
fi
auth_line="$(grep '"authorizationUrl"' <<<"$realm" | head -1)"
if ! grep -q "https://api.${fqdn}/oidc/auth" <<<"$auth_line"; then
  echo "FAIL: authorizationUrl is no longer the public https://api.${fqdn}/oidc/auth. It is a BROWSER redirect; an in-cluster URL is unresolvable from the User's machine. Got: ${auth_line}" >&2
  exit 1
fi
if grep -q "catalyst-api.catalyst-system" <<<"$auth_line"; then
  echo "FAIL: authorizationUrl was moved to the in-cluster backchannel host — the frontchannel must stay public (#6087)." >&2
  exit 1
fi
issuer_line="$(grep '"issuer"' <<<"$realm" | head -1)"
if ! grep -q "https://api.${fqdn}" <<<"$issuer_line"; then
  echo "FAIL: catalyst-pin issuer is no longer public https://api.${fqdn}. It is compared against the iss claim catalyst-api mints from SOVEREIGN_FQDN; pointing it inward trades a connectivity failure for a validation failure. Got: ${issuer_line}" >&2
  exit 1
fi
echo "[bp-keycloak/6106] Case 3: PASS (frontchannel + issuer unchanged and public)"

# ── Case 4: FORM — ClusterMesh-safe endpoint selectors, never CIDR ────────
echo "[bp-keycloak/6106] Case 4: toEndpoints + io.cilium.k8s.policy.cluster Exists, never CIDR"
if ! grep -q 'toEndpoints:' <<<"$egress_block"; then
  echo "FAIL: the backchannel allow does not use toEndpoints (#6106)." >&2
  exit 1
fi
if ! grep -q 'key: io.cilium.k8s.policy.cluster' <<<"$egress_block"; then
  echo "FAIL: io.cilium.k8s.policy.cluster is not named. Cilium AND-injects cluster=<local> into a bare toEndpoints, so a region-B Keycloak Pod's dial to the ClusterMesh-resolved region-A catalyst-api is silently denied — the policy renders clean and half the Sovereign's brokered logins stay dark (#6106)." >&2
  exit 1
fi
if ! grep -A1 'key: io.cilium.k8s.policy.cluster' <<<"$egress_block" | grep -q 'operator: Exists'; then
  echo "FAIL: io.cilium.k8s.policy.cluster is named but not with operator Exists — the suppression of the AND-injection is the whole point (#6106)." >&2
  exit 1
fi
for bad in toCIDR toCIDRSet ipBlock; do
  if grep -q "${bad}" <<<"$egress_block"; then
    echo "FAIL: the backchannel allow uses ${bad} — a CIDR match NEVER resolves a ClusterMesh remote pod identity (#6106)." >&2
    exit 1
  fi
done
for wild in world cluster all; do
  if grep -qE "^\s*-\s*${wild}\s*$" <<<"$egress_block"; then
    echo "FAIL: the backchannel allow includes the '${wild}' entity — that is a wildcard, not a carve-out (#6106)." >&2
    exit 1
  fi
done
echo "[bp-keycloak/6106] Case 4: PASS (endpoint-identity selectors only)"

# ── Case 5: VACUITY — the empty escape hatch removes BOTH halves ──────────
echo "[bp-keycloak/6106] Case 5: VACUITY — catalystAPIInternalURL='' removes the policy and restores the public backchannel"
off="$(render --set catalystAPIInternalURL= --show-only "$tpl" 2>/dev/null || true)"
if grep -q "${cnp_name}" <<<"$off"; then
  echo "FAIL: the CNP still renders with catalystAPIInternalURL='' — the conditional is dead and Case 1 proves nothing." >&2
  exit 1
fi
realm_off="$(render --set catalystAPIInternalURL= --show-only templates/configmap-sovereign-realm.yaml || true)"
if [ -z "$realm_off" ]; then
  echo "FAIL: the realm ConfigMap did not render with catalystAPIInternalURL='' — cannot verify the escape hatch." >&2
  exit 1
fi
if grep -q "catalyst-api.catalyst-system.svc" <<<"$realm_off"; then
  echo "FAIL: with catalystAPIInternalURL='' the realm still carries the in-cluster backchannel — the chart would ship a dial with no policy behind it." >&2
  exit 1
fi
echo "[bp-keycloak/6106] Case 5: PASS (both halves follow the same value)"

# ── Case 6: VACUITY — the Capabilities gate is real ───────────────────────
echo "[bp-keycloak/6106] Case 6: VACUITY — no cilium.io/v2 capability means no CNP"
nocaps="$("$helm" template keycloak "$chart_dir" --namespace keycloak --set sovereignFQDN="$fqdn" --show-only "$tpl" 2>/dev/null || true)"
if grep -q "${cnp_name}" <<<"$nocaps"; then
  echo "FAIL: the CNP renders without the cilium.io/v2 API version — the Capabilities gate is decorative and the chart would fail to install on a CRD-less cluster." >&2
  exit 1
fi
echo "[bp-keycloak/6106] Case 6: PASS (Capabilities gate genuinely gates)"

# ── Case 7: DERIVATION — the policy follows the URL, it does not restate it ─
echo "[bp-keycloak/6106] Case 7: DERIVATION — a different backchannel URL moves the policy with it"
alt="$(render --set catalystAPIInternalURL=http://cat-api.other-ns.svc.cluster.local:9090 --show-only "$tpl" || true)"
if [ -z "$alt" ]; then
  echo "FAIL: the CNP did not render for an alternate in-cluster backchannel URL." >&2
  exit 1
fi
alt_egress="$(awk '/^  egress:/,0' <<<"$alt" | strip_comments)"
if ! grep -q 'k8s:io.kubernetes.pod.namespace: "other-ns"' <<<"$alt_egress"; then
  echo "FAIL: the destination namespace did not follow catalystAPIInternalURL — it is hardcoded, so Case 1's catalyst-system assertion is a tautology." >&2
  exit 1
fi
if ! grep -q 'k8s:app.kubernetes.io/name: "cat-api"' <<<"$alt_egress"; then
  echo "FAIL: the destination Pod label did not follow catalystAPIInternalURL — it is hardcoded." >&2
  exit 1
fi
if ! grep -q 'port: "9090"' <<<"$alt_egress"; then
  echo "FAIL: the destination port did not follow catalystAPIInternalURL — it is hardcoded, so Case 1's 8080 assertion is a tautology." >&2
  exit 1
fi
echo "[bp-keycloak/6106] Case 7: PASS (namespace + Pod label + port all derived)"

echo "[bp-keycloak/6106] All gates green."
