# Catalyst Sovereign on Hetzner — canonical Phase 0 OpenTofu module.
#
# Per docs/ARCHITECTURE.md §10 + docs/SOVEREIGN-PROVISIONING.md §3-§4:
#   - This module provisions Phase 0 cloud resources on Hetzner.
#   - Cloud-init on the control-plane node installs k3s + bootstraps Flux +
#     installs Crossplane + provider-hcloud.
#   - Flux then takes over (Phase 1 hand-off): reconciles
#     clusters/<sovereign-fqdn>/ from the public OpenOva monorepo, installing
#     the 11-component bootstrap kit and bp-catalyst-platform umbrella.
#   - Crossplane adopts day-2 management of cloud resources after Phase 1.
#
# Per INVIOLABLE-PRINCIPLES.md:
#   - No hardcoded values (region, sizes, k3s flags all come from variables)
#   - No bespoke API calls (we use the canonical hcloud terraform provider)
#   - Phase 0 is OpenTofu, day-2 is Crossplane, GitOps is Flux, install unit is Blueprints

# ── Network: ONE hcloud_network PER REGION — no shared private net ────────
#
# Founder ruling 2026-05-15 (see docs/SOVEREIGN-MULTI-REGION-DOD.md A2):
#
#   > "fuck!!! You can never use private links between regions, why are you
#   >  wasting my time, how many times I need to explain the architecture!!!
#   >  irrespective from the same provider or different providers, your
#   >  wireguard always happens through the DMZ wireguard, never internal
#   >  shitty routing!!!!!!"
#
# Earlier slice-G1 design used ONE shared hcloud_network.main with a /24
# subnet per region inside the same /16. That gave every CP across every
# region addresses in 10.0.x.0/24 inside a Hetzner-routed Network — i.e.
# Hetzner's internal cross-zone routing carrying inter-region pod and
# control-plane traffic. The DoD contract forbids that explicitly.
#
# The replacement: ONE hcloud_network PER REGION, each carrying its OWN
# 10.0.0.0/16 (the /16 ranges are identical inside each network — they
# don't share a routing domain, so the collision is harmless and intended).
# Each network gets a single /24 subnet 10.0.1.0/24, so every CP across
# every region is at the SAME private IP 10.0.1.2 — uniform, intra-region-
# only. Inter-region traffic flows EXCLUSIVELY over Cilium WireGuard
# (UDP 51871) on the public IPs through each region's DMZ vCluster.
#
# Provider-agnostic by design (A6): whether the second/third region is
# Hetzner, AWS, or Huawei, the inter-region link is the same DMZ-WG
# overlay on public IPs. The Hetzner module never assumes a Hetzner peer
# on the other side.
#
# Resource address impact: the legacy `hcloud_network.main` and
# `hcloud_network_subnet.main` (singletons) are REPLACED by
# `hcloud_network.region[<key>]` and `hcloud_network_subnet.region[<key>]`
# (for_each maps keyed by region key — "primary" for regions[0], the
# slice-G1 "{cloudRegion}-{index}" form for secondaries). The legacy
# `hcloud_network_subnet.secondary` (for_each) is also replaced by the
# unified `hcloud_network_subnet.region` map. Any pre-2026-05-15 Sovereign
# state will plan a network-recreate on the next apply — by founder
# directive every Sovereign re-provisions cleanly from the fresh DoD
# contract, so the state-migration cost is consciously accepted.
locals {
  # Region key set: "primary" for regions[0] (driven by the singular
  # path) plus every secondary region key. Used as the for_each map of
  # hcloud_network.region / hcloud_network_subnet.region so EVERY region —
  # primary and secondary — has its own isolated /16.
  all_region_keys = concat(["primary"], [for k, _ in local.secondary_regions : k])

  # Per-region network zone. The primary region reads var.region; each
  # secondary reads its cloudRegion from local.secondary_regions. The
  # network zone is just a Hetzner placement hint for the subnet; the
  # subnet CIDR (10.0.1.0/24) is identical across regions because they
  # live in DIFFERENT networks.
  region_network_zones = merge(
    {
      primary = lookup(local.hetzner_network_zones, var.region, "eu-central")
    },
    {
      for k, r in local.secondary_regions :
      k => lookup(local.hetzner_network_zones, r.cloudRegion, "eu-central")
    }
  )

  # Per-region k3s cluster-cidr / service-cidr — must NOT collide across
  # ClusterMesh peers, otherwise inter-region pod-to-pod packets (DoD
  # gate D11) double-DNAT inside Cilium. Each region gets its own /16
  # off two non-overlapping /12 supernets:
  #   - cluster (pod) CIDR: 10.42+i.0/16 (10.42.0.0/16 .. 10.57.0.0/16 — 16 regions)
  #   - service CIDR:        10.96+i.0/16 (10.96.0.0/16 .. 10.111.0.0/16)
  # Index 0 = primary, secondary entries in stable insertion order of
  # local.secondary_regions. These are threaded into the k3s install
  # line via --cluster-cidr= / --service-cidr= (cloudinit-control-plane.tftpl).
  region_index = {
    for i, k in local.all_region_keys : k => i
  }
  region_cluster_cidr = {
    for k, _ in local.region_index :
    k => format("10.%d.0.0/16", 42 + local.region_index[k])
  }
  region_service_cidr = {
    for k, _ in local.region_index :
    k => format("10.%d.0.0/16", 96 + local.region_index[k])
  }

  # Per-region canonical 4-segment region label per docs/NAMING-CONVENTION.md
  # §2.1: `{prov}-{reg}-{bb}-{env_type}`. For the Hetzner module the prov
  # prefix is `hz`; reg is the cloudRegion with trailing digits stripped
  # (`fsn1` → `fsn`, `nbg1` → `nbg`, `sin` → `sin`); bb is the building-
  # block tag `rtz` (regional-tier-zero) and env_type is `prod`. Each
  # cluster's k3s nodes label themselves with this canonical tag via
  # --node-label openova.io/region=<canonical>. The label is consumed by:
  #   - qa-fixtures Pods (CNPGPair primary/replica, status seeder Jobs,
  #     qa-wp Application) — their hard nodeAffinity targets
  #     `openova.io/region in [<primary>]` so qa fixtures schedule on the
  #     primary's nodes only.
  #   - the Continuum DR controller's region awareness.
  #   - the OpenovaFlow canvas's per-region grouping.
  #
  # Before this local existed, cloudinit-control-plane.tftpl hardcoded the
  # literal `hz-fsn-rtz-prod` into the k3s --node-label flag. That broke
  # every multi-region Sovereign whose primary was NOT fsn1 (e.g. omantel
  # primary=fsn1 worked by luck; t114-omani-works primary=hel1 + nbg1
  # secondary surfaced the bug — every CP node carried `hz-fsn-rtz-prod`
  # regardless of its actual region, breaking canvas region grouping and
  # any downstream selector targeting the real region). DoD A6 demands
  # provider-agnostic shape — the `hz` prefix is correct ONLY because
  # this file lives under infra/hetzner/; future infra/aws/ + infra/huawei/
  # modules will derive `aw` / `hw` in their own per-module locals.
  region_canonical_label = merge(
    {
      primary = format("hz-%s-rtz-prod", replace(var.region, "/[0-9]+/", ""))
    },
    {
      for k, r in local.secondary_regions :
      k => format("hz-%s-rtz-prod", replace(r.cloudRegion, "/[0-9]+/", ""))
    }
  )
}

resource "hcloud_network" "region" {
  for_each = toset(local.all_region_keys)

  name     = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.key}-net"
  ip_range = "10.0.0.0/16"
  labels = {
    "catalyst.openova.io/sovereign"  = var.sovereign_fqdn
    "catalyst.openova.io/region-key" = each.key
  }
}

resource "hcloud_network_subnet" "region" {
  for_each = toset(local.all_region_keys)

  network_id   = hcloud_network.region[each.key].id
  type         = "cloud"
  network_zone = local.region_network_zones[each.key]
  # Same /24 inside every region's /16. Each subnet sits in its OWN
  # hcloud_network, so addresses don't collide across regions. CP at .2,
  # workers at .10+, LB pinned at .254 — uniform across regions.
  ip_range = "10.0.1.0/24"
}

# ── Firewall: 80/443 + 6443 + ICMP + DMZ-WG 51871 open; 22 only when ssh_allowed_cidrs set ─

resource "hcloud_firewall" "main" {
  name = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-fw"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "6443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # DNS/53 — open to the world so the Sovereign's PowerDNS authoritative
  # server is reachable from Let's Encrypt resolvers (DNS-01 challenge) and
  # from the public internet for subdomain NS delegation. Both TCP and UDP
  # are required: TCP for zone transfers and large responses, UDP for
  # standard query traffic. The LB service (hcloud_load_balancer_service.dns)
  # forwards :53 → node:53 on the standard port — §854 / #4765: the
  # powerdns-anycast Service is type=LoadBalancer with
  # allocateLoadBalancerNodePorts:false (bp-powerdns ≥1.2.18 fails render on
  # NodePort), so no node:3xxxx hop exists to forward to. The node:53 LISTENER
  # on Hetzner is the bp-powerdns ≥1.2.21 dnsdist DaemonSet (hostPortMode, gated
  # ON via the Hetzner cloud-init POWERDNS_DNSDIST_HOSTPORT substitute): it binds
  # hostPort:53 on every node (§854 hostPort, NEVER a nodePort). The Cilium
  # LB-IPAM sovereign-vip anycast VIP stays dead on Hetzner (gated OFF; no
  # hcloud-ccm fallback) — hostPort is the front door. LIVE-GATED (#5086):
  # unproven until a real Hetzner prov confirms `dig @<node> <fqdn>` resolves.
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "53"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "53"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  # 🛑 #4765 (founder 2026-07-03, "FUCK THE NODEPORTS!!!") — the former
  # :30053 "powerdns NodePort" TCP+UDP rules are ERADICATED. bp-powerdns
  # ≥1.2.18 hard-fails on anycast serviceType=NodePort and renders
  # allocateLoadBalancerNodePorts:false, so node:30053 can never exist on a
  # fresh prov. Public DNS rides the standard :53 rules above.

  # Cilium clustermesh-apiserver dial :2379 (#4765 — replaces the forbidden
  # :32379 NodePort rule; mirrors infra/providers/huawei
  # `ingress_clustermesh_2379`). Cross-region ClusterMesh peers dial the
  # clustermesh-apiserver Service VIP on :2379 directly — the nodePort
  # fallback in catalyst-api's clustermesh connect was DELETED in #4766
  # (hard-fail), and the slot-01 bootstrap-kit postRenderer suppresses
  # nodePort allocation on the Service unconditionally, so :32379 can never
  # be bound on any fresh prov.
  #
  # 1. Hetzner LB → backend health checks probe the standard :2379 the
  #    Service serves; without this rule the probe is dropped → LB marks
  #    target unhealthy → external clustermesh clients see "unexpected eof
  #    while reading" at TLS handshake → cilium-dbg status reports
  #    `0/N remote clusters ready`.
  # 2. Cross-region peer agents connect to the remote clustermesh-apiserver
  #    via the LB. Hetzner LB use-private-ip is not viable because the
  #    per-region LB has no private-network attachment by default and our
  #    network topology pins one private /24 per region; the LB->backend hop
  #    must transit the public path. The cross-region case is by design
  #    public per DoD A2 (inter-region link = WireGuard over public IPs
  #    ALWAYS).
  #
  # Security: clustermesh-apiserver requires mTLS — peer agents present
  # a client cert signed by the peer cluster's cilium-ca. Anonymous
  # connections are immediately rejected at TLS handshake. The
  # firewall is therefore not the security boundary here; mTLS is.
  # (History: the D11 chain — PR #1525 regionKeyFromSpec, #1528 clusterName
  # derivation, #1530 peer cert with peer's CA, #1536 hostAlias pattern —
  # was proven on t129 with the pre-#4765 :32379 shape.)
  rule {
    direction   = "in"
    protocol    = "tcp"
    port        = "2379"
    source_ips  = ["0.0.0.0/0", "::/0"]
    description = "Cilium clustermesh-apiserver VIP dial :2379 (mTLS-protected; #4765 — replaces the forbidden NodePort)"
  }

  # Cilium WireGuard inter-region node encryption (DMZ-WG). Per DoD A2
  # (docs/SOVEREIGN-MULTI-REGION-DOD.md), inter-region traffic flows
  # EXCLUSIVELY over Cilium WireGuard on the DMZ vCluster's public IPs,
  # NEVER over Hetzner's internal network. Cilium's default WG port is
  # UDP 51871 (config: `encryption.wireguard.userspaceFallback=false`
  # + `encryption.type=wireguard` in bp-cilium). Without this rule, the
  # WG mesh between regions cannot form on a fresh provision and DoD
  # gate D11 (inter-region pod-to-pod packet test) fails immediately.
  # Open to the world because each region's CP/worker public IP rotates
  # at provision time and the catalyst-api does not know the public IP
  # of sister-region peers ahead of time — Cilium's node-discovery
  # auth + WG static-key crypto is the actual security boundary, not
  # the firewall source filter.
  rule {
    direction   = "in"
    protocol    = "udp"
    port        = "51871"
    source_ips  = ["0.0.0.0/0", "::/0"]
    description = "Cilium WireGuard inter-region node encryption (DMZ-WG)"
  }

  # SSH (22) is intentionally NOT open to the world. When ssh_allowed_cidrs is
  # set, we add a narrow rule for those operators only; otherwise the rule is
  # omitted entirely and break-glass is via Hetzner Console (out-of-band).
  # Operators tighten/widen this via Crossplane Composition once Phase 1
  # finishes — see infra/hetzner/README.md §"Firewall rules".
  dynamic "rule" {
    for_each = length(var.ssh_allowed_cidrs) > 0 ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = var.ssh_allowed_cidrs
    }
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

# ── SSH key: from wizard input, never auto-generated ──────────────────────

resource "hcloud_ssh_key" "main" {
  name       = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}"
  public_key = var.ssh_public_key
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

# ── Control plane: 1 server (or 3 if ha_enabled), with k3s cloud-init ─────

locals {
  control_plane_count = var.ha_enabled ? 3 : 1

  # #4053 console-isolation gate (Refs #4431 #4212). When "true" (default) the
  # dedicated console LB stack is created; "false" drops it (console front
  # doors collapse onto hcloud_load_balancer.main). The console LB-target
  # resources fan out per node, so their counts multiply by the gate.
  console_isolation_on        = var.console_isolation_enabled == "true"
  console_cp_target_count     = local.console_isolation_on ? local.control_plane_count : 0
  console_worker_target_count = local.console_isolation_on ? var.worker_count : 0

  # Wildcard cert ClusterIssuer selector (Fix #176 — qa-loop iter-1 LE
  # PROD rate-limit unblock for clusters/_template/sovereign-tls/cilium-
  # gateway-cert.yaml). The sovereign-tls Kustomization's
  # postBuild.substitute WILDCARD_CERT_ISSUER below resolves to:
  #   - letsencrypt-dns01-staging-powerdns  when qa_test_session_enabled (or
  #     wildcard_cert_use_staging) is "true" → fast iteration, no rate limit
  #   - letsencrypt-dns01-prod-powerdns     when "false" → real-trusted cert
  # Both ClusterIssuers are shipped by bp-cert-manager-powerdns-webhook
  # (bootstrap-kit slot 49). Without this, cilium-gateway-cert.yaml
  # always hits PROD even on qaTestEnabled Sovereigns, and the 5/168h
  # rate limit pins the Gateway to a `Ready=False` Certificate.
  wildcard_cert_issuer = var.wildcard_cert_use_staging == "true" ? "letsencrypt-dns01-staging-powerdns" : "letsencrypt-dns01-prod-powerdns"

  # ── Cilium Gateway listeners per parent zone (issue #831, parent #827) ───
  # The Sovereign supports N parent zones (primary + 0..N org-pool). For
  # each zone the Cilium Gateway must declare a listener pair (HTTPS:443
  # + HTTP:80) hostnamed `*.<zone>` so cilium-envoy programs the SDS
  # subscription against the per-zone cert. Without these listeners,
  # tenant URLs under non-primary parent zones (e.g. wp-foo.omani.homes)
  # hit the envoy default fallback cert and TLS-mismatch.
  #
  # parent_domains_yaml is a YAML inline-array literal (see variables.tf).
  # yamldecode() turns it into a list of objects we can iterate at plan
  # time. The result is a JSON-flow array string injected verbatim into
  # clusters/_template/sovereign-tls/cilium-gateway.yaml via Flux
  # postBuild.substitute ${PARENT_DOMAINS_LISTENERS_YAML}. YAML accepts
  # JSON-flow syntax (`[{...}, {...}]`) as a native inline-flow array,
  # so the substituted value parses correctly under spec.listeners.
  #
  # The single-zone fallback (var.parent_domains_yaml empty) renders one
  # pair derived from sovereign_fqdn — keeps legacy single-zone
  # provisioning paths plan-clean (one zone → two listeners).
  parent_domains_decoded = yamldecode(coalesce(
    var.parent_domains_yaml,
    format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
  ))

  # Render the Gateway listeners block as a YAML inline-flow array (JSON-
  # compatible syntax — `[{key: val, ...}, {...}]`). Each parent zone
  # produces two entries:
  #   - HTTPS listener on port 443 hostnamed `*.<zone>` with certificateRefs
  #     targeting `sovereign-wildcard-tls-<sanitised-zone>` (the chart's
  #     sovereign-wildcard-certs.yaml writes a Secret of that exact name).
  #   - HTTP  listener on port 80 hostnamed `*.<zone>` for /.well-known/
  #     and ACME HTTP-01 challenge paths.
  #
  # Listener-NAMING contract (t20 critical fix #3):
  #   - SINGLE parent zone (the common case)  → listener names are the
  #     bare strings `https` / `http`. Every platform chart's HTTPRoute
  #     (harbor, keycloak, grafana, gitea, openbao, powerdns, stalwart-
  #     tenant) hardcodes `parentRefs[0].sectionName: https`. If the
  #     listener is renamed to `https-<sanitised-zone>` for a single-zone
  #     Sovereign, every HTTPRoute reports `Accepted=False
  #     NoMatchingListener` and the Sovereign Console / Harbor / Keycloak
  #     etc. are unreachable at the Gateway. Keeping the bare names for
  #     the single-zone case is the safer rollback. (Was broken between
  #     PR #1640 and this fix.)
  #   - MULTIPLE parent zones (Org pool present) → unique names per zone
  #     (`https-<sanitised-zone>` / `http-<sanitised-zone>`) so the
  #     Gateway controller programs all of them (duplicate listener names
  #     produce a Conflicting status condition and silently skip every
  #     duplicate but the first). For multi-zone Sovereigns, charts whose
  #     HTTPRoutes must attach under a non-primary zone need their
  #     sectionName overridden via values.yaml — that is a per-tenant
  #     concern and out of scope here.
  #
  # Why inline-flow not block-style:
  #   - clusters/_template/sovereign-tls/cilium-gateway.yaml uses the
  #     placeholder as a SCALAR value (`listeners: ${PARENT_DOMAINS_
  #     LISTENERS_YAML}`). A block-style multi-line value would break
  #     kustomize's pre-substitute YAML parse (the placeholder lands
  #     mid-document and kustomize doesn't see the structure yet).
  #   - Inline-flow is YAML-spec valid AND kustomize-build accepts the
  #     scalar `${VAR}` until Flux's postBuild.substitute swaps it for
  #     the materialised list. Same pattern PARENT_DOMAINS_YAML uses
  #     (bp-powerdns slot 11 zones field).
  #
  # jsonencode() is used downstream (in cloudinit-control-plane.tftpl)
  # to wrap this string for safe storage inside the Flux Kustomization's
  # substitute map. JSON-encoded scalars are valid YAML; this avoids
  # quoting hell when the listener data contains characters cloud-init
  # YAML would otherwise mis-parse.
  parent_domains_single_zone = length(local.parent_domains_decoded) == 1

  # Per-prov dashed FQDN, e.g. "t28.omani.works" → "t28-omani-works".
  # This is the SAME suffix clusters/_template/sovereign-tls/cilium-gateway-
  # cert.yaml uses for its per-prov Certificate (`sovereign-wildcard-tls-
  # ${SOVEREIGN_FQDN_DASHED}`) and the same one cilium-envoy-tls-restart-
  # job.yaml grants the cert-manager CSR-signing job RBAC for. Keeping a
  # single named local here so every listener block stays in lockstep with
  # the cert resource without re-deriving the dashed form inline.
  sovereign_fqdn_dashed = replace(var.sovereign_fqdn, ".", "-")

  # TBD-A29 (issue #1883) — wildcard cert LE rate-limit on parent zone.
  # Each listener's certificateRefs now points at the PER-PROV cert
  # (`sovereign-wildcard-tls-${SOVEREIGN_FQDN_DASHED}`) rendered by
  # clusters/_template/sovereign-tls/cilium-gateway-cert.yaml, NOT the
  # parent-zone wildcard `sovereign-wildcard-tls-<sanitised(parent)>`
  # the chart's templates/sovereign-wildcard-certs.yaml used to mint.
  #
  # Why: Let's Encrypt's "5 New Certificates per Exact Set of Identifiers
  # per 168h" caps the parent-zone identifier set `[*.omani.works,
  # omani.works]` across EVERY prov on that parent. After ~5 wipe+reprov
  # cycles on omani.works the Gateway listener pinned to a `Ready=False`
  # Certificate (cert-manager spun the order forever, LE returned
  # `urn:ietf:params:acme:error:rateLimited`). Per-prov identifier sets
  # (`[console.t28.omani.works, auth.t28.omani.works, ...]`) get their
  # OWN 5/168h bucket per Sovereign, so reprovs no longer share the LE
  # ceiling with sibling Sovereigns under the same parent zone. This is
  # the same per-name model the legacy cilium-gateway-cert.yaml already
  # codified in 2026-05-15 — the listener was the last piece still
  # tied to the parent-zone wildcard.
  #
  # Hostname semantics for the PARENT-ZONE listener: `hostname: *.<parent>`
  # matches exactly one label depth (`foo.omani.works`), so Org-tenant
  # 1-label subdomains on the parent zone still hit it. cilium-envoy's
  # SNI dispatch picks the per-prov cert whose SAN list includes the
  # requested hostname. For PER-PROV 2-label-deep operator FQDNs
  # (`console.t28.omani.works` under shared parent `omani.works`), see
  # TBD-A32 (#1886) below — the parent-zone listener does NOT catch
  # those; a per-prov `*.<sovereign_fqdn>` listener is added.
  # Tenant URLs under non-primary parent zones (e.g. wp-foo.omani.homes)
  # remain out of scope here — that path needs explicit per-tenant
  # cert wiring under #831, not parent-zone wildcards.

  # ── TBD-A32 (#1886) — per-prov 2-label wildcard listener ─────────────────
  # Closes #2118 (2026-05-20): the listener YAML was historically
  # materialised here (locals.parent_domains_listeners_yaml,
  # locals.per_prov_listeners, locals.parent_domains_includes_sovereign_fqdn)
  # and threaded into cloud-init as an inline postBuild.substitute value.
  # That scaled O(N) with parent-zone count and pushed cloud-init over
  # Hetzner's 32 KiB user_data cap on 4-zone Org-pool Sovereigns (t39
  # audit, 2026-05-20: 33,656 bytes for omantel.biz + 3-zone .omani.X).
  #
  # The listener block is now rendered inside bp-catalyst-platform's
  # templates/sovereign-tls-vars-cm.yaml from .Values.parentZones +
  # .Values.global.sovereignFQDN — same Helm template that already owns
  # the per-zone Certificate render shape. The chart writes a ConfigMap
  # `flux-system/sovereign-tls-vars` whose `PARENT_DOMAINS_LISTENERS_YAML`
  # key is read by the sovereign-tls Kustomization via
  # `postBuild.substituteFrom` (cloud-init writes that Kustomization).
  #
  # The TBD-A32 collision guard (sovereign_fqdn ∈ parent_zone_names →
  # skip per-prov pair to avoid duplicate listener-name Conflict) is
  # preserved in the Helm template as a `range` over parentZones that
  # sets a `$fqdnInZones` boolean.
  #
  # Single-zone fallback (legacy Sovereigns shipping parentZones empty)
  # is preserved in the Helm template as a `if eq (len $zones) 0 →
  # list (dict "name" $fqdn "role" "primary")` substitution — matches
  # the historical `coalesce(var.parent_domains_yaml, format("[{name:
  # \"%s\", role: \"primary\"}]", var.sovereign_fqdn))` shape.

  # ── Effective singular-path SKU selection (Fix #157) ─────────────────────
  # When qa_fixtures_enabled='true', the Sovereign is a QA-loop matrix
  # consumer carrying the full bp-* stack PLUS qaFixtures (Continuum +
  # CNPGPair + status-seeder Jobs + bp-keycloak/harbor/cnpg/openbao race).
  # The production cpx22 CP / cpx32 worker defaults OOM-cascade on first
  # apply (validated 12 of 12 fresh provisions in the 2026-05-10 bounded-
  # cycle session — see memory/session_2026_05_10_bounded_cycle_handover.md).
  # Auto-flip the SKUs to qa_control_plane_size / qa_worker_size for QA
  # provisions WITHOUT touching customer-facing Sovereign defaults
  # (qa_fixtures_enabled='false' → coalesce returns var.control_plane_size /
  # var.worker_size verbatim, preserving the cpx22/cpx32 baseline).
  #
  # coalesce() guards the empty-string corner case the worker_size schema
  # already permits (solo Sovereign, worker_count=0): an empty
  # qa_worker_size would otherwise short-circuit to "" — coalesce() falls
  # back to the production default in that mode.
  qa_mode = var.qa_fixtures_enabled == "true"
  # Fix #183: body's control_plane_size / worker_size win over QA defaults
  # when present. Previously `coalesce(var.qa_*, var.*)` returned the QA
  # default whenever it was non-empty, silently downgrading a body's
  # explicit cpx42 → cpx32. Now QA defaults only kick in when the body
  # left the SKU empty (zero-override prov / legacy QA path). On customer
  # Sovereigns (qa_fixtures_enabled='false') the QA defaults are never
  # considered. See provisioner.go writeTfvars — body-supplied SKUs are
  # emitted as JSON only when non-empty, so var.control_plane_size /
  # var.worker_size already inherit variables.tf defaults when the body
  # left them blank; coalesce-with-body-first is the right precedence.
  effective_cp_size     = local.qa_mode ? coalesce(var.control_plane_size, var.qa_control_plane_size) : var.control_plane_size
  effective_worker_size = local.qa_mode ? coalesce(var.worker_size, var.qa_worker_size) : var.worker_size

  # k3s deterministic bootstrap token derived from project ID + sovereign FQDN.
  # Workers join with this; k3s rotates it after first join.
  k3s_token = sha256("${var.hcloud_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")

  # ── Hetzner location → network-zone lookup ───────────────────────────────
  # Hetzner Network Subnets are zoned (eu-central / us-east / us-west /
  # ap-southeast). The mapping is documented in the hcloud Terraform
  # provider's hcloud_network_subnet docs. Adding a new Hetzner location
  # means updating this lookup AND the var.region validation in
  # variables.tf in the same PR (so the multi-region for_each below cannot
  # land in a location whose zone we can't resolve).
  # Hetzner /v1/locations as of 2026-05-11: hel1 is in eu-central
  # (not eu-north — Fix #179 incorrectly used "eu-north" and tofu apply
  # FATALed with `network zone does not exist` on prov #32). Verified
  # via `curl https://api.hetzner.cloud/v1/locations` — single source
  # of truth. The original prov #29/#30 "IP not available" on
  # secondary[hel1-1] was misdiagnosed; both regions live in eu-central
  # and the failure has a different root cause to be re-traced.
  hetzner_network_zones = {
    fsn1 = "eu-central"
    nbg1 = "eu-central"
    hel1 = "eu-central"
    ash  = "us-east"
    hil  = "us-west"
    sin  = "ap-southeast"
  }

  # Legacy singular-region path's network zone — preserved as a separate
  # local so existing single-region applies (every Sovereign provisioned
  # before slice G1) keep producing identical plans. The multi-region
  # for_each path below uses the same lookup table per region entry.
  network_zone = lookup(local.hetzner_network_zones, var.region, "eu-central")

  # ── Multi-region overlay (slice G1, EPIC-0 #1095) ────────────────────────
  # Slice G1 wires every entry in var.regions[] end-to-end. The legacy
  # singular-region path (var.region + var.control_plane_size + var.worker_*
  # + count = local.control_plane_count) stays untouched so existing
  # Sovereign state (omantel, otech*) continues to plan-clean — those
  # resources keep their addresses (hcloud_server.control_plane[0],
  # hcloud_load_balancer.main, etc).
  #
  # New regions (regions[1+]) are realised via a parallel for_each set of
  # resources keyed by a deterministic per-region key. The legacy path
  # owns regions[0] semantically — when var.regions is non-empty,
  # provisioner.go writes regions[0]'s SKU/size into the singular fields
  # so the singular path drives regions[0] verbatim. Slice G1 layers in
  # the additional regions; slice G3 wires Cilium ClusterMesh between
  # them (out of scope here).
  #
  # Why a hybrid (singular path + for_each overlay) instead of a full
  # for_each refactor: a full refactor would change every existing
  # resource address from hcloud_server.control_plane[0] to
  # hcloud_server.control_plane["mgmt"], forcing every running Sovereign
  # to run `tofu state mv` for ~12 resources or face destructive
  # plan-time recreates. The brief explicitly bans that. Hybrid is
  # additive: secondary-region resources are NEW addresses that no
  # existing state carries, so legacy `tofu plan` outputs are unchanged
  # for any Sovereign whose request body has len(regions) ≤ 1.
  #
  # Key shape: "{cloudRegion}-{index}". Including the index protects
  # against same-region duplicates (e.g. fsn1 mgmt + fsn1 dataplane in
  # the same Sovereign — legal, even if uncommon). Index starts at 1
  # because regions[0] is owned by the singular path.
  secondary_regions = {
    for i, r in var.regions :
    "${r.cloudRegion}-${i}" => r
    if i > 0 && r.provider == "hetzner"
  }

  # Per-secondary-region Cilium ClusterMesh peer anchors (#1101 EPIC-6).
  # Auto-derive cluster.name as `<sovereign-stem>-<region-code-no-digits>`
  # (e.g. omantel + hel1 -> omantel-hel) when the operator left
  # var.cluster_mesh_name empty, OR honour the explicit override per
  # region (RegionSpec.cluster_mesh_name when present).  cluster.id is
  # derived as `var.cluster_mesh_id + 1 + index(secondary_regions, k)`
  # so the primary region keeps id=cluster_mesh_id and each peer claims
  # the next free id within the mesh — matching the registry convention
  # in docs/CLUSTERMESH-CLUSTER-IDS.md (mesh-omantel: fsn=1, hel=2, ...).
  # When var.cluster_mesh_name is empty AND there are no secondary
  # regions, the peer name remains empty (single-cluster Sovereign).
  # `coalesce` is intentionally NOT used here: it errors when every
  # argument is empty (e.g. tofu test mock with var.cluster_mesh_name="").
  # The conditional yields "" when both the per-region override AND the
  # umbrella var are empty (the not-in-mesh path).
  secondary_region_cluster_mesh_name = {
    for k, r in local.secondary_regions :
    k => try(r.clusterMeshName, "") != "" ? r.clusterMeshName : (
      var.cluster_mesh_name == "" ? "" : format(
        "%s-%s",
        split(".", var.sovereign_fqdn)[0],
        replace(r.cloudRegion, "/[0-9]+/", "")
      )
    )
  }
  secondary_region_cluster_mesh_id = {
    for k, _ in local.secondary_regions :
    k => var.cluster_mesh_id == 0 ? 0 : (
      var.cluster_mesh_id + 1 + index(keys(local.secondary_regions), k)
    )
  }

  # Per-secondary-region first-IP for control plane. Every region now has
  # its OWN hcloud_network with its OWN 10.0.1.0/24 subnet — so every
  # secondary CP sits at 10.0.1.2, the same as the primary CP. This was
  # cidrhost(local.secondary_region_subnets[k], 2) under the old shared-
  # /16 design; with per-region networks the subnet is uniform.
  secondary_region_cp_ips = {
    for k, _ in local.secondary_regions :
    k => "10.0.1.2"
  }

  # GHCR pull token + the dockerconfigjson `auth` field, computed once here
  # so the cloud-init template stays a clean string-interpolation.
  #
  # The dockerconfigjson Secret format wants a top-level `auth` value of
  # base64("<username>:<token>"). Computing it inside the templatefile()
  # via OpenTofu's `base64encode()` would force the template to know about
  # OpenTofu functions; deriving it here keeps the template a pure heredoc
  # that emits valid YAML regardless of who renders it (production
  # provisioner, integration test harness, `tofu console`).
  #
  # `ghcr_pull_username` is the GHCR convention: the username is fixed for
  # token-based auth — GitHub validates the token, not the username. We use
  # `openova-bot` as a stable identity string so audit logs in CI / GHCR
  # pulls show a recognisable principal.
  ghcr_pull_username = "openova-bot"
  ghcr_pull_auth_b64 = base64encode("${local.ghcr_pull_username}:${var.ghcr_pull_token}")

  # Cloud-init for the control-plane node — installs k3s, then Flux, then
  # writes the Flux GitRepository + Kustomization that points at
  # clusters/<sovereign-fqdn>/ in the public OpenOva monorepo.
  # ── Hetzner Object Storage S3 endpoint (Phase 0b — issue #371) ──────────
  # Composed once here from the chosen region so the cloud-init template
  # and the Object Storage K8s Secret it writes both reference the same
  # canonical URL. Hetzner's public docs pin the format to
  # `https://<region>.your-objectstorage.com`. Per
  # docs/INVIOLABLE-PRINCIPLES.md #4 the URL is composed from the
  # operator's region choice, never hardcoded in cloudinit-control-plane.tftpl.
  object_storage_endpoint = "https://${var.object_storage_region}.your-objectstorage.com"

  # ── #3145 cloud-agnostic bootstrap: Hetzner provider-injected strings ──
  # The Kubernetes bootstrap lives in infra/providers/_shared/cloudinit-
  # control-plane.tftpl (ONE template, every cloud). These locals carry the
  # ONLY Hetzner-specific pieces (the "hard dependency" exceptions, plan §5),
  # injected into the shared template as string vars. Everything else is
  # identical across clouds. NEVER re-add bootstrap logic here.

  # registry mirror — Hetzner routes upstream pulls through harbor.openova.io
  # pull-through proxy projects (containerd reads /etc/rancher/k3s/registries.yaml).
  registry_mirror_yaml_hetzner = <<-EOT
    mirrors:
      "docker.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-dockerhub/$1"
      "quay.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-quay/$1"
      "gcr.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-gcr/$1"
      "registry.k8s.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-k8s/$1"
      "ghcr.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-ghcr/$1"
      # #3913: route the two remaining upstream registries through Harbor too,
      # so the bootstrap-time mirror set MATCHES the proven post-cutover set in
      # self-sovereign-cutover/04-registry-pivot-daemonset.yaml (ghcr/docker/
      # k8s/gcr/quay/xpkg/ecr). Before this, crossplane provider packages from
      # xpkg.upbound.io (e.g. provider-hcloud, the bp-mgmt-vcluster controllers)
      # and any public.ecr.aws image pulled DIRECT from upstream — exactly the
      # un-proxied-pull class #3913 closes. The proxy-xpkg + proxy-ecr Harbor
      # projects already exist on harbor.openova.io (the 04-pivot rewrite proves
      # it), so this is a pure config add with no new infra.
      "xpkg.upbound.io":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-xpkg/$1"
      "public.ecr.aws":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-ecr/$1"
    configs:
      "harbor.openova.io":
        auth:
          username: "robot$openova-bot"
          password: "${var.harbor_robot_token}"
  EOT

  # node external-IP discovery — Hetzner metadata service (hard dependency:
  # Hetzner populates 169.254.169.254/hetzner/v1/metadata/public-ipv4).
  node_external_ip_cmd_hetzner = "curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/public-ipv4"

  # node PRIVATE-IP discovery — Hetzner per-region /24 deterministically puts
  # the CP at 10.0.1.2 (the prelude brings the private NIC up first). Echoed
  # (no detection needed) — k3s --node-ip + cilium k8sServiceHost use it.
  node_ip_cmd_hetzner = "echo 10.0.1.2"

  # providerID patch — runcmd list-item. hcloud://<server-id> from metadata.
  # Non-fatal: bp-hcloud-ccm may set it later. Plain POSIX (dash -n clean).
  provider_id_cmd_hetzner = <<-EOT
    - 'HCLOUD_SERVER_ID=$(curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/instance-id) && NODE_NAME=$(hostname) && for i in $(seq 1 30); do kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml get node "$NODE_NAME" >/dev/null 2>&1 && kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml patch node "$NODE_NAME" --type=merge -p "{\"spec\":{\"providerID\":\"hcloud://$HCLOUD_SERVER_ID\"}}" && break || sleep 5; done || echo "providerID patch failed after 30 retries (non-fatal — bp-hcloud-ccm may set it later)"'
  EOT

  # Crossplane Provider + ProviderConfig (hcloud). Reads cloud-credentials.
  crossplane_provider_yaml_hetzner = <<-EOT
    ---
    apiVersion: pkg.crossplane.io/v1
    kind: Provider
    metadata:
      name: provider-hcloud
      labels:
        catalyst.openova.io/sovereign: ${var.sovereign_fqdn}
    spec:
      package: xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0
      packagePullPolicy: IfNotPresent
    ---
    apiVersion: hcloud.crossplane.io/v1beta1
    kind: ProviderConfig
    metadata:
      name: default
    spec:
      credentials:
        source: Secret
        secretRef:
          namespace: flux-system
          name: cloud-credentials
          key: hcloud-token
  EOT

  # provider prelude — Hetzner private-NIC bring-up. The hcloud private
  # network attaches a second interface post-boot; k3s --node-ip needs it up
  # before install or the apiserver crashloops. Generates a netplan stanza
  # for the extra NIC + waits for cp_private_ip to appear. runcmd block-scalar
  # item (2-space indented `- |`). cp_private_ip is injected via the shared
  # template's interpolation, so this prelude references it as a shell var
  # baked at render time. Plain POSIX (dash -n clean).
  provider_prelude_hetzner = <<-EOT
    - |
      EXPECTED_PRIVATE_IP="10.0.1.2"
      PRIMARY_NIC=$(ip -br link | awk '$1 == "eth0" {print $1; exit}')
      echo "[private-nic] waiting for $${EXPECTED_PRIVATE_IP} on any interface (primary=$${PRIMARY_NIC})..."
      for i in $(seq 1 60); do
        if ip -4 addr show | grep -qE "inet $${EXPECTED_PRIVATE_IP}/"; then
          echo "[private-nic] $${EXPECTED_PRIVATE_IP} is up (iter $${i})"
          break
        fi
        EXTRA=$(ip -br link | awk -v primary="$${PRIMARY_NIC}" '$1 != "lo" && $1 != primary && $1 !~ /^(docker|veth|tun|wg|cni|flannel|cilium)/ {print $1; exit}')
        if [ -n "$${EXTRA}" ] && ! grep -q "$${EXTRA}:" /etc/netplan/*.yaml 2>/dev/null; then
          echo "[private-nic] generating netplan stanza for $${EXTRA}"
          cat >/etc/netplan/60-private-network.yaml <<NETPLAN
      network:
        version: 2
        ethernets:
          $${EXTRA}:
            dhcp4: true
            dhcp4-overrides:
              use-dns: false
              use-routes: true
      NETPLAN
          chmod 0600 /etc/netplan/60-private-network.yaml
          netplan apply || true
        fi
        sleep 2
      done
      if ! ip -4 addr show | grep -qE "inet $${EXPECTED_PRIVATE_IP}/"; then
        echo "[private-nic] FATAL: $${EXPECTED_PRIVATE_IP} never appeared after 120s; k3s would crashloop" >&2
        ip -br addr >&2
        exit 1
      fi
  EOT

  # kubeconfig PUT-back — Hetzner. Rewrites k3s.yaml's 127.0.0.1 to the CP's
  # PUBLIC IPv4 (from metadata) so the cross-cloud mothership can reach the
  # apiserver on 6443, then PUTs with a Bearer header (issue #183 Option D).
  # runcmd list-items (0-indent; the shared template re-indents via indent(2,…)).
  # Primary region → no `?region=` suffix (stored at <id>.yaml). Plain POSIX.
  # The `kubeconfig_put_region_suffix_*` differs primary("") vs secondary("?region=<k>").
  kubeconfig_put_block_hetzner_primary = <<-EOT
    - install -m 0600 /dev/null /etc/rancher/k3s/k3s.yaml.public
    - 'CP_PUBLIC_IPV4=$(curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/public-ipv4) && sed "s|https://127.0.0.1:6443|https://$${CP_PUBLIC_IPV4}:6443|g" /etc/rancher/k3s/k3s.yaml > /etc/rancher/k3s/k3s.yaml.public'
    - chmod 0600 /etc/rancher/k3s/k3s.yaml.public
    - |
      curl -fsSL --retry 60 --retry-delay 10 --retry-all-errors \
        -X PUT \
        -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
        -H "Content-Type: application/x-yaml" \
        --data-binary @/etc/rancher/k3s/k3s.yaml.public \
        "${var.catalyst_api_url}/api/v1/deployments/${var.deployment_id}/kubeconfig"
    - rm -f /etc/rancher/k3s/k3s.yaml.public
  EOT

  # cloud-credentials Secret — Hetzner. hcloud-token + the autoscaler keys
  # (hcloud-cloud-init / network / firewall / ssh-key names) the bp-cluster-
  # autoscaler-hcloud HelmRelease lifts via valuesFrom (#921 / #1778).
  cloud_credentials_secret_yaml_hetzner_primary = <<-EOT
    apiVersion: v1
    kind: Secret
    metadata:
      name: cloud-credentials
      namespace: flux-system
    type: Opaque
    stringData:
      hcloud-token: ${var.hcloud_token}
      hcloud-cloud-init: ${base64encode(local.worker_cloud_init)}
      hcloud-network-name: ${hcloud_network.region["primary"].name}
      hcloud-firewall-name: ${hcloud_firewall.main.name}
      hcloud-ssh-key-name: ${hcloud_ssh_key.main.name}
      # #4739: empty cross-cloud placeholders. The cloud-agnostic
      # opentofu-cloud-adoption Composition wires HW_ACCESS_KEY / HW_SECRET_KEY /
      # HW_PROJECT_ID / HW_REGION_NAME via REQUIRED secretKeyRefs (the
      # provider-opentofu Workspace env schema has no `optional` field). Without
      # these keys on a Hetzner Sovereign every adoption Workspace fails Sync
      # "couldn't find key huawei-ak…". Empty values are inert (cloud==hetzner so
      # the huaweicloud data lookups have count=0 and that provider is unused).
      huawei-ak: ""
      huawei-sk: ""
      huawei-project-id: ""
      huawei-region: ""
  EOT

  # ── Worker cloud-init computed BEFORE the control-plane cloud-init so it
  # can be threaded into the CP template as `worker_cloud_init_b64`
  # (issue #921). The CP cloud-init writes the base64-encoded value
  # under the `hcloud-cloud-init` key of the canonical
  # `flux-system/cloud-credentials` Secret, which the bp-cluster-autoscaler-
  # hcloud HelmRelease lifts via Flux `valuesFrom` into
  # `clusterAutoscalerHcloud.cloudInit`. cluster-autoscaler 1.32.x's
  # Hetzner provider FATALs at startup ("`HCLOUD_CLUSTER_CONFIG` or
  # `HCLOUD_CLOUD_INIT` is not specified") without it. The autoscaler-
  # spawned worker uses the IDENTICAL bootstrap as Phase-0 workers so
  # k3s join token, control-plane address, hardening drop-ins are all
  # already aligned.
  worker_cloud_init = replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    cp_private_ip              = "10.0.1.2" # First static IP in the subnet — control plane
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    harbor_robot_token         = var.harbor_robot_token # #2842: seed harbor mirror auth on worker nodes
  }), "/(?m)^[ ]*#( |$).*\n/", "")

  # Strip ALL pure-comment lines (any indent) from the rendered cloud-init
  # before passing it to Hetzner (HARD 32 KiB user_data limit per the hcloud
  # API). The source template ships ~44 KB of documentation prose in comments
  # — explanatory text for future readers, not operationally meaningful at
  # boot. Comments live at indent-0/2 (template-level prose explaining the
  # tftpl itself) AND at indent-6+ INSIDE heredoc `content: |` blocks (YAML
  # comments inside flux-bootstrap.yaml, cloud-credentials-secret.yaml, etc.
  # — every write_files file we materialise is YAML, JSON, or a key=value
  # config; none are shell scripts). YAML/JSON/INI/conf parsers all ignore
  # `#`-prefixed comment lines so stripping them in Tofu loses nothing at
  # runtime. The regex `^[ ]*#( |$).*` matches a leading-whitespace `#`
  # followed by either a space (prose comment) OR end-of-line (separator
  # `#`). Lines whose `#` is followed by ANY OTHER char (e.g. `#!shebang`,
  # `#cloud-config` at line 1, `#pragma`) are NOT matched — they're operative.
  # Phase-8a-preflight bug #5 surfaced the 32 KiB cap initially; issue #966
  # raised it again on otech114 after #921 (cluster-autoscaler HCLOUD_CLOUD_INIT
  # b64) pushed rendered size past 36 KiB. The any-indent strip lands rendered
  # cloud-init at ~22 KB with ~10 KB of headroom for future additions.
  # Guardrail in this same module: see `validate_user_data_size` precondition
  # below — any future bloat that pushes user_data ≥ 30 KiB fails at plan-time.
  control_plane_cloud_init = replace(templatefile("${path.module}/../_shared/cloudinit-control-plane.tftpl", {
    # ── #3145: render the ONE cloud-agnostic bootstrap. Hetzner-specific
    # pieces are injected as the *_hetzner locals (the "hard dependency"
    # exceptions). Common vars are identical to every other cloud.
    provider = "hetzner"

    # Every Hetzner CP sits at 10.0.1.2 (each region has its own /24 inside
    # its own hcloud_network — see local.secondary_region_cp_ips).
    cp_private_ip                  = "10.0.1.2"
    cluster_cidr                   = local.region_cluster_cidr["primary"]
    service_cidr                   = local.region_service_cidr["primary"]
    sovereign_fqdn                 = var.sovereign_fqdn
    sovereign_fqdn_slug            = replace(var.sovereign_fqdn, ".", "-")
    deployment_id                  = var.deployment_id
    org_name                       = var.org_name
    org_email                      = var.org_email
    region                         = var.region
    sovereign_region_key           = var.region # raw region key (flow-emitter REGION_KEY)
    region_canonical_label         = local.region_canonical_label["primary"]
    primary_region_canonical_label = local.region_canonical_label["primary"]
    replica_region_canonical_label = length(local.secondary_regions) > 0 ? (
      local.region_canonical_label[keys(local.secondary_regions)[0]]
    ) : ""
    cluster_mesh_name   = var.cluster_mesh_name
    cluster_mesh_id     = var.cluster_mesh_id
    k3s_version         = var.k3s_version
    k3s_token           = local.k3s_token
    gitops_repo_url     = var.gitops_repo_url
    gitops_branch       = var.gitops_branch
    marketplace_enabled = var.marketplace_enabled
    qa_fixtures_enabled = var.qa_fixtures_enabled
    # North Star — decouple cutover auto-fire from qaTestEnabled. → shared
    # template's FIRE_CUTOVER_ON_HANDOVER substitute → slot-13
    # catalystApi.fireCutoverOnHandover (ORed with qaFixtures.enabled by the
    # chart, #4648). INDEPENDENT of qa_fixtures_enabled so a PROD-cert Sovereign
    # auto-fires the cutover too.
    fire_cutover_on_handover = var.fire_cutover_on_handover
    # #4053 console-isolation toggle (Refs #4431 #4212) → shared template's
    # SOVEREIGN_CONSOLE_GATEWAY substitute → slot-13 ingress.gateway.parentRef.name.
    console_isolation_enabled = var.console_isolation_enabled
    wildcard_cert_issuer      = local.wildcard_cert_issuer
    bcp_topology              = var.bcp_topology
    enable_hot_standby        = var.enable_hot_standby
    enable_shared_pg          = var.enable_shared_pg
    default_storage_class     = var.default_storage_class
    sovereign_cnpg_instances  = length(var.regions) > 1 ? "2" : "1"
    continuum_enabled         = length(var.regions) > 1 ? "true" : "false"
    parent_domains_yaml = coalesce(
      var.parent_domains_yaml,
      format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
    )
    sovereign_regions_json = jsonencode(var.regions)
    sovereign_configured_regions_yaml = jsonencode([
      for r in var.regions : r.cloudRegion
    ])
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    ghcr_pull_username         = local.ghcr_pull_username
    ghcr_pull_token            = var.ghcr_pull_token
    ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64
    object_storage_endpoint    = local.object_storage_endpoint
    object_storage_region      = var.object_storage_region
    object_storage_bucket_name = var.object_storage_bucket_name
    object_storage_access_key  = var.object_storage_access_key
    object_storage_secret_key  = var.object_storage_secret_key
    harbor_robot_token         = var.harbor_robot_token
    powerdns_api_key           = var.powerdns_api_key
    pdm_basic_auth_user        = var.pdm_basic_auth_user
    pdm_basic_auth_pass        = var.pdm_basic_auth_pass
    handover_jwt_public_key    = var.handover_jwt_public_key
    kubeconfig_bearer_token    = var.kubeconfig_bearer_token
    catalyst_api_url           = var.catalyst_api_url
    load_balancer_ipv4         = hcloud_load_balancer.main.ipv4
    # #4236 — the dedicated console LB IPv4 (#4053 isolation). Threads through
    # cloud-init → bootstrap-kit slot 13 → sovereign-fqdn ConfigMap `consoleLBIP`
    # → organization-controller so the per-Org pool-DNS `console.<slug>.<pool>`
    # A-record targets the console front door (the #4179 final layer).
    console_load_balancer_ipv4 = local.console_isolation_on ? hcloud_load_balancer.console[0].ipv4 : ""

    # Huawei-branch substitute vars — passed empty/placeholder on Hetzner so
    # the shared template's `provider == "huawei"` block parses (tofu
    # evaluates var refs even in non-taken directive branches). Inert here,
    # EXCEPT sovereign_region_role: it now feeds the SHARED
    # SOVEREIGN_REGION_ROLE substitute (bp-cnpg-pair split-side, slot 16b
    # `cnpgPair.side`), so the per-CP value is live on Hetzner too.
    pdns_api_host = ""
    # #5017 keystone — provider API CIDRs for the step-08 deny-egress
    # allow-list (empty on Hetzner; see variables.tf provider_api_cidrs).
    provider_api_cidrs_yaml = jsonencode(var.provider_api_cidrs)
    sovereign_region_role   = "primary"
    node_external_ip_value  = ""
    clustermesh_proxy_port  = 12379 # #4784 — inert on Hetzner (clustermesh-proxy off; hcloud-ccm LB IP has no k3s-etcd :2379 host collision)

    # ── Provider-injected strings (the §5 hard-dependency exceptions) ──
    registry_mirror_yaml          = local.registry_mirror_yaml_hetzner
    cloud_credentials_secret_yaml = local.cloud_credentials_secret_yaml_hetzner_primary
    crossplane_provider_yaml      = local.crossplane_provider_yaml_hetzner
    node_external_ip_cmd          = local.node_external_ip_cmd_hetzner
    node_ip_cmd                   = local.node_ip_cmd_hetzner
    provider_id_cmd               = local.provider_id_cmd_hetzner
    provider_prelude              = local.provider_prelude_hetzner
    # Hetzner k3s flags: OIDC (kubectl SSO via auth.<fqdn>), disable cloud
    # controller (no CCM at bootstrap), + control-plane taint when workers
    # exist so user pods don't schedule on the CP.
    k3s_extra_args = "--kube-apiserver-arg=oidc-issuer-url=https://auth.${var.sovereign_fqdn}/realms/sovereign --kube-apiserver-arg=oidc-client-id=kubectl --kube-apiserver-arg=oidc-username-claim=preferred_username --kube-apiserver-arg=oidc-username-prefix=oidc: --kube-apiserver-arg=oidc-groups-claim=groups --kube-apiserver-arg=oidc-groups-prefix=oidc: --disable-cloud-controller ${var.worker_count > 0 ? "--node-taint node-role.kubernetes.io/control-plane=true:NoSchedule" : ""}"
    # PUT-back: primary region → no ?region= suffix (catalyst-api stores at
    # <id>.yaml). Public IP from Hetzner metadata (rewrite 127.0.0.1→public).
    kubeconfig_put_block = local.kubeconfig_put_block_hetzner_primary
  }), "/(?m)^[ ]*#( |$).*\n/", "")
}

resource "hcloud_server" "control_plane" {
  count = local.control_plane_count
  name  = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-cp${count.index + 1}"
  image = "ubuntu-24.04"
  # Fix #157 — auto-flip to qa_control_plane_size on QA Sovereigns
  # (qa_fixtures_enabled='true'); customer Sovereigns continue to read
  # var.control_plane_size verbatim. See locals.effective_cp_size.
  server_type  = local.effective_cp_size
  location     = var.region
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.control_plane_cloud_init

  network {
    network_id = hcloud_network.region["primary"].id
    ip         = "10.0.1.${count.index + 2}" # cp1=10.0.1.2, cp2=10.0.1.3, cp3=10.0.1.4
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "control-plane"
  }

  # Issue #966 — Hetzner Cloud HARD limit on user_data is 32768 bytes.
  # Fail at plan-time (not at apply-time after the network/LB/firewall are
  # already created) if the rendered cloud-init exceeds the guardrail (which
  # keeps a 256 B safety buffer under the Hetzner hard cap of 32768). PR #1981
  # repackaged TBD-A50 Layer 3 (#1979) into write_files (~770 B savings) and
  # bumped the guardrail from 31744 to 32256 so the ExternalIP reconciler
  # ships with a healthy headroom in front of the hard cap.
  # #5057 / #5042 (2026-07-14): the #5042 forensic fail-loud retry loops (the
  # gateway-api / flux-install / cilium `for i in 1..5 … exit 93/94/95` blocks)
  # and their sentinels legitimately consumed the prior margin. Every safe
  # byte reclaim was applied first (kubectl `--kubeconfig` flags factored into
  # single-`export KUBECONFIG` shell blocks in cloudinit-control-plane.tftpl,
  # ~101 B), leaving the three-region render at 32306 B. The remaining gap is
  # not cuttable without either razor-thin (<2 B) fail-loud-eroding merges or
  # behavior-risky config trims, so the guardrail is raised to 32512 — still
  # 256 B below the 32768 hard cap. Prefer cutting; only raise for #5042-class
  # fail-loud features that must not be silently truncated.
  # #5086 (2026-07-15): +1 substitute var POWERDNS_DNSDIST_HOSTPORT (~46 B) —
  # the Hetzner DNS-front-door gate (flips dnsdist to a hostPort:53 DaemonSet).
  # It's a config value that must NOT be silently truncated (else node:53 never
  # binds and Sovereign DNS stays dead). Comments are stripped pre-measure (the
  # replace() regex above), so no comment cull can offset it AND the render is
  # already at its documented floor — guardrail raised 32512 → 32576, still
  # 192 B below the 32768 hard cap.
  lifecycle {
    precondition {
      condition = length(local.control_plane_cloud_init) <= 32576
      # G107 #2702 (2026-06-01): wrap length() with nonsensitive() so the
      # byte count shows in the error_message. Without it tofu redacts the
      # entire message because the local references hcloud_token / ssh_key
      # marked sensitive. Operators iterating on cloud-init slim-downs
      # cannot target their cuts without knowing the actual overshoot;
      # nonsensitive() leaks only the integer length, not any token bytes.
      error_message = "Rendered control-plane cloud-init is ${nonsensitive(length(local.control_plane_cloud_init))} bytes, exceeds 32576 (192 B below the Hetzner 32768 hard cap) guardrail. Cull comments / move bloat out of cloudinit-control-plane.tftpl. See issues #966 / #1981 / #5057 / #5042 / #5086."
    }
    precondition {
      condition     = length(local.worker_cloud_init) <= 30720
      error_message = "Rendered worker cloud-init is ${nonsensitive(length(local.worker_cloud_init))} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-worker.tftpl. See issue #966."
    }
  }

  depends_on = [hcloud_network_subnet.region]
}

# ── Workers: variable count ───────────────────────────────────────────────

resource "hcloud_server" "worker" {
  count = var.worker_count
  name  = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-w${count.index + 1}"
  image = "ubuntu-24.04"
  # Fix #157 — auto-flip to qa_worker_size on QA Sovereigns
  # (qa_fixtures_enabled='true'); customer Sovereigns continue to read
  # var.worker_size verbatim. See locals.effective_worker_size.
  server_type  = local.effective_worker_size
  location     = var.region
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.worker_cloud_init

  network {
    network_id = hcloud_network.region["primary"].id
    ip         = "10.0.1.${count.index + 10}" # workers start at .10
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "worker"
  }

  # Issue #966 — same 32 KiB user_data hard cap applies to workers. The
  # precondition on hcloud_server.control_plane already guards both rendered
  # cloud-inits, but mirror it here so a worker-only future change can't
  # bypass the gate by editing only cloudinit-worker.tftpl.
  lifecycle {
    precondition {
      condition     = length(local.worker_cloud_init) <= 30720
      error_message = "Rendered worker cloud-init is ${nonsensitive(length(local.worker_cloud_init))} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-worker.tftpl. See issue #966."
    }
  }

  depends_on = [hcloud_server.control_plane]
}

# ── Load balancer: lb11, 80/443 → cilium Gateway LoadBalancer Service :80/:443 ─

resource "hcloud_load_balancer" "main" {
  name               = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-lb"
  load_balancer_type = "lb11"
  location           = var.region
  algorithm {
    type = "round_robin"
  }
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

resource "hcloud_load_balancer_network" "main" {
  load_balancer_id = hcloud_load_balancer.main.id
  network_id       = hcloud_network.region["primary"].id
  # Fix #182: pin LB private IP to top-of-subnet so it cannot race the
  # CP server's explicit `ip = "10.0.1.2"` during parallel apply. Without
  # this, Hetzner auto-allocates the first free IP in the matching-zone
  # subnet. .254 is the last usable host in /24 and is reserved platform-
  # wide for LB anchors. After the per-region network refactor each region
  # has its OWN /24 inside its OWN hcloud_network, so the cross-region IP
  # collision class (prov #32 root cause) is gone by construction — but
  # the .254 pin still guards intra-region CP/LB races.
  ip = "10.0.1.254"

  depends_on = [hcloud_network_subnet.region]
}

resource "hcloud_load_balancer_target" "control_plane" {
  count            = local.control_plane_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.main.id
  server_id        = hcloud_server.control_plane[count.index].id
  use_private_ip   = true

  depends_on = [hcloud_load_balancer_network.main]
}

# ── LB targets: workers ────────────────────────────────────────────────
# Cilium Gateway runs as a DaemonSet on every node
# (clusters/_template/sovereign-tls/cilium-gateway.yaml), so any node can
# serve ingress traffic on the standard :80/:443 (§854 — never a
# NodePort). Adding workers as LB targets
# gives the Hetzner LB N+1 healthy endpoints (1 CP + N workers) for the
# public 80/443/53 services — node failure on any single node no longer
# breaks the front door, and inbound traffic is round-robin'd across
# every node for genuine horizontal scale (issue #733). use_private_ip
# routes through the 10.0.1.0/24 subnet; the worker's public IP is not
# used for this path. Worker count > 0 means at least one extra LB
# endpoint; worker_count=0 (solo dev/POC) leaves only the CP target.
resource "hcloud_load_balancer_target" "workers" {
  count            = var.worker_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.main.id
  server_id        = hcloud_server.worker[count.index].id
  use_private_ip   = true

  depends_on = [
    hcloud_load_balancer_network.main,
    hcloud_server.worker,
  ]
}

resource "hcloud_load_balancer_service" "http" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 80
  # destination_port=80 — the Cilium Gateway is now served as a
  # Service type=LoadBalancer on the standard :80/:443 (hostNetwork
  # OFF, no NodePort). hcloud-ccm materialises the gateway Service LB;
  # this tofu-declared LB service forwards public 80→node:80 as a
  # transitional belt-and-suspenders that keeps the DNS A-record LB IP
  # stable. Operator-facing URL is `http://console.<fqdn>/`.
  destination_port = 80
}

resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 443
  # destination_port=443 — see http service comment above. The Cilium
  # Gateway HTTPS listener is served on standard :443 via the
  # LoadBalancer Service; HCLB forwards public 443→node:443 so external
  # users hit `https://console.<fqdn>/`.
  destination_port = 443
}

resource "hcloud_load_balancer_service" "dns" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 53
  # destination_port=53 — §854 / #4765: NodePorts are ABSOLUTELY FORBIDDEN
  # (founder 2026-07-03). The former :53 → node:30053 forward targeted the
  # anycast-endpoint ServiceType=NodePort overlay that #4766 eradicated from
  # 11-powerdns.yaml (now type=LoadBalancer + lbipam.cilium.io/sharing-key,
  # allocateLoadBalancerNodePorts:false — bp-powerdns ≥1.2.18 hard-fails on
  # NodePort), so node:30053 can never be bound again. §854-clean.
  # #5086: the node:53 LISTENER on Hetzner is now the bp-powerdns ≥1.2.21
  # dnsdist DaemonSet (hostPortMode, gated ON via the Hetzner cloud-init
  # POWERDNS_DNSDIST_HOSTPORT substitute) — it binds hostPort:53 on every node
  # (§854 hostPort, NEVER a nodePort), so this LB forward reaches a real
  # listener. (The Cilium LB-IPAM sovereign-vip anycast VIP path stays dead on
  # Hetzner: no hcloud-ccm fallback, LB-IPAM gated OFF — hostPort is the front
  # door here.) LIVE-GATED: unproven until a real Hetzner prov confirms
  # `dig @<node> <fqdn>` resolves; no Hetzner env is currently up.
  # lb11 supports TCP only; UDP :53 is handled via the Hetzner Firewall
  # opening UDP/53 directly to the node's public IP. The LB TCP path handles
  # zone transfers and ACME challenge TXT queries; UDP is used for regular
  # resolution.
  destination_port = 53
  health_check {
    protocol = "tcp"
    port     = 53
    interval = 15
    timeout  = 10
    retries  = 3
  }
}

# ── #4053 — Dedicated CONSOLE load balancer (poison-proof gateway isolation) ──
#
# Cilium Gateway API compiles every HTTPRoute + backend of a Gateway into ONE
# CiliumEnvoyConfig committed atomically; a single unresolvable app backend
# fails the whole CEC and 404s the entire gateway (cilium#35728, unfixed on the
# pinned 1.16.5). The console is therefore isolated onto its OWN Cilium Gateway
# (`cilium-gateway-console` — its own Service type=LoadBalancer on the
# standard :443/:80, hostNetwork off, no NodePort/host-port; see
# clusters/_template/sovereign-tls/cilium-gateway-console.yaml), carrying ONLY
# the stable catalyst-ui + catalyst-api backends.
#
# Both `hcloud_load_balancer` (lb11) and the Huawei ELB do TCP PASSTHROUGH on
# 443 (TLS terminates at cilium-envoy, NOT the LB), so neither can route by SNI
# / Host header — a single LB IP + single public 443 can reach exactly ONE
# gateway Service. Isolating the console onto a second Gateway (with its own
# LoadBalancer Service IP) therefore requires a SECOND public LB IP. The console
# A-records (console./api.<fqdn>) point at THIS LB → node:443; the wildcard
# `*.<fqdn>` (every volatile app/tenant host) keeps pointing at the shared
# `hcloud_load_balancer.main` → node:443. DNS split is owned by
# core/pool-domain-manager (consoleLoadBalancerIP) + the catalyst-api handover.
#
# This is the only design that (a) keeps the console always reachable on the
# canonical https://console.<fqdn>/ :443 URL, (b) is byte-identical across
# Hetzner + Huawei (both just do dumb TCP 443→443), and (c) needs no Cilium
# upgrade — the pinned 1.16.5 does NOT reconcile a TLSRoute-passthrough SNI
# front gateway (TLSRoute is a startup CRD presence-check only on 1.16.x), which
# rules out the single-IP front-gateway alternative.
resource "hcloud_load_balancer" "console" {
  count              = local.console_isolation_on ? 1 : 0
  name               = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-console-lb"
  load_balancer_type = "lb11"
  location           = var.region
  algorithm {
    type = "round_robin"
  }
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "console"
  }
}

resource "hcloud_load_balancer_network" "console" {
  count            = local.console_isolation_on ? 1 : 0
  load_balancer_id = hcloud_load_balancer.console[0].id
  network_id       = hcloud_network.region["primary"].id
  # .253 — one below the shared LB's .254 anchor (see hcloud_load_balancer_
  # network.main). Pins the console LB's private IP so it cannot race the CP /
  # shared-LB allocations during parallel apply.
  ip = "10.0.1.253"

  depends_on = [hcloud_network_subnet.region]
}

# Console LB targets — same node set as the shared LB. The shared and console
# gateways each own a separate Service type=LoadBalancer on :443/:80 (hostNetwork
# off), so every node is a valid target for the console LB too.
resource "hcloud_load_balancer_target" "console_control_plane" {
  count            = local.console_cp_target_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.console[0].id
  server_id        = hcloud_server.control_plane[count.index].id
  use_private_ip   = true

  depends_on = [hcloud_load_balancer_network.console]
}

resource "hcloud_load_balancer_target" "console_workers" {
  count            = local.console_worker_target_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.console[0].id
  server_id        = hcloud_server.worker[count.index].id
  use_private_ip   = true

  depends_on = [
    hcloud_load_balancer_network.console,
    hcloud_server.worker,
  ]
}

resource "hcloud_load_balancer_service" "console_http" {
  count            = local.console_isolation_on ? 1 : 0
  load_balancer_id = hcloud_load_balancer.console[0].id
  protocol         = "tcp"
  listen_port      = 80
  # :80 → node:80 — the console gateway is now its own Service
  # type=LoadBalancer on the standard ports (hostNetwork off, no
  # NodePort/host-port; #4682). This tofu-declared LB service is the
  # transitional belt-and-suspenders keeping the console LB IP stable.
  destination_port = 80
}

resource "hcloud_load_balancer_service" "console_https" {
  count            = local.console_isolation_on ? 1 : 0
  load_balancer_id = hcloud_load_balancer.console[0].id
  protocol         = "tcp"
  listen_port      = 443
  # :443 → node:443 — see console_http above. TLS terminates at the
  # console gateway's cilium-envoy (per-prov wildcard cert), NOT here.
  destination_port = 443
}

# ── DNS: deliberately NOT a tofu concern ──────────────────────────────────
#
# Per the PDM (pool-domain-manager) ownership boundary set at #168, ALL
# Dynadot writes for managed pool subdomains flow through the central
# pool-domain-manager service. The lifecycle is:
#
#   1. catalyst-api receives POST /v1/deployments. Before launching
#      `tofu apply`, it calls PDM /reserve to put the subdomain on hold
#      with a TTL. (See deployments.go:127.)
#   2. `tofu apply` runs THIS module — provisioning Hetzner network,
#      firewall, server, load balancer. NO DNS writes here.
#   3. catalyst-api reads the LB IP from the tofu outputs and calls PDM
#      /commit (deployments.go:247). PDM writes the canonical record set
#      via the Dynadot API.
#   4. On any tofu failure, catalyst-api calls PDM /release so the
#      subdomain returns to the available pool.
#
# A previous revision of this module also wrote DNS via a `null_resource`
# with a `local-exec` provisioner shelling out to `/usr/local/bin/catalyst-dns`.
# That created a dual-ownership pattern — both tofu AND PDM writing
# Dynadot — which (a) duplicated work, (b) put credentials in two places,
# and (c) failed on every Launch with an opaque "Invalid field in API
# request" Dynadot error. The null_resource was removed in this commit;
# DNS is now a single-owner concern (PDM) end-to-end.
#
# BYO Sovereigns continue to own their own DNS — the customer points their
# CNAME at the LB IP shown on the success screen.

# ── Hetzner Object Storage bucket (Phase 0b — issue #371) ─────────────────
#
# This is the Sovereign's S3 bucket for Velero (cluster-state backup) and
# Harbor (container-image registry storage). Both Blueprints consume the
# `flux-system/object-storage` K8s Secret cloud-init writes into the Sovereign
# the bucket itself MUST exist before those Blueprints reconcile their first
# HelmRelease, otherwise their startup probes fail with NoSuchBucket and
# Phase 1 stalls.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #3, day-2 cloud resource mutation is
# Crossplane's job. THIS resource is Phase 0 — created exactly once at
# Sovereign provisioning time, never mutated afterwards. If a Sovereign
# operator wants to add a second bucket post-handover (for an analytics
# product, for example), that is a Crossplane-managed XR/XRC, not a
# rerun of this OpenTofu module.
#
# The hashicorp/aws provider's `aws_s3_bucket` resource is idempotent
# enough for our needs: the Create handler issues a single CreateBucket
# call. If the bucket already exists IN THE SAME ACCOUNT (Hetzner's
# tenant-scoped S3 ownership model — the access key fully scopes the
# tenant), Hetzner returns 200 OK + BucketAlreadyOwnedByYou which the
# AWS SDK normalises into a no-op. This is critical because:
#   - re-running `tofu apply` (e.g. operator changed worker count) must
#     not bounce off the bucket with AlreadyExists
#   - the wipe + re-provision flow (issue #318) destroys the Sovereign
#     servers but does NOT destroy the bucket — Velero backup data must
#     survive a control-plane reinstall
#
# Why we moved off `aminueza/minio v3.34.0`:
#   That provider's Create handler post-create called DeleteBucketPolicy
#   as part of state normalization. Hetzner Object Storage's standard
#   read/write credentials don't grant `s3:DeleteBucketPolicy`, so the
#   call returned AccessDenied and tofu rolled back the resource — even
#   though Hetzner had created the bucket. Provisions #13 and #17 both
#   wedged on this in <2 min. The aws provider does no such normalization;
#   a successful CreateBucket is its terminal Create state. See the
#   matching prose in versions.tf for the full root-cause writeup.
#
# We deliberately do NOT set `force_destroy = true`: a `tofu destroy` of
# this module must NOT take the Velero archive with it. The operator
# performs explicit bucket deletion via the Hetzner Console (or the
# wipe-handler S3 purge step, when present) as a separate, auditable
# step when a Sovereign is decommissioned.
resource "aws_s3_bucket" "main" {
  bucket = var.object_storage_bucket_name

  # No `force_destroy` — see comment block above.
}

# ACL is a separate resource on aws_s3_bucket (the bucket-level `acl`
# argument was deprecated upstream when aws-provider 4.x split the ACL
# into its own resource, and removed entirely from the bucket schema in
# 5.x). Hetzner Object Storage supports the standard canned `private`
# ACL; we set it explicitly so a bucket adopted from a previous
# provisioning run can never be left world-readable by a stale ACL.
#
# Object lock is intentionally NOT configured: Velero relies on standard
# S3 versioning + the operator's retention policy, not on WORM semantics.
# Harbor stores immutable image layers but doesn't require object lock —
# the layer content-addressed digest IS the immutability guarantee.
resource "aws_s3_bucket_acl" "main" {
  bucket = aws_s3_bucket.main.id
  acl    = "private"
}

# ── Multi-region overlay (slice G1 → DMZ-WG refactor 2026-05-15) ──────────
#
# Realises every var.regions[1+] entry as a parallel set of Hetzner
# resources keyed off local.secondary_regions. Cilium ClusterMesh joins
# the regions over the public DMZ WireGuard endpoint (UDP 51871) — this
# module only provisions the cloud substrate.
#
# Architectural decision (2026-05-15, founder ruling, see
# docs/SOVEREIGN-MULTI-REGION-DOD.md): no shared private network across
# regions. Each region gets its OWN hcloud_network + its OWN /24 subnet
# (declared at the TOP of this file under `hcloud_network.region` and
# `hcloud_network_subnet.region`, both keyed `for_each = toset(
# local.all_region_keys)` so they cover the "primary" key plus every
# secondary key). Inter-region pod-to-pod traffic flows EXCLUSIVELY
# over Cilium WireGuard on each region's public IP through the DMZ
# vCluster — provider-agnostic (works the same way for an AWS or
# Huawei secondary region, A6) and zero-trust against the provider's
# internal network fabric.
#
# Architectural decision (2026-05-15, founder ruling): no shared private
# network across regions, full stop. The previous slice-G1 design had
# one shared `/16` with per-region `/24`s and was explicitly rejected
# (DoD A2 trigger phrase: "Hetzner private net spans zones, let me use
# that for cross-region" → STOP). Replaced with one Network per region,
# each carrying an identical 10.0.1.0/24 subnet — addresses don't
# collide because the networks are isolated.
#
# Resource-address impact: every legacy Sovereign would replan the
# network resources on the next apply. By founder directive (DoD cycle
# protocol — every wipe-and-create cycle is a fresh provision), the
# state-migration cost is consciously accepted: NO `tofu state mv`
# runbook ships with this PR; pre-2026-05-15 state is destroyed and
# reprovisioned cleanly.

# Per-secondary-region cloud-init — same template as the primary CP,
# parameterised with the secondary region's LB IPv4 and CP private IP
# in its own subnet. Sovereign FQDN, Org name, GitOps repo, secrets,
# region label etc. are SHARED with the primary so the secondary CP
# bootstraps as another node in the same logical Sovereign.
#
# Note for slice G3: a future per-cluster GitOps path differentiation
# (clusters/<provider>-<region>-<bb>-<env>/ per docs/NAMING-CONVENTION.md
# §4.1) will require a per-region `cluster_name` template knob; that
# is intentionally NOT introduced in slice G1 to keep this slice purely
# additive. Today every secondary CP renders an identical Flux
# Kustomization pointed at clusters/<sovereign_fqdn>/ — functionally
# valid k3s clusters for the EPIC-6 #1101 cloud-substrate gate; G3
# refines path separation when ClusterMesh + per-cluster Flux land.
locals {
  secondary_region_cloud_init = {
    for k, r in local.secondary_regions :
    k => replace(templatefile("${path.module}/../_shared/cloudinit-control-plane.tftpl", {
      # ── #3145: secondary-region CP renders the SAME cloud-agnostic bootstrap
      # as primary (infra/providers/_shared/cloudinit-control-plane.tftpl). Only
      # the per-region values + the `?region=<k>` PUT suffix differ.
      provider = "hetzner"

      cp_private_ip                  = local.secondary_region_cp_ips[k]
      cluster_cidr                   = local.region_cluster_cidr[k]
      service_cidr                   = local.region_service_cidr[k]
      sovereign_fqdn                 = var.sovereign_fqdn
      sovereign_fqdn_slug            = replace(var.sovereign_fqdn, ".", "-")
      deployment_id                  = var.deployment_id
      org_name                       = var.org_name
      org_email                      = var.org_email
      region                         = r.cloudRegion
      sovereign_region_key           = k # secondary each.key (raw region key)
      region_canonical_label         = local.region_canonical_label[k]
      primary_region_canonical_label = local.region_canonical_label["primary"]
      replica_region_canonical_label = length(local.secondary_regions) > 0 ? (
        local.region_canonical_label[keys(local.secondary_regions)[0]]
      ) : ""
      cluster_mesh_name   = local.secondary_region_cluster_mesh_name[k]
      cluster_mesh_id     = local.secondary_region_cluster_mesh_id[k]
      k3s_version         = var.k3s_version
      k3s_token           = local.k3s_token
      gitops_repo_url     = var.gitops_repo_url
      gitops_branch       = var.gitops_branch
      marketplace_enabled = var.marketplace_enabled
      qa_fixtures_enabled = var.qa_fixtures_enabled
      # North Star — decouple cutover auto-fire from qaTestEnabled (secondary
      # region). → shared template's FIRE_CUTOVER_ON_HANDOVER substitute →
      # slot-13 catalystApi.fireCutoverOnHandover. INDEPENDENT of qa_fixtures_enabled.
      fire_cutover_on_handover = var.fire_cutover_on_handover
      # #4053 console-isolation toggle (Refs #4431 #4212) → shared template's
      # SOVEREIGN_CONSOLE_GATEWAY substitute → slot-13 ingress.gateway.parentRef.name.
      console_isolation_enabled = var.console_isolation_enabled
      wildcard_cert_issuer      = local.wildcard_cert_issuer
      bcp_topology              = var.bcp_topology
      enable_hot_standby        = var.enable_hot_standby
      enable_shared_pg          = var.enable_shared_pg
      default_storage_class     = var.default_storage_class
      sovereign_cnpg_instances  = length(var.regions) > 1 ? "2" : "1"
      continuum_enabled         = length(var.regions) > 1 ? "true" : "false"
      parent_domains_yaml = coalesce(
        var.parent_domains_yaml,
        format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
      )
      sovereign_regions_json = jsonencode(var.regions)
      sovereign_configured_regions_yaml = jsonencode([
        for rr in var.regions : rr.cloudRegion
      ])
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
      ghcr_pull_username         = local.ghcr_pull_username
      ghcr_pull_token            = var.ghcr_pull_token
      ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64
      object_storage_endpoint    = local.object_storage_endpoint
      object_storage_region      = var.object_storage_region
      object_storage_bucket_name = var.object_storage_bucket_name
      object_storage_access_key  = var.object_storage_access_key
      object_storage_secret_key  = var.object_storage_secret_key
      harbor_robot_token         = var.harbor_robot_token
      powerdns_api_key           = var.powerdns_api_key
      pdm_basic_auth_user        = var.pdm_basic_auth_user
      pdm_basic_auth_pass        = var.pdm_basic_auth_pass
      handover_jwt_public_key    = var.handover_jwt_public_key
      kubeconfig_bearer_token    = var.kubeconfig_bearer_token
      catalyst_api_url           = var.catalyst_api_url
      load_balancer_ipv4         = hcloud_load_balancer.secondary[k].ipv4
      # #4236 — the dedicated console LB IPv4 is a single (region-agnostic)
      # resource; pass it in every region's cloud-init so the shared template's
      # ${console_load_balancer_ipv4} substitute resolves. The organization-
      # controller only runs in the primary region but the substitute must be
      # defined in both templatefile() calls.
      console_load_balancer_ipv4 = local.console_isolation_on ? hcloud_load_balancer.console[0].ipv4 : ""

      # Huawei-branch substitute vars — inert on Hetzner (see primary call),
      # EXCEPT sovereign_region_role: live via the shared
      # SOVEREIGN_REGION_ROLE substitute (bp-cnpg-pair split-side).
      pdns_api_host = ""
      # #5017 keystone — provider API CIDRs for the step-08 deny-egress
      # allow-list (empty on Hetzner; see variables.tf provider_api_cidrs).
      provider_api_cidrs_yaml = jsonencode(var.provider_api_cidrs)
      sovereign_region_role   = "secondary"
      node_external_ip_value  = ""
      clustermesh_proxy_port  = 12379 # #4784 — inert on Hetzner (clustermesh-proxy off)

      # ── Provider-injected strings (the §5 hard-dependency exceptions) ──
      registry_mirror_yaml     = local.registry_mirror_yaml_hetzner
      crossplane_provider_yaml = local.crossplane_provider_yaml_hetzner
      node_external_ip_cmd     = local.node_external_ip_cmd_hetzner
      node_ip_cmd              = local.node_ip_cmd_hetzner
      provider_id_cmd          = local.provider_id_cmd_hetzner
      provider_prelude         = local.provider_prelude_hetzner
      k3s_extra_args           = "--kube-apiserver-arg=oidc-issuer-url=https://auth.${var.sovereign_fqdn}/realms/sovereign --kube-apiserver-arg=oidc-client-id=kubectl --kube-apiserver-arg=oidc-username-claim=preferred_username --kube-apiserver-arg=oidc-username-prefix=oidc: --kube-apiserver-arg=oidc-groups-claim=groups --kube-apiserver-arg=oidc-groups-prefix=oidc: --disable-cloud-controller ${r.workerCount > 0 ? "--node-taint node-role.kubernetes.io/control-plane=true:NoSchedule" : ""}"
      # cloud-credentials Secret — secondary region's autoscaler keys.
      cloud_credentials_secret_yaml = <<-CREDS
        apiVersion: v1
        kind: Secret
        metadata:
          name: cloud-credentials
          namespace: flux-system
        type: Opaque
        stringData:
          hcloud-token: ${var.hcloud_token}
          hcloud-cloud-init: ${base64encode(local.secondary_region_worker_cloud_init[k])}
          hcloud-network-name: ${hcloud_network.region[k].name}
          hcloud-firewall-name: ${hcloud_firewall.main.name}
          hcloud-ssh-key-name: ${hcloud_ssh_key.main.name}
      CREDS
      # kubeconfig PUT-back — secondary region appends `?region=<k>` so
      # catalyst-api stores at <id>-<k>.yaml + phase1Watch spawns a per-region
      # helmwatch.Bridge. Public IP from Hetzner metadata. runcmd 0-indent items.
      kubeconfig_put_block = <<-PUT
        - install -m 0600 /dev/null /etc/rancher/k3s/k3s.yaml.public
        - 'CP_PUBLIC_IPV4=$(curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/public-ipv4) && sed "s|https://127.0.0.1:6443|https://$${CP_PUBLIC_IPV4}:6443|g" /etc/rancher/k3s/k3s.yaml > /etc/rancher/k3s/k3s.yaml.public'
        - chmod 0600 /etc/rancher/k3s/k3s.yaml.public
        - |
          curl -fsSL --retry 60 --retry-delay 10 --retry-all-errors \
            -X PUT \
            -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
            -H "Content-Type: application/x-yaml" \
            --data-binary @/etc/rancher/k3s/k3s.yaml.public \
            "${var.catalyst_api_url}/api/v1/deployments/${var.deployment_id}/kubeconfig?region=${k}"
        - rm -f /etc/rancher/k3s/k3s.yaml.public
      PUT
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }

  # Per-secondary-region worker cloud-init — joins the secondary region's
  # own k3s CP via the secondary subnet's first usable IP (cidrhost(.../24, 2)).
  # Phase-0 workers use this; cluster-autoscaler-spawned workers in the
  # secondary region use the b64 form threaded into the secondary CP's
  # cloudinit (HCLOUD_CLOUD_INIT env var on bp-cluster-autoscaler-hcloud,
  # per issue #921).
  secondary_region_worker_cloud_init = {
    for k, r in local.secondary_regions :
    k => replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
      sovereign_fqdn             = var.sovereign_fqdn
      k3s_version                = var.k3s_version
      k3s_token                  = local.k3s_token
      cp_private_ip              = local.secondary_region_cp_ips[k]
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
      harbor_robot_token         = var.harbor_robot_token # #2842: seed harbor mirror auth on worker nodes
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }
}

# Per-secondary-region control-plane node — slice G1 lands single-CP per
# secondary region (HA in secondaries comes with G3 alongside ClusterMesh
# wiring). Each secondary CP shares the same hcloud_ssh_key.main and
# hcloud_firewall.main as the primary; only the network subnet, location,
# and cloud-init differ.
resource "hcloud_server" "secondary_control_plane" {
  for_each = local.secondary_regions

  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.key}-cp1"
  image        = "ubuntu-24.04"
  server_type  = each.value.controlPlaneSize
  location     = each.value.cloudRegion
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.secondary_region_cloud_init[each.key]

  network {
    network_id = hcloud_network.region[each.key].id
    ip         = local.secondary_region_cp_ips[each.key]
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "control-plane"
    "catalyst.openova.io/region"    = each.value.cloudRegion
    "catalyst.openova.io/region-id" = each.key
  }

  # Same 32 KiB user_data hard cap applies to secondary regions —
  # mirror the precondition from the primary CP (issue #966). The CP
  # variant bumped to 32256 in PR #1981 to absorb TBD-A50 Layer 3 (#1979)
  # write_files payload; worker variant stays at 30720 because
  # cloudinit-worker.tftpl is unaffected by this change.
  lifecycle {
    precondition {
      condition     = length(local.secondary_region_cloud_init[each.key]) <= 32256
      error_message = "Rendered control-plane cloud-init for secondary region ${each.key} is ${nonsensitive(length(local.secondary_region_cloud_init[each.key]))} bytes, exceeds 32256 (~31.5 KiB) guardrail (Hetzner hard cap is 32768)."
    }
    precondition {
      condition     = length(local.secondary_region_worker_cloud_init[each.key]) <= 30720
      error_message = "Rendered worker cloud-init for secondary region ${each.key} is ${nonsensitive(length(local.secondary_region_worker_cloud_init[each.key]))} bytes, exceeds 30720 (30 KiB) guardrail."
    }
  }

  depends_on = [hcloud_network_subnet.region]
}

# Per-secondary-region workers. Hetzner's `count` semantics inside a
# `for_each` map require flattening — we expand the (region, worker-index)
# product into a single map keyed on "{region-key}-w{index}".
#
# Worker private IPs are uniform across regions now that each region has
# its own 10.0.1.0/24: workers count up from 10.0.1.10 in their region's
# subnet, identical to the primary region's hcloud_server.worker layout
# (`10.0.1.${count.index + 10}`).
locals {
  secondary_workers = {
    for pair in flatten([
      for k, r in local.secondary_regions : [
        for i in range(r.workerCount) : {
          key        = "${k}-w${i + 1}"
          region_key = k
          region     = r
          worker_idx = i
          private_ip = "10.0.1.${10 + i}"
        }
      ]
    ]) :
    pair.key => pair
  }
}

resource "hcloud_server" "secondary_worker" {
  for_each = local.secondary_workers

  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.value.region_key}-w${each.value.worker_idx + 1}"
  image        = "ubuntu-24.04"
  server_type  = each.value.region.workerSize
  location     = each.value.region.cloudRegion
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.secondary_region_worker_cloud_init[each.value.region_key]

  network {
    network_id = hcloud_network.region[each.value.region_key].id
    ip         = each.value.private_ip
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "worker"
    "catalyst.openova.io/region"    = each.value.region.cloudRegion
    "catalyst.openova.io/region-id" = each.value.region_key
  }

  lifecycle {
    precondition {
      condition     = length(local.secondary_region_worker_cloud_init[each.value.region_key]) <= 30720
      error_message = "Rendered worker cloud-init for secondary region ${each.value.region_key} is ${length(local.secondary_region_worker_cloud_init[each.value.region_key])} bytes, exceeds 30720 (30 KiB) guardrail."
    }
  }

  depends_on = [hcloud_server.secondary_control_plane]
}

# Per-secondary-region load balancer — separate lb11 in each region's
# location so traffic terminates region-locally. PowerDNS lua-records
# (`ifurlup` health probes per docs/MULTI-REGION-DNS.md) point a single
# FQDN at the union of LB IPs and steer per-client based on geographic
# proximity + liveness.
resource "hcloud_load_balancer" "secondary" {
  for_each = local.secondary_regions

  name               = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.key}-lb"
  load_balancer_type = "lb11"
  location           = each.value.cloudRegion
  algorithm {
    type = "round_robin"
  }
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/region"    = each.value.cloudRegion
    "catalyst.openova.io/region-id" = each.key
  }
}

resource "hcloud_load_balancer_network" "secondary" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  network_id       = hcloud_network.region[each.key].id
  # Fix #182: pin LB private IP to top-of-its-own-subnet (10.0.1.254) so
  # an apply cannot race the CP's explicit `ip = "10.0.1.2"`. After the
  # per-region network refactor (DoD A2) every region has its OWN /24
  # inside its OWN hcloud_network, so the cross-region IP collision
  # class (prov #32 root cause) is gone by construction — but the .254
  # pin still guards intra-region CP/LB races at apply time.
  ip         = "10.0.1.254"
  depends_on = [hcloud_network_subnet.region]
}

resource "hcloud_load_balancer_target" "secondary_control_plane" {
  for_each = local.secondary_regions

  type             = "server"
  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  server_id        = hcloud_server.secondary_control_plane[each.key].id
  use_private_ip   = true

  depends_on = [hcloud_load_balancer_network.secondary]
}

resource "hcloud_load_balancer_target" "secondary_workers" {
  for_each = local.secondary_workers

  type             = "server"
  load_balancer_id = hcloud_load_balancer.secondary[each.value.region_key].id
  server_id        = hcloud_server.secondary_worker[each.key].id
  use_private_ip   = true

  depends_on = [
    hcloud_load_balancer_network.secondary,
    hcloud_server.secondary_worker,
  ]
}

resource "hcloud_load_balancer_service" "secondary_http" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 80
  # :80 → node:80 — Cilium Gateway served as LoadBalancer Service on
  # standard ports (hostNetwork off, no NodePort). Same per secondary region.
  destination_port = 80
}

resource "hcloud_load_balancer_service" "secondary_https" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 443
  # :443 → node:443 — Cilium Gateway served as LoadBalancer Service on
  # standard ports (hostNetwork off, no NodePort). Same per secondary region.
  destination_port = 443
}

resource "hcloud_load_balancer_service" "secondary_dns" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 53
  # :53 → node:53 — §854 / #4765: NodePorts are ABSOLUTELY FORBIDDEN; same
  # §854-clean-but-INERT shape as hcloud_load_balancer_service.dns above (the
  # :30053 nodePort it used to target can never be bound again — bp-powerdns
  # ≥1.2.18 renders allocateLoadBalancerNodePorts:false). ⚠️ node:53 does not
  # bind on Hetzner until #5086 lands (see the dns service comment above).
  destination_port = 53
  health_check {
    protocol = "tcp"
    port     = 53
    interval = 15
    timeout  = 10
    retries  = 3
  }
}
