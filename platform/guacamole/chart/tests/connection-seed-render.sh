#!/usr/bin/env bash
# connection-seed-render — the chart must SHIP a connection producer, and it
# must produce nothing when nothing is declared.
#
# WHY THIS FILE EXISTS (#5991 / UAT row 115). `guacamole_connection` is created
# empty by the vendored Apache schema and, until 0.2.38, no writer existed
# anywhere in the repository — not a CRD, not a controller, not a chart hook,
# not the catalyst-api `GuacamoleClient` (interface only, no production
# implementation). A signed-in sovereign-admin reached Guacamole successfully
# and landed on an empty ALL CONNECTIONS list. The four sibling test scripts
# all passed throughout, because none of them asserted anything about a
# connection: deleting seed_connections() left the suite green.
#
# The load-bearing pair here is:
#   * the producer renders a real target, and
#   * the CONTROL — an empty declared set renders ZERO connection writes.
# Without the control, this file would also pass for a producer that invents a
# row, which on a table that holds credentials is worse than the empty list
# row 115 is about.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
fails=0
pass() { printf '  ok   — %s\n' "$1"; }
fail() { printf '  FAIL — %s\n' "$1"; fails=$((fails + 1)); }

cd "${CHART_DIR}"
if [[ ! -d charts ]] || [[ -z "$(ls -A charts 2>/dev/null)" ]]; then
  helm dependency update >/dev/null 2>&1 || {
    echo "[connection-seed] SKIPPED — helm dependency update failed (no network);"
    echo "[connection-seed] NOTHING WAS ASSERTED. Re-run after \`helm dep build\`."
    exit 0
  }
fi

# --api-versions is REQUIRED: the seed Job + enroll CronJob are gated on
# `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"`, and without it the
# render is silently empty and every assertion below "fails" for a reason that
# has nothing to do with the chart. The vacuity check at the end catches that.
render() { # render <outfile> [extra --set args…]
  local out="$1"; shift
  helm template bp-guacamole . \
    --api-versions postgresql.cnpg.io/v1 \
    --set guacamole.enabled=true \
    --set guacamole.httproute.hostname=guacamole.test \
    --set guacamole.oidc.issuer=https://kc.test/realms/c \
    "$@" >"${out}" 2>"${out}.err"
}

# scripts <render> <key> — the ConfigMap value, parsed as YAML so a comment
# elsewhere in the render can never satisfy an assertion.
scripts() {
  python3 - "$1" "$2" <<'PY'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
for d in docs:
    if d.get("kind") == "ConfigMap" and d.get("metadata", {}).get("name", "").endswith("-jdbc-seed-scripts"):
        sys.stdout.write((d.get("data") or {}).get(sys.argv[2], ""))
        break
PY
}

# body <script-file> <function> — just the named shell function's body, so
# "the producer emits no writes" cannot be satisfied by a call in a comment or
# in a different function.
body() {
  python3 - "$1" "$2" <<'PY'
import re, sys
text = open(sys.argv[1]).read()
m = re.search(r"^%s\(\) \{\n(.*?)^\}" % re.escape(sys.argv[2]), text, re.S | re.M)
sys.stdout.write(m.group(1) if m else "")
PY
}

echo "[connection-seed] 1/6 — default render ships a producer"
render "${TMP}/default.yaml" || { echo "helm template failed:"; cat "${TMP}/default.yaml.err"; exit 1; }
scripts "${TMP}/default.yaml" common.sh >"${TMP}/common.sh"
scripts "${TMP}/default.yaml" seed.sh >"${TMP}/seed.sh"
scripts "${TMP}/default.yaml" promote.sh >"${TMP}/promote.sh"
common="$(cat "${TMP}/common.sh")"
seed="$(cat "${TMP}/seed.sh")"
promote="$(cat "${TMP}/promote.sh")"

for fn in ensure_connection set_connection_parameter load_connection_credentials seed_connections; do
  if printf '%s' "${common}" | grep -q "^${fn}() {"; then
    pass "common.sh defines ${fn}()"
  else
    fail "common.sh does not define ${fn}() — the producer is missing"
  fi
done

# Both legs: the one-shot seed writes the set, the per-minute CronJob converges
# it. Losing either leg is the #5598 shape (one leg deleted, suite still green).
if printf '%s' "${seed}" | grep -qx 'seed_connections'; then
  pass "seed.sh calls seed_connections (install/upgrade leg)"
else
  fail "seed.sh never calls seed_connections"
fi
if printf '%s' "${promote}" | grep -qx 'seed_connections'; then
  pass "promote.sh calls seed_connections (per-minute converging leg)"
else
  fail "promote.sh never calls seed_connections — the set would never re-converge"
fi

echo "[connection-seed] 2/6 — the produced connection is a real, named target"
seed_body="$(body "${TMP}/common.sh" seed_connections)"
if printf '%s' "${seed_body}" | grep -q "ensure_connection 'sovereign-node' 'ssh'"; then
  pass "seed_connections() creates the sovereign-node ssh connection (asserted on the VALUES)"
else
  fail "seed_connections() has no sovereign-node/ssh target"
fi
# The hostname must come from the downward API at run time — a hardcoded
# address in a public chart would be both wrong and a leak.
if printf '%s' "${seed_body}" | grep -q 'set_connection_parameter .sovereign-node. .hostname. "\${NODE_IP}"'; then
  pass "hostname is \${NODE_IP} (downward API), not a baked address"
else
  fail "hostname is not sourced from \${NODE_IP}"
fi
if printf '%s' "${seed_body}" | grep -q "NODE_IP:-" && printf '%s' "${seed_body}" | grep -q 'NOT written'; then
  pass "an empty NODE_IP writes no connection (runtime half of the control)"
else
  fail "no runtime guard on an empty NODE_IP — the producer could write a hostname-less row"
fi

echo "[connection-seed] 3/6 — idempotent and permission-granting SQL"
if printf '%s' "${common}" | grep -A4 'INSERT INTO guacamole_connection (connection_name, protocol)' | grep -q 'WHERE NOT EXISTS'; then
  pass "the connection INSERT is guarded by NOT EXISTS (re-run adds no duplicate)"
else
  fail "the connection INSERT has no NOT EXISTS guard — UNIQUE(connection_name, parent_id) does NOT dedupe when parent_id IS NULL"
fi
if printf '%s' "${common}" | grep -A6 'INSERT INTO guacamole_connection_parameter' | grep -q 'ON CONFLICT'; then
  pass "parameter writes converge via ON CONFLICT DO UPDATE"
else
  fail "parameter writes are not idempotent"
fi
if printf '%s' "${common}" | grep -q "INSERT INTO guacamole_connection_permission"; then
  pass "the admin USER_GROUP is granted READ on the connection"
else
  fail "no connection_permission grant — a non-admin group member would not see the connection"
fi

echo "[connection-seed] 4/6 — CONTROL: an empty declared set writes nothing"
render "${TMP}/empty.yaml" \
  --set guacamole.database.connections.nodeShell.enabled=false \
  --set-json 'guacamole.database.connections.extra=[]' \
  || { echo "helm template failed:"; cat "${TMP}/empty.yaml.err"; exit 1; }
scripts "${TMP}/empty.yaml" common.sh >"${TMP}/empty-common.sh"
empty_common="$(cat "${TMP}/empty-common.sh")"
if [[ -z "${empty_common}" ]]; then
  fail "CONTROL rendered no seed ConfigMap at all — the control asserts nothing"
else
  empty_body="$(body "${TMP}/empty-common.sh" seed_connections)"
  n="$(printf '%s\n' "${empty_body}" | grep -c 'ensure_connection ' || true)"
  if [[ "${n}" == "0" ]]; then
    pass "no declared target ⇒ ZERO connection writes (not one fabricated row)"
  else
    fail "CONTROL: ${n} connection write(s) rendered from an empty declared set"
  fi
  if printf '%s' "${empty_common}" | grep -q '^seed_connections() {'; then
    pass "CONTROL is non-vacuous: seed_connections() still renders, it just writes nothing"
  else
    fail "CONTROL: seed_connections() absent, so the zero-writes result proves nothing"
  fi
fi

echo "[connection-seed] 5/6 — credentials cannot be declared in values"
if render "${TMP}/cred.yaml" \
     --set 'guacamole.database.connections.extra[0].name=legacy' \
     --set 'guacamole.database.connections.extra[0].protocol=rdp' \
     --set 'guacamole.database.connections.extra[0].parameters.hostname=10.0.0.9' \
     --set 'guacamole.database.connections.extra[0].parameters.password=hunter2'; then
  fail "a password in values rendered successfully — it would ship in a ConfigMap, in a PUBLIC repo"
else
  if grep -q 'credential parameter' "${TMP}/cred.yaml.err"; then
    pass "a credential parameter in values FAILS the render, naming the parameter"
  else
    fail "render failed but not with the credential guard message: $(head -2 "${TMP}/cred.yaml.err")"
  fi
fi
if render "${TMP}/quote.yaml" \
     --set 'guacamole.database.connections.nodeShell.name=bad'"'"'name'; then
  fail "a quote in a connection name rendered — it breaks out of the rendered shell word"
else
  pass "a quote in a connection name FAILS the render"
fi
# A declared target with nowhere to dial is a broken row, which is what the
# row-115 vacuity guard is aimed at.
if render "${TMP}/nohost.yaml" \
     --set 'guacamole.database.connections.extra[0].name=nowhere' \
     --set 'guacamole.database.connections.extra[0].protocol=ssh'; then
  fail "a connection with no hostname rendered — it would seed a row that cannot dial"
else
  pass "a declared target with no hostname FAILS the render"
fi
# …and the same entry WITH a hostname must render, so the guard above is not
# just rejecting every extra entry.
if render "${TMP}/withhost.yaml" \
     --set 'guacamole.database.connections.extra[0].name=nowhere' \
     --set 'guacamole.database.connections.extra[0].protocol=ssh' \
     --set 'guacamole.database.connections.extra[0].parameters.hostname=10.0.0.9'; then
  scripts "${TMP}/withhost.yaml" common.sh >"${TMP}/withhost-common.sh"
  if body "${TMP}/withhost-common.sh" seed_connections | grep -q "ensure_connection 'nowhere' 'ssh'"; then
    pass "an extra target WITH a hostname renders its own connection"
  else
    fail "the extra target did not render a connection write"
  fi
else
  fail "a valid extra target failed to render: $(head -2 "${TMP}/withhost.yaml.err")"
fi

echo "[connection-seed] 6/6 — credential Secret is mounted, optional, on both legs"
render "${TMP}/secret.yaml" \
  --set guacamole.database.connections.nodeShell.credentialSecretName=guac-node-key \
  || { echo "helm template failed:"; cat "${TMP}/secret.yaml.err"; exit 1; }
mounts="$(python3 - "${TMP}/secret.yaml" <<'PY'
import sys, yaml
out = []
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    kind = d.get("kind")
    if kind == "Job":
        spec = d["spec"]["template"]["spec"]
    elif kind == "CronJob":
        spec = d["spec"]["jobTemplate"]["spec"]["template"]["spec"]
    else:
        continue
    for v in spec.get("volumes", []):
        sec = v.get("secret") or {}
        if sec.get("secretName") == "guac-node-key":
            out.append(f"{kind}:volume:optional={sec.get('optional')}")
    for c in spec.get("containers", []):
        for m in c.get("volumeMounts", []):
            if m.get("mountPath") == "/conn-cred/sovereign-node":
                out.append(f"{kind}:mount:readOnly={m.get('readOnly')}")
        for e in c.get("env", []) or []:
            fp = ((e.get("valueFrom") or {}).get("fieldRef") or {}).get("fieldPath")
            if e.get("name") == "NODE_IP":
                out.append(f"{kind}:NODE_IP={fp}")
print("\n".join(out))
PY
)"
for want in "Job:volume:optional=True" "CronJob:volume:optional=True" \
            "Job:mount:readOnly=True" "CronJob:mount:readOnly=True" \
            "Job:NODE_IP=status.hostIP" "CronJob:NODE_IP=status.hostIP"; do
  if printf '%s\n' "${mounts}" | grep -qx -- "${want}"; then
    pass "${want}"
  else
    fail "missing: ${want} (got: $(printf '%s' "${mounts}" | tr '\n' ' '))"
  fi
done

# Vacuity check — every assertion above reads the seed ConfigMap, so prove the
# default render actually contained the seed Job it belongs to.
if grep -q 'catalyst.openova.io/component: jdbc-seed' "${TMP}/default.yaml"; then
  pass "vacuity: the default render did contain the jdbc-seed Job"
else
  fail "vacuity: no jdbc-seed Job in the default render — assertions above were empty-string comparisons"
fi

if [[ "${fails}" -gt 0 ]]; then
  echo "[connection-seed] ${fails} assertion(s) FAILED"
  exit 1
fi
echo "[connection-seed] all assertions passed"
