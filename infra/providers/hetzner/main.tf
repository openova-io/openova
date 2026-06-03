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
  # forwards :53 → NodePort 30053 on the control-plane node where k3s exposes
  # the powerdns Service.
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

  # Kubernetes NodePort range (30000-32767) — required so:
  #
  # 1. Hetzner LB → backend health checks can probe the NodePort the
  #    clustermesh-apiserver Service binds (each region's LB probes
  #    `<node-public-ip>:<NodePort>`; without this rule the probe is
  #    dropped → LB marks target unhealthy → external clustermesh
  #    clients see "unexpected eof while reading" at TLS handshake →
  #    cilium-dbg status reports `0/N remote clusters ready`).
  # 2. Cross-region peer agents can connect to the remote
  #    clustermesh-apiserver via the LB. Hetzner LB use-private-ip is
  #    not viable because the per-region LB has no private-network
  #    attachment by default and our network topology pins one
  #    private /24 per region; the LB->backend hop must transit the
  #    public path. The cross-region case is by design public per
  #    DoD A2 (inter-region link = WireGuard over public IPs ALWAYS).
  #
  # Security: clustermesh-apiserver requires mTLS — peer agents present
  # a client cert signed by the peer cluster's cilium-ca. Anonymous
  # connections are immediately rejected at TLS handshake. The
  # firewall is therefore not the security boundary here; mTLS is.
  #
  # Caught on t129 (6cddff7ef4432bdc, 2026-05-16) — completes the D11
  # chain that began with PR #1525 (regionKeyFromSpec) and continued
  # through #1528 (clusterName derivation), #1530 (peer cert with
  # peer's CA), #1536 (hostAlias pattern). With this firewall rule
  # landed AND all the earlier fixes, the chain becomes end-to-end
  # working on a fresh prov.
  rule {
    direction   = "in"
    protocol    = "tcp"
    port        = "30000-32767"
    source_ips  = ["0.0.0.0/0", "::/0"]
    description = "Kubernetes NodePort range — clustermesh-apiserver LB health + cross-region traffic (mTLS-protected)"
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
  # The Sovereign supports N parent zones (primary + 0..N sme-pool). For
  # each zone the Cilium Gateway must declare a listener pair (HTTPS:30443
  # + HTTP:30080) hostnamed `*.<zone>` so cilium-envoy programs the SDS
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
  #   - HTTPS listener on port 30443 hostnamed `*.<zone>` with certificateRefs
  #     targeting `sovereign-wildcard-tls-<sanitised-zone>` (the chart's
  #     sovereign-wildcard-certs.yaml writes a Secret of that exact name).
  #   - HTTP  listener on port 30080 hostnamed `*.<zone>` for /.well-known/
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
  #   - MULTIPLE parent zones (SME pool present) → unique names per zone
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
  # matches exactly one label depth (`foo.omani.works`), so SME-tenant
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
  # Hetzner's 32 KiB user_data cap on 4-zone SME-pool Sovereigns (t39
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

  # Worker cloud-init computed BEFORE the control-plane cloud-init so it
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
  control_plane_cloud_init = replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
    # Primary CP's stable private IP — first allocatable host in the
    # primary subnet (10.0.1.2 in the canonical 10.0.1.0/24). Used by
    # the bp-cilium HelmRelease's CILIUM_K8S_SERVICE_HOST substitute
    # so cilium-operator on the primary cluster reaches its OWN local
    # CP (matching CA), not a different region's CP. Secondary CPs also
    # render 10.0.1.2 since every region has its OWN /24 (see
    # local.secondary_region_cp_ips above — the per-region network refactor
    # made every CP uniform on 10.0.1.2 within its own subnet).
    cp_private_ip = "10.0.1.2"
    # Per-region k3s pod/service CIDRs (DoD gate D11 — no collision across
    # ClusterMesh peers). Primary uses region_cluster_cidr["primary"]
    # (= 10.42.0.0/16) and region_service_cidr["primary"] (= 10.96.0.0/16).
    # Threaded into the k3s install line as --cluster-cidr= / --service-cidr=
    # in cloudinit-control-plane.tftpl.
    cluster_cidr   = local.region_cluster_cidr["primary"]
    service_cidr   = local.region_service_cidr["primary"]
    sovereign_fqdn = var.sovereign_fqdn
    # Slug form of the FQDN (dots → dashes) used to name per-Sovereign
    # Hetzner LBs (e.g. clustermesh-apiserver LB). Hetzner LB names are
    # limited to 63 chars and exclude dots; the slug is safe.
    sovereign_fqdn_slug = replace(var.sovereign_fqdn, ".", "-")
    sovereign_subdomain = var.sovereign_subdomain
    # OpenovaFlow integration (Agent #3, PR #1389/#1390 follow-up). The
    # bp-openova-flow-emitter (bootstrap-kit slot 57) reads SOVEREIGN_
    # DEPLOYMENT_ID + SOVEREIGN_REGION_KEY from the bootstrap-kit
    # Kustomization's postBuild.substitute env. Primary CP renders
    # var.region as the region key; secondary CPs render each.key from
    # the for_each loop in local.secondary_region_cloud_init.
    sovereign_deployment_id   = var.sovereign_deployment_id
    sovereign_region_key      = var.region
    marketplace_enabled       = var.marketplace_enabled
    qa_fixtures_enabled       = var.qa_fixtures_enabled
    qa_test_session_enabled   = var.qa_test_session_enabled
    qa_fixtures_namespace     = var.qa_fixtures_namespace
    qa_organization           = var.qa_organization
    wildcard_cert_use_staging = var.wildcard_cert_use_staging
    wildcard_cert_issuer      = local.wildcard_cert_issuer
    cluster_mesh_name         = var.cluster_mesh_name
    cluster_mesh_id           = var.cluster_mesh_id
    # G93.1 (Refs #2666) — BCP topology threading into the cloud-init
    # Kustomization postBuild.substitute map. Pre-G93.1 the template
    # hardcoded `SOVEREIGN_ENABLE_HOT_STANDBY: ""`; the chart's
    # `${SOVEREIGN_ENABLE_HOT_STANDBY:-}` envsubst then always
    # evaluated to empty and every multi-region Sovereign landed
    # Pillar 3 broken. The two vars below carry the operator's choice
    # verbatim — catalyst-api computes them from Request.BcpTopology
    # (with auto-derivation for the empty/multi-region case).
    bcp_topology       = var.bcp_topology
    enable_hot_standby = var.enable_hot_standby

    # #2840 (2026-06-03): per-CNPG-cluster instance count. Mirrors
    # infra/providers/huawei/main.tf:695. Multi-region Sovereigns get
    # 2 instances per CNPG cluster so the operator can spread across
    # regions via podAntiAffinity. Single-region keeps 1. The
    # bootstrap-kit substitute map references this via
    # `SOVEREIGN_CNPG_INSTANCES: "$${sovereign_cnpg_instances}"` —
    # see cloudinit-control-plane.tftpl PR-companion to this var.
    sovereign_cnpg_instances = length(var.regions) > 1 ? "2" : "1"

    # Multi-domain Sovereign (issue #827). When the wizard supplies an
    # explicit parent-domain list, use it verbatim. Otherwise default to a
    # single-zone array derived from sovereign_fqdn so legacy single-zone
    # provisioning paths render an identical Helm values shape (one zone,
    # one wildcard cert) — no special-casing in the chart templates.
    parent_domains_yaml = coalesce(
      var.parent_domains_yaml,
      format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
    )
    # Cilium Gateway listener YAML is no longer threaded into cloud-init
    # (Closes #2118). The bp-catalyst-platform chart's
    # templates/sovereign-tls-vars-cm.yaml renders the listener block
    # from .Values.parentZones into a flux-system/sovereign-tls-vars
    # ConfigMap; the sovereign-tls Kustomization's
    # postBuild.substituteFrom picks it up. Keeps cloud-init under
    # Hetzner's 32 KiB user_data cap on multi-zone SME-pool Sovereigns.
    # sovereign_regions_json — canonical multi-region RegionSpec[]
    # JSON literal. Threaded into bp-catalyst-platform's
    # .Values.sovereign.regionsJson via the bootstrap-kit slot 13
    # postBuild.substitute `SOVEREIGN_REGIONS_JSON`. Catalyst-api on
    # the Sovereign side reads via the `sovereign-fqdn` ConfigMap env
    # `SOVEREIGN_REGIONS_JSON` and uses to seed Request.Regions on the
    # chroot Deployment so /infrastructure/topology returns the
    # full multi-region tree (DoD D5).
    sovereign_regions_json = jsonencode(var.regions)
    # sovereign_configured_regions_yaml — YAML inline-list literal of the
    # cloudRegions this Sovereign was provisioned with (e.g. `["fsn1","hel1"]`).
    # Threaded into bootstrap-kit slot 13's
    # `sovereign.configuredRegions: ${SOVEREIGN_CONFIGURED_REGIONS_YAML:-[]}`
    # substitute (PR for issue #1844). The chart's sovereign-fqdn ConfigMap
    # joins this list into a comma-separated `configuredRegions` key for the
    # catalyst-ui Dashboard's SovereignCard + Networking → ClusterMesh tab.
    # When var.regions is empty (back-compat singular-region path) the
    # rendered YAML is `[]` so the chart's `default (list)` keeps the
    # ConfigMap key empty (no regression).
    sovereign_configured_regions_yaml = jsonencode([
      for r in var.regions : r.cloudRegion
    ])
    org_name  = var.org_name
    org_email = var.org_email
    region    = var.region
    # Canonical 4-segment region label for k3s --node-label
    # openova.io/region=<region_canonical_label>. Replaces the previously
    # hardcoded `hz-fsn-rtz-prod` literal that broke every non-fsn1
    # primary Sovereign. See locals.region_canonical_label above.
    region_canonical_label = local.region_canonical_label["primary"]
    # Primary region canonical label — same as region_canonical_label on
    # the primary CP, but kept as a separate template var so the
    # secondary CP template knows the SOVEREIGN's primary region
    # (different from its own). Threaded into the bootstrap-kit
    # Kustomization's QA_PRIMARY_REGION substitute so qa-fixtures
    # primaryRegion follows the actual Sovereign primary, never the
    # chart's hardcoded `hz-fsn-rtz-prod` default.
    primary_region_canonical_label = local.region_canonical_label["primary"]
    # First non-primary canonical region label (used as the D31
    # active-hot-standby `replicaRegion` default). Threaded into the
    # bootstrap-kit Kustomization's SOVEREIGN_REPLICA_REGION substitute
    # so the chart's `sovereign.replicaRegion` is populated on every
    # multi-region Sovereign without a per-overlay write. Empty when
    # the Sovereign is single-region (var.regions length <= 1). TBD-A15
    # (issue #1844, 2026-05-18).
    replica_region_canonical_label = length(local.secondary_regions) > 0 ? (
      local.region_canonical_label[keys(local.secondary_regions)[0]]
    ) : ""
    # Per-role vCluster enable flags (DoD A4 topology). Primary region
    # renders MGMT+DMZ vClusters → mgmt_vcluster_enabled=true. RTZ stays
    # off here — secondary regions flip RTZ on. The bp-dmz-vcluster slot
    # 54 chart-side default already enables DMZ everywhere, so no flag
    # for DMZ here. See clusters/_template/bootstrap-kit/54,58,59-*.yaml.
    mgmt_vcluster_enabled      = "true"
    rtz_vcluster_enabled       = "false"
    ha_enabled                 = var.ha_enabled
    worker_count               = var.worker_count
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    gitops_repo_url            = var.gitops_repo_url
    gitops_branch              = var.gitops_branch
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    ghcr_pull_username         = local.ghcr_pull_username
    ghcr_pull_token            = var.ghcr_pull_token
    ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64

    # Object Storage credentials — interpolated into the Sovereign's
    # `object-storage` K8s Secret at cloud-init time so Harbor (#383)
    # and Velero (#384) HelmReleases find the credentials in the cluster
    # from Phase 1 onwards. Same pattern as ghcr_pull_token: never in
    # git, only in the encrypted per-deployment OpenTofu workdir + the
    # Sovereign's user_data, wiped on `tofu destroy`. Per #425 the K8s
    # Secret name is vendor-agnostic (`flux-system/object-storage`) —
    # no `hetzner-` prefix — so a future AWS / Azure / GCP / OCI
    # Sovereign reuses every existing chart without rename.
    object_storage_endpoint    = local.object_storage_endpoint
    object_storage_region      = var.object_storage_region
    object_storage_bucket_name = var.object_storage_bucket_name
    object_storage_access_key  = var.object_storage_access_key
    object_storage_secret_key  = var.object_storage_secret_key

    # OpenTofu→Crossplane handover (issue #425). The Hetzner Cloud API
    # token is interpolated into both the `flux-system/cloud-credentials`
    # K8s Secret AND the cloud-init's runcmd that applies the matching
    # Crossplane Provider+ProviderConfig. Once Crossplane core comes up
    # (via bp-crossplane) the Provider transitions Healthy=True and the
    # Sovereign is ready to accept Day-2 XRC writes — at which point
    # the catalyst-api's bespoke Hetzner-API hatching is retired in
    # favour of XRC writes per ADR-0001 §11.3 + INVIOLABLE-PRINCIPLES #3.
    hcloud_token = var.hcloud_token

    # Dynadot credentials — injected into cert-manager/dynadot-api-credentials
    # K8s Secret at cloud-init time so the bp-cert-manager-dynadot-webhook Pod
    # can start without a manual secret-creation step (issue #550 root-cause fix).
    # dynadot_managed_domains defaults to the parent zone of sovereign_fqdn when
    # the caller leaves it blank — e.g. "omani.works" for "console.otech22.omani.works".
    dynadot_key             = var.dynadot_key
    dynadot_secret          = var.dynadot_secret
    dynadot_managed_domains = coalesce(var.dynadot_managed_domains, join(".", slice(split(".", var.sovereign_fqdn), 1, length(split(".", var.sovereign_fqdn)))))

    # Cloud-init kubeconfig postback (issue #183, Option D). When
    # all three are non-empty, the template renders a runcmd that
    # rewrites k3s.yaml's 127.0.0.1:6443 to the LB's public IPv4
    # and PUTs the result to the catalyst-api with a Bearer header.
    # When any is empty (legacy out-of-band fetch path), the runcmd
    # is omitted entirely.
    #
    # load_balancer_ipv4 is interpolated from the hcloud_load_balancer
    # resource at apply time. Referencing it here implicitly forces
    # the LB to be created before the control-plane server boots —
    # which is exactly the ordering we want, because the new
    # Sovereign's curl PUT to catalyst-api needs to come from a
    # source IP the firewall accepts (any 0.0.0.0/0 → 443 outbound)
    # and arrive on a kubeconfig whose `server:` field is a
    # public-routable address.
    # Harbor pull-through mirror token (issue #557, Option A).
    # Passed into registries.yaml written at cloud-init time so containerd
    # authenticates against harbor.openova.io proxy-cache projects.
    harbor_robot_token = var.harbor_robot_token

    # Contabo PowerDNS API key (PR #686, F3 followup). Interpolated into
    # the Sovereign's cert-manager/powerdns-api-credentials Secret so
    # bp-cert-manager-powerdns-webhook can write DNS-01 challenge TXT
    # records to contabo's authoritative omani.works zone.
    powerdns_api_key = var.powerdns_api_key

    # PDM (Pool Domain Manager) basic-auth credentials (issue #879 Bug 2).
    # Interpolated into the Sovereign's `flux-system/pdm-basicauth` Secret
    # at cloud-init time so catalyst-api in catalyst-system can call PDM
    # at https://pool.openova.io with `Authorization: Basic …` for the
    # Day-2 multi-domain "Add another parent domain" flow. Reflector
    # auto-mirrors the Secret into `catalyst-system` (same canonical
    # pattern flux-system/ghcr-pull and flux-system/harbor-robot-token
    # already use). Sensitive — never logged, never committed.
    pdm_basic_auth_user = var.pdm_basic_auth_user
    pdm_basic_auth_pass = var.pdm_basic_auth_pass

    deployment_id           = var.deployment_id
    kubeconfig_bearer_token = var.kubeconfig_bearer_token
    catalyst_api_url        = var.catalyst_api_url
    handover_jwt_public_key = var.handover_jwt_public_key
    load_balancer_ipv4      = hcloud_load_balancer.main.ipv4
    # control_plane_ipv4 is NOT templated — it would create a dependency cycle
    # (cloud-init → control_plane.ipv4_address → control_plane.user_data → cloud-init).
    # The cloud-init runs ON the CP node, so it resolves its own public IP at boot
    # via Hetzner metadata service (169.254.169.254) — see cloudinit-control-plane.tftpl.

    # Issue #921 — base64-encoded worker cloud-init for the bp-cluster-
    # autoscaler-hcloud HelmRelease's HCLOUD_CLOUD_INIT env var. Same
    # bootstrap content the Phase-0 workers receive, so autoscaler-spawned
    # workers join the cluster identically.
    worker_cloud_init_b64 = base64encode(local.worker_cloud_init)

    # Issue #1778 — Hetzner resource names threaded into
    # flux-system/cloud-credentials so the cluster-autoscaler can map them
    # onto HCLOUD_NETWORK / HCLOUD_FIREWALL / HCLOUD_SSH_KEY env vars.
    # Without these the autoscaler-spawned VMs come up on public-only
    # interfaces (no private 10.0.0.0/16 attachment), the worker cloud-
    # init's `K3S_URL=https://10.0.1.2:6443` is unreachable, the k3s
    # agent join silently fails, and the autoscaler times out the
    # scale-up after 15m → backoff. Names are the Phase-0 resource names
    # verbatim — the autoscaler resolves them via the Hetzner API at
    # scale-up time. Primary CP points at the primary region's per-region
    # network so autoscaler-spawned workers join the primary region's k3s
    # (which is reachable on the local 10.0.1.2). Secondary CPs render
    # their own region's network name (see local.secondary_region_cloud_init).
    hcloud_network_name  = hcloud_network.region["primary"].name
    hcloud_firewall_name = hcloud_firewall.main.name
    hcloud_ssh_key_name  = hcloud_ssh_key.main.name

    # Multi-region kubeconfig PUT-back (operator mandate, 2026-05-12).
    # Empty string for the primary CP → catalyst-api stores the file
    # at <kubeconfigsDir>/<id>.yaml (back-compat with single-region).
    # Secondary regions pass their region key here (see the for_each
    # call below) so catalyst-api stores them at
    # <kubeconfigsDir>/<id>-<region>.yaml. catalyst-api's phase1Watch
    # then spawns one helmwatch.Bridge per kubeconfig so the canvas
    # surfaces install-* HRs from EVERY region, not just primary.
    kubeconfig_postback_region = ""
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
  # already created) if the rendered cloud-init exceeds 32256 bytes (32 KiB
  # minus 512 B safety buffer under the Hetzner hard cap of 32768). PR #1981
  # repackaged TBD-A50 Layer 3 (#1979) into write_files (~770 B savings) and
  # bumped the guardrail from 31744 to 32256 so the ExternalIP reconciler
  # ships with a healthy headroom in front of the hard cap.
  lifecycle {
    precondition {
      condition = length(local.control_plane_cloud_init) <= 32256
      # G107 #2702 (2026-06-01): wrap length() with nonsensitive() so the
      # byte count shows in the error_message. Without it tofu redacts the
      # entire message because the local references hcloud_token / ssh_key
      # marked sensitive. Operators iterating on cloud-init slim-downs
      # cannot target their cuts without knowing the actual overshoot;
      # nonsensitive() leaks only the integer length, not any token bytes.
      error_message = "Rendered control-plane cloud-init is ${nonsensitive(length(local.control_plane_cloud_init))} bytes, exceeds 32256 (~31.5 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-control-plane.tftpl. See issues #966 / #1981."
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

# ── Load balancer: lb11, 80/443 → control plane NodePorts 31080/31443 ─────

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
# serve ingress traffic on its NodePort. Adding workers as LB targets
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
  # destination_port=30080 — Cilium Gateway listens on a high port
  # (clusters/_template/sovereign-tls/cilium-gateway.yaml) because even
  # with hostNetwork=true + privileged=true + NET_BIND_SERVICE +
  # envoy-keep-cap-netbindservice=true, cilium-envoy still gets
  # "Permission denied" binding 0.0.0.0:80 on the host. The bind is
  # intercepted by cilium-agent's BPF socket-LB program in a way that
  # is not resolvable via container caps. High ports work without
  # privileged binding (verified on otech47 after iterating through
  # the privileged-bind chain). Hetzner LB translates the public 80→
  # node:30080 so the operator-facing URL stays `http://console.<fqdn>/`.
  destination_port = 30080
}

resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 443
  # destination_port=30443 — see http service comment above. The
  # cilium-gateway HTTPS listener binds 30443 (not 443) because
  # privileged-port bind through cilium-agent's BPF intercept fails
  # regardless of capability configuration. HCLB does the listener-side
  # port translation so external users still hit `https://console.<fqdn>/`.
  destination_port = 30443
}

resource "hcloud_load_balancer_service" "dns" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 53
  # NodePort 30053 — the powerdns Service exposes DNS on this NodePort via
  # the anycast-endpoint ServiceType=NodePort overlay in 11-powerdns.yaml.
  # lb11 supports TCP only; UDP :53 is handled via the Hetzner Firewall
  # opening UDP/53 directly to the node's public IP (k3s NodePort handles
  # UDP natively via iptables DNAT). The LB TCP path handles zone transfers
  # and ACME challenge TXT queries; UDP is used for regular resolution.
  destination_port = 30053
  health_check {
    protocol = "tcp"
    port     = 30053
    interval = 15
    timeout  = 10
    retries  = 3
  }
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
    k => replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
      # Per-region CP's stable private IP. After the per-region network
      # refactor (2026-05-15 DoD A2) every region has its OWN /24 inside
      # its OWN hcloud_network, so every secondary CP also sits at
      # 10.0.1.2 — uniform with the primary. Used by the bp-cilium
      # HelmRelease's CILIUM_K8S_SERVICE_HOST substitute so cilium-
      # operator on each cluster reaches its OWN local CP (matching CA).
      cp_private_ip = local.secondary_region_cp_ips[k]
      # Per-region k3s pod/service CIDRs (DoD gate D11). Each region gets
      # its own /16 off the 10.42+i.0/12 + 10.96+i.0/12 supernets so
      # ClusterMesh peer pods/services don't collide in routing tables.
      cluster_cidr        = local.region_cluster_cidr[k]
      service_cidr        = local.region_service_cidr[k]
      sovereign_fqdn      = var.sovereign_fqdn
      sovereign_fqdn_slug = replace(var.sovereign_fqdn, ".", "-")
      sovereign_subdomain = var.sovereign_subdomain
      # OpenovaFlow integration (Agent #3). The secondary CP's region
      # key is each.key from the secondary_regions for_each (e.g. "hel1"
      # for a Helsinki secondary). Multi-region Sovereigns thus emit
      # distinct region tags on FlowNodes, which the canvas groups into
      # per-region super-bubbles via `contains` relationships.
      sovereign_deployment_id   = var.sovereign_deployment_id
      sovereign_region_key      = k
      marketplace_enabled       = var.marketplace_enabled
      qa_fixtures_enabled       = var.qa_fixtures_enabled
      qa_test_session_enabled   = var.qa_test_session_enabled
      qa_fixtures_namespace     = var.qa_fixtures_namespace
      qa_organization           = var.qa_organization
      wildcard_cert_use_staging = var.wildcard_cert_use_staging
      wildcard_cert_issuer      = local.wildcard_cert_issuer
      # G93.1 (Refs #2666) — same BCP topology values as the primary CP.
      # Sovereign-wide invariant: every region's bootstrap-kit Kustomization
      # substitute resolves to the same SOVEREIGN_ENABLE_HOT_STANDBY +
      # SOVEREIGN_BCP_TOPOLOGY so sme_tenant_gitops's tenant render and
      # the chart-side cnpg-pair gating agree across regions.
      bcp_topology       = var.bcp_topology
      enable_hot_standby = var.enable_hot_standby
      # Per-secondary-region ClusterMesh anchors. id is incremented per
      # peer index so each secondary region gets a unique slot in the
      # mesh registry; primary region keeps var.cluster_mesh_id.
      cluster_mesh_name = local.secondary_region_cluster_mesh_name[k]
      cluster_mesh_id   = local.secondary_region_cluster_mesh_id[k]
      parent_domains_yaml = coalesce(
        var.parent_domains_yaml,
        format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
      )
      # Cilium Gateway listener YAML is no longer threaded into cloud-init
      # (Closes #2118). Same bp-catalyst-platform chart runs in every
      # region — each peer's chart renders its own
      # flux-system/sovereign-tls-vars ConfigMap and sovereign-tls reads
      # it locally via postBuild.substituteFrom. See main.tf locals
      # comment (~line 422) for full rationale.
      # Same JSON-encoded RegionSpec[] as the primary CP — every region's
      # bp-catalyst-platform renders the same sovereign.regionsJson value
      # (the cluster topology is Sovereign-wide, not per-region).
      sovereign_regions_json = jsonencode(var.regions)
      # Same SOVEREIGN_CONFIGURED_REGIONS_YAML as the primary — chart-wide
      # configuredRegions list is Sovereign-wide invariant. TBD-A15.
      sovereign_configured_regions_yaml = jsonencode([
        for rr in var.regions : rr.cloudRegion
      ])
      org_name  = var.org_name
      org_email = var.org_email
      region    = r.cloudRegion
      # Per-region canonical 4-segment label — each secondary CP labels
      # its k3s node with ITS OWN region tag (e.g. `hz-nbg-rtz-prod` for
      # nbg1-1), not the primary's tag. See locals.region_canonical_label
      # above.
      region_canonical_label = local.region_canonical_label[k]
      # Primary region's canonical label — threaded into THIS secondary's
      # bootstrap-kit Kustomization substitute so the per-cluster
      # qa-fixtures rendered on the secondary still targets the
      # Sovereign-wide primary region (qaFixtures.primaryRegion is
      # singular per the chart contract).
      primary_region_canonical_label = local.region_canonical_label["primary"]
      # Same as primary CP — Sovereign-wide D31 active-hot-standby
      # replicaRegion default (first non-primary region's canonical label).
      replica_region_canonical_label = length(local.secondary_regions) > 0 ? (
        local.region_canonical_label[keys(local.secondary_regions)[0]]
      ) : ""
      # Per-role vCluster enable flags (DoD A4). Secondary region
      # renders DMZ+RTZ vCluster → rtz_vcluster_enabled=true. MGMT
      # stays off on secondaries (single MGMT vCluster on primary).
      mgmt_vcluster_enabled      = "false"
      rtz_vcluster_enabled       = "true"
      ha_enabled                 = false # secondary regions land single-CP in slice G1; G3 introduces per-region HA
      worker_count               = r.workerCount
      k3s_version                = var.k3s_version
      k3s_token                  = local.k3s_token
      gitops_repo_url            = var.gitops_repo_url
      gitops_branch              = var.gitops_branch
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
      hcloud_token               = var.hcloud_token
      dynadot_key                = var.dynadot_key
      dynadot_secret             = var.dynadot_secret
      dynadot_managed_domains    = coalesce(var.dynadot_managed_domains, join(".", slice(split(".", var.sovereign_fqdn), 1, length(split(".", var.sovereign_fqdn)))))
      harbor_robot_token         = var.harbor_robot_token
      powerdns_api_key           = var.powerdns_api_key
      pdm_basic_auth_user        = var.pdm_basic_auth_user
      pdm_basic_auth_pass        = var.pdm_basic_auth_pass
      deployment_id              = var.deployment_id
      kubeconfig_bearer_token    = var.kubeconfig_bearer_token
      catalyst_api_url           = var.catalyst_api_url
      handover_jwt_public_key    = var.handover_jwt_public_key
      load_balancer_ipv4         = hcloud_load_balancer.secondary[k].ipv4
      worker_cloud_init_b64      = base64encode(local.secondary_region_worker_cloud_init[k])

      # Issue #1778 (F7 multi-region completion) — same hcloud_*_name
      # threading as the primary CP templatefile call so the secondary
      # regions' cluster-autoscaler also has the private-network
      # attachment names. Each secondary references its OWN region's
      # network (per the 2026-05-15 DoD A2 per-region-network refactor)
      # so autoscaler-spawned workers land in the same isolated /16 as
      # the region's CP and reach k3s at 10.0.1.2 locally.
      hcloud_network_name  = hcloud_network.region[k].name
      hcloud_firewall_name = hcloud_firewall.main.name
      hcloud_ssh_key_name  = hcloud_ssh_key.main.name

      # Multi-region kubeconfig PUT-back — region key for this secondary
      # CP. cloudinit-control-plane.tftpl appends `?region=<k>` to the
      # PUT URL so catalyst-api stores it at
      # <kubeconfigsDir>/<id>-<k>.yaml and phase1Watch can spawn a
      # per-region helmwatch.Bridge.
      kubeconfig_postback_region = k
      # G117 #2896-followup (2026-06-03): match primary CP's
      # sovereign_cnpg_instances threading so the secondary regions'
      # bootstrap-kit also stamps the right CNPG instance count. Same
      # rule as primary — `2` when var.regions has >1 entry (because we
      # ARE multi-region by construction at this point), `1` for
      # singleton (never reached on secondary path, defensive default).
      sovereign_cnpg_instances = length(var.regions) > 1 ? "2" : "1"
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
  destination_port = 30080
}

resource "hcloud_load_balancer_service" "secondary_https" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 443
  destination_port = 30443
}

resource "hcloud_load_balancer_service" "secondary_dns" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 53
  destination_port = 30053
  health_check {
    protocol = "tcp"
    port     = 30053
    interval = 15
    timeout  = 10
    retries  = 3
  }
}
