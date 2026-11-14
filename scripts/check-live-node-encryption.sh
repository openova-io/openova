#!/usr/bin/env bash
# check-live-node-encryption.sh — CLUSTER-side WireGuard node-encryption
# uniformity guard (#5637).
#
# WHY THIS EXISTS
#
# `cilium-config` carrying `enable-wireguard: true` + `encrypt-node: true` is
# NOT evidence that node-to-node encryption is on for every node. The Cilium
# agent applies a per-node opt-out driven by the label selector in
# `--node-encryption-opt-out-labels`, whose UPSTREAM DEFAULT is
# `node-role.kubernetes.io/control-plane`. On k3s every server node carries
# that label, so on every Catalyst Sovereign the control-plane node of each
# region silently drops out of node-to-node encryption while the ConfigMap,
# the chart values, and `cilium-dbg status`'s own "Wireguard, Peers: N" line
# all keep saying encryption is on. Only the `NodeEncryption:` sub-field
# disagrees, and only on that one node per region.
#
# Proven live on hw292 (dep 1c56518035a83e03, 2026-08-03):
#
#   cilium-97bpn  (region-A cp1) Encryption: Wireguard [NodeEncryption: OptedOut, …]
#   cilium-s8mw5  (region-B cp1) Encryption: Wireguard [NodeEncryption: OptedOut, …]
#   the other six agents          Encryption: Wireguard [NodeEncryption: Enabled,  …]
#
#   cilium-97bpn log, verbatim:
#     "Opting out from node-to-node encryption on this node as per
#      node-encryption-opt-out-labels label selector"
#      module=agent.datapath.wireguard-agent
#      Selector=node-role.kubernetes.io/control-plane
#
# A per-node security property that exactly one node per region lacks is the
# shape a spot check never catches: sample a worker and the fleet reads
# uniform. So this guard reads EVERY agent in EVERY region given to it and
# fails closed the moment the fleet stops matching its declared posture.
#
# WHAT IT ASSERTS (all fail closed — no WARN, no silent skip)
#
#   1. Every region's cilium-config has enable-wireguard=true, encrypt-node=true.
#   2. The EFFECTIVE opt-out selector equals the posture declared in
#      docs/SECURITY.md §2.1 (EXPECTED_OPT_OUT_SELECTOR below). An upstream
#      default that drifts — or a hand-widened selector — is a violation even
#      though every node would still "agree" with it.
#   3. Every agent reports a NodeEncryption state that is exactly `Enabled` or
#      `OptedOut`. Unreadable, absent, not-Running, or any other token is a
#      violation, never a skip.
#   4. Each agent's state is the one its node's LABELS predict: a node matching
#      the declared selector must report OptedOut, every other node must report
#      Enabled. This catches, in one rule, all four failure shapes:
#        - a worker that silently opted out            (the #5637 fear)
#        - a control-plane node that silently encrypts (undeclared drift)
#        - an agent still carrying a state its node's labels no longer imply
#          (the "agent started before the setting applied" shape — a restart
#          would mask it, so the guard names it instead)
#        - a selector nobody can parse (parse failure is a violation)
#   5. An empty fleet is a setup error, not a pass. A guard that reports clean
#      on zero rows is the vacuity trap this repo has paid for before.
#
# WHY IT DOES NOT DEMAND ONE UNIFORM STATE ACROSS THE WHOLE FLEET
#
# It cannot: the control-plane opt-out is upstream-deliberate, documented, and
# load-bearing (the connection that updates a node's WireGuard public key would
# otherwise be encrypted with the very key being replaced). A "all agents must
# read Enabled" gate would be red on every healthy Sovereign, and a gate that
# is always red gets deleted. Pinning the label→state MAPPING is strictly
# stronger than pinning uniformity: it still fails closed on divergence, and it
# additionally catches an exclusion set that grows.
#
# RELATIONSHIP TO THE OTHER CHART/SOURCE GUARDS
#
# Same split as check-no-nodeports.sh (source) vs check-live-nodeports.sh
# (live): no amount of chart reading can see this, because the chart is
# correct — `encryption.nodeEncryption: true` is set and renders. The divergence
# only exists in the agent's runtime decision on a specific node. It is wired
# into scripts/verify-sovereign-convergence.sh so it runs in the existing
# post-convergence battery rather than as a guard nobody invokes.
#
# Usage:
#   scripts/check-live-node-encryption.sh --kubeconfig <A> --kubeconfig <B>
#   scripts/check-live-node-encryption.sh --context <ctx> [--context <ctx2>]
#   scripts/check-live-node-encryption.sh --fixture <snapshot.tsv>   # replay
#   scripts/check-live-node-encryption.sh --self-test                # no cluster
#   scripts/check-live-node-encryption.sh --kubeconfig <A> --dump    # capture
#
# Exit: 0 = fleet matches the declared posture
#       1 = divergence (a node's encryption state is not what it must be)
#       2 = setup / self-test failure / nothing to check
#
# Refs #5637 #960.

set -uo pipefail

# ── the declared posture ────────────────────────────────────────────────
# Verified against Cilium 1.19.3 on 2026-08-03 by reading the agent itself,
# not by reading the docs:
#   cilium-agent --help | grep node-encryption-opt-out-labels
#   →  (default "node-role.kubernetes.io/control-plane")
# Keep this in lockstep with docs/SECURITY.md §2.1. Changing it is a security-
# posture change and must be argued there, not here.
EXPECTED_OPT_OUT_SELECTOR="${EXPECTED_OPT_OUT_SELECTOR:-node-role.kubernetes.io/control-plane}"

KUBECONFIGS=()
CONTEXTS=()
FIXTURE=""
SELF_TEST_ONLY=0
DUMP=0

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig) KUBECONFIGS+=("$2"); shift 2 ;;
    --context)    CONTEXTS+=("$2");    shift 2 ;;
    --fixture)    FIXTURE="$2";        shift 2 ;;
    --self-test)  SELF_TEST_ONLY=1;    shift ;;
    --dump)       DUMP=1;              shift ;;
    -h|--help)
      sed -n '/^# Usage:/,/^# Refs/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--kubeconfig <path>]... [--context <ctx>]... [--fixture <f>] [--self-test] [--dump]" >&2
       exit 2 ;;
  esac
done

# ── the evaluator ───────────────────────────────────────────────────────
#
# Pure function of a snapshot, so the self-test exercises THE SAME code the
# live sweep uses — a classifier tested only through a copy of itself is not
# tested.
#
# Snapshot format (TSV, one line per cilium agent, '#' lines ignored):
#   region  pod  node  labels_csv  state  encrypt_node  enable_wireguard  selector
#
#   labels_csv        k=v pairs, comma separated (valueless label ⇒ bare key)
#   state             Enabled | OptedOut | anything else (⇒ violation)
#   encrypt_node      the region's cilium-config encrypt-node value
#   enable_wireguard  the region's cilium-config enable-wireguard value
#   selector          effective --node-encryption-opt-out-labels for the region;
#                     prefix "default:" when inherited (key absent from the
#                     ConfigMap) rather than pinned in it
evaluate() {
  EXPECTED_OPT_OUT_SELECTOR="$EXPECTED_OPT_OUT_SELECTOR" python3 -c '
import os, sys

EXPECTED = os.environ["EXPECTED_OPT_OUT_SELECTOR"]
rows, bad = [], []

def parse_labels(csv):
    out = {}
    for item in csv.split(","):
        item = item.strip()
        if not item:
            continue
        if "=" in item:
            k, v = item.split("=", 1)
            out[k.strip()] = v.strip()
        else:
            out[item] = ""
    return out

class Unparseable(Exception):
    pass

def matches(selector, labels):
    """Minimal k8s label-selector match: exists / = / == / !=.

    Anything else raises, and the caller turns that into a violation. A
    selector this cannot read is a selector whose blast radius is unknown,
    and an unknown blast radius on an encryption exclusion is never a pass."""
    if selector.strip() == "":
        return False              # empty selector excludes nothing
    for term in selector.split(","):
        term = term.strip()
        if not term:
            continue
        if "!=" in term:
            k, v = term.split("!=", 1)
            if labels.get(k.strip()) == v.strip():
                return False
        elif "==" in term:
            k, v = term.split("==", 1)
            if labels.get(k.strip()) != v.strip():
                return False
        elif "=" in term:
            k, v = term.split("=", 1)
            if labels.get(k.strip()) != v.strip():
                return False
        elif term.replace("-", "").replace(".", "").replace("/", "").replace("_", "").isalnum():
            if term not in labels:
                return False
        else:
            raise Unparseable(term)
    return True

for lineno, raw in enumerate(sys.stdin, 1):
    line = raw.rstrip("\n")
    if not line.strip() or line.lstrip().startswith("#"):
        continue
    f = line.split("\t")
    if len(f) != 8:
        bad.append("line %d: expected 8 tab-separated fields, got %d" % (lineno, len(f)))
        continue
    rows.append(dict(zip(
        ["region", "pod", "node", "labels", "state",
         "encrypt_node", "enable_wireguard", "selector"], f)))

if bad:
    for b in bad:
        print("SNAPSHOT-ERROR: %s" % b)
    sys.exit(2)

if not rows:
    print("SETUP-ERROR: no cilium agents in the snapshot — nothing was checked.")
    print("             An empty fleet is not a clean fleet.")
    sys.exit(2)

violations = []
print("%-10s %-16s %-58s %-9s %s" % ("REGION", "AGENT", "NODE", "STATE", "VERDICT"))

for r in rows:
    labels = parse_labels(r["labels"])
    sel_raw = r["selector"]
    sel = sel_raw[len("default:"):] if sel_raw.startswith("default:") else sel_raw
    inherited = sel_raw.startswith("default:")
    verdict = []

    if r["enable_wireguard"] != "true":
        violations.append("%s: cilium-config enable-wireguard=%r (want \"true\")"
                          % (r["region"], r["enable_wireguard"]))
        verdict.append("WIREGUARD-OFF")
    if r["encrypt_node"] != "true":
        violations.append("%s: cilium-config encrypt-node=%r (want \"true\")"
                          % (r["region"], r["encrypt_node"]))
        verdict.append("ENCRYPT-NODE-OFF")

    if sel != EXPECTED:
        violations.append(
            "%s: effective node-encryption-opt-out-labels=%r (declared posture is %r%s)"
            % (r["region"], sel, EXPECTED,
               "; inherited from the agent default" if inherited else "; pinned in cilium-config"))
        verdict.append("SELECTOR-DRIFT")

    if r["state"] not in ("Enabled", "OptedOut"):
        violations.append("%s/%s on %s: NodeEncryption state is %r — unreadable states are violations, not skips"
                          % (r["region"], r["pod"], r["node"], r["state"]))
        verdict.append("STATE-UNREADABLE")
    else:
        try:
            excluded = matches(sel, labels)
        except Unparseable as e:
            violations.append("%s: opt-out selector term %r is not parseable by this guard — "
                              "extend the matcher rather than widening the exclusion silently"
                              % (r["region"], str(e)))
            verdict.append("SELECTOR-UNPARSEABLE")
            excluded = None
        if excluded is not None:
            want = "OptedOut" if excluded else "Enabled"
            if r["state"] != want:
                if want == "Enabled":
                    why = ("node does NOT match the declared opt-out selector, so its "
                           "node-to-node traffic must be encrypted")
                else:
                    why = ("node matches the declared opt-out selector, so reporting Enabled "
                           "means the live fleet no longer matches the declared posture")
                violations.append("%s/%s on %s: NodeEncryption=%s, expected %s — %s"
                                  % (r["region"], r["pod"], r["node"], r["state"], want, why))
                verdict.append("MISMATCH")
            else:
                verdict.append("excluded-by-declared-selector" if excluded else "encrypted")

    print("%-10s %-16s %-58s %-9s %s"
          % (r["region"], r["pod"], r["node"][:58], r["state"], ",".join(verdict)))

enabled  = sum(1 for r in rows if r["state"] == "Enabled")
optedout = sum(1 for r in rows if r["state"] == "OptedOut")
print("")
print("agents=%d  NodeEncryption Enabled=%d  OptedOut=%d  regions=%d"
      % (len(rows), enabled, optedout, len(set(r["region"] for r in rows))))

if violations:
    print("")
    print("❌ NODE-ENCRYPTION DIVERGENCE (%d):" % len(violations))
    for v in violations:
        print("   - %s" % v)
    sys.exit(1)

print("✅ every agent reports the NodeEncryption state its node labels imply, "
      "under the declared opt-out selector %r." % EXPECTED)
sys.exit(0)
'
}

# ── Phase 0 — detector self-test (vacuity guard) ────────────────────────
#
# Runs before ANY live sweep. Both directions are proven: the golden fixture
# must come back GREEN (a detector that flags everything is as useless as one
# that flags nothing), and each divergent fixture must come back RED.
GOLDEN_SNAPSHOT='# region	pod	node	labels	state	encrypt_node	enable_wireguard	selector
region-a	cilium-97bpn	hw292-a-cp1	node-role.kubernetes.io/control-plane=true,node-role.kubernetes.io/etcd=true	OptedOut	true	true	default:node-role.kubernetes.io/control-plane
region-a	cilium-cfspn	hw292-a-w63852a	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane
region-a	cilium-mg727	hw292-a-w784020	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane
region-a	cilium-vlz6f	hw292-a-w5020a3	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane
region-b	cilium-s8mw5	hw292-b-cp1	node-role.kubernetes.io/control-plane=true,node-role.kubernetes.io/etcd=true	OptedOut	true	true	default:node-role.kubernetes.io/control-plane
region-b	cilium-28xdd	hw292-b-w8e097b	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane
region-b	cilium-nb5ll	hw292-b-wa9df1f	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane
region-b	cilium-tvdn6	hw292-b-w54fec6	kubernetes.io/os=linux	Enabled	true	true	default:node-role.kubernetes.io/control-plane'

self_test() {
  local out rc failures=0

  st_case() {
    local name="$1" want_rc="$2" snapshot="$3"
    out="$(printf '%s\n' "$snapshot" | evaluate 2>&1)"; rc=$?
    if [ "$rc" -ne "$want_rc" ]; then
      echo "SELF-TEST FAIL: $name — exit $rc, expected $want_rc" >&2
      printf '%s\n' "$out" | sed 's/^/    /' >&2
      failures=$((failures + 1))
    else
      echo "  ok  [exit $rc] $name"
    fi
  }

  echo "── Phase 0: detector self-test ─────────────────────────────────"

  # GREEN control — the real hw292 fleet shape must pass, or the guard is
  # merely a fleet-shaped tripwire that reddens on everything.
  st_case "hw292 golden fleet (2 cp OptedOut + 6 workers Enabled)" 0 "$GOLDEN_SNAPSHOT"

  # RED — a worker silently opted out. This is the shape #5637 was filed as.
  st_case "a worker reports OptedOut" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's/cilium-cfspn\(.*\)Enabled/cilium-cfspn\1OptedOut/')"

  # RED — a control-plane node reports Enabled: undeclared drift the other way.
  st_case "a control-plane node reports Enabled" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's/cilium-97bpn\(.*\)OptedOut/cilium-97bpn\1Enabled/')"

  # RED — the node lost the control-plane label but the agent still carries the
  # opt-out. This is exactly the "started before the setting applied and never
  # re-evaluated" case: a restart would mask it, so the guard must name it.
  st_case "stale agent: labels no longer imply the state it reports" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's|\(cilium-97bpn\thw292-a-cp1\t\)[^\t]*|\1kubernetes.io/os=linux|')"

  # RED — encryption switched off in one region while the other stays on.
  st_case "encrypt-node=false in one region" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's/\(cilium-28xdd.*Enabled\)\ttrue\ttrue/\1\tfalse\ttrue/')"

  # RED — WireGuard itself off in one region.
  st_case "enable-wireguard=false in one region" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's/\(cilium-nb5ll.*Enabled\)\ttrue\ttrue/\1\ttrue\tfalse/')"

  # RED — an agent whose state could not be read. Unreadable is not clean.
  st_case "an agent state is UNREADABLE" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's/cilium-tvdn6\(.*\)Enabled/cilium-tvdn6\1UNREADABLE/')"

  # RED — the exclusion set grew. Every agent still agrees with the selector,
  # so a uniformity-only check would pass this; the declared-posture check
  # does not.
  st_case "opt-out selector widened beyond the declared posture" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's|default:node-role.kubernetes.io/control-plane|default:node-role.kubernetes.io/control-plane,kubernetes.io/os=linux|')"

  # RED — a selector this matcher cannot read must not be assumed harmless.
  st_case "unparseable selector term" 1 \
    "$(printf '%s\n' "$GOLDEN_SNAPSHOT" | sed 's|default:node-role.kubernetes.io/control-plane$|default:role in (cp, worker)|')"

  # SETUP-ERROR — an empty fleet must never read clean.
  st_case "empty snapshot" 2 "# nothing here"

  # SETUP-ERROR — a malformed snapshot must never read clean either.
  st_case "malformed snapshot row" 2 "region-a	cilium-x	node-x	labels	Enabled"

  echo ""
  if [ "$failures" -ne 0 ]; then
    echo "❌ detector self-test FAILED ($failures case(s)) — refusing to report on a real fleet." >&2
    return 2
  fi
  echo "✅ detector self-test passed (11 cases, both directions)."
  return 0
}

self_test || exit 2
if [ "$SELF_TEST_ONLY" -eq 1 ]; then
  exit 0
fi

# ── live acquisition ────────────────────────────────────────────────────
if [ -n "$FIXTURE" ]; then
  echo ""
  echo "── replaying snapshot: $FIXTURE ────────────────────────────────"
  if [ "$FIXTURE" = "-" ]; then
    evaluate
  else
    [ -r "$FIXTURE" ] || { echo "ERROR: cannot read fixture $FIXTURE" >&2; exit 2; }
    evaluate < "$FIXTURE"
  fi
  exit $?
fi

if [ "${#KUBECONFIGS[@]}" -eq 0 ] && [ "${#CONTEXTS[@]}" -eq 0 ]; then
  echo "ERROR: give at least one --kubeconfig or --context (a one-region sweep of a" >&2
  echo "       two-region Sovereign is exactly the spot check this guard replaces)." >&2
  exit 2
fi

command -v kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found" >&2; exit 2; }

KC_TIMEOUT="${KC_TIMEOUT:-25}"
K_REQ_TIMEOUT="${K_REQ_TIMEOUT:-20s}"

# Read one region into snapshot rows on stdout. $1 = region tag, rest = the
# kubectl selector args for that region.
snapshot_region() {
  local region="$1"; shift
  local kargs=("$@")
  local cm nodes pods

  cm="$(timeout "$KC_TIMEOUT" kubectl "${kargs[@]}" --request-timeout="$K_REQ_TIMEOUT" \
        -n kube-system get cm cilium-config -o json 2>/dev/null)"
  if [ -z "$cm" ]; then
    echo "ERROR: $region: cannot read kube-system/cilium-config" >&2
    return 2
  fi
  nodes="$(timeout "$KC_TIMEOUT" kubectl "${kargs[@]}" --request-timeout="$K_REQ_TIMEOUT" \
           get nodes -o json 2>/dev/null)"
  pods="$(timeout "$KC_TIMEOUT" kubectl "${kargs[@]}" --request-timeout="$K_REQ_TIMEOUT" \
          -n kube-system get pods -l k8s-app=cilium -o json 2>/dev/null)"
  if [ -z "$nodes" ] || [ -z "$pods" ]; then
    echo "ERROR: $region: cannot list nodes / cilium agents" >&2
    return 2
  fi

  local encrypt_node enable_wg selector
  encrypt_node="$(printf '%s' "$cm" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("data",{}).get("encrypt-node",""))')"
  enable_wg="$(printf '%s' "$cm" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("data",{}).get("enable-wireguard",""))')"
  selector="$(printf '%s' "$cm" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("data",{}).get("node-encryption-opt-out-labels",""))')"
  # An absent key means the agent default is in force. Carry that fact into the
  # snapshot instead of pretending the value was declared: "inherited" is a
  # materially different posture from "pinned", and the report says which.
  if [ -z "$selector" ]; then
    selector="default:${EXPECTED_OPT_OUT_SELECTOR}"
  fi

  # pod<TAB>node for every cilium agent, plus its phase.
  local agents
  agents="$(printf '%s' "$pods" | python3 -c '
import json,sys
d=json.load(sys.stdin)
for p in d.get("items",[]):
    m=p.get("metadata",{}); s=p.get("status",{}); sp=p.get("spec",{})
    print("%s\t%s\t%s" % (m.get("name","?"), sp.get("nodeName","?"), s.get("phase","?")))
')"
  [ -n "$agents" ] || { echo "ERROR: $region: zero cilium agents found" >&2; return 2; }

  local labels_json
  labels_json="$(printf '%s' "$nodes" | python3 -c '
import json,sys
d=json.load(sys.stdin)
out={}
for n in d.get("items",[]):
    m=n.get("metadata",{})
    out[m.get("name","?")]={k:v for k,v in (m.get("labels") or {}).items()}
print(json.dumps(out))
')"

  while IFS=$'\t' read -r pod node phase; do
    [ -n "$pod" ] || continue
    local state labels
    labels="$(printf '%s' "$labels_json" | NODE="$node" python3 -c '
import json,os,sys
d=json.load(sys.stdin)
print(",".join("%s=%s" % (k,v) if v != "" else k
               for k,v in sorted(d.get(os.environ["NODE"],{}).items())))
')"
    if [ "$phase" != "Running" ]; then
      # Not a skip: an agent we cannot interrogate is an agent whose encryption
      # state we cannot claim. Re-run once the DaemonSet roll settles.
      state="NOT-RUNNING(${phase})"
    else
      state="$(timeout "$KC_TIMEOUT" kubectl "${kargs[@]}" --request-timeout="$K_REQ_TIMEOUT" \
               -n kube-system exec "$pod" -c cilium-agent -- cilium-dbg status 2>/dev/null \
               | sed -n 's/.*NodeEncryption: \([A-Za-z]*\).*/\1/p' | head -1)"
      [ -n "$state" ] || state="UNREADABLE"
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$region" "$pod" "$node" "${labels:-none}" "$state" \
      "${encrypt_node:-MISSING}" "${enable_wg:-MISSING}" "$selector"
  done <<< "$agents"
}

SNAPSHOT_FILE="$(mktemp)"
trap 'rm -f "$SNAPSHOT_FILE"' EXIT
ACQ_FAIL=0

for kc in ${KUBECONFIGS[@]+"${KUBECONFIGS[@]}"}; do
  region="$(basename "$kc" | sed 's/\.ya\?ml$//')"
  echo ""
  echo "── region snapshot: $region (kubeconfig $kc) ───────────────────"
  snapshot_region "$region" --kubeconfig "$kc" >> "$SNAPSHOT_FILE" || ACQ_FAIL=1
done

for ctx in ${CONTEXTS[@]+"${CONTEXTS[@]}"}; do
  echo ""
  echo "── region snapshot: $ctx (context) ─────────────────────────────"
  snapshot_region "$ctx" --context "$ctx" >> "$SNAPSHOT_FILE" || ACQ_FAIL=1
done

if [ "$ACQ_FAIL" -ne 0 ]; then
  echo "" >&2
  echo "❌ a region could not be read — a partial sweep is not a pass." >&2
  exit 2
fi

if [ "$DUMP" -eq 1 ]; then
  echo ""
  echo "── snapshot (TSV — replay with --fixture) ──────────────────────"
  cat "$SNAPSHOT_FILE"
fi

echo ""
echo "── verdict ─────────────────────────────────────────────────────"
evaluate < "$SNAPSHOT_FILE"
exit $?
