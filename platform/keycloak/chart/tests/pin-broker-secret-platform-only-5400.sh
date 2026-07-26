#!/usr/bin/env bash
# pin-broker-secret-platform-only-5400.sh — #5400 SECURITY guard.
#
# The `catalyst-pin-broker-credentials` Secret carries reflector
# auto-reflection annotations targeting the single shared
# `catalyst-system/catalyst-pin-broker-credentials`. bp-keycloak installs
# per-Organization as well as at platform tier, and `sovereignRealm.enabled`
# defaults TRUE — so before this guard a tenant release rendered the Secret in
# its own namespace and auto-reflected it OVER the platform's copy. Last
# writer wins, and `helm.sh/resource-policy: keep` made every Org's copy
# durable.
#
# Live hw290 proof (sha256[:12] of client-secret): the authoritative `keycloak`
# copy was 58a6a6c54300, while `catalyst-system` — what catalyst-api actually
# reads — held 24b855cea675, byte-identical to `gamma-corp` and annotated
# `reflects: gamma-corp/catalyst-pin-broker-credentials`. A tenant namespace
# controlled a platform credential, and catalyst-api presented it to a Keycloak
# holding a different one, so every app federating through the pin broker got
# 401 invalid_client.
#
# Because it is last-writer-wins it presents as FLAKE — green on one env, red
# on the next, purely by Org-creation order. That is why it survived: it looked
# like an intermittent SSO problem rather than a template defect.
#
# The sibling `configmap-sovereign-realm.yaml` already had the correct guard;
# this template simply never got it. This test pins the alignment.
#
# Usage:  platform/keycloak/chart/tests/pin-broker-secret-platform-only-5400.sh
# Exit:   0 = correct, 1 = the tenant guard regressed.

set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
SECRET_NAME="catalyst-pin-broker-credentials"
EXIT=0

fail() { echo "FAIL: $1" >&2; EXIT=1; }

# Tenant mode has REQUIRED values (Inviolable #4 — the chart fails the render
# rather than hardcoding). Every one of them must be supplied or `helm template`
# errors out, and an errored render contains no Secret — which would make this
# whole test pass vacuously against a VULNERABLE template. That is exactly the
# trap the first draft of this test fell into: it reported OK while the guard
# was reverted. render_tenant_or_die below refuses to let that happen again.
render_tenant() {
  helm template kc "${CHART}" \
    --set sovereignRealm.enabled=true \
    --set realmConfig.tenant.enabled=true \
    --set realmConfig.tenant.realmName=org-acme \
    --set realmConfig.tenant.orgSlug=acme \
    --set realmConfig.tenant.parentDomain=omani.homes \
    --set realmConfig.tenant.subdomain=acme 2>&1
}

# A tenant render must SUCCEED and be substantial. If it errored, or came back
# suspiciously small, we cannot conclude anything from "the Secret is absent" —
# so fail loudly instead of reporting a green we did not earn.
render_tenant_or_die() {
  local out; out="$(render_tenant)"
  if printf '%s\n' "${out}" | grep -qE '^Error'; then
    echo "FAIL: tenant render ERRORED — this test cannot evaluate the guard, and an errored render would pass it vacuously. Fix the values below (a new required realmConfig.tenant.* was likely added):" >&2
    printf '%s\n' "${out}" | grep -E '^Error' >&2
    exit 1
  fi
  if [ "${#out}" -lt 5000 ]; then
    echo "FAIL: tenant render is only ${#out} bytes — too small to be a real render; refusing to draw a conclusion from it." >&2
    exit 1
  fi
  printf '%s' "${out}"
}

render_sovereign() {
  helm template kc "${CHART}" --set sovereignRealm.enabled=true 2>/dev/null || true
}

echo "== #5400: platform (Sovereign-mode) render MUST contain the pin-broker Secret =="
sov="$(render_sovereign)"
if [ -z "${sov}" ]; then
  echo "WARN: chart did not render offline — skipping (cannot evaluate)."
  exit 0
fi
sov_hits="$(printf '%s\n' "${sov}" | grep -c "name: ${SECRET_NAME}" || true)"
if [ "${sov_hits}" -lt 1 ]; then
  fail "Sovereign-mode render has NO ${SECRET_NAME} — the platform lost its broker credential. The guard is too strict."
else
  echo "OK — Sovereign mode renders it (${sov_hits})."
fi

echo ""
echo "== #5400: tenant (per-Org) render MUST NOT contain it =="
ten="$(render_tenant_or_die)"
ten_hits="$(printf '%s\n' "${ten}" | grep -c "name: ${SECRET_NAME}" || true)"
if [ "${ten_hits}" -ne 0 ]; then
  fail "TENANT MODE RENDERS ${SECRET_NAME} (${ten_hits}). A per-Org release would auto-reflect its own secret over catalyst-system and take control of the platform's OIDC broker credential (#5400)."
else
  echo "OK — tenant mode renders zero."
fi

echo ""
echo "== #5400: no reflector auto-reflection of ANY object in tenant mode =="
# Anything a per-Org release auto-reflects into a shared namespace is the same
# class of defect, so assert the whole annotation is absent, not just this one
# Secret. Catches a future template copying the annotation block without a guard.
refl="$(printf '%s\n' "${ten}" | grep -n 'reflection-auto-namespaces' || true)"
if [ -n "${refl}" ]; then
  fail "tenant-mode render carries reflector auto-reflection annotations — a per-Org release must never auto-reflect into a shared namespace (#5400):"
  printf '%s\n' "${refl}" >&2
else
  echo "OK — tenant mode auto-reflects nothing."
fi

echo ""
if [ "${EXIT}" -ne 0 ]; then
  echo "───────────────────────────────────────────────────────────────" >&2
  echo "#5400: the pin-broker credential is PLATFORM-TIER. Gate it on" >&2
  echo "  {{- if and .Values.sovereignRealm.enabled (not .Values.realmConfig.tenant.enabled) -}}" >&2
  echo "matching the sibling configmap-sovereign-realm.yaml guard." >&2
  echo "───────────────────────────────────────────────────────────────" >&2
  exit 1
fi
echo "OK: pin-broker credential is platform-only (#5400)."
exit 0
