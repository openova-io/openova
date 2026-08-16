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
#
# #5991 mTLS leg: a SECOND target, `cluster-shell`, is asserted here.
# The distinction matters and the assertions below keep it visible.
# `sovereign-node` dials a key-only sshd whose key is deliberately not in
# the cluster, so absent an operator Secret it renders and then PROMPTS
# on click. `cluster-shell` speaks guacd's `kubernetes` protocol to
# bp-k8s-ws-proxy over mTLS, with a client certificate the platform mints
# for itself — so it is the one seeded connection that authenticates on
# click with no operator input. A producer that shipped only the first
# would satisfy "the list is non-empty" while the feature stayed broken.
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

# --api-versions is still passed here so this file exercises the same shape a
# converged cluster reports.
#
# IT IS NO LONGER REQUIRED, and that is the whole point of case 7 below. The
# seed Job used to be gated on `.Capabilities.APIVersions.Has
# "postgresql.cnpg.io/v1"` and THIS FILE ALWAYS SUPPLIED THAT CAPABILITY — so
# the suite could not observe the one condition under which the producer
# disappeared. That is a guard testing a surface that cannot fail: every
# assertion below passed while live hw298 had zero connections, because the
# render under test was never the render that shipped. The gate is gone
# (#5991); case 7 pins its absence.
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

if printf '%s' "${seed_body}" | grep -q "ensure_connection 'cluster-shell' 'kubernetes'"; then
  pass "seed_connections() creates the cluster-shell connection on guacd's kubernetes protocol"
else
  fail "seed_connections() has no cluster-shell/kubernetes target — nothing seeded can authenticate on click"
fi
# use-ssl is what makes guacd present a client certificate at all: with
# it false, guacamole-server 1.5.5 settings.c never even PARSES
# client-cert/client-key/ca-cert (they are read inside `if
# (settings->use_ssl)`), so the connection would dial plaintext and 401.
if printf '%s' "${seed_body}" | grep -q "set_connection_parameter 'cluster-shell' 'use-ssl' 'true'"; then
  pass "cluster-shell sets use-ssl=true (guacd parses the client-cert parameters only when it is on)"
else
  fail "cluster-shell does not set use-ssl=true — guacd would ignore the client certificate entirely"
fi
# guacd verifies the SERVER certificate against this exact string, so a
# short name here is a handshake failure.
if printf '%s' "${seed_body}" | grep -q "set_connection_parameter 'cluster-shell' 'hostname' 'k8s-ws-proxy.catalyst-system.svc.cluster.local'"; then
  pass "cluster-shell dials the proxy by its fully-qualified Service name"
else
  fail "cluster-shell hostname is not the fully-qualified proxy Service name"
fi
if printf '%s' "${seed_body}" | grep -q "set_connection_parameter 'cluster-shell' 'pod' 'k8s-ws-proxy'"; then
  pass "cluster-shell targets a WORKLOAD name (survives a pod rollout; a literal pod name would 404)"
else
  fail "cluster-shell has no pod target"
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
  --set guacamole.database.connections.clusterShell.enabled=false \
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

csmounts="$(python3 - "${TMP}/default.yaml" <<'PY'
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
        if sec.get("secretName") == "k8s-ws-proxy-client-tls":
            out.append("%s:volume:optional=%s" % (kind, sec.get("optional")))
    for c in spec.get("containers", []):
        for m in c.get("volumeMounts", []):
            if m.get("mountPath") == "/conn-cred/cluster-shell":
                out.append("%s:mount:readOnly=%s" % (kind, m.get("readOnly")))
print("\n".join(out))
PY
)"
# optional=True is load-bearing: bp-k8s-ws-proxy mints this Secret and
# reflector mirrors it in, so on a fresh Sovereign it can lag the seed by
# a reconcile. Optional means the connection is produced credential-free
# and the per-minute CronJob converges it — a REQUIRED mount would wedge
# the seed Pod in ContainerCreating and produce NO connections at all.
for want in "Job:volume:optional=True" "CronJob:volume:optional=True" \
            "Job:mount:readOnly=True" "CronJob:mount:readOnly=True"; do
  if printf '%s\n' "${csmounts}" | grep -qx -- "${want}"; then
    pass "cluster-shell mTLS credential ${want}"
  else
    fail "cluster-shell mTLS credential missing: ${want} (got: $(printf '%s' "${csmounts}" | tr '\n' ' '))"
  fi
done

# The credential RENAME is the load-bearing line: a cert-manager Secret
# is always tls.crt/tls.key/ca.crt, guacd reads client-cert/client-key/
# ca-cert. Writing them through unrenamed leaves guacd with no client
# certificate and every click 401s, with nothing in the row to say why.
for pair in 'tls.crt:client-cert' 'tls.key:client-key' 'ca.crt:ca-cert'; do
  if printf '%s' "${common}" | grep -q "${pair}"; then
    pass "load_connection_credentials maps ${pair}"
  else
    fail "load_connection_credentials does not map ${pair} — guacd would receive no client certificate"
  fi
done

# The connection must also be REACHABLE. The Sovereign runs a
# namespace-wide default-deny (bp-plane-isolation ships podSelector:{}
# Ingress+Egress per component namespace) and guacd carried NO egress
# policy of its own, because until now nothing dialled out of guacd —
# the kubectl path went through the webapp. guacd's `kubernetes`
# protocol opens its OWN WebSocket, so without this policy the
# cluster-shell connection renders in ALL CONNECTIONS and then hangs on
# click: present, listed, and useless.
netpol() { # netpol <render>
  python3 - "$1" <<'PY'
import sys, yaml
for d in (x for x in yaml.safe_load_all(open(sys.argv[1])) if x):
    if d.get("kind") == "NetworkPolicy" and d["metadata"]["name"].endswith("guacd-egress"):
        sel = d["spec"]["podSelector"].get("matchLabels", {})
        ports = []
        nss = []
        for rule in d["spec"].get("egress", []):
            for to in rule.get("to", []):
                ns = (to.get("namespaceSelector") or {}).get("matchLabels", {})
                if ns:
                    nss.append(list(ns.values())[0])
            for pt in rule.get("ports", []):
                ports.append(str(pt.get("port")))
        print("selector=%s;ports=%s;ns=%s" % (
            sel.get("app.kubernetes.io/component") or sorted(sel.items()),
            ",".join(sorted(set(ports))), ",".join(sorted(set(nss)))))
        break
else:
    print("ABSENT")
PY
}
np="$(netpol "${TMP}/default.yaml")"
if [[ "${np}" == "ABSENT" ]]; then
  fail "no guacd-egress NetworkPolicy — under the Sovereign's namespace default-deny the cluster-shell connection would render and then hang on click"
else
  [[ "${np}" == *";ports="*"8443"* ]] \
    && pass "guacd-egress allows the proxy mTLS port (${np})" \
    || fail "guacd-egress does not allow the proxy port: ${np}"
  [[ "${np}" == *"catalyst-system"* ]] \
    && pass "guacd-egress targets the proxy namespace" \
    || fail "guacd-egress does not reach catalyst-system: ${np}"
  [[ "${np}" == *"53"* ]] \
    && pass "guacd-egress allows DNS (guacd resolves the proxy by the name it also verifies the certificate against)" \
    || fail "guacd-egress has no DNS rule: ${np}"
fi
# CONTROL — no mTLS target declared, no egress grant. A policy that
# renders unconditionally would be granting guacd egress on a Sovereign
# that never asked for the connection.
if [[ "$(netpol "${TMP}/empty.yaml")" == "ABSENT" ]]; then
  pass "CONTROL: with no cluster-shell target, no guacd egress is granted"
else
  fail "CONTROL: guacd-egress rendered for a Sovereign with no mTLS connection declared"
fi

# Vacuity check — every assertion above reads the seed ConfigMap, so prove the
# default render actually contained the seed Job it belongs to.
if grep -q 'catalyst.openova.io/component: jdbc-seed' "${TMP}/default.yaml"; then
  pass "vacuity: the default render did contain the jdbc-seed Job"
else
  fail "vacuity: no jdbc-seed Job in the default render — assertions above were empty-string comparisons"
fi

echo "[connection-seed] 7/7 — the producer survives WITHOUT the CNPG api-version"
# THE CASE THIS SUITE COULD NOT HAVE HAD. `.Capabilities` is evaluated at
# RENDER time, so when the chart rendered before the CNPG CRD was visible the
# Job was not skipped-at-runtime — it was ABSENT FROM THE MANIFEST. Being a
# post-install/post-upgrade hook, no later reconcile re-ran it, so the gap was
# permanent. Measured on hw298: chart 0.2.41, NEWER than the 0.2.38 that
# shipped the producer, with an empty ALL CONNECTIONS list.
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/c \
  >"${TMP}/nocap.yaml" 2>"${TMP}/nocap.yaml.err" \
  || { echo "helm template failed:"; cat "${TMP}/nocap.yaml.err"; exit 1; }

if grep -q 'catalyst.openova.io/component: jdbc-seed' "${TMP}/nocap.yaml"; then
  pass "seed Job renders without postgresql.cnpg.io/v1 declared"
else
  fail "seed Job VANISHES without the CNPG api-version — the #5991 render-time gate is back"
fi
for target in sovereign-node cluster-shell; do
  if grep -q "${target}" "${TMP}/nocap.yaml"; then
    pass "connection target '${target}' present without the api-version"
  else
    fail "connection target '${target}' missing without the api-version"
  fi
done
# CONTROL: the master switch must still suppress it, so case 7 cannot be
# satisfied by a chart that simply always renders. This also pins the sprig
# trap — `default true $db.enabled` returns TRUE for an explicit false, so the
# switch was inert until it was resolved by hasKey.
helm template bp-guacamole . \
  --set guacamole.enabled=true \
  --set guacamole.database.enabled=false \
  --set guacamole.httproute.hostname=guacamole.test \
  --set guacamole.oidc.issuer=https://kc.test/realms/c \
  >"${TMP}/dboff.yaml" 2>/dev/null || true
if grep -q 'catalyst.openova.io/component: jdbc-seed' "${TMP}/dboff.yaml"; then
  fail "CONTROL: database.enabled=false still rendered the seed Job — the master switch is inert"
else
  pass "CONTROL: database.enabled=false suppresses the seed Job"
fi

if [[ "${fails}" -gt 0 ]]; then
  echo "[connection-seed] ${fails} assertion(s) FAILED"
  exit 1
fi
echo "[connection-seed] all assertions passed"
