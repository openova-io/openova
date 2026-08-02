#!/usr/bin/env bash
# bp-powerdns — Hetzner DNS front-door render contract (#5086, Refs #4765 §854).
#
# The bp-powerdns 1.2.18 §854 flip (commit 3ce78f0d7) eradicated the anycast
# NodePort Service. On Huawei / kom4dc / contabo the front door is the Cilium
# LB-IPAM anycast VIP (node:53 == CP-node public IP) — unchanged. On Hetzner the
# LB-IPAM `sovereign-vip` pool is gated OFF (hcloud-ccm owns LBs) and hcloud-ccm
# cannot materialise the anycast Service, so node:53 never binds and the
# Sovereign lost its DNS front door. 1.2.21 (PR #5090) restored it §854-cleanly
# with `dnsdist.hostPortMode`: on Hetzner ONLY, dnsdist becomes a DaemonSet
# binding hostPort:53 on every node the tofu `dns` LB targets — hostPort, NEVER
# a NodePort.
#
# This test locks that render contract so a future edit cannot silently regress
# the Hetzner front door (drop hostPort, flip a NodePort back in, add
# hostNetwork, or narrow the DaemonSet off the CP node).
#
# Cases:
#   1. DEFAULT (Huawei/kom4dc/contabo): dnsdist => Deployment, its Service =>
#      ClusterIP, NO hostPort, NO NodePort — the anycast-VIP path is untouched.
#   2. DEFAULT: anycast Service => type=LoadBalancer + allocateLoadBalancerNodePorts:false,
#      and `nodePort: 0` stated EXPLICITLY on each port (#5348 — omitting the
#      field does NOT deallocate an already-assigned port).
#   3. HETZNER (dnsdist.hostPortMode=true): dnsdist => DaemonSet, hostPort:53 on
#      BOTH the UDP and TCP ports, tolerations `operator: Exists` (covers the CP
#      node — an LB :53 target), and NO hostNetwork (pod-netns :53 bind dodges
#      the systemd-resolved 127.0.0.53 stub).
#   4. §854: NEITHER render carries `type: NodePort` or a NONZERO `nodePort:`
#      anywhere. `nodePort: 0` is the k8s released-sentinel and is INERT;
#      allocateLoadBalancerNodePorts:false is the suppression flag, not a
#      NodePort. Both are excluded from the scan (#5348).
#   5. §854 hard guard: anycast.serviceType=NodePort FAILS the render (the
#      template's explicit `fail`), so a NodePort can never be reintroduced by
#      an overlay value.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# ── Case 1: default dnsdist workload is a Deployment with no host binding ──
echo "[bp-powerdns] Case 1: default dnsdist => Deployment, ClusterIP, no hostPort"
def_dnsdist=$("$helm" template smoke "$chart_dir" --show-only templates/dnsdist.yaml 2>&1)
if ! echo "$def_dnsdist" | grep -qE '^kind: Deployment$'; then
  echo "FAIL: default render must produce a dnsdist Deployment (LB-IPAM anycast-VIP path)"
  echo "$def_dnsdist" | grep -E '^kind:' || true
  exit 1
fi
if echo "$def_dnsdist" | grep -qE '^kind: DaemonSet$'; then
  echo "FAIL: default render must NOT be a DaemonSet — Huawei/kom4dc path would change"
  exit 1
fi
if echo "$def_dnsdist" | grep -qE 'hostPort:'; then
  echo "FAIL: default render must NOT bind a hostPort (that is the Hetzner-only path)"
  exit 1
fi
if ! echo "$def_dnsdist" | grep -qE '^  type: ClusterIP$'; then
  echo "FAIL: default dnsdist Service must be ClusterIP"
  exit 1
fi
echo "[bp-powerdns] Case 1: PASS"

# ── Case 2: default anycast Service is the §854 shared-VIP LoadBalancer ──
echo "[bp-powerdns] Case 2: anycast Service => LoadBalancer, allocateLoadBalancerNodePorts:false"
anycast=$("$helm" template smoke "$chart_dir" --show-only templates/anycast-endpoint.yaml 2>&1)
if ! echo "$anycast" | grep -qE '^  type: LoadBalancer$'; then
  echo "FAIL: anycast Service must be type=LoadBalancer (Cilium LB-IPAM shared VIP)"
  echo "$anycast" | grep -E '^  type:' || true
  exit 1
fi
if ! echo "$anycast" | grep -qE '^  allocateLoadBalancerNodePorts: false$'; then
  echo "FAIL: anycast Service must set allocateLoadBalancerNodePorts:false (§854 — zero nodePorts)"
  exit 1
fi
# §854: a NONZERO nodePort is the violation. `nodePort: 0` is the k8s
# "unspecified/released" sentinel and is INERT — it is also the deallocation
# instruction #5348 requires, because `allocateLoadBalancerNodePorts: false`
# does NOT free ports the apiserver already assigned, and a declarative apply
# that OMITS the field leaves the existing value untouched forever. The live
# mothership proved that: the chart set the flag, omitted nodePort, and
# powerdns-anycast still served 32015/31425.
#
# This assertion previously matched the FIELD, so it failed the very fix for
# the defect it guards — it blocked the bp-powerdns 1.2.23 publish on main and
# left the catalog-seed + bootstrap-kit pins hollow. Match a nonzero value, the
# same way scripts/check-no-nodeports.sh does.
if echo "$anycast" | grep -qE '^ *nodePort:[[:space:]]*[1-9][0-9]*'; then
  echo "FAIL: anycast Service carries a NONZERO nodePort (§854)"
  echo "$anycast" | grep -nE '^ *nodePort:' || true
  exit 1
fi
# Vacuity guard: `nodePort: 0` must be PRESENT. Without it the render is
# "compliant" only by omission, which is precisely the #5348 shape that let a
# live allocation survive. Absence is not compliance here.
if ! echo "$anycast" | grep -qE '^ *nodePort:[[:space:]]*0[[:space:]]*$'; then
  echo "FAIL: anycast Service must state nodePort: 0 EXPLICITLY on each port (§854/#5348)"
  echo "      Omitting the field does not deallocate an existing nodePort."
  exit 1
fi
echo "[bp-powerdns] Case 2: PASS"

# ── Case 3: Hetzner render flips dnsdist to a hostPort:53 DaemonSet ──
echo "[bp-powerdns] Case 3: hostPortMode=true => DaemonSet + hostPort:53 (UDP+TCP), tolerate-all, no hostNetwork"
hz_dnsdist=$("$helm" template smoke "$chart_dir" --set dnsdist.hostPortMode=true \
             --show-only templates/dnsdist.yaml 2>&1)
if ! echo "$hz_dnsdist" | grep -qE '^kind: DaemonSet$'; then
  echo "FAIL: hostPortMode=true must produce a dnsdist DaemonSet (every-node :53 binder)"
  echo "$hz_dnsdist" | grep -E '^kind:' || true
  exit 1
fi
hostport_count=$(echo "$hz_dnsdist" | grep -cE '^ *hostPort: 53([^0-9]|$)' || true)
if [ "$hostport_count" -ne 2 ]; then
  echo "FAIL: expected hostPort:53 on BOTH the UDP and TCP ports (2), got $hostport_count"
  exit 1
fi
if ! echo "$hz_dnsdist" | grep -qE '^ *- operator: Exists$'; then
  echo "FAIL: Hetzner DaemonSet must tolerate all taints (operator: Exists) so the CP node — an LB :53 target — is covered"
  exit 1
fi
if echo "$hz_dnsdist" | grep -qE 'hostNetwork:'; then
  echo "FAIL: dnsdist must NOT use hostNetwork (pod-netns :53 bind dodges the systemd-resolved 127.0.0.53 stub)"
  exit 1
fi
echo "[bp-powerdns] Case 3: PASS"

# ── Case 4: §854 — neither render carries a NodePort anywhere ──
echo "[bp-powerdns] Case 4: §854 — zero NodePorts in either render (full chart)"
for mode in "default" "hetzner"; do
  if [ "$mode" = "hetzner" ]; then
    full=$("$helm" template smoke "$chart_dir" --set dnsdist.hostPortMode=true 2>&1)
  else
    full=$("$helm" template smoke "$chart_dir" 2>&1)
  fi
  # allocateLoadBalancerNodePorts:false is the §854 SUPPRESSION flag, not a
  # NodePort — exclude it, then any residual nodePort/type:NodePort is a defect.
  hits=$(echo "$full" \
    | grep -vE 'allocateLoadBalancerNodePorts' \
    | grep -niE 'type:[[:space:]]*NodePort|^ *nodePort:[[:space:]]*[1-9][0-9]*' || true)
  if [ -n "$hits" ]; then
    echo "FAIL: §854 violation — NodePort rendered in $mode mode:"
    echo "$hits"
    exit 1
  fi
done
echo "[bp-powerdns] Case 4: PASS"

# ── Case 5: §854 hard guard — anycast.serviceType=NodePort must fail render ──
echo "[bp-powerdns] Case 5: anycast.serviceType=NodePort is a hard render failure (#4765)"
if "$helm" template smoke "$chart_dir" --set anycast.serviceType=NodePort \
     --show-only templates/anycast-endpoint.yaml >/dev/null 2>&1; then
  echo "FAIL: anycast.serviceType=NodePort must FAIL the render (§854), but it rendered"
  exit 1
fi
echo "[bp-powerdns] Case 5: PASS"

echo "[bp-powerdns] All Hetzner front-door render-contract cases PASS"
