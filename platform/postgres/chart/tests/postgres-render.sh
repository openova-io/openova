#!/usr/bin/env bash
# bp-postgres render gate (ADR-0010, #3188; chart 0.1.0).
#
# Asserts the chart's load-bearing rules:
#   1. Default render WITHOUT the CNPG CRD registered → ZERO resources
#      (the Capabilities gate skips the Cluster + Database CRs on a cold
#      install before bp-cnpg reconciles).
#   2. Shared render (two bindings, CRD present) → ONE Cluster + TWO
#      Database CRs + TWO reflected Secrets, with TWO managed.roles on the
#      single Cluster. This is the reuse proof: 2 consumers, 1 engine.
#   3. The Database CRs each carry the binding owner + the shared cluster
#      name, proving logical isolation on one engine.
#   3b. (#3283 deadlock fix) Every reflected connection Secret lands in the
#      RELEASE namespace (shared-data) — NONE targets gitea/harbor directly
#      (those namespaces don't exist when bp-postgres-shared installs). Each
#      hub Secret carries reflection-auto-namespaces naming the consumer ns
#      so bp-reflector push-copies it in once the namespace appears.
#   4. active-hot-standby + sync → the Cluster carries
#      synchronous_commit + synchronous_standby_names (ADR-0004 Pillar-3).
#   5. singleton → the Cluster carries NEITHER sync GUC.
#
# Usage: bash tests/postgres-render.sh [CHART_DIR]
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

fail() { echo "FAIL: $1" >&2; exit 1; }

# ── Case 1: cold install (no CRD) → zero resources ───────────────
echo "[render] Case 1: no CNPG CRD → ZERO resources (Capabilities gate)"
helm template smoke . > "$TMP/cold.yaml" 2> "$TMP/cold.err" || {
  cat "$TMP/cold.err" >&2; fail "cold render errored"; }
if grep -qE '^(kind: Cluster|kind: Database)$' "$TMP/cold.yaml"; then
  fail "cold render emitted a Cluster/Database without the CRD registered"
fi

# ── Case 2: shared render — 1 Cluster, 2 Databases, 2 Secrets ────
echo "[render] Case 2: shared render → 1 Cluster + 2 Databases + 2 Secrets + 2 roles"
cat > "$TMP/shared.values.yaml" <<'YAML'
instance:
  name: shared-pg
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: registry
    owner: harbor
    consumer: { blueprint: bp-harbor, mode: shared }
    reflect: { secretName: harbor-database-secret, namespaces: [harbor] }
  - name: gitea
    owner: gitea
    consumer: { blueprint: bp-gitea, mode: shared }
    reflect: { secretName: gitea-database-secret, namespaces: [gitea] }
YAML
helm template shared-pg . -f "$TMP/shared.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared.yaml" 2> "$TMP/shared.err" || {
  cat "$TMP/shared.err" >&2; fail "shared render errored"; }

cluster_count=$(grep -cE '^kind: Cluster$' "$TMP/shared.yaml" || true)
db_count=$(grep -cE '^kind: Database$' "$TMP/shared.yaml" || true)
secret_count=$(grep -cE '^kind: Secret$' "$TMP/shared.yaml" || true)
role_count=$(grep -cE '^      - name: "(harbor|gitea)"$' "$TMP/shared.yaml" || true)

[ "$cluster_count" -eq 1 ] || fail "expected 1 Cluster, got $cluster_count"
[ "$db_count" -eq 2 ]      || fail "expected 2 Databases, got $db_count"
# 0.1.3 (#3285): 2 hub connection Secrets + 2 chart-minted role-password
# Secrets (CNPG never creates managed-role secrets — proven live hw130).
[ "$secret_count" -eq 4 ]  || fail "expected 4 Secrets (2 hub + 2 role-password), got $secret_count"
basic_auth_count=$(grep -cE '^type: kubernetes.io/basic-auth$' "$TMP/shared.yaml" || true)
[ "$basic_auth_count" -eq 2 ] || fail "expected 2 basic-auth role Secrets, got $basic_auth_count"
[ "$role_count" -eq 2 ]    || fail "expected 2 managed roles, got $role_count"

# ── Case 3: both Databases reference the SAME shared cluster ──────
echo "[render] Case 3: both Database CRs reference the shared cluster"
shared_refs=$(grep -cE '^    name: shared-pg$' "$TMP/shared.yaml" || true)
# 1 in cluster.metadata path is name: shared-pg at root indent; the
# Database spec.cluster.name lives at 4-space indent. Count those.
[ "$shared_refs" -ge 2 ] || fail "expected both Database CRs to point at shared-pg (got $shared_refs)"
grep -q 'owner: "harbor"' "$TMP/shared.yaml" || fail "harbor owner missing"
grep -q 'owner: "gitea"'  "$TMP/shared.yaml" || fail "gitea owner missing"

# ── Case 3b: #3283 — connection Secrets land in shared-data, NOT the ──
# consumer namespaces (which don't exist when bp-postgres-shared installs).
# This is the deadlock regression-lock: the SOURCE-side reflector pattern.
echo "[render] Case 3b: #3283 — every reflected Secret in shared-data (push-source), none in gitea/harbor"
# Extract the namespace of every Secret. The shared.values namespace is
# shared-data; assert all reflected connection Secrets carry it.
# 0.1.4: namespaces render quoted; 4 Secrets live in shared-data (2 role-password + 2 hub).
secret_ns_in_shared=$(awk '/^kind: Secret$/{s=1} s&&/^  namespace: "?shared-data"?$/{c++} /^---$/{s=0} END{print c+0}' "$TMP/shared.yaml")
[ "$secret_ns_in_shared" -eq 4 ] || fail "#3283: expected 4 Secrets in shared-data (2 role + 2 hub), got $secret_ns_in_shared"
# NO Secret may target the consumer namespaces directly — that is the bug.
if grep -E '^  namespace: "?(gitea|harbor)"?$' "$TMP/shared.yaml" | grep -q .; then
  fail "#3283 REGRESSION: a Secret targets a consumer namespace (gitea/harbor) directly — re-introduces the deadlock"
fi
# Each hub Secret must carry the push-source annotations naming its
# consumer namespace so reflector auto-copies it once the ns appears.
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed: "true"' "$TMP/shared.yaml" \
  || fail "#3283: hub Secret missing reflection-allowed"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"' "$TMP/shared.yaml" \
  || fail "#3283: hub Secret missing reflection-auto-enabled"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "harbor"' "$TMP/shared.yaml" \
  || fail "#3283: harbor hub Secret missing reflection-auto-namespaces: harbor"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "gitea"' "$TMP/shared.yaml" \
  || fail "#3283: gitea hub Secret missing reflection-auto-namespaces: gitea"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "harbor"' "$TMP/shared.yaml" \
  || fail "#3283: harbor hub Secret missing reflection-allowed-namespaces: harbor"
grep -q 'reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "gitea"' "$TMP/shared.yaml" \
  || fail "#3283: gitea hub Secret missing reflection-allowed-namespaces: gitea"
# And each still PULLS from the CNPG role Secret in shared-data (hop 1).
# 0.1.4 (#3285): the `reflects:` PULL is gone (two reflector limitations
# killed it live on hw130 — no retry for late sources; mirrors can't be
# push sources). The hub now renders its data DIRECTLY; assert the
# password is present and matches the role Secret's contract instead.
grep -q 'reflector.v1.k8s.emberstack.com/reflects:' "$TMP/shared.yaml" \
  && fail "#3285 REGRESSION: a reflects: pull annotation re-appeared — that pattern is dead (see role-secrets.yaml header)"
hub_pw_count=$(grep -cE '^  password: ' "$TMP/shared.yaml" || true)
[ "$hub_pw_count" -ge 4 ] || fail "#3285: expected password rendered in 2 role + 2 hub Secrets, got $hub_pw_count"
hub_host_count=$(grep -cE '^  host: "shared-pg-rw.shared-data.svc.cluster.local"$' "$TMP/shared.yaml" || true)
[ "$hub_host_count" -eq 2 ] || fail "#3285: expected 2 hub Secrets with the shared-pg-rw host, got $hub_host_count"

# ── Case 4: active-hot-standby + sync → sync GUCs present ─────────
echo "[render] Case 4: active-hot-standby + sync → synchronous-replication GUCs"
cat > "$TMP/ahs.values.yaml" <<'YAML'
instance: { name: harbor-pg }
topology:
  mode: active-hot-standby
  instances: 2
  replication: { mode: sync, sync: { commit: remote_apply, numSync: 1 } }
databases:
  - { name: registry, owner: harbor }
YAML
helm template harbor-pg . -f "$TMP/ahs.values.yaml" \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/ahs.yaml" 2>&1 || fail "ahs render errored"
grep -q 'synchronous_commit: "remote_apply"' "$TMP/ahs.yaml" \
  || fail "active-hot-standby missing synchronous_commit"
grep -q 'synchronous_standby_names: "FIRST 1 (harbor-pg-r)"' "$TMP/ahs.yaml" \
  || fail "active-hot-standby missing synchronous_standby_names"

# ── Case 5: singleton → NO sync GUCs ─────────────────────────────
echo "[render] Case 5: singleton → no synchronous-replication GUCs"
if grep -q 'synchronous_commit' "$TMP/shared.yaml"; then
  fail "singleton render leaked synchronous_commit"
fi

# ── Case 6: master gate OFF → ZERO resources (#3188 safe-by-default) ──
# `enabled=false` must render an EMPTY release even with the CNPG CRD
# present and bindings declared — so the bootstrap-kit slot 16a HR is a
# Ready (but empty) release that satisfies the bp-gitea / bp-harbor
# dependsOn WITHOUT deploying an unused shared-pg Cluster. This is the
# regression-lock for the #3191 / hw124 wedge fix.
echo "[render] Case 6: enabled=false → ZERO resources (master gate, even with CRD + bindings)"
helm template shared-pg . -f "$TMP/shared.values.yaml" --set enabled=false \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/off.yaml" 2> "$TMP/off.err" || {
  cat "$TMP/off.err" >&2; fail "gated-off render errored"; }
if grep -qE '^kind:' "$TMP/off.yaml"; then
  fail "enabled=false render emitted resources (expected an empty release)"
fi

# ── Case 7: enabled=true explicit → identical to the default render ──
# Guards against a `toString` regression where the gate misfires on the
# truthy path (Sprig default-bool trap, memory feedback_sprig_default_
# bool_unsafe).
echo "[render] Case 7: enabled=true explicit → full reuse-proof render (1 Cluster + 2 Databases + 2 Secrets)"
helm template shared-pg . -f "$TMP/shared.values.yaml" --set enabled=true \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/on.yaml" 2> "$TMP/on.err" || {
  cat "$TMP/on.err" >&2; fail "gated-on render errored"; }
on_cluster=$(grep -cE '^kind: Cluster$' "$TMP/on.yaml" || true)
on_db=$(grep -cE '^kind: Database$' "$TMP/on.yaml" || true)
on_secret=$(grep -cE '^kind: Secret$' "$TMP/on.yaml" || true)
[ "$on_cluster" -eq 1 ] || fail "enabled=true expected 1 Cluster, got $on_cluster"
[ "$on_db" -eq 2 ]      || fail "enabled=true expected 2 Databases, got $on_db"
[ "$on_secret" -eq 4 ]  || fail "enabled=true expected 4 Secrets (2 hub + 2 role), got $on_secret"

# ── Case 8: instance-B shape (#3188 three-instance model) ────────────
# grafana binding with extraData (static GF_DATABASE_* env keys) +
# passwordAliases (GF_DATABASE_PASSWORD) + ready-to-adopt pdns + pda
# bindings. Locks: the env-style hub contract, the `uri` key
# (powerdns-admin #3189 A3 adoption contract), 3 Databases on ONE
# shared-pg-b Cluster.
echo "[render] Case 8: instance-B shape → grafana env-style hub + uri key + 3 Databases on shared-pg-b"
cat > "$TMP/shared-b.values.yaml" <<'YAML'
instance:
  name: shared-pg-b
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: grafana
    owner: grafana
    consumer: { blueprint: bp-grafana, mode: shared }
    reflect:
      secretName: grafana-database-env
      namespaces: [grafana]
      extraData:
        GF_DATABASE_TYPE: postgres
        GF_DATABASE_HOST: shared-pg-b-rw.shared-data.svc.cluster.local:5432
        GF_DATABASE_NAME: grafana
        GF_DATABASE_USER: grafana
        GF_DATABASE_SSL_MODE: disable
      passwordAliases: [GF_DATABASE_PASSWORD]
  - name: pdns
    owner: pdns
    consumer: { blueprint: bp-powerdns, mode: shared }
    reflect: { secretName: pdns-database-secret, namespaces: [powerdns] }
  - name: pda
    owner: pda
    consumer: { blueprint: bp-powerdns-admin, mode: shared }
    reflect: { secretName: pda-shared-database-secret, namespaces: [powerdns-admin] }
YAML
helm template shared-pg-b . -f "$TMP/shared-b.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared-b.yaml" 2> "$TMP/shared-b.err" || {
  cat "$TMP/shared-b.err" >&2; fail "instance-B render errored"; }
b_cluster=$(grep -cE '^kind: Cluster$' "$TMP/shared-b.yaml" || true)
b_db=$(grep -cE '^kind: Database$' "$TMP/shared-b.yaml" || true)
b_secret=$(grep -cE '^kind: Secret$' "$TMP/shared-b.yaml" || true)
[ "$b_cluster" -eq 1 ] || fail "instance-B expected 1 Cluster, got $b_cluster"
[ "$b_db" -eq 3 ]      || fail "instance-B expected 3 Databases, got $b_db"
[ "$b_secret" -eq 6 ]  || fail "instance-B expected 6 Secrets (3 role + 3 hub), got $b_secret"
grep -q 'GF_DATABASE_TYPE: "postgres"' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_TYPE extraData"
grep -q 'GF_DATABASE_HOST: "shared-pg-b-rw.shared-data.svc.cluster.local:5432"' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_HOST extraData"
grep -qE '^  GF_DATABASE_PASSWORD: ' "$TMP/shared-b.yaml" \
  || fail "instance-B grafana hub missing GF_DATABASE_PASSWORD alias"
# The alias must carry the SAME password as the grafana role Secret.
grafana_role_pw=$(awk '/name: "shared-pg-b-grafana"/{s=1} s&&/^  password: /{print $2; exit}' "$TMP/shared-b.yaml")
grafana_alias_pw=$(awk '/^  GF_DATABASE_PASSWORD: /{print $2; exit}' "$TMP/shared-b.yaml")
[ -n "$grafana_role_pw" ] && [ "$grafana_role_pw" = "$grafana_alias_pw" ] \
  || fail "instance-B GF_DATABASE_PASSWORD alias drifted from the grafana role password"
uri_count=$(grep -cE '^  uri: "postgresql://' "$TMP/shared-b.yaml" || true)
[ "$uri_count" -eq 3 ] || fail "instance-B expected 3 hub uri keys (#3189 A3 contract), got $uri_count"
grep -q 'uri: "postgresql://pda:' "$TMP/shared-b.yaml" \
  || fail "instance-B pda hub uri missing/owner-mismatched"

# ── Case 9: instance-C shape — same-owner bindings (SME mesh) ─────────
# THREE databases (sme_auth, sme_billing, sme_documents) owned by ONE
# role `sme`. Locks the 0.1.5 dedupe (1 managed role, 1 role Secret)
# and the underscore sanitize on Database CR k8s names.
echo "[render] Case 9: instance-C shape → 3 same-owner Databases, 1 role, sanitized CR names"
cat > "$TMP/shared-c.values.yaml" <<'YAML'
instance:
  name: shared-pg-c
  namespace: shared-data
topology:
  mode: singleton
  instances: 1
databases:
  - name: sme_auth
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
    reflect: { secretName: sme-database-secret, namespaces: [sme] }
  - name: sme_billing
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
  - name: sme_documents
    owner: sme
    consumer: { blueprint: bp-catalyst-platform, mode: shared }
YAML
helm template shared-pg-c . -f "$TMP/shared-c.values.yaml" --namespace shared-data \
  --api-versions postgresql.cnpg.io/v1 > "$TMP/shared-c.yaml" 2> "$TMP/shared-c.err" || {
  cat "$TMP/shared-c.err" >&2; fail "instance-C render errored"; }
c_db=$(grep -cE '^kind: Database$' "$TMP/shared-c.yaml" || true)
c_role=$(grep -cE '^      - name: "sme"$' "$TMP/shared-c.yaml" || true)
c_basic=$(grep -cE '^type: kubernetes.io/basic-auth$' "$TMP/shared-c.yaml" || true)
c_secret=$(grep -cE '^kind: Secret$' "$TMP/shared-c.yaml" || true)
[ "$c_db" -eq 3 ]    || fail "instance-C expected 3 Databases, got $c_db"
[ "$c_role" -eq 1 ]  || fail "instance-C expected EXACTLY 1 managed role sme (dedupe), got $c_role"
[ "$c_basic" -eq 1 ] || fail "instance-C expected EXACTLY 1 role-password Secret (dedupe), got $c_basic"
[ "$c_secret" -eq 2 ] || fail "instance-C expected 2 Secrets (1 role + 1 hub), got $c_secret"
# Sanitized k8s names; verbatim Postgres names.
grep -q 'name: shared-pg-c-sme-auth' "$TMP/shared-c.yaml" \
  || fail "instance-C Database CR name not underscore-sanitized"
if grep -qE '^  name: shared-pg-c-sme_' "$TMP/shared-c.yaml"; then
  fail "instance-C Database CR k8s name leaked an underscore"
fi
grep -q 'name: "sme_auth"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_auth missing"
grep -q 'name: "sme_billing"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_billing missing"
grep -q 'name: "sme_documents"' "$TMP/shared-c.yaml" || fail "instance-C spec.name sme_documents missing"

echo "[render] PASS — bp-postgres render gate green (reuse proof: 1 Cluster, 2 Databases, 2 roles, 2 Secrets; master gate OFF → empty; 3-instance shapes locked)"
