#!/usr/bin/env bash
# mtls-render — the chart must ship a COMPLETE mTLS listener or none of
# it, and the identity it issues must be the identity it accepts.
#
# WHY THIS FILE EXISTS (#5991 / UAT row 115). guacd cannot set HTTP
# headers on its WebSocket upgrade, so it can never present the proxy's
# X-Catalyst-HMAC credential; the only credential it CAN present is a
# TLS client certificate. This chart mints that certificate and
# configures the listener that accepts it. Two failure modes are
# invisible to a plain "does it render" check and both are silent in
# production:
#
#   * HALF a gate — the DaemonSet mounts a TLS Secret while the
#     Certificate that creates it did not render (cert-manager CRD
#     absent, or tls.enabled=false reaching only some blocks). The Pod
#     sits in ContainerCreating forever and the exec proxy is DOWN, not
#     merely un-upgraded. So the CONTROLS below assert the OFF state is
#     complete, not just that Certificates disappeared.
#
#   * IDENTITY DRIFT — the client Certificate's commonName and the
#     proxy's CLIENT_CERT_ALLOWED_SUBJECTS are two separate writes of
#     one string. If they diverge, guacd presents a certificate that
#     chains correctly and is then denied by the allowlist: a 401 with
#     no visible cause on either side. This file compares the two
#     RENDERED VALUES, not the presence of the keys.
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
    echo "[mtls-render] SKIPPED — helm dependency update failed (no network);"
    echo "[mtls-render] NOTHING WAS ASSERTED. Re-run after \`helm dep build\`."
    exit 0
  }
fi

# --api-versions cert-manager.io/v1 is REQUIRED for the armed render:
# the TLS blocks are gated on that CRD (bp-k8s-ws-proxy.tlsEnabled), so
# without it the render is legitimately empty of TLS and every armed
# assertion would "fail" for a reason that has nothing to do with the
# chart. The vacuity check at the end catches a render that came back
# empty for some other reason.
render() { # render <outfile> [--api-versions …] [extra --set args…]
  local out="$1"; shift
  helm template k8s-ws-proxy . --set k8sWsProxy.enabled=true "$@" \
    >"${out}" 2>"${out}.err"
}

# q <render> <python-expr-body> — parse the render as YAML so a comment
# can never satisfy an assertion.
q() {
  python3 - "$1" <<PY
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
def kind(k):
    return [d for d in docs if d.get("kind") == k]
def ds():
    return (kind("DaemonSet") or [None])[0]
def container():
    d = ds()
    return d["spec"]["template"]["spec"]["containers"][0] if d else None
def env(name):
    c = container()
    if not c: return None
    for e in c.get("env", []):
        if e.get("name") == name:
            return e.get("value")
    return None
$2
PY
}

echo "[mtls-render] 1/6 — armed render ships the whole chain"
render "${TMP}/armed.yaml" --api-versions cert-manager.io/v1 \
  || { echo "helm template failed:"; cat "${TMP}/armed.yaml.err"; exit 1; }

certs="$(q "${TMP}/armed.yaml" 'print(",".join(sorted(d["metadata"]["name"] for d in kind("Certificate"))))')"
if [[ "${certs}" == "k8s-ws-proxy-ca,k8s-ws-proxy-client-tls,k8s-ws-proxy-server-tls" ]]; then
  pass "CA + server leaf + client leaf all render"
else
  fail "Certificates are '${certs}', want the CA, the server leaf and the client leaf"
fi
issuers="$(q "${TMP}/armed.yaml" 'print(",".join(sorted(d["metadata"]["name"] for d in kind("Issuer"))))')"
if [[ "${issuers}" == "k8s-ws-proxy-ca-issuer,k8s-ws-proxy-selfsigned" ]]; then
  pass "the two-step self-signed → CA issuer chain renders"
else
  fail "Issuers are '${issuers}'"
fi

echo "[mtls-render] 2/6 — the identity issued IS the identity accepted"
cn="$(q "${TMP}/armed.yaml" 'print([d for d in kind("Certificate") if d["metadata"]["name"].endswith("client-tls")][0]["spec"]["commonName"])')"
allow="$(q "${TMP}/armed.yaml" 'print(env("CLIENT_CERT_ALLOWED_SUBJECTS") or "")')"
if [[ -z "${allow}" ]]; then
  fail "CLIENT_CERT_ALLOWED_SUBJECTS renders EMPTY — an empty allowlist silently disables client-certificate auth, so every guacd click would 401"
elif [[ "${cn}" == "${allow}" ]]; then
  pass "client certificate CN == CLIENT_CERT_ALLOWED_SUBJECTS (${cn})"
else
  fail "identity drift: certificate CN is '${cn}' but the proxy allowlists '${allow}'"
fi

echo "[mtls-render] 3/6 — the server leaf carries the name a cross-namespace caller dials"
sans="$(q "${TMP}/armed.yaml" 'print(",".join([d for d in kind("Certificate") if d["metadata"]["name"].endswith("server-tls")][0]["spec"]["dnsNames"]))')"
# guacd runs its OWN hostname check against the connection's `hostname`
# parameter, so a missing SAN is a handshake failure, not a warning.
if [[ ",${sans}," == *",k8s-ws-proxy.catalyst-system.svc.cluster.local,"* ]] \
   || [[ ",${sans}," == *",k8s-ws-proxy.default.svc.cluster.local,"* ]]; then
  pass "server leaf carries the fully-qualified Service name (${sans})"
else
  fail "server leaf SANs are '${sans}' — no fully-qualified <svc>.<ns>.svc.cluster.local entry"
fi

echo "[mtls-render] 4/6 — listener, mount and Service port all agree"
port="$(q "${TMP}/armed.yaml" 'c=container(); print(",".join("%s:%s" % (x["name"], x["containerPort"]) for x in c.get("ports", [])))')"
[[ ",${port}," == *",https:8443,"* ]] && pass "container exposes https:8443" || fail "container ports are '${port}'"
svcports="$(q "${TMP}/armed.yaml" 'print(",".join("%s:%s:%s" % (x["name"], x["port"], x.get("targetPort")) for x in kind("Service")[0]["spec"]["ports"]))')"
[[ ",${svcports}," == *",https:8443:https,"* ]] && pass "Service publishes https:8443 → https" || fail "Service ports are '${svcports}'"
mount="$(q "${TMP}/armed.yaml" 'c=container(); print(",".join(m["mountPath"] for m in c.get("volumeMounts", [])))')"
[[ ",${mount}," == *",/etc/k8s-ws-proxy/tls,"* ]] && pass "server keypair is mounted" || fail "volumeMounts are '${mount}'"
certfile="$(q "${TMP}/armed.yaml" 'print(env("TLS_CERT_FILE") or "")')"
[[ "${certfile}" == "/etc/k8s-ws-proxy/tls/tls.crt" ]] && pass "TLS_CERT_FILE points into that mount" || fail "TLS_CERT_FILE='${certfile}'"
cafile="$(q "${TMP}/armed.yaml" 'print(env("TLS_CLIENT_CA_FILE") or "")')"
[[ "${cafile}" == "/etc/k8s-ws-proxy/tls/ca.crt" ]] && pass "TLS_CLIENT_CA_FILE is set (without it the allowlist verifies nothing)" || fail "TLS_CLIENT_CA_FILE='${cafile}'"
alias_label="$(q "${TMP}/armed.yaml" 'print(env("POD_ALIAS_LABEL") or "")')"
[[ -n "${alias_label}" ]] && pass "POD_ALIAS_LABEL=${alias_label} (a stored connection survives a pod rollout)" || fail "POD_ALIAS_LABEL renders empty — a seeded connection would 404 after the first rollout"
nodename="$(q "${TMP}/armed.yaml" 'c=container(); print(",".join((e.get("valueFrom",{}).get("fieldRef",{}) or {}).get("fieldPath","") for e in c.get("env",[]) if e.get("name")=="NODE_NAME"))')"
[[ "${nodename}" == "spec.nodeName" ]] && pass "NODE_NAME comes from the downward API" || fail "NODE_NAME fieldPath is '${nodename}'"

echo "[mtls-render] 5/6 — CONTROLS: the OFF state is COMPLETE, not half-applied"
# Control A — cert-manager CRD absent. The chart must not mount a Secret
# that nothing creates; that Pod never starts and the exec proxy is DOWN.
render "${TMP}/nocrd.yaml" || { echo "helm template failed:"; cat "${TMP}/nocrd.yaml.err"; exit 1; }
# Control B — operator turned it off, CRD present.
render "${TMP}/off.yaml" --api-versions cert-manager.io/v1 --set k8sWsProxy.tls.enabled=false \
  || { echo "helm template failed:"; cat "${TMP}/off.yaml.err"; exit 1; }
for ctl in nocrd off; do
  n="$(q "${TMP}/${ctl}.yaml" 'print(len(kind("Certificate")) + len(kind("Issuer")))')"
  m="$(q "${TMP}/${ctl}.yaml" 'c=container(); print(sum(1 for x in c.get("volumeMounts", []) if x["mountPath"].endswith("/tls")))')"
  e="$(q "${TMP}/${ctl}.yaml" 'print(1 if env("TLS_CERT_FILE") else 0)')"
  p="$(q "${TMP}/${ctl}.yaml" 'c=container(); print(sum(1 for x in c.get("ports", []) if x["name"] == "https"))')"
  s="$(q "${TMP}/${ctl}.yaml" 'print(sum(1 for x in kind("Service")[0]["spec"]["ports"] if x["name"] == "https"))')"
  if [[ "${n}${m}${e}${p}${s}" == "00000" ]]; then
    pass "CONTROL ${ctl}: zero cert objects AND zero TLS mount/env/port/service-port"
  else
    fail "CONTROL ${ctl}: half-applied gate — certs+issuers=${n} tlsMounts=${m} tlsEnv=${e} containerPort=${p} servicePort=${s}"
  fi
  # Non-vacuous: the SAME render still produced a working plaintext proxy.
  hp="$(q "${TMP}/${ctl}.yaml" 'c=container(); print(sum(1 for x in c.get("ports", []) if x["name"] == "http"))')"
  [[ "${hp}" == "1" ]] && pass "CONTROL ${ctl} is non-vacuous: the plaintext listener still renders" \
                       || fail "CONTROL ${ctl}: no http port either — the zero above proves nothing"
done

echo "[mtls-render] 6/6 — guards, and no nodePort anywhere"
if render "${TMP}/nosubject.yaml" --api-versions cert-manager.io/v1 --set k8sWsProxy.tls.clientSubject=""; then
  fail "an empty tls.clientSubject rendered — it would disable client-certificate auth silently and 401 every guacd click"
else
  grep -q 'clientSubject is empty' "${TMP}/nosubject.yaml.err" \
    && pass "an empty tls.clientSubject FAILS the render, naming the knob" \
    || fail "render failed but not with the clientSubject guard: $(head -2 "${TMP}/nosubject.yaml.err")"
fi
if grep -qE '^\s*nodePort:' "${TMP}/armed.yaml"; then
  fail "the render contains a nodePort — absolutely forbidden (docs/PRINCIPLES.md)"
else
  pass "no nodePort in the armed render"
fi
svctype="$(q "${TMP}/armed.yaml" 'print(kind("Service")[0]["spec"].get("type"))')"
[[ "${svctype}" == "ClusterIP" ]] && pass "Service stays ClusterIP" || fail "Service type is '${svctype}'"

# Vacuity — every assertion above reads the armed render's DaemonSet.
if [[ "$(q "${TMP}/armed.yaml" 'print(1 if ds() else 0)')" == "1" ]]; then
  pass "vacuity: the armed render did contain the DaemonSet"
else
  fail "vacuity: no DaemonSet in the armed render — the assertions above compared empty strings"
fi

if [[ "${fails}" -gt 0 ]]; then
  echo "[mtls-render] ${fails} assertion(s) FAILED"
  exit 1
fi
echo "[mtls-render] all assertions passed"
