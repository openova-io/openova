#!/usr/bin/env bash
# bp-catalyst-platform — catalyst-system may reach the Keycloak of every region (SEC-061).
#
# Service keycloak/keycloak is a ClusterMesh global Service with local affinity
# (platform/keycloak/chart/values.yaml, keycloak.service): while a region's own
# Keycloak Pod has no ready endpoint, for example while kured drains its node,
# the Service sends the region's callers to the other region's Keycloak.
# catalyst-api is one: the PIN sign-in (pin/verify, EnsureUser) dials
# keycloak.keycloak.svc. The policy baseline-default-deny admits the keycloak
# namespace through its rule 4, which matches the local cluster only (Cilium
# selects local endpoints unless a selector names io.cilium.k8s.policy.cluster),
# so the call to the other region was dropped and the sign-in answered 502.
#
# Cases:
#   1. the policy has an egress rule to the Pods of namespace keycloak named
#      keycloak whose selector names the cluster key with operator Exists
#   2. that rule opens the Keycloak HTTP port 8080 over TCP and nothing else
#
# On main this fails at case 1. yq expressions use plain index access only
# (chart tests run under yq v4.44.3, .github/workflows/chart-tests-pr.yaml
# lines 5-34); the render is one document.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm dependency build --skip-refresh "$chart_dir" > /dev/null

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

helm template smoke "$chart_dir" --show-only templates/network-policies/baseline-catalyst-system.yaml > "$tmp/cnp.yaml"
[ "$(yq '.metadata.name' "$tmp/cnp.yaml")" = "baseline-default-deny" ] || fail "the template renders no policy baseline-default-deny"

rule='.spec.egress[] | select(.toEndpoints[0].matchLabels["k8s:io.kubernetes.pod.namespace"] == "keycloak" and .toEndpoints[0].matchLabels["k8s:app.kubernetes.io/name"] == "keycloak")'

echo "[keycloak-every-region] Case 1: an egress rule names the Keycloak Pods of every cluster"
[ "$(yq "[$rule] | length" "$tmp/cnp.yaml")" = "1" ] || fail "baseline-default-deny has $(yq "[$rule] | length" "$tmp/cnp.yaml") egress rules to the Keycloak Pods, want 1"
[ "$(yq "$rule | .toEndpoints[0].matchExpressions[0].key" "$tmp/cnp.yaml")" = "io.cilium.k8s.policy.cluster" ] || fail "the rule to the Keycloak Pods does not name the cluster key, so it admits this region's Keycloak only"
[ "$(yq "$rule | .toEndpoints[0].matchExpressions[0].operator" "$tmp/cnp.yaml")" = "Exists" ] || fail "the cluster key of the rule to the Keycloak Pods is not matched with Exists"

echo "[keycloak-every-region] Case 2: it opens port 8080 over TCP only"
[ "$(yq "$rule | .toPorts | length" "$tmp/cnp.yaml")" = "1" ] || fail "the rule to the Keycloak Pods has not exactly one toPorts entry"
[ "$(yq "$rule | .toPorts[0].ports | length" "$tmp/cnp.yaml")" = "1" ] || fail "the rule to the Keycloak Pods opens more than one port"
[ "$(yq "$rule | .toPorts[0].ports[0].port" "$tmp/cnp.yaml")" = "8080" ] || fail "the rule to the Keycloak Pods does not open port 8080"
[ "$(yq "$rule | .toPorts[0].ports[0].protocol" "$tmp/cnp.yaml")" = "TCP" ] || fail "the rule to the Keycloak Pods does not open TCP"

echo "PASS: catalyst-system may reach the Keycloak Pods of every region on port 8080"
