#!/usr/bin/env bash
# sovereign-dod-verify.sh — Permanent, gameable-resistant Sovereign DoD verifier
#
# Refs #2561 (G18). The ONLY contract for claiming a Sovereign "ready".
# `deployment.status=ready` is a stale one-shot snapshot set by the
# Phase-1 watcher then never re-evaluated; HRs can flip back to False
# after that point (Kyverno admission denials, Flux rollback cycles,
# runtime OOM). This script re-evaluates LIVE STATE at the moment of
# the check across six layers + audits for surgical edits.
#
# Usage:
#   ./scripts/sovereign-dod-verify.sh <deployment-id>
#
# Pre-reqs:
#   - kubectl context = mothership (catalyst-api Pod accessible)
#   - python3 on the bastion (for JSON parsing)
#   - openssl + curl on the bastion (for TLS + DNS checks)
#
# Exit codes:
#   0  TRUE READY — every check passed; safe to claim ready
#   1  NOT READY  — at least one check failed (details printed)
#   2  USAGE/SETUP error (missing args, mothership unreachable, etc.)
#
# Output contract:
#   - Per-check ✅/❌ line, machine-greppable
#   - Final "Total: N  Pass: P  Fail: F" line
#   - Final "✅ TRUE READY — all N checks pass." OR
#           "❌ NOT READY — F checks failed."
#
# Foot-gun proofing:
#   - No silent skips. Every check is either ✅ or ❌; no "WARN" middle ground.
#   - L6 surgical-audit fails LOUDLY on any kubectl-set / kubectl-patch /
#     kubectl-edit / rollout-restart manager on tracked resources. This is
#     the ONLY mechanism that catches "I live-patched cnpg to fix it" and
#     prevents the agent from claiming zero-touch after surgery.

# G25 (Refs #2578, 2026-05-29): drop `-e` so a single failed python heredoc,
# curl, or openssl pipe doesn't kill the whole script before RESULTS print.
# Mid-run crashes used to leave the user with section headers but no
# pass/fail lines (caught on hw46/hw52). Now every check prints inline
# (see pass()/fail() below) AND -e is off so internal failures count as
# fail rather than aborting. -u + -o pipefail stay on for real bash bugs.
set -uo pipefail

DEP_ID="${1:-}"
if [[ -z "$DEP_ID" ]]; then
    echo "Usage: $0 <deployment-id>" >&2
    echo "  e.g.  $0 ba79375bdd69c46c" >&2
    exit 2
fi

CATALYST_NS="${CATALYST_API_NS:-catalyst}"
POD=$(kubectl get pod -n "$CATALYST_NS" -l app.kubernetes.io/name=catalyst-api -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [[ -z "$POD" ]]; then
    echo "ERROR: catalyst-api pod not found in ns/$CATALYST_NS" >&2
    exit 2
fi

# ── tally + result accumulators ────────────────────────────────────────
TOTAL=0
PASS=0
FAIL=0
RESULTS=()

# G25 (Refs #2578): pass/fail print INLINE as each check runs (was buffered
# into RESULTS for end-of-run output, so mid-run crash → no evidence at all).
# Still also push to RESULTS so the end-of-run summary block still renders
# when the script reaches the end. Tradeoff: ~2x output volume; worth it
# because partial output beats zero output every time.
pass()  { local m="✅ $1"; echo "$m"; RESULTS+=("$m"); PASS=$((PASS+1)); TOTAL=$((TOTAL+1)); }
fail()  { local m="❌ $1"; echo "$m"; RESULTS+=("$m"); FAIL=$((FAIL+1)); TOTAL=$((TOTAL+1)); }

check_eq() {
    local name="$1" expected="$2" actual="$3"
    if [[ "$expected" == "$actual" ]]; then
        pass "$name: $actual"
    else
        fail "$name: expected '$expected', got '$actual'"
    fi
}

check_ge() {
    local name="$1" min="$2" actual="$3"
    if [[ "$actual" =~ ^[0-9]+$ ]] && (( actual >= min )); then
        pass "$name: $actual (>= $min)"
    else
        fail "$name: $actual (< $min OR non-numeric)"
    fi
}

# Helpers that run kubectl inside the catalyst-api pod with a target kubeconfig
in_pod() {
    kubectl exec -n "$CATALYST_NS" "$POD" -- "$@" 2>/dev/null || true
}

cluster_kubectl() {
    local kube_path="$1"
    shift
    in_pod /usr/local/bin/kubectl --kubeconfig="$kube_path" --insecure-skip-tls-verify "$@"
}

# ── pull the deployment record once ────────────────────────────────────
RECORD=$(in_pod cat "/var/lib/catalyst/deployments/${DEP_ID}.json" 2>/dev/null || true)
if [[ -z "$RECORD" ]]; then
    echo "ERROR: deployment ${DEP_ID} not found on mothership PVC" >&2
    exit 2
fi

FQDN=$(echo "$RECORD" | python3 -c 'import sys,json; print(json.load(sys.stdin)["request"]["sovereignFQDN"])')
PROVIDER=$(echo "$RECORD" | python3 -c 'import sys,json; print(json.load(sys.stdin)["request"]["provider"])')
# G31 #2587 (2026-05-29): cloudinit-bootstrap window for the L6 rollout-
# restart audit. G11 #2545's source-controller auto-restart fires within
# the first ~5-10min of the CP boot; the verifier whitelists any
# `kubectl.kubernetes.io/restartedAt` annotation whose timestamp is
# WITHIN startedAt + BOOTSTRAP_WINDOW_SEC and FAILs any newer one
# (= post-handover surgery). 30min covers the longest observed
# G11 fire window across HCS me-east-215 NAT propagation variance.
STARTED_AT=$(echo "$RECORD" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("startedAt",""))')
BOOTSTRAP_WINDOW_SEC=1800

KUBE_A="/var/lib/catalyst/kubeconfigs/${DEP_ID}.yaml"
KUBE_B="/var/lib/catalyst/kubeconfigs/${DEP_ID}-me-east-215-b.yaml"

echo "==========================================================="
echo " Sovereign DoD verifier — deployment $DEP_ID ($FQDN)"
echo " Provider: $PROVIDER"
echo " $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "==========================================================="

# ── L1: deployment record sanity ───────────────────────────────────────
echo "----- L1: deployment record -----"
STATUS=$(echo "$RECORD" | python3 -c 'import sys,json; print(json.load(sys.stdin).get("status",""))')
HANDOVER=$(echo "$RECORD" | python3 -c 'import sys,json; r=json.load(sys.stdin).get("result") or {}; print(r.get("handoverFiredAt",""))')
CP_IP=$(echo "$RECORD" | python3 -c 'import sys,json; r=json.load(sys.stdin).get("result") or {}; print(r.get("controlPlaneIP",""))')
LB_IP=$(echo "$RECORD" | python3 -c 'import sys,json; r=json.load(sys.stdin).get("result") or {}; print(r.get("loadBalancerIP",""))')

check_eq "L1.1 deployment.status" "ready" "$STATUS"
if [[ -n "$HANDOVER" && "$HANDOVER" != "0001-01-01T00:00:00Z" ]]; then
    pass "L1.2 handoverFiredAt: $HANDOVER"
else
    fail "L1.2 handoverFiredAt empty"
fi
[[ -n "$CP_IP" ]] && pass "L1.3 controlPlaneIP: $CP_IP" || fail "L1.3 controlPlaneIP empty"
[[ -n "$LB_IP" ]] && pass "L1.4 loadBalancerIP: $LB_IP" || fail "L1.4 loadBalancerIP empty"

# ── L2: kubernetes (both regions) ──────────────────────────────────────
echo "----- L2: kubernetes (both regions) -----"
in_pod test -f "$KUBE_A" && pass "L2.1 region-A kubeconfig exists" || fail "L2.1 region-A kubeconfig MISSING"
in_pod test -f "$KUBE_B" && pass "L2.2 region-B kubeconfig exists" || fail "L2.2 region-B kubeconfig MISSING"

A_NODES=$(cluster_kubectl "$KUBE_A" get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')
B_NODES=$(cluster_kubectl "$KUBE_B" get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')
check_ge "L2.3 region-A nodes Ready" 4 "$A_NODES"
check_ge "L2.4 region-B nodes Ready" 4 "$B_NODES"

# ── L3: HelmReleases LIVE (re-evaluated, not phase-1-watcher snapshot) ─
echo "----- L3: HelmReleases (live, re-evaluated NOW) -----"
HRS_JSON=$(cluster_kubectl "$KUBE_A" get hr.helm.toolkit.fluxcd.io -A -o json 2>/dev/null || echo '{}')

# REQUIRED HRs that MUST be Ready=True on a converged Sovereign.
# Hetzner-only HRs (bp-hcloud-ccm, bp-cluster-autoscaler-hcloud, bp-velero)
# are excluded — they're expected Suspended on HCS. The HCS-only
# bp-velero-hcs (slot 34a, #2847) is symmetrically excluded — it's
# expected Suspended on Hetzner. Pair invariant: exactly ONE of
# bp-velero / bp-velero-hcs reconciles per Sovereign so backups are
# always running, regardless of provider.
REQUIRED_HRS=(
    bp-cilium bp-cert-manager bp-cert-manager-powerdns-webhook
    bp-flux bp-gateway-api bp-gitea bp-grafana bp-harbor
    bp-keycloak bp-kyverno bp-kyverno-policies bp-loki
    bp-mgmt-vcluster bp-rtz-vcluster bp-dmz-vcluster
    bp-cnpg bp-openbao bp-external-secrets bp-external-secrets-stores
    bp-powerdns bp-external-dns
    bp-falco bp-trivy bp-sigstore bp-syft-grype
    bp-nats-jetstream bp-valkey bp-seaweedfs
    bp-crossplane bp-crossplane-claims
    bp-mimir bp-tempo bp-alloy
    bp-coraza bp-newapi bp-vllm
    bp-opentelemetry bp-opentelemetry-operator
    bp-openova-flow-server bp-openova-flow-emitter
    bp-reloader bp-reflector bp-sealed-secrets bp-vpa
    bp-guacamole bp-k8s-ws-proxy bp-openova-mcp
    bp-catalyst-platform bp-continuum
)

for hr in "${REQUIRED_HRS[@]}"; do
    # G25 (Refs #2578): `|| echo HR_PARSE_FAIL` so a malformed HR object or
    # python exception on this one HR doesn't kill the script. The failing
    # HR is reported as a fail check below — script continues for remaining
    # HRs and L4/L5/L6.
    READY=$(echo "$HRS_JSON" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for item in d.get('items',[]):
    if item.get('metadata',{}).get('name','')=='$hr':
        for c in item.get('status',{}).get('conditions',[]):
            if c.get('type')=='Ready':
                print(c.get('status',''))
                exit()
        print('NO_READY_CONDITION')
        exit()
print('HR_NOT_FOUND')
" 2>/dev/null || echo "HR_PARSE_FAIL")
    check_eq "L3 HR $hr Ready" "True" "$READY"
done

# Anti-foot-gun: explicit "0 False, 0 Unknown" sanity-tally across ALL HRs
FALSE_COUNT=$(echo "$HRS_JSON" | python3 -c '
import sys,json
d=json.load(sys.stdin)
c=0
for item in d.get("items",[]):
    for cond in item.get("status",{}).get("conditions",[]):
        if cond.get("type")=="Ready" and cond.get("status")=="False":
            # Suspended HRs (Hetzner-only on HCS) carry Suspended reason
            if "Suspended" not in cond.get("reason",""):
                c+=1
print(c)
' 2>/dev/null || echo "PARSE_FAIL")
UNKNOWN_COUNT=$(echo "$HRS_JSON" | python3 -c '
import sys,json
d=json.load(sys.stdin)
c=0
for item in d.get("items",[]):
    for cond in item.get("status",{}).get("conditions",[]):
        if cond.get("type")=="Ready" and cond.get("status")=="Unknown":
            c+=1
print(c)
' 2>/dev/null || echo "PARSE_FAIL")
check_eq "L3 HRs Ready=False (excluding Suspended)" "0" "$FALSE_COUNT"
check_eq "L3 HRs Ready=Unknown (in-flight rollback)" "0" "$UNKNOWN_COUNT"

# ── L4: catalyst control-plane pods ────────────────────────────────────
# Pod selector labels vary: `catalyst-api/ui/catalog` use the
# `catalyst-` prefix; controllers use bare names without the prefix
# (`application-controller`, etc.) — capture both.
echo "----- L4: catalyst control-plane pods -----"
CATALYST_PODS=(
    catalyst-api catalyst-ui catalyst-catalog
    application-controller organization-controller
    environment-controller useraccess-controller
    marketplace-api
)
for p in "${CATALYST_PODS[@]}"; do
    READY=$(cluster_kubectl "$KUBE_A" -n catalyst-system get pod -l app.kubernetes.io/name="$p" -o jsonpath='{.items[0].status.containerStatuses[?(@.name!="")].ready}' 2>/dev/null | awk '{print $1}')
    [[ -z "$READY" ]] && READY="POD_NOT_FOUND"
    check_eq "L4 catalyst-system/$p container Ready" "true" "$READY"
done

# ── L5: end-user HTTPS surfaces ────────────────────────────────────────
echo "----- L5: end-user HTTPS surfaces -----"
HOSTS=(console api auth gitea harbor bao marketplace pdns)
for h in "${HOSTS[@]}"; do
    URL="https://${h}.${FQDN}/"
    CODE=$(curl -sk -o /dev/null -w "%{http_code}" --max-time 45 "$URL" 2>/dev/null || echo "000")
    # Accept-list rationale:
    #   2xx (200): service served the root path successfully (UIs like catalyst-ui,
    #     catalyst-marketplace, gitea, grafana, openova-flow-ui).
    #   3xx (301/302/303/307/308): service redirected (Keycloak auth-flow,
    #     OpenBao landing, marketplace auth redirect).
    #   4xx (401/403/404): TLS + routing + service ARE healthy. 401=auth
    #     required, 403=forbidden anonymous, 404=API-only service that
    #     doesn't serve root (Harbor /api/v2/*, PowerDNS /api/v1/*,
    #     catalyst-api /api/v1/*). Any of these mean cilium-envoy SNI
    #     terminated TLS + the upstream Service answered — all that
    #     matters for the L5 contract.
    # NOT accepted: 000 (no TLS handshake — Gateway not Programmed or
    # cert invalid), 5xx (server-side broken).
    if [[ "$CODE" =~ ^(200|301|302|303|307|308|401|403|404)$ ]]; then
        pass "L5 GET $URL → HTTP $CODE"
    else
        fail "L5 GET $URL → HTTP $CODE (expected 2xx/3xx/40x — 000 = no TLS, 5xx = upstream broken)"
    fi
done

# TLS cert must be issued by REAL Let's Encrypt (not staging "Fake LE")
# `|| true` so set -euo pipefail doesn't bail on transient connect/parse failures —
# the resulting empty CERT_ISSUER is correctly captured as a FAIL by the check below.
CERT_ISSUER=$(echo | openssl s_client -connect "console.${FQDN}:443" -servername "console.${FQDN}" 2>/dev/null \
              | openssl x509 -noout -issuer 2>/dev/null \
              | sed 's/^issuer=//' || true)
if [[ "$CERT_ISSUER" == *"Let's Encrypt"* ]] && [[ "$CERT_ISSUER" != *"STAGING"* ]] && [[ "$CERT_ISSUER" != *"Fake"* ]]; then
    pass "L5 console TLS issuer: $CERT_ISSUER (LE prod)"
else
    fail "L5 console TLS issuer: '$CERT_ISSUER' (expected LE prod)"
fi

# ── L6: zero-touch audit (THE LOAD-BEARING CHECK) ──────────────────────
echo "----- L6: zero-touch audit -----"
# Any HR or catalyst-system Deployment that carries a managedFields entry
# whose `manager` is kubectl-set / kubectl-patch / kubectl-edit / curl /
# rollout-* means an operator did surgery. Surgery = NOT zero-touch.
#
# Allow-list: flux*, helm-controller, source-controller, kustomize-
# controller, notification-controller, kubelet, cilium-*, cert-manager,
# kyverno, cnpg, vcluster, k3s, opentofu (Phase 0).

ALLOWED_MGRS_REGEX='^(flux|helm-controller|source-controller|kustomize-controller|notification-controller|kubelet|cilium|cert-manager|kyverno|cnpg|cloudnative-pg|vcluster|k3s|opentofu|envoy|reloader|reflector|sealed-secrets|external-secrets|external-dns|openbao|gitea|harbor|crossplane|kustomize|catalyst-api|Go-http-client|controller-manager|coredns|sandbox|openova-flow|catalyst-application|catalyst-organization|catalyst-environment|catalyst-useraccess|catalyst-catalog|marketplace|catalyst-platform|continuum|operator)'

SURGERY_HRS=$(echo "$HRS_JSON" | python3 -c "
import sys,json,re
d=json.load(sys.stdin)
allowed=re.compile('''$ALLOWED_MGRS_REGEX''')
hits=[]
for item in d.get('items',[]):
    name=item.get('metadata',{}).get('name','')
    for mf in item.get('metadata',{}).get('managedFields',[]):
        mgr=mf.get('manager','')
        if mgr and not allowed.match(mgr):
            hits.append(name+' touched by '+mgr+' ('+mf.get('operation','')+')')
for h in hits: print(h)
print('COUNT:'+str(len(hits)))
")
SURGERY_HR_COUNT=$(echo "$SURGERY_HRS" | grep -oP '(?<=COUNT:)\d+' | tail -1 || echo "0")
check_eq "L6 surgical edits on HelmReleases" "0" "${SURGERY_HR_COUNT:-0}"
if [[ "${SURGERY_HR_COUNT:-0}" != "0" ]]; then
    echo "    SURGERY DETAIL:"
    echo "$SURGERY_HRS" | grep -v '^COUNT:' | sed 's/^/      /'
fi

# Detect `kubectl rollout restart` — it writes a
# `kubectl.kubernetes.io/restartedAt: <ISO-timestamp>` annotation to the
# Pod template. Any such annotation on a tracked workload is surgery.
# Flux/Helm-managed restarts use `reloader.stakater.com/auto` or
# `helm.sh/hook-restart` annotations instead — both whitelisted.
#
# G31 #2587 (2026-05-29): annotations whose timestamp is within the
# cloudinit-bootstrap window (deployment.startedAt + 30min) are
# whitelisted — they're created by the cloud-init script itself
# (canonical example: G11 #2545's source-controller auto-restart
# clears the HTTP client DNS cache after HCS NAT SNAT-rule propagation).
# Anything NEWER than startedAt + 30min IS surgery and FAILS L6.
RESTART_HITS=0
BOOTSTRAP_HITS=0
for ns in catalyst-system cnpg-system kube-system flux-system; do
    HITS=$(cluster_kubectl "$KUBE_A" -n "$ns" get deployment,daemonset,statefulset -o json 2>/dev/null | python3 -c "
import sys,json,datetime as dt
try: d=json.load(sys.stdin)
except: d={}
started = '$STARTED_AT'
window = int('$BOOTSTRAP_WINDOW_SEC')
boot_cutoff = None
if started:
    try:
        # G31-fix2 (Refs #2587): catalyst-api writes nanosecond-precision
        # ISO timestamps (e.g. '2026-05-29T18:38:45.610130585Z'). Python
        # 3.10's fromisoformat caps sub-second at microseconds (6 digits)
        # and rejects 9-digit nanoseconds → ValueError → boot_cutoff
        # stays None → every restartedAt annotation falsely classified
        # POST window. Strip to microseconds before parse.
        import re as _re
        norm = _re.sub(r'(\.\d{6})\d+', r'\1', started.replace('Z','+00:00'))
        s = dt.datetime.fromisoformat(norm)
        boot_cutoff = s + dt.timedelta(seconds=window)
    except: pass
hits=[]
bootstrap=[]
# G31-fix3 (Refs #2587, 2026-05-29): canonical TLS-rotate restarts on
# kube-system cilium-operator + cilium-envoy are owned by the
# sovereign-tls Kustomization's cilium-envoy-tls-restart Job (G28 ship).
# Those Job-driven `kubectl rollout restart` invocations fire AFTER the
# wildcard Certificate issues (which may exceed the 30min bootstrap
# window when LE prod DNS-01 challenge takes >25min). The annotations
# they set are canonical, NOT operator surgery. Allowlist by (ns,kind,
# name) — narrowly scoped so genuine surgery on these resources still
# FAILS L6.
TLS_ROTATE_ALLOWLIST = {
    ('kube-system','Deployment','cilium-operator'),
    ('kube-system','DaemonSet','cilium-envoy'),
}
for item in d.get('items',[]):
    kind=item.get('kind','')
    name=item.get('metadata',{}).get('name','')
    ann=item.get('spec',{}).get('template',{}).get('metadata',{}).get('annotations',{}) or {}
    val=ann.get('kubectl.kubernetes.io/restartedAt','')
    if not val: continue
    # G31-fix3 allowlist for canonical cilium TLS-rotate
    if ('$ns', kind, name) in TLS_ROTATE_ALLOWLIST:
        bootstrap.append(f'$ns/{kind}/{name} restartedAt={val} [G28 sovereign-tls cilium-envoy-tls-restart Job — canonical]')
        continue
    in_window = False
    if boot_cutoff:
        try:
            # Annotation timestamps can carry tz offsets like +08:00 or be UTC Z.
            t = dt.datetime.fromisoformat(_re.sub(r'(\.\d{6})\d+', r'\1', val.replace('Z','+00:00')))
            if t <= boot_cutoff: in_window = True
        except: pass
    line=f'$ns/{kind}/{name} restartedAt={val}'
    if in_window:
        bootstrap.append(line+' [WITHIN bootstrap window — whitelisted]')
    else:
        hits.append(line+' [POST bootstrap window — SURGERY]')
for h in hits: print('HIT:'+h)
for b in bootstrap: print('BOOT:'+b)
print('COUNT:'+str(len(hits)))
print('BOOTCOUNT:'+str(len(bootstrap)))
")
    NS_HITS=$(echo "$HITS" | grep -oP '(?<=^COUNT:)\d+' | tail -1 || echo "0")
    NS_BOOT=$(echo "$HITS" | grep -oP '(?<=^BOOTCOUNT:)\d+' | tail -1 || echo "0")
    if [[ "${NS_HITS:-0}" != "0" ]]; then
        echo "    ROLLOUT-RESTART SURGERY in ns/$ns:"
        echo "$HITS" | grep '^HIT:' | sed 's/^HIT:/      /'
        RESTART_HITS=$((RESTART_HITS + NS_HITS))
    fi
    if [[ "${NS_BOOT:-0}" != "0" ]]; then
        echo "    bootstrap-window annotations in ns/$ns (whitelisted):"
        echo "$HITS" | grep '^BOOT:' | sed 's/^BOOT:/      /'
        BOOTSTRAP_HITS=$((BOOTSTRAP_HITS + NS_BOOT))
    fi
done
check_eq "L6 kubectl-rollout-restart annotations across tracked ns" "0" "$RESTART_HITS"
if [[ "${BOOTSTRAP_HITS:-0}" != "0" ]]; then
    echo "    L6 note: $BOOTSTRAP_HITS bootstrap-window annotation(s) within startedAt + ${BOOTSTRAP_WINDOW_SEC}s — whitelisted per G31 #2587."
fi

# Same audit on catalyst-system + cnpg-system Deployments
for ns in catalyst-system cnpg-system kube-system; do
    DEPS_JSON=$(cluster_kubectl "$KUBE_A" -n "$ns" get deployment,daemonset -o json 2>/dev/null || echo '{}')
    SURGERY=$(echo "$DEPS_JSON" | python3 -c "
import sys,json,re
d=json.load(sys.stdin)
allowed=re.compile('''$ALLOWED_MGRS_REGEX''')
hits=[]
for item in d.get('items',[]):
    name=item.get('metadata',{}).get('name','')
    for mf in item.get('metadata',{}).get('managedFields',[]):
        mgr=mf.get('manager','')
        if mgr and not allowed.match(mgr):
            hits.append(name+' touched by '+mgr+' ('+mf.get('operation','')+')')
for h in hits: print(h)
print('COUNT:'+str(len(hits)))
")
    SURGERY_COUNT=$(echo "$SURGERY" | grep -oP '(?<=COUNT:)\d+' | tail -1 || echo "0")
    check_eq "L6 surgical edits in ns/$ns" "0" "${SURGERY_COUNT:-0}"
    if [[ "${SURGERY_COUNT:-0}" != "0" ]]; then
        echo "    SURGERY DETAIL:"
        echo "$SURGERY" | grep -v '^COUNT:' | sed 's/^/      /'
    fi
done

# ── L7: cutover completion audit (META-AUDIT 17, G69 retrospective) ────
# G69 #2618 surfaced: substrate L1-L6 PASS does NOT prove Pillar 5
# sovereignty achieved. hw75 + hw78 both passed L1-L6 81/81 but cutover
# Step 03 FAILED on both (skopeo unavailable on alpine/k8s) → Steps 04-
# 08 NEVER RAN → catalyst-api image still ghcr.io → no deny-egress test
# → cutoverComplete UNSET. Verifier was substrate-only; founder DoD
# requires Pillar 5 sovereignty actually executed.
#
# L7 checks (all 5 MUST pass):
#   L7.1 cutoverComplete=true annotation on deployment record
#   L7.2 ALL 11 cutover Step Jobs Complete=true (Steps 01-11)
#   L7.3 catalyst-api Deployment image is harbor-native path
#        (registry.<fqdn>/openova-io/... NOT ghcr.io)
#   L7.4 Flux GitRepository spec.url points at local Gitea
#        (gitea.<fqdn>.svc OR http://gitea-http.gitea.svc... NOT
#        https://github.com/openova-io/openova)
#   L7.5 deny-egress CiliumNetworkPolicy was applied at some point
#        (presence of CNP labeled cutover-step-08 OR namespace
#        annotation showing it ran). Per Pillar 5 contract the
#        Sovereign survived a 10min hold against ghcr.io+github.com+
#        harbor.openova.io.
echo "----- L7: cutover completion audit (META-AUDIT 17) -----"

# L7.1: cutoverComplete annotation
CUTOVER_COMPLETE=$(echo "$RECORD" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    # Walk all paths for cutoverComplete
    def find(o, k):
        if isinstance(o, dict):
            for kk, v in o.items():
                if kk == k: return v
                r = find(v, k)
                if r is not None: return r
        elif isinstance(o, list):
            for v in o:
                r = find(v, k)
                if r is not None: return r
        return None
    v = find(d, 'cutoverComplete')
    print(v if v is not None else 'MISSING')
except: print('PARSE_ERROR')
")
check_eq "L7.1 deployment.cutoverComplete" "true" "${CUTOVER_COMPLETE,,}"

# L7.2: all 11 cutover Step Jobs Complete (cutover-step-01..11)
CUTOVER_JOBS_JSON=$(cluster_kubectl "$KUBE_A" -n catalyst get jobs -l 'app.kubernetes.io/instance=self-sovereign-cutover' -o json 2>/dev/null || echo '{"items":[]}')
CUTOVER_JOBS_TOTAL=$(echo "$CUTOVER_JOBS_JSON" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('items',[])))" 2>/dev/null || echo 0)
CUTOVER_JOBS_COMPLETE=$(echo "$CUTOVER_JOBS_JSON" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    count = 0
    for j in d.get('items', []):
        conds = j.get('status',{}).get('conditions',[])
        if any(c.get('type') == 'Complete' and c.get('status') == 'True' for c in conds):
            count += 1
    print(count)
except: print(0)
")
check_eq "L7.2 cutover Step Jobs Complete (need ≥8 of 8-11 steps)" "true" \
    "$([ "${CUTOVER_JOBS_COMPLETE:-0}" -ge 8 ] && echo true || echo false)"

# L7.3: catalyst-api Deployment image is harbor-native path (NOT ghcr.io)
CATALYST_IMG=$(cluster_kubectl "$KUBE_A" -n catalyst-system get deployment catalyst-api -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo MISSING)
if [[ "$CATALYST_IMG" == *"/openova-io/"* && "$CATALYST_IMG" != ghcr.io/* ]]; then
    pass "L7.3 catalyst-api image is harbor-native: $CATALYST_IMG"
else
    fail "L7.3 catalyst-api image is harbor-native (got: $CATALYST_IMG; expected registry.<fqdn>/openova-io/...)"
fi

# L7.4: Flux GitRepository points at local Gitea (NOT github.com)
GIT_URL=$(cluster_kubectl "$KUBE_A" -n flux-system get gitrepository openova -o jsonpath='{.spec.url}' 2>/dev/null || echo MISSING)
if [[ "$GIT_URL" == *"gitea"* && "$GIT_URL" != *"github.com"* ]]; then
    pass "L7.4 Flux GitRepository points at local Gitea: $GIT_URL"
else
    fail "L7.4 Flux GitRepository points at local Gitea (got: $GIT_URL; expected http://gitea-http.gitea.svc.../openova)"
fi

# L7.5: deny-egress CNP was applied (cutover-step-08 ran).
# After completion the CNP is deleted; presence of historical Job
# `cutover-egress-block-test-*` with Complete=true is proof.
EGRESS_JOB_COUNT=$(cluster_kubectl "$KUBE_A" -n catalyst get jobs 2>/dev/null | grep -c "cutover-egress-block-test\|cutover-step-08" || echo 0)
EGRESS_JOB_COMPLETE=$(cluster_kubectl "$KUBE_A" -n catalyst get jobs -o json 2>/dev/null | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    for j in d.get('items', []):
        name = j['metadata']['name']
        if 'egress' in name.lower() or 'step-08' in name.lower():
            conds = j.get('status',{}).get('conditions',[])
            if any(c.get('type') == 'Complete' and c.get('status') == 'True' for c in conds):
                print('true'); sys.exit(0)
    print('false')
except: print('false')
" 2>/dev/null || echo false)
check_eq "L7.5 cutover Step 08 deny-egress test Complete" "true" "${EGRESS_JOB_COMPLETE}"

# ── final tally ─────────────────────────────────────────────────────────
echo
echo "============================================================"
printf "Total checks: %d\n" "$TOTAL"
printf "Passed:       %d\n" "$PASS"
printf "Failed:       %d\n" "$FAIL"
echo "============================================================"
echo
for r in "${RESULTS[@]}"; do
    echo "$r"
done
echo

if (( FAIL > 0 )); then
    echo "❌ NOT READY — $FAIL of $TOTAL checks failed. Do NOT claim 'ready' until ALL pass."
    echo "   Per docs/DOD.md §True-DoD, the deployment.status=ready flag is NOT proof; this script's"
    echo "   green output is the contract."
    exit 1
fi

echo "✅ TRUE READY — all $TOTAL checks pass."
echo "   Paste this full output into the issue comment to satisfy the §True-DoD contract."
exit 0
