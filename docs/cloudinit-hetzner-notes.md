# Hetzner cloud-init extended commentary

Extracted from `infra/providers/hetzner/cloudinit-control-plane.tftpl` to keep
the rendered cloud-init below the 32256-byte Hetzner guardrail (G107 #2702).
The .tftpl carries 1-line pointers; full rationale lives below.

## block-01-line-2095

Formerly `cloudinit-control-plane.tftpl:2095-2110` (G107 #2702 strip).

```
  # ── flux-system/cloud-credentials Secret + Crossplane Provider (issue #425) ─
  #
  # Apply the Hetzner Cloud API token Secret + the Crossplane Provider
  # package + ProviderConfig BEFORE Flux's bootstrap-kit lands
  # bp-crossplane. The Provider package itself is installed by
  # Crossplane core (which bp-crossplane brings up); applying the
  # Provider CR here just registers the package install request — it
  # transitions Healthy=True a few minutes later once the bootstrap-
  # kit's Crossplane core controllers come online. The ProviderConfig
  # sits in waiting state until the Provider's CRDs are registered, at
  # which point it goes Ready=True and the Sovereign is ready to accept
  # Day-2 XRC writes.
  #
  # Per ADR-0001 §11.3 + INVIOLABLE-PRINCIPLES #3 this is the OpenTofu
  # → Crossplane handover seam. Tofu provisions Phase 0 exactly once;
  # everything else flows through XRC writes against this Provider.
```

## block-02-line-2079

Formerly `cloudinit-control-plane.tftpl:2079-2092` (G107 #2702 strip).

```
  # ── flux-system/object-storage Secret (issue #371, vendor-agnostic since #425) ─
  #
  # Apply the operator-issued Object Storage credentials so they're in
  # the cluster BEFORE Flux reconciles bp-harbor (#383) and bp-velero
  # (#384). Both Blueprints reference `secretRef: name: object-storage`
  # in their HelmRelease values; without this Secret the install reports
  # NoSuchKey at chart-install probe time and Phase 1 stalls.
  #
  # Same idempotency property as ghcr-pull above — re-running cloud-init
  # against an existing Sovereign overwrites the manifest with the same
  # bytes (or rotated bytes when the operator has issued fresh keys); a
  # missing-bucket scenario is impossible by construction because main.tf's
  # aws_s3_bucket resource creates the bucket in the same `tofu apply`
  # run that renders this user_data.
```

## block-03-line-2029

Formerly `cloudinit-control-plane.tftpl:2029-2067` (G107 #2702 strip).

```
  # ── OpenBao auto-unseal seed Secret (issue #316) ─────────────────────
  #
  # Generate a one-shot 32-byte recovery seed during cloud-init and
  # write it to a K8s Secret `openbao-recovery-seed` in the `openbao`
  # namespace. The bp-openbao chart (v1.2.0+) renders a post-install
  # Job (templates/init-job.yaml, Helm hook weight 5) that:
  #   1. Reads this seed Secret.
  #   2. Calls `bao operator init -recovery-shares=1 -recovery-threshold=1`.
  #   3. Persists the recovery key inside OpenBao's auto-unseal config
  #      (so subsequent pod restarts unseal automatically).
  #   4. Deletes this seed Secret on success.
  #
  # The seed is single-use — once consumed by the init Job, it never
  # exists again. The recovery key + root token live ONLY inside
  # OpenBao's Raft state (acceptance criterion #6 of issue #316).
  #
  # Why a fresh /dev/urandom value (NOT a value baked into Terraform):
  # the recovery seed must NEVER be readable from outside the
  # control-plane node, NEVER appear in tfstate, NEVER appear in any
  # cloud-init render audit log. Generating it here at provision time
  # means the only window of plaintext exposure is the few seconds
  # between this Secret apply and the Helm post-install Job consuming
  # it — bounded by the bootstrap-kit reconcile cadence (1m max).
  #
  # Why we create the namespace here: the bp-openbao HelmRelease in
  # clusters/_template/bootstrap-kit/08-openbao.yaml ships a Namespace
  # manifest, but Flux applies that Namespace + the HelmRelease
  # together. The Helm post-install hook would race the seed Secret
  # apply if we waited for Flux to create the namespace. Pre-creating
  # the namespace at cloud-init time eliminates the race.
  #
  # Idempotency: `kubectl apply` of the namespace and `kubectl create
  # secret --dry-run=client -o yaml | kubectl apply -f -` of the
  # Secret are both safe to re-run. A re-provision (same Sovereign
  # FQDN) regenerates a fresh seed and re-applies — at which point the
  # init Job has either already consumed the previous seed (so the new
  # one becomes a no-op the next time the Helm hook runs) OR sees
  # OpenBao already initialised and exits idempotently without
  # touching the new seed.
```

## block-04-line-2013

Formerly `cloudinit-control-plane.tftpl:2013-2025` (G107 #2702 strip).

```
  # ── catalyst-system/catalyst-handover-jwt-public Secret (issue #606 followup) ─
  #
  # Apply the Sovereign-side Catalyst handover-JWT public-key Secret BEFORE
  # Flux reconciles bp-catalyst-platform. Without this Secret the catalyst-api
  # Pod's optional Secret-volume mount falls through, the JWK file is absent
  # at /etc/catalyst/handover-jwt-public/public.jwk, and GET /auth/handover
  # returns "server misconfiguration: public key unavailable" (caught live on
  # otech48, 2026-05-03).
  #
  # Pre-create the catalyst-system namespace (the bp-catalyst-platform
  # HelmRelease wrapper also declares it, but Flux applies the namespace
  # alongside the HelmRelease — racing this Secret apply). Same idempotency
  # pattern as the cert-manager pre-create above.
```

## block-05-line-1998

Formerly `cloudinit-control-plane.tftpl:1998-2009` (G107 #2702 strip).

```
  # ── cert-manager/powerdns-api-credentials Secret (PR #681 followup) ──
  #
  # Apply the contabo PowerDNS credentials BEFORE Flux reconciles
  # bp-cert-manager-powerdns-webhook: the webhook reads
  # apiKeySecretRef.name=powerdns-api-credentials at startup. A missing
  # Secret causes the DNS-01 challenge to hang indefinitely with
  # "secrets powerdns-api-credentials not found" and the wildcard cert
  # never issues — the Sovereign Console TLS handshake then fails
  # (caught live on otech47).
  #
  # cert-manager namespace is created by bp-cert-manager via Flux — we
  # pre-create it idempotently here so the Secret apply does not fail.
```

## block-06-line-1961

Formerly `cloudinit-control-plane.tftpl:1961-1974` (G107 #2702 strip).

```
  # ── flux-system/ghcr-pull Secret (applied BEFORE GitRepository) ──────
  #
  # Apply the docker-registry pull secret rendered above. This MUST land
  # before the GitRepository + Kustomization in flux-bootstrap.yaml,
  # because the bootstrap-kit Kustomization includes HelmRepository CRs
  # that reference this Secret by name; the source-controller resolves
  # them on its first reconciliation tick and a missing Secret propagates
  # as a Ready=False/AuthError state that has been observed to persist
  # for 5+ minutes even after the Secret is later applied.
  #
  # Idempotent: `kubectl apply` against an existing Secret is a no-op
  # when the manifest's bytes match. A reprovision (same Sovereign FQDN)
  # rewrites this with the same content; a token rotation propagates
  # through here on the next cloud-init render.
```

## block-07-line-1948

Formerly `cloudinit-control-plane.tftpl:1948-1957` (G107 #2702 strip).

```
  # G22 (Refs #2572, 2026-05-29): strip flux-bundled NetworkPolicies.
  # The upstream install.yaml creates flux-system/{allow-egress,
  # allow-scraping,allow-webhooks}. On Cilium 1.16 default mode, the
  # empty `egress: [{}]` rule is interpreted as deny-world → source-
  # controller can't reach github.com to clone bootstrap-kit. bp-network-
  # policies (slot 30) provides correct zero-trust via CCNPs with flux-
  # system in allowSystemNamespaces, so no security regression. (Huawei
  # cloudinit uses `flux install --network-policy=false` for the same
  # effect; here we delete post-apply since this code path uses the
  # upstream install.yaml directly.)
```

## block-08-line-1919

Formerly `cloudinit-control-plane.tftpl:1919-1946` (G107 #2702 strip).

```
  # Install Flux core. Cilium is now the cluster's CNI, so Flux pods will
  # actually start. Flux then reconciles clusters/_template/ (with
  # SOVEREIGN_FQDN substituted via postBuild — issue #218) which
  # adopts the Helm release above as bp-cilium and continues with
  # bp-cert-manager, bp-flux (which ADOPTS this Flux install rather than
  # reinstalls — see version-pin invariant below), bp-crossplane, etc.
  #
  # CRITICAL VERSION-PIN INVARIANT — DO NOT CHANGE IN ISOLATION
  # -----------------------------------------------------------
  # The version pinned in the URL below MUST match the upstream Flux
  # release that `platform/flux/chart/Chart.yaml`'s `flux2` subchart
  # bundles, otherwise bp-flux's HelmRelease runs `helm install` on top
  # of THIS Flux installation with a different upstream version, the
  # CRD `status.storedVersions` mismatches, Helm install fails, rollback
  # fires, and rollback DELETES the running Flux controllers — leaving
  # the cluster with no GitOps engine, unrecoverable in-place.
  #
  # Live verified on omantel.omani.works on 2026-04-29 — every Sovereign
  # provisioned without this pin in sync was destroyed minutes after
  # bp-flux's first reconcile. See docs/RUNBOOK-PROVISIONING.md
  # §"bp-flux double-install".
  #
  # Mapping (cloud-init install.yaml -> chart subchart -> appVersion):
  #   v2.4.0  ->  flux2 2.14.1  ->  appVersion 2.4.0  <- CURRENT
  #   v2.3.0  ->  flux2 2.13.0  ->  appVersion 2.3.0
  #
  # CI gate `platform/flux/chart/tests/version-pin-replay.sh` rejects
  # divergence between this URL's version and the chart's subchart pin.
```

## block-09-line-1849

Formerly `cloudinit-control-plane.tftpl:1849-1905` (G107 #2702 strip).

```
  # ── Cilium FIRST (before Flux) ───────────────────────────────────────────
  #
  # k3s started with --flannel-backend=none, so the cluster has NO CNI yet.
  # If we apply Flux install.yaml at this point, the Flux controller pods
  # stay Pending forever — kubelet rejects them with
  #   "container runtime network not ready: cni plugin not initialized"
  # Flux is then unable to reconcile bp-cilium, so Cilium is never
  # installed → bootstrap deadlock that we hit in production at
  # omantel.omani.works deployment 5cd1bceaaacb71f6 (25 min stuck Pending).
  #
  # Bootstrap chicken-and-egg: Cilium IS the install unit (bp-cilium), but
  # Flux needs a CNI to run, and Cilium IS the CNI. Resolution: install
  # Cilium ONCE here via Helm with the same chart + values bp-cilium would
  # apply later. When Flux reconciles bp-cilium, it adopts the existing
  # release (Helm release-name match), so there is no churn.
  #
  # Per INVIOLABLE-PRINCIPLES.md #3 the GitOps engine is Flux — this Helm
  # install is the one-shot bootstrap exception explicitly authorised by
  # the same principle's "everything ELSE" qualifier. Both the chart
  # version AND the values must match `platform/cilium/blueprint.yaml`
  # + `clusters/_template/bootstrap-kit/01-cilium.yaml` so the bootstrap
  # install and the reconciled HelmRelease are byte-identical — issue
  # #491. The values come from /var/lib/catalyst/cilium-values.yaml
  # written via cloud-init `write_files:` above; chart version stays
  # inline as a --version flag because OpenTofu's `var.k3s_version`
  # parameterisation wires through to it (per INVIOLABLE-PRINCIPLES
  # #4 — never hardcode).
  # ── Gateway API CRDs BEFORE Cilium ──────────────────────────────────────
  #
  # Cilium 1.16.x operator checks for gateway.networking.k8s.io CRDs at
  # startup. If the CRDs are absent the operator disables its gateway
  # controller entirely and never re-checks — a static decision made once
  # at boot. This creates a race when Gateway API CRDs are installed AFTER
  # k3s/Cilium, which is the normal Flux GitOps order (bp-gateway-api
  # reconciles minutes after bp-cilium). Result: every fresh Sovereign has
  # no GatewayClass/cilium, all HTTPRoutes are orphaned, no routing.
  #
  # Fix: pre-install the Gateway API experimental CRDs here, before the
  # Cilium helm install below. The experimental channel is required because
  # Cilium 1.16.x references tlsroutes.gateway.networking.k8s.io (v1alpha2)
  # at startup; the standard channel does not ship TLSRoute.
  #
  # Version choice — v1.1.0 NOT v1.2.0:
  #   Gateway API v1.2.0 changed status.supportedFeatures from an array of
  #   strings to an array of objects ({name: string}). Cilium 1.16.5 still
  #   writes the old string format; the v1.2.0 CRD rejects its status patch
  #   with "must be of type object: string", leaving GatewayClass/cilium
  #   permanently in status=Unknown/Pending. v1.1.0 retains the string
  #   format and is fully compatible with Cilium 1.16.x.
  #
  # bp-gateway-api Flux blueprint becomes a no-op on first reconcile
  # (CRDs already present, kubectl apply is idempotent); it is kept as the
  # GitOps record and handles CRD upgrades when Cilium is bumped.
  #
  # Incident reference: otech22 2026-05-02 — all 8 HTTPRoutes orphaned,
  # cilium-operator log: "Required GatewayAPI resources are not found …
  # tlsroutes.gateway.networking.k8s.io not found". Fix: issue #503.
```

## block-10-line-1803

Formerly `cloudinit-control-plane.tftpl:1803-1835` (G107 #2702 strip).

```
  # ── Cloud-init kubeconfig postback (issue #183, Option D) ───────────────
  #
  # The k3s install above wrote /etc/rancher/k3s/k3s.yaml with the API
  # server URL pinned to https://127.0.0.1:6443 — kubectl's default for a
  # local single-node install. catalyst-api lives off-cluster (Catalyst-Zero
  # franchise console on contabo-mkt) and cannot reach 127.0.0.1 on this
  # node, so we MUST rewrite that field before sending the kubeconfig
  # back.
  #
  # Issue #542: kubeconfig server: must be the control-plane's PUBLIC IPv4,
  # NOT the load balancer's. The Hetzner LB only forwards 80/443 (Cilium
  # Gateway ingress); 6443 is exposed directly on the CP via firewall rule
  # main.tf:51-56 (0.0.0.0/0 → CP:6443). Earlier code rewrote to the LB IP
  # which silently fails with "connect: connection refused" — wizard jobs
  # page stuck PENDING for 50+ minutes after install completes.
  #
  # Plaintext: we read from /etc/rancher/k3s/k3s.yaml (mode 0644 written
  # by k3s), apply the rewrite via sed, write the result to
  # /etc/rancher/k3s/k3s.yaml.public (mode 0600 explicitly), then
  # curl --data-binary the file content to catalyst-api with the bearer
  # token. The .public file is removed at the end of the runcmd block
  # so the bearer-protected kubeconfig only lives on this node for the
  # few seconds it takes to PUT.
  #
  # --retry 60 --retry-delay 10 --retry-all-errors handles the case
  # where catalyst-api is briefly unreachable (image roll, ingress
  # reconciliation) — the cloud-init runcmd budget is bounded by the
  # systemd cloud-final timeout (~30 minutes).
  # control_plane_ipv4 resolved at runtime via Hetzner metadata service
  # (rather than templated by Tofu — that would create a dependency cycle:
  # cloud-init → control_plane.ipv4_address → control_plane.user_data → cloud-init).
  # 169.254.169.254 is the standard cloud metadata endpoint; Hetzner exposes
  # public-ipv4 at /hetzner/v1/metadata/public-ipv4 — single line, no auth.
```

## block-11-line-1772

Formerly `cloudinit-control-plane.tftpl:1772-1797` (G107 #2702 strip).

```
  # ── Default StorageClass: local-path (k3s built-in) ─────────────────────
  #
  # k3s ships local-path-provisioner (deployment in kube-system,
  # `app=local-path-provisioner`) and registers a `local-path`
  # StorageClass on first boot. We need the StorageClass to exist AND
  # be marked default BEFORE Flux applies the bootstrap-kit Kustomization
  # below — otherwise the 11-component bootstrap kit (bp-spire,
  # bp-keycloak postgres, bp-openbao, bp-nats-jetstream, bp-gitea,
  # bp-catalyst-platform postgres) ships HelmReleases with PVCs that
  # have no `storageClassName` set, expecting the cluster default to
  # take over. Without a default, every one of those PVCs sits Pending
  # waiting on a class that nobody nominates, and the bootstrap-kit
  # Kustomization deadlocks.
  #
  # Sequence (#207 — fix the circular wait that blocked every fresh provision):
  #   1. Poll until the `local-path` StorageClass object is registered by
  #      k3s. We CANNOT wait for the local-path-provisioner POD to be
  #      Ready here — k3s runs with --flannel-backend=none so the node
  #      stays Ready=False until Cilium installs (further down). Waiting
  #      on the Pod creates a circular deadlock and 60s timeout. The SC
  #      object itself is registered by k3s manifests independently of CNI
  #      (verified live: SC creationTimestamp 3s after k3s start).
  #   2. Patch the `local-path` StorageClass with the
  #      `storageclass.kubernetes.io/is-default-class: "true"` annotation.
  #   3. Verify (the poll already implies presence; the explicit grep stays
  #      as defensive belt-and-braces, identical exit semantics).
```

## block-12-line-1740

Formerly `cloudinit-control-plane.tftpl:1740-1769` (G107 #2702 strip).

```
  # ── Patch node providerID — DoD A3/D10/D11/D12 unblocker (2026-05-16) ───
  #
  # k3s sets node.spec.providerID to `k3s://<hostname>` by default. Hetzner
  # CCM (bp-hcloud-ccm, bootstrap-kit slot ~55) rejects LB allocation for
  # any Service type=LoadBalancer with:
  #
  #   hcops/LoadBalancerOps.ReconcileHCLBTargets: providerID does not have
  #   one of the expected prefixes (hcloud://, hrobot://, hcloud://bm-):
  #   k3s://catalyst-tNNN-omani-works-cp1
  #
  # That blocks D12 (clustermesh-apiserver Service stays <pending>), which
  # cascades into D10 (no peer entries), D11 (no inter-region pod traffic),
  # D5 (child /cloud can't see secondaries).
  #
  # Prior attempts to flip k3s into `--cloud-provider=external` mode
  # (PR #1513) or pre-install hcloud-ccm in cloud-init (PR #1516) both
  # broke cold-start (chicken-and-egg: kubelet tainted uninitialized,
  # Flux couldn't schedule, bp-hcloud-ccm never installed to lift the
  # taint). Both reverted.
  #
  # This patch is the architecturally-clean alternative: k3s boots
  # normally (no taint), we look up THIS server's Hetzner ID via the
  # metadata API (169.254.169.254 — every Hetzner Cloud server's link-
  # local instance metadata endpoint), and patch the local Node's
  # spec.providerID once k3s apiserver is reachable. hcloud-ccm then
  # sees `hcloud://<id>` and allocates LBs normally.
  #
  # The metadata API field is `instance-id` (canonical Hetzner Cloud
  # metadata key per https://docs.hetzner.com/cloud/servers/cloud-init/
  # — distinct from AWS `instance-id` but same role).
```

## block-13-line-1705

Formerly `cloudinit-control-plane.tftpl:1705-1719` (G107 #2702 strip).

```
  # k3s install — server mode, embedded etcd (--cluster-init), Cilium-ready
  # (flannel/network-policy/traefik/servicelb all disabled). The
  # --cluster-cidr and --service-cidr flags are per-region (10.42+i.0/16
  # for pods, 10.96+i.0/16 for services) so ClusterMesh peers across
  # regions don't collide on pod/service routing tables — DoD gate D11
  # (docs/SOVEREIGN-MULTI-REGION-DOD.md) verifies inter-region pod-to-pod
  # packet flow over Cilium WireGuard which requires non-overlapping
  # CIDRs end-to-end. Values are interpolated by OpenTofu from
  # local.region_cluster_cidr / local.region_service_cidr in main.tf.
  # ── Layer 1 fail-fast: Hetzner public-ipv4 must be non-empty (#1941). ────
  # Body in /usr/local/bin/openova-externalip-bootstrap.sh (write_files
  # above). Exit 87 → cloud-init.log surfaces the root cause. Persists the
  # IP to /etc/openova/cp-public-ipv4 for the next runcmd item. Issue #1977
  # moved this body out of an inline runcmd heredoc to keep rendered
  # cloud-init under the 30 720 B Hetzner guardrail.
```

## block-14-line-1672

Formerly `cloudinit-control-plane.tftpl:1672-1691` (G107 #2702 strip).

```
            # use-routes: true — accept Hetzner DHCP's classless static
            # routes (including the per-subnet route that lets the kernel
            # reach OTHER hosts on the private network without going
            # through eth0's default route). Without this, Hetzner LB ->
            # backend asymmetric routing breaks LB health checks:
            # inbound SYN arrives on enp7s0 (private NIC), but the
            # kernel routes SYN-ACK via eth0 because 10.0.0.0/8 has no
            # route on the private NIC. Hetzner drops the reply at the
            # LB and the target stays unhealthy forever.
            #
            # The default route (0.0.0.0/0 via eth0) wins because eth0's
            # DHCP route is set first (metric 100). Hetzner private-net
            # DHCP routes get a default metric of 200+, so eth0 stays
            # the default. We DO need the per-subnet 10.0.0.0/N route
            # from private DHCP for cross-host private traffic.
            #
            # Caught live on prov omani.works/6dfade27 (2026-05-14):
            # interface had 10.0.1.2/32 (host route only, no subnet),
            # LB health checks for 80/443/53 all unhealthy → public
            # surface blackholed.
```

## block-15-line-1631

Formerly `cloudinit-control-plane.tftpl:1631-1651` (G107 #2702 strip).

```
  # ── Private NIC bring-up BEFORE k3s install (prov #71 root cause) ────────
  #
  # Hetzner Cloud attaches the private-network NIC by hot-plug AFTER the
  # server is created. cloud-init init-local runs at boot and reads
  # /hetzner/v1/metadata/private-networks to render netplan — but on
  # secondary regions (and intermittently on primaries) the NIC is
  # attached ~10-20s AFTER cloud-init finalises 50-cloud-init.yaml.
  # Result: netplan only has eth0, the private NIC (kernel-renamed eth1
  # → enp7s0 by udev) stays DOWN with no IP, k3s starts with
  # --node-ip=${cp_private_ip} and fatals on
  #   "bind: cannot assign requested address"
  # then crashloops forever. Prov #71 (omantel.biz, nbg1-1-cp1) hit this:
  # k3s.service restart counter reached 5394, /var/lib/rancher/k3s never
  # became ready, kubeconfig was never PUT back to the mothership, and
  # the canvas showed the secondary region as a permanent black hole.
  # Diagnosed via Hetzner rescue mode 2026-05-14.
  #
  # Fix: poll for ${cp_private_ip} on any interface. If absent after the
  # NIC is detected, write a netplan stanza for it and apply. Bail loudly
  # if the IP never appears so failures surface in cloud-init.log instead
  # of disguising as a slow k3s boot.
```

## block-16-line-1471

Formerly `cloudinit-control-plane.tftpl:1471-1629` (G107 #2702 strip).

```
  # k3s control-plane. Flags per docs/SOVEREIGN-PROVISIONING.md §3 and
  # docs/PLATFORM-TECH-STACK.md §8.1:
  #   --cluster-init                Initialise embedded etcd (HA-ready).
  #   --flannel-backend=none        Cilium replaces flannel.
  #   --disable=traefik             Cilium Gateway replaces traefik.
  #   --disable=servicelb           Hetzner LB handles ingress.
  #   --disable-network-policy      Cilium handles NetworkPolicy.
  #   --tls-san=${sovereign_fqdn}   API server cert valid for the sovereign FQDN.
  #
  # ── kube-apiserver OIDC flags (issue #326) ─────────────────────────────
  #   --kube-apiserver-arg=oidc-issuer-url=https://auth.<sovereign_fqdn>/realms/sovereign
  #   --kube-apiserver-arg=oidc-client-id=kubectl
  #   --kube-apiserver-arg=oidc-username-claim=preferred_username
  #   --kube-apiserver-arg=oidc-username-prefix=oidc:
  #   --kube-apiserver-arg=oidc-groups-claim=groups
  #   --kube-apiserver-arg=oidc-groups-prefix=oidc:
  # Wire k3s api-server's OIDC validator to the per-Sovereign Keycloak
  # realm (`sovereign`), shipped by bp-keycloak's keycloakConfigCli realm
  # import (platform/keycloak/chart/values.yaml). After the Sovereign's
  # bootstrap kit lands, customer admins authenticate kubectl against
  # Keycloak (see docs/omantel-handover-wbs.md §11 "kubectl OIDC for
  # customer admins"). The username/groups prefixes prefix every
  # OIDC-issued subject with `oidc:` so RoleBindings reference them as
  # e.g. `subjects[0].name=oidc:alice@org` — distinct from any local
  # ServiceAccount or x509 subject. Per INVIOLABLE-PRINCIPLES #4 the
  # issuer URL is composed from sovereign_fqdn — never hardcoded.
  #
  # Trust-chain note: the per-Sovereign Keycloak is exposed via the
  # `cilium-gateway` Gateway (kube-system), whose serving Certificate is
  # issued by Let's Encrypt via bp-cert-manager. k3s's kube-apiserver
  # reaches the in-cluster Keycloak Service over plain HTTPS using the
  # node's system trust store; LE roots are present by default on the
  # Ubuntu 24.04 control-plane image, so no `--oidc-ca-file` is needed
  # in this configuration. Air-gapped Sovereigns (deferred Phase 9+)
  # add a CA-file flag here when their Keycloak fronts a private CA.
  #
  # NOTE: --disable=local-storage is intentionally NOT passed. k3s ships a
  # built-in local-path-provisioner (Rancher) and registers a `local-path`
  # StorageClass. That is the canonical solo-Sovereign StorageClass:
  # PVCs (bp-spire data dir, bp-keycloak postgres, bp-openbao raft store,
  # bp-nats-jetstream, bp-gitea, bp-catalyst-platform postgres) bind to
  # node-local storage on the single CPX21/CPX31 control-plane node and
  # come up immediately. Operators upgrading to multi-node migrate to
  # hcloud-csi (Hetzner Cloud Volumes) as a separate, deliberate step —
  # see docs/RUNBOOK-PROVISIONING.md §"StorageClass missing".
  #
  # Architectural background: the prior version of this template passed
  # `--disable=local-storage` with the intent that Crossplane would
  # install hcloud-csi day-2 and register the StorageClass after
  # bp-crossplane reconciled. That created a circular dependency: the
  # 11-component bootstrap kit (bp-spire / bp-keycloak / bp-openbao / …)
  # all carry PVCs whose bind step blocks waiting for a StorageClass that
  # would only exist AFTER bp-crossplane had finished installing AND
  # provisioned hcloud-csi. Result on a fresh Sovereign: every PVC stuck
  # Pending forever, bootstrap-kit deadlocked. Keeping local-path solves
  # the circularity by giving the cluster a default StorageClass at boot.
  # ── k3s server install ───────────────────────────────────────────────────
  #
  # --node-taint node-role.kubernetes.io/control-plane=true:NoSchedule
  # ─────────────────────────────────────────────────────────────────────────
  # CONDITIONAL: applied ONLY when worker_count > 0 (multi-node Sovereign).
  #
  # Why: by k3s default, the server (control-plane) node is fully
  # schedulable — the kube-scheduler distributes pods by resource fit. On
  # a 1-CP + N-worker Sovereign with the 37-HelmRelease bootstrap-kit +
  # guest workloads (bp-keycloak, bp-cnpg, bp-harbor, bp-catalyst-platform,
  # SME microservices), the scheduler happily lands guest workloads on the
  # CP. They eat its memory, crowd kubelet/etcd/apiserver, and the whole
  # cluster degrades: kubectl flakes, Helm post-install hooks time out,
  # HelmReleases get stuck mid-reconcile. This is the root cause of the
  # "apiserver flake / cpx22 too small / 8 stuck HRs" symptom chain
  # (issue #751). The taint reserves the CP for system + bootstrap
  # controllers (cilium agent + cilium-operator + flux + cert-manager +
  # kube-system pods, all of which tolerate this taint by upstream chart
  # convention), and pushes guest workloads to workers where they belong.
  #
  # Why CONDITIONAL on worker_count > 0: a Catalyst-Zero / solo Sovereign
  # (worker_count=0) has only the CP — tainting NoSchedule there leaves
  # NO node for any deployment to schedule onto, the cluster never
  # becomes ready. Skip the taint when there are no workers; fall back
  # to k3s default (CP fully schedulable) so the solo node carries
  # everything.
  #
  # --node-ip + --advertise-address pin the API server to ${cp_private_ip}
  # (10.0.1.2 primary; 10.0.<10+idx>.2 secondary). Without them k3s
  # auto-detects the public interface (49.x.x.x), kube-apiserver
  # advertises that IP, and any pod (cilium init/operator, coredns)
  # dialing 10.0.1.2:6443 times out because nothing listens on it.
  # Symptom on prov #62 (cpx52, kernel 6.8.0-111): cilium-agent init
  # CrashLoop with "dial tcp 10.0.1.2:6443: i/o timeout" → primary
  # cluster never makes a Ready node. Worked by luck on cpx42 (earlier
  # kernel + network-init order); cpx52 reproduces reliably.
  # Pre-fetch the CP's public IPv4 from Hetzner metadata so the k3s
  # server's TLS cert includes it as a SAN. The mothership catalyst-api's
  # helmwatch.Bridge connects to secondary CPs via PUBLIC IP (kubeconfig
  # public-IP rewrite at line ~1310), and without the public IP in
  # --tls-san k3s presents a cert valid only for private 10.0.x.2 +
  # 127.0.0.1 + cluster-ip 10.43.0.1 → "x509: certificate is valid for
  # 10.0.10.2, 10.43.0.1, 127.0.0.1, ::1, not <public>" → silent
  # helmwatch failure → secondary regions' bp-* HRs never reach the
  # mothership canvas. Caught on prov #64 (hel1-2 kubeconfig posted
  # but bridge connection failed; 0 HRs observed). 169.254.169.254 is
  # the Hetzner metadata endpoint; same path used by the public-IP
  # rewrite step at line 1310.
  #
  # --node-external-ip=$${CP_PUBLIC_IPV4} — DoD A2 / TBD-A7 fix (2026-05-18)
  # ──────────────────────────────────────────────────────────────────────
  # Without this flag, k3s only publishes node.status.addresses[InternalIP=
  # ${cp_private_ip}] and NO ExternalIP. After the 2026-05-15 per-region-
  # network refactor every region's CP sits in its OWN isolated
  # hcloud_network, so the InternalIP is 10.0.1.2 *uniformly* across every
  # region — locally-scoped, NOT routable cross-region. Cilium picks the
  # InternalIP as its tunnel-endpoint by default → cross-region VXLAN
  # tunnels resolve to 10.0.1.2 on every peer → inter-region pod traffic
  # blackholes. Caught live on t22-omantel-biz (2026-05-18 Wave 28-E):
  # all 3 CPs (hel/fsn/sin) advertised InternalIP=10.0.1.2 with no
  # ExternalIP, Cilium tunnel endpoints unroutable, pod-to-pod
  # inter-region 0/6.
  # docs/SOVEREIGN-MULTI-REGION-DOD.md A2 mandate: "inter-region link =
  # DMZ WireGuard over PUBLIC IPs ALWAYS (never any provider's private
  # network)". Publishing the public IPv4 as ExternalIP lets Cilium
  # promote it to its tunnel endpoint when peer addresses include
  # External + Internal, which restores cross-region pod reachability
  # without breaking intra-cluster paths (InternalIP stays primary for
  # kube-apiserver advertise + pod-to-CP dial).
  # --node-label openova.io/region=${region_canonical_label}
  # (NAMING-CONVENTION §2.1 canonical region tag, e.g.
  # `hz-fsn-rtz-prod` for a fsn1 primary, `hz-hel-rtz-prod` for a hel1
  # primary, `hz-nbg-rtz-prod` for a nbg1 secondary). qa-fixtures Pods
  # (CNPGPair primary/replica, status seeder Jobs, qa-wp Application)
  # carry hard nodeAffinity for `openova.io/region in [<primary-label>]`.
  # Without the label k8s FailedScheduling rejects every fixture pod →
  # bp-catalyst-platform post-install hook waits forever → entire
  # bootstrap-kit chain hangs at 44/45 with bp-catalyst-platform
  # Running. Caught on prov #64 (qaTestEnabled=true).
  #
  # 2026-05-16 regression fix: the label used to be hardcoded
  # `hz-fsn-rtz-prod` literal here, which silently broke every Sovereign
  # whose primary region was NOT fsn1 (e.g. t114-omani-works /
  # a1448e0b9e471f5d had primary=hel1 + secondaries nbg1, sin; all 3 CP
  # nodes carried `hz-fsn-rtz-prod`, breaking the OpenovaFlow canvas's
  # per-region grouping and any downstream selector targeting the real
  # cluster name). The label now flows from main.tf's
  # locals.region_canonical_label[primary|<secondary-key>], which the
  # primary-CP templatefile() call seeds from var.region and the
  # secondary-CP templatefile() call seeds from each.value.cloudRegion.
  # DoD A6 demands provider-agnostic shape; the `hz` prefix is correct
  # only inside infra/hetzner/ — future infra/aws/ + infra/huawei/
  # modules derive `aw` / `hw` in their own per-module locals.
  #
  # qa-fixtures behaviour: the chart template
  # (products/catalyst/chart/templates/qa-fixtures/*.yaml) reads
  # Values.qaFixtures.primaryRegion which is wired via the bootstrap-kit
  # Flux Kustomization's QA_PRIMARY_REGION substitute (see
  # clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml). To
  # keep the chart unchanged the substitute is threaded in from the same
  # local — see the bootstrap-kit substitute block above. On secondary
  # regions qa-fixtures Pending by design (qaFixtures.primaryRegion is
  # singular; secondaries don't host the fixture workload).
```

## block-17-line-1380

Formerly `cloudinit-control-plane.tftpl:1380-1393` (G107 #2702 strip).

```
          # substituteFrom: ConfigMap/sovereign-tls-vars (Closes #2118).
          # The bp-catalyst-platform chart's templates/sovereign-tls-vars-cm.yaml
          # renders this ConfigMap from .Values.parentZones into flux-system.
          # Keys it carries:
          #   - PARENT_DOMAINS_LISTENERS_YAML: JSON-flow listener array
          #     consumed by clusters/_template/sovereign-tls/cilium-gateway.yaml
          #     at `spec.listeners: $${PARENT_DOMAINS_LISTENERS_YAML}`.
          # Moved out of the inline `substitute` map above to keep cloud-init
          # under Hetzner's 32 KiB user_data cap on multi-zone SME-pool
          # Sovereigns (the listener block scales O(N) with parent-zone
          # count; 4 zones → ~2.2 KiB → cloud-init at 33.6 KiB before this fix).
          # optional: false is correct — bp-catalyst-platform is INSIDE
          # bootstrap-kit, and this Kustomization dependsOn bootstrap-kit
          # Ready, so the ConfigMap is guaranteed to exist before reconcile.
```

## block-18-line-1358

Formerly `cloudinit-control-plane.tftpl:1358-1376` (G107 #2702 strip).

```
            # TBD-A31 (#1885) — Hetzner LB annotations on cilium-gateway
            # Gateway resource (spec.infrastructure.annotations). These
            # substitute vars name and locate the LB hcloud-CCM provisions
            # for the auto-generated `cilium-gateway-cilium-gateway`
            # Service in kube-system. Mirrors the same 3 vars threaded
            # into the bootstrap-kit Kustomization for the clustermesh-
            # apiserver LB (see 01-cilium.yaml apiserver.service.annotations).
            #   - SOVEREIGN_FQDN_SLUG: short, DNS-safe Sovereign identifier
            #     used as the LB name prefix so operators can spot the
            #     gateway LB in the Hetzner Console.
            #   - SOVEREIGN_REGION_KEY: per-region suffix so each
            #     multi-region peer's cilium-gateway gets its own LB
            #     (Hetzner LBs are unique by name — duplicates collapse to
            #     the first-created instance, hiding the LB for every
            #     subsequent region).
            #   - HCLOUD_LB_LOCATION: Hetzner datacenter location for the
            #     LB. Per-region rendered (primary CP renders var.region,
            #     secondary CPs render each.value.cloudRegion) so the LB
            #     and its backend node are co-located.
```

## block-19-line-1338

Formerly `cloudinit-control-plane.tftpl:1338-1356` (G107 #2702 strip).

```
            # PARENT_DOMAINS_LISTENERS_YAML — historically materialised here
            # by infra/hetzner/main.tf locals.parent_domains_listeners_yaml
            # and inlined as a substitute value, but that scaled O(N) with
            # parent-zone count and overflowed Hetzner's 32 KiB user_data
            # cap on 4-zone SME-pool Sovereigns (Closes #2118 — t39 audit,
            # 2026-05-20). Now rendered inside bp-catalyst-platform's
            # templates/sovereign-tls-vars-cm.yaml from .Values.parentZones
            # (single source of truth — same input the chart's per-zone
            # Certificate render already consumes). Picked up below via
            # `substituteFrom: ConfigMap/sovereign-tls-vars`. Ordering is
            # safe: this Kustomization `dependsOn: bootstrap-kit Ready`, and
            # bootstrap-kit is Ready only when bp-catalyst-platform's HR
            # (which renders the ConfigMap) is Ready.
            # WILDCARD_CERT_ISSUER (Fix #176 — qa-loop iter-1 LE
            # rate-limit unblock). cilium-gateway-cert.yaml references
            # this via $${WILDCARD_CERT_ISSUER}. When
            # wildcard_cert_use_staging=true → STAGING ClusterIssuer
            # (no 5/168h limit); default → PROD. Locals in main.tf
            # render the final string so this template stays declarative.
```

## block-20-line-1256

Formerly `cloudinit-control-plane.tftpl:1256-1268` (G107 #2702 strip).

```
            # Cilium's apiserver target — must be the LOCAL CP's
            # private IP for the cluster this Flux is running in.
            # Primary cluster: 10.0.1.2 (hardcoded in main.tf:317).
            # Secondary clusters: cidrhost(<subnet>, 2) (main.tf:267).
            # Without this each region's bp-cilium would use the
            # chart-default 10.0.1.2 — which is the PRIMARY's IP, NOT
            # the local cluster's. Result on secondary regions: cilium
            # operator crash-loops with `tls: failed to verify
            # certificate: x509: certificate signed by unknown
            # authority` because the primary's k3s API presents the
            # primary's CA, not the secondary cluster's CA. Each
            # region is an INDEPENDENT k3s cluster per
            # NAMING-CONVENTION §1.3.
```

## block-21-line-1244

Formerly `cloudinit-control-plane.tftpl:1244-1254` (G107 #2702 strip).

```
            # Wildcard cert ACME directory selector (Fix #123 — qa-loop
            # iter-1 LE rate-limit unblock). "true" makes
            # bp-catalyst-platform 1.4.136+ render
            # sovereign-wildcard-tls Certificate(s) against the staging
            # ClusterIssuer (`letsencrypt-dns01-staging-powerdns`,
            # shipped by bp-cert-manager-powerdns-webhook 1.1.0+) so the
            # production 5/168h LE rate limit per registered domain is
            # bypassed during high-cadence QA iteration. Catalyst-api
            # auto-stamps "true" alongside QA_FIXTURES_ENABLED on QA
            # Sovereigns; default "false" → real-trusted production
            # certs on customer Sovereigns.
```

## block-22-line-1222

Formerly `cloudinit-control-plane.tftpl:1222-1239` (G107 #2702 strip).

```
            # QA fixtures auto-enable (Fix #73 — qa-loop bounded-cycle
            # iter-16). Default "false" so a customer-facing zero-touch
            # provision lands a Sovereign with NO qaFixtures stack
            # rendered. Catalyst-api flips to "true" + populates the
            # namespace + Organization names when the wizard / API
            # caller sets Request.QATestEnabled=true (QA Sovereigns
            # only). The bp-catalyst-platform chart's bootstrap-kit
            # slot 13 reads via $${QA_FIXTURES_ENABLED:-false} and the
            # other three placeholders below — see
            # clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml
            # lines 496/510/511/519.
            #
            # QA_FIXTURES_NAMESPACE / QA_ORGANIZATION are derived
            # server-side from SovereignFQDN's first label per
            # docs/INVIOLABLE-PRINCIPLES.md #4 — "qa-omantel" /
            # "omantel-platform" for omantel.biz, etc. — so a
            # non-omantel QA Sovereign never inherits the chart's
            # legacy bootstrapping defaults.
```

## block-23-line-1198

Formerly `cloudinit-control-plane.tftpl:1198-1207` (G107 #2702 strip).

```
            # SOVEREIGN_BCP_TOPOLOGY — operator-visible enum string
            # (single-region | active-hotstandby | active-active).
            # Rendered into the Settings page Continuity Plan row +
            # the BSS-menu DR posture chip on the Sovereign console.
            # Distinct from SOVEREIGN_ENABLE_HOT_STANDBY because the
            # `active-active` future shape and the today-canonical
            # `active-hotstandby` shape both flip the boolean but the
            # operator-visible label must distinguish them. Charts
            # that need the boolean keep reading the existing key;
            # catalyst-api status surfaces read this string one.
```

## block-24-line-1183

Formerly `cloudinit-control-plane.tftpl:1183-1196` (G107 #2702 strip).

```
            # SOVEREIGN_ENABLE_HOT_STANDBY — D31 / G93.1 (Refs #2666).
            # Derived from var.bcp_topology by catalyst-api's
            # provisioner.bcpTopologyEnableHotStandby helper:
            # "active-hotstandby" and "active-active" → "true";
            # "single-region" → "false". Pre-G93.1 this key was
            # HARDCODED to "" here, so the chart's
            # `$${SOVEREIGN_ENABLE_HOT_STANDBY:-}` envsubst always
            # evaluated to literal empty → the chart-side default
            # `false` always won → every multi-region prov silently
            # landed single-Cluster CNPG → Pillar 3 zero-tx-loss
            # impossible without a per-overlay flip. Now driven by the
            # tofu var, which the wizard's StepProvider chooses or
            # which auto-derives from len(regions)>=2 inside
            # provisioner.Request.Validate().
```

## block-25-line-1156

Formerly `cloudinit-control-plane.tftpl:1156-1171` (G107 #2702 strip).

```
            # TBD-A15 (t24 zero-touch, 2026-05-18, issue #1844) — wire the
            # remaining sovereign-fqdn ConfigMap fields the chart's bootstrap-
            # kit slot 13 expects via envsubst. Previously these placeholders
            # resolved to empty on every fresh prov because nothing was
            # writing them into the Kustomization substitute map → catalyst-
            # api on the Sovereign read empty strings → Dashboard "configured
            # regions" chips, settings page, D31 hot-standby gating, and
            # /api/v1/sovereign/self all silently returned defaults.
            #
            # SOVEREIGN_CONTROL_PLANE_IP — Sovereign's CP public IPv4. We
            # cannot reference hcloud_server.control_plane.ipv4_address here
            # (dep cycle), but the load balancer IP IS the canonical control-
            # plane address operators interact with (kubectl, console, API).
            # Same value as SOVEREIGN_LB_IP — duplicating intentionally so a
            # future split (separate CP-direct IP vs LB-IP) can rewire only
            # this key without breaking the SOVEREIGN_LB_IP consumers.
```

## block-26-line-1136

Formerly `cloudinit-control-plane.tftpl:1136-1152` (G107 #2702 strip).

```
            # D22 (settings empty values) — sovereign-side identity wired
            # into the bp-catalyst-platform slot 13 sovereign.* values
            # (clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml
            # PR #1570 added the $${ORG_EMAIL:-}/$${ORG_NAME:-}/...
            # placeholders). Chart's sovereign-fqdn ConfigMap (PR #1569)
            # exposes them as ConfigMap keys; catalyst-api reads via env
            # (api-deployment.yaml); chrootEnsureDeployment populates the
            # deployment record so Sovereign Console Settings page renders
            # real ownerEmail/region/controlPlaneIP/gitopsRepoURL/consoleURL.
            # control_plane_ip is NOT templated here — see
            # main.tf:691 ("would create a dependency cycle" — the
            # hcloud_server.cp resource doesn't exist yet at cloudinit
            # render time). The chart's SOVEREIGN_CONTROL_PLANE_IP env
            # stays empty until a future PR wires it via either (a)
            # hcloud metadata-service lookup at runtime by catalyst-api,
            # or (b) a cloud-init post-create step that writes it into
            # the sovereign-fqdn ConfigMap.
```

## block-27-line-1125

Formerly `cloudinit-control-plane.tftpl:1125-1134` (G107 #2702 strip).

```
            # SOVEREIGN_REGIONS_JSON — JSON-encoded RegionSpec[] from
            # mothership prov body. Read by bp-catalyst-platform slot 13
            # (sovereign.regionsJson) → sovereign-fqdn ConfigMap
            # `regionsJson` → catalyst-api env SOVEREIGN_REGIONS_JSON →
            # chrootEnsureDeployment seeds Request.Regions[] →
            # /cloud?view=graph renders multi-region (DoD D5).
            # Single-quoted so YAML treats JSON braces/quotes/commas
            # as literal. Caught on t126 (2026-05-16): UI showed
            # "1 cluster 1 region" because chroot fell back to
            # live-Nodes enumeration.
```

## block-28-line-1111

Formerly `cloudinit-control-plane.tftpl:1111-1122` (G107 #2702 strip).

```
            # Per-role vCluster enable flags (DoD A4 topology):
            #   primary    region -> MGMT  + DMZ  vCluster
            #   secondary  region -> DMZ   + RTZ  vCluster
            # bp-dmz-vcluster slot 54 stays default-on (chart-side default).
            # MGMT_VCLUSTER_ENABLED / RTZ_VCLUSTER_ENABLED are flipped
            # per-region by tofu so the bootstrap-kit slot reads the
            # right value at apply time. Note: tofu's templatefile()
            # also parses comments for $${...} interpolations, so any
            # Flux envsubst expressions in comments would need to be
            # escaped. Keeping comments free of $${...} avoids the
            # tofu-plan "Extra characters after interpolation expression"
            # failure mode (caught on t127, 2026-05-16).
```

## block-29-line-1100

Formerly `cloudinit-control-plane.tftpl:1100-1109` (G107 #2702 strip).

```
            # SOVEREIGN_REGION_CANONICAL_LABEL — THIS region's canonical
            # k3s node label value (e.g. "hz-hel-rtz-prod" for hel1,
            # "hz-nbg-rtz-prod" for nbg1, "hz-sin-rtz-prod" for sin).
            # Used by bp-{mgmt,dmz,rtz}-vcluster slots 54/58/59 for the
            # vCluster Pod nodeSelector — the SOVEREIGN_REGION_KEY
            # ("hel1", "nbg1-1") does NOT match the k3s node-label value
            # written at install time. Caught on t126 (2026-05-16): DMZ
            # vCluster Pods Pending on every region because nodeSelector
            # `openova.io/region=hel1` didn't match node label
            # `openova.io/region=hz-hel-rtz-prod`.
```

## block-30-line-1085

Formerly `cloudinit-control-plane.tftpl:1085-1098` (G107 #2702 strip).

```
            # QA_PRIMARY_REGION — Sovereign-wide canonical primary
            # region label (e.g. `hz-fsn-rtz-prod`, `hz-hel-rtz-prod`).
            # Threaded into clusters/_template/bootstrap-kit/
            # 13-bp-catalyst-platform.yaml's
            # `primaryRegion: $${QA_PRIMARY_REGION:-hz-fsn-rtz-prod}`
            # so qa-fixtures Pods schedule on the actual primary region
            # of THIS Sovereign — never the chart's hardcoded
            # `hz-fsn-rtz-prod` fallback. Identical value on the
            # primary's bootstrap-kit AND every secondary's because
            # qaFixtures.primaryRegion is Sovereign-wide singular per
            # the chart contract; the primary CP renders qaFixtures
            # workload, secondaries render the same primaryRegion seam
            # but their qaFixtures Pods stay Pending by design (no node
            # carries the primary's label on a secondary cluster).
```

## block-31-line-1059

Formerly `cloudinit-control-plane.tftpl:1059-1071` (G107 #2702 strip).

```
            # Cilium ClusterMesh per-Sovereign anchors (#1101 EPIC-6).
            # Empty string + 0 = not joined to any mesh (single-cluster
            # Sovereign); set to the registered values from
            # docs/CLUSTERMESH-CLUSTER-IDS.md when the operator joins
            # this Sovereign to a multi-region mesh. The bootstrap-kit
            # cilium HelmRelease consumes via $${CLUSTER_MESH_NAME} +
            # $${CLUSTER_MESH_ID} envsubst placeholders (the $$ escape
            # is required so tofu does NOT treat them as tftpl vars).
            # Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
            # operator-supplied request.cluster_mesh_name +
            # cluster_mesh_id flow through tofu vars
            # cluster_mesh_name/cluster_mesh_id (variables.tf) into this
            # template at provision time.
```

## block-32-line-1029

Formerly `cloudinit-control-plane.tftpl:1029-1039` (G107 #2702 strip).

```
            # OpenovaFlow integration (Agent #3, PR #1389/#1390 follow-up).
            # SOVEREIGN_DEPLOYMENT_ID — catalyst-api's per-deployment 16-
            # char hex id. Used by bp-openova-flow-emitter (slot 57) as
            # the FlowID so the openova-flow-server keys nodes/edges
            # under the same id the catalyst-api proxy
            # /sovereign/api/v1/flows/{deploymentId}/* queries against.
            # SOVEREIGN_REGION_KEY — Hetzner region key for THIS cluster
            # ("fsn1", "hel1", …). Stamped onto every FlowNode.region so
            # the canvas groups bubbles by region. Primary CP renders
            # var.region; secondary CPs render each.key from the
            # for_each over local.secondary_regions in main.tf.
```

## block-33-line-991

Formerly `cloudinit-control-plane.tftpl:991-1005` (G107 #2702 strip).

```
        # timeout: 5m (issue #492). Phase-8a iteration discipline: when
        # the FIRST apply of bootstrap-kit is unhealthy (e.g. cilium
        # crash-loop from issue #491), kustomize-controller holds the
        # revision lock for the FULL timeout window and refuses to pick
        # up new GitRepository revisions, even fixes that have already
        # landed on main. The 30m default deadlocked otech8 deployment
        # 1bfc46347564467b: fix `66ea39f0` was on main 1m after the bad
        # SHA, but bootstrap-kit's `lastAttemptedRevision` stayed pinned
        # to the old SHA waiting for HRs to become Ready (which they
        # never would, because of #491). Operator wiped + reprovisioned.
        # 5m matches the GitRepository poll interval — failed reconciles
        # release the revision lock fast (~6m worst case) so a fresh fix
        # gets applied on the next poll. We KEEP `wait: true` to preserve
        # the consolidated "Kustomization Ready=True ⇒ every HR Ready"
        # contract that downstream `dependsOn: bootstrap-kit` relies on.
```

## block-34-line-954

Formerly `cloudinit-control-plane.tftpl:954-977` (G107 #2702 strip).

```
      # Two Flux Kustomizations with dependsOn so Crossplane CRDs land
      # before any resource that uses them is dry-run-applied.
      #
      # bootstrap-kit installs the 11 HelmReleases (Cilium, cert-manager,
      # Flux, Crossplane core, sealed-secrets, SPIRE, NATS-JetStream,
      # OpenBao, Keycloak, Gitea, bp-catalyst-platform). bp-crossplane
      # registers the Crossplane core CRDs (Provider, ProviderConfig…)
      # AND the bp-catalyst-platform umbrella reconciles the rest.
      #
      # infrastructure-config applies the cluster's Provider package +
      # ProviderConfig + Compositions. Because it dependsOn bootstrap-kit
      # AND uses wait: true, Flux waits until bootstrap-kit's HelmReleases
      # are Ready (Crossplane core + provider-hcloud installed,
      # hcloud.crossplane.io/v1beta1 CRDs registered) before dry-running
      # ProviderConfig — which is the exact ordering the prior single-
      # Kustomization model tripped over with:
      #   no matches for kind "ProviderConfig" in version
      #   "hcloud.crossplane.io/v1beta1"
      #
      # postBuild.substitute (issue #218): Flux's envsubst runs over the
      # rendered manifests after kustomize build, replacing $${SOVEREIGN_FQDN}
      # with the Sovereign's FQDN that this cloud-init was rendered for.
      # The template manifests in clusters/_template/bootstrap-kit/*.yaml
      # use $${SOVEREIGN_FQDN} as the substitution token.
```

## block-35-line-912

Formerly `cloudinit-control-plane.tftpl:912-934` (G107 #2702 strip).

```
  # Flux GitRepository + Kustomizations that take over after k3s is up.
  #
  # ── Per-Sovereign tree vs. shared _template (issue #218) ─────────────
  #
  # Earlier revisions of this template selected a per-FQDN cluster tree
  # (`!/clusters/${sovereign_fqdn}`) and pointed the Kustomization
  # `spec.path` at `./clusters/${sovereign_fqdn}/bootstrap-kit`. That
  # required a per-Sovereign directory to be committed to the public
  # openova repo BEFORE provisioning, which the wizard does NOT do —
  # only `clusters/_template/` is canonical. Result on every fresh
  # Sovereign was Phase-1 stall:
  #   kustomization path not found:
  #     stat /tmp/kustomization-…/clusters/<fqdn>/bootstrap-kit:
  #     no such file or directory
  # (live evidence: otech.omani.works deployment ce476aaf80731a46.)
  #
  # Canonical fix: GitRepository selects the shared `_template/` tree,
  # Kustomization paths point at `clusters/_template/{bootstrap-kit,
  # infrastructure}`, and Flux's `postBuild.substitute` interpolates
  # `$${SOVEREIGN_FQDN}` into the template manifests at apply time. The
  # per-FQDN copy that prior provisioning depended on becomes a no-op:
  # one shared tree serves every Sovereign, with the Sovereign's FQDN
  # injected by Flux on the cluster instead of by sed in the repo.
```

## block-36-line-831

Formerly `cloudinit-control-plane.tftpl:831-845` (G107 #2702 strip).

```
  # ── ExternalIP bootstrap script — Layer 1 + Layer 2 (#1941, #1977) ───────
  #
  # Packaged as a write_files script (not inline runcmd heredocs) because
  # PR #1958's inline blocks pushed rendered cloud-init past the 30 720 B
  # guardrail and blocked every fresh provision at tofu plan precondition
  # (Issue #1977). Subcommands:
  #   l1  — fail-fast on empty Hetzner metadata public-ipv4 (exit 87).
  #         Persists validated IP to /etc/openova/cp-public-ipv4 so the
  #         next runcmd item (k3s install) reads it from disk.
  #   l2  — post-install ExternalIP assertion. Restart k3s once if absent,
  #         re-check, exit 88 if still empty (DoD A2 invariant guard).
  # Verbose diagnostic strings were trimmed vs PR #1958 — exit codes alone
  # suffice; the in-script identifier (l1-fatal / l2-fatal) + Issue #1941
  # ref is the runbook-lookup token. Leaves headroom (~2.7 KB) for the
  # Layer 3 idempotent reconciler (separate follow-up).
```

## block-37-line-783

Formerly `cloudinit-control-plane.tftpl:783-798` (G107 #2702 strip).

```
      # Harbor proxy-cache projects use the URL form
      #   https://harbor.openova.io/v2/<project>/<image>/manifests/<tag>
      # NOT
      #   https://harbor.openova.io/<project>/v2/<image>/manifests/<tag>
      # which is what containerd would naively build from
      # `endpoint: ["https://harbor.openova.io/<project>"]`.
      # Harbor returns its UI HTML (status 200, content-type text/html)
      # for the wrong-shape URL — containerd then surfaces:
      #   "unexpected media type text/html for sha256:..."
      # and cilium / coredns / pause-image pulls all fail forever.
      #
      # k3s registries.yaml supports a per-mirror `rewrite` map:
      # containerd builds `<endpoint>/v2/<repo>/...` (host-only endpoint),
      # then rewrite() transforms the repo path before the request goes out.
      # Mapping `(.*)` → `proxy-<flavor>/$1` produces the correct
      # Harbor-project-prefixed path. Diagnosed live during otech25.
```

## block-38-line-748

Formerly `cloudinit-control-plane.tftpl:748-779` (G107 #2702 strip).

```
  # ── containerd pull-through mirror: harbor.openova.io (issue #557, Option A) ──
  #
  # k3s uses containerd. Containerd's mirror table is configured via
  # /etc/rancher/k3s/registries.yaml BEFORE k3s starts — the file is
  # read once at startup and cannot be hot-reloaded without a k3s restart.
  #
  # Each `mirrors:` key is the upstream registry hostname containerd
  # intercepts. When a pod pulls `nats:2.10` (implicitly `docker.io/library/
  # nats:2.10`), containerd tries `harbor.openova.io/proxy-dockerhub/library/
  # nats:2.10` first; on a cache miss, harbor.openova.io fetches from
  # DockerHub and caches the blob for subsequent pulls by other pods or
  # Sovereign nodes.
  #
  # This eliminates DockerHub's anonymous rate limit (100 pulls/6h per IP)
  # on fresh Sovereign IPs where the bootstrap-kit pulls 50+ images in the
  # first 30 minutes.
  #
  # The `configs:` block supplies harbor.openova.io credentials so containerd
  # can authenticate against the proxy project (Harbor proxy-cache projects
  # are set Public but a robot account is provided for future private-project
  # pulls and consistent audit logging).
  #
  # harbor_robot_token is interpolated from var.harbor_robot_token (added to
  # infra/hetzner/variables.tf); the catalyst-api provisioner reads it from
  # the `harbor-robot-token` K8s Secret in the openova-harbor namespace on
  # contabo and passes it to each new Sovereign's cloud-init render at
  # provisioning time. This keeps the token out of git.
  #
  # CRITICAL ORDERING: this file MUST be written to disk BEFORE k3s installs
  # (the k3s install runcmd below). k3s reads registries.yaml at startup and
  # configures containerd's mirror table; a missing file at startup means
  # direct pulls from DockerHub for the entire lifetime of that node.
```

## block-39-line-670

Formerly `cloudinit-control-plane.tftpl:670-687` (G107 #2702 strip).

```
      # k8sServiceHost: the LOCAL CP's stable private IP per region.
      # Primary cluster: 10.0.1.2 (main.tf:317). Each secondary region's
      # cloud-init renders ${cp_private_ip} to its own subnet's .2
      # (e.g. 10.0.11.2 for nbg1-1, 10.0.12.2 for hel1-2 — main.tf:267
      # secondary_region_cp_ips). Without this each region's pre-Flux
      # cilium install would talk to the PRIMARY's 10.0.1.2 which
      # presents the primary cluster's CA, NOT this cluster's CA →
      # x509 unknown authority → cilium-operator crash-loops →
      # no CNI → flux controllers Pending forever.
      #
      # Was previously 127.0.0.1 which works on the CP (k3s server
      # listens on localhost:6443) but FAILS on workers (k3s agent
      # does NOT expose apiserver on localhost — only the supervisor
      # port on :6444). When worker_count>0 (issue #733 multi-node
      # default), the Cilium DaemonSet on workers used to crashloop
      # with "127.0.0.1:6443: connect: connection refused" forever.
      # Routing every Cilium agent to the LOCAL CP's private IP fixes
      # both the multi-node worker path AND the multi-region path.
```

## block-40-line-649

Formerly `cloudinit-control-plane.tftpl:649-665` (G107 #2702 strip).

```
      # Catalyst bootstrap cilium values — MUST stay in lock-step with
      # platform/cilium/chart/values.yaml `cilium:` block + the overlay
      # in clusters/_template/bootstrap-kit/01-cilium.yaml. See the
      # comment block immediately above this write_files entry, and
      # cilium_values_parity_test.go for the regression guard.
      # cluster.name + cluster.id — set from the FIRST helm install so
      # cilium-agent announces with the correct identity from boot. If
      # we relied on the post-bootstrap Flux helm-upgrade to fix these,
      # the agent would NOT pick up the change without a DaemonSet
      # rollout (it reads cilium-config once at startup) and downstream
      # consumers (hubble-relay, clustermesh-apiserver, kvstoremesh)
      # would x509-fail their TLS handshakes because the cert SAN list
      # contains the new cluster.name but the peer announcements still
      # carry "default". Caught on prov t105.omani.works
      # (a6c0f5dfebd63bd0, 2026-05-15) — hubble-relay crashlooping with
      # `certificate is valid for *.t105-mesh.hubble-grpc.cilium.io,
      # not catalyst-t105-omani-works-cp1.default.hubble-grpc.cilium.io`.
```

## block-41-line-600

Formerly `cloudinit-control-plane.tftpl:600-645` (G107 #2702 strip).

```
  # ── Cilium bootstrap values (issue #491) ─────────────────────────────
  #
  # The bootstrap helm install below MUST land the same effective values
  # as the Flux bp-cilium HelmRelease (clusters/_template/bootstrap-kit/
  # 01-cilium.yaml). Anything that differs becomes drift, and drift in
  # this particular release is fatal because:
  #
  #   1. Flux applies bp-cilium with `helm upgrade --install`, which is
  #      a no-op when the in-cluster release already has the right values
  #      and a UPGRADE when it does not.
  #   2. The bootstrap-kit Kustomization is `wait: true` (issue #492).
  #      Until cilium-agent is Ready, NO other HelmRelease in
  #      bootstrap-kit reconciles — including the bp-cilium upgrade
  #      itself, because Flux's source-controller will not pull a fresh
  #      GitRepository revision while the existing one is unhealthy.
  #   3. cilium-agent waits for the operator to register
  #      `ciliumenvoyconfigs` + `ciliumclusterwideenvoyconfigs` CRDs.
  #      The upstream chart only registers them when
  #      `envoyConfig.enabled=true`. If the bootstrap install omits
  #      that flag, the CRDs are never registered, the agent never
  #      reaches Ready, the upgrade never fires, and Phase 1 deadlocks.
  #
  # Phase-8a bug #16 (otech8 2026-05-01): the prior bootstrap helm
  # install used six --set flags (`kubeProxyReplacement`, `k8sService*`,
  # `tunnelProtocol`, `bpf.masquerade`) and produced a release missing
  # `envoyConfig.enabled`, `gatewayAPI.enabled`, `envoy.enabled`,
  # `l7Proxy`, `encryption.*`, `hubble.*`, etc. Every fresh provision
  # crash-looped cilium-agent.
  #
  # Canonical seam: this file IS the values overlay for the bootstrap
  # install, and `clusters/_template/bootstrap-kit/01-cilium.yaml`'s
  # `spec.values.cilium:` block IS the values overlay for the Flux HR.
  # The umbrella chart wraps under `cilium:` (subchart key), the
  # bootstrap install targets the upstream `cilium/cilium` chart
  # directly so values land at top level. The merged effective set
  # below mirrors `platform/cilium/chart/values.yaml`'s `cilium:`
  # block PLUS the overlay in 01-cilium.yaml. A divergence test in
  # `products/catalyst/bootstrap/api/internal/provisioner/
  # cilium_values_parity_test.go` (issue #491) locks the two files
  # together so a future operator cannot change one without the other.
  #
  # Per INVIOLABLE-PRINCIPLES.md #4 (never hardcode): the chart
  # version is parameterised below via the helm install --version flag,
  # and the values in this file are operator-overridable post-bootstrap
  # via the Flux HR's `spec.values` block (which always wins on
  # subsequent `helm upgrade`).
```

## block-42-line-555

Formerly `cloudinit-control-plane.tftpl:555-587` (G107 #2702 strip).

```
  # ── catalyst-system/catalyst-handover-jwt-public Secret (issue #606 followup) ─
  #
  # On Phase-8b live (otech48, 2026-05-03) the Sovereign-side catalyst-api
  # responded to GET /auth/handover with
  #   {"error":"server misconfiguration: public key unavailable"}.
  # Root cause: the JWK above was written ONLY to host disk
  # (/var/lib/catalyst/handover-jwt-public.jwk). The catalyst-api Pod's
  # volume mount references K8s Secret `catalyst-handover-jwt-public`
  # (key: public.jwk). That Secret was never created on the Sovereign,
  # so the volume mount fell through (the Secret is `optional: true`)
  # and the file was missing inside the container at the env-pinned
  # path `/etc/catalyst/handover-jwt-public/public.jwk`.
  #
  # Fix: mirror the canonical pattern that flux-system/ghcr-pull (PR #543)
  # and flux-system/harbor-robot-token (PR #680) already follow. Cloud-init
  # writes the Secret manifest into catalyst-system NS and runcmd: applies
  # it BEFORE flux-bootstrap, so the Secret exists by the time the
  # bp-catalyst-platform HelmRelease lands and the catalyst-api Pod starts.
  #
  # Why catalyst-system rather than flux-system + Reflector: the Secret is
  # mounted ONLY by the catalyst-api Pod (single workload, single namespace).
  # Reflector auto-mirror would just create unused copies in every other
  # namespace — extra blast radius for no benefit. The harbor-robot-token
  # case is different: it has to be visible to flux-system (Flux pulls OCI
  # charts) AND catalyst-system (catalyst-api re-stamps it onto child
  # provisions), so Reflector is justified there.
  #
  # The catalyst-system namespace itself is created by the bp-catalyst-platform
  # HelmRelease wrapper in clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml,
  # but Flux applies that namespace alongside the HelmRelease — racing with
  # this Secret apply. Pre-creating the namespace here (idempotent dry-run/apply
  # pattern, identical to the cert-manager pre-create at line ~1099) eliminates
  # the race.
```

## block-43-line-539

Formerly `cloudinit-control-plane.tftpl:539-549` (G107 #2702 strip).

```
  # ── Handover JWT public key (issue #605, Phase-8b) ───────────────────
  #
  # RFC 7517 JWK JSON of the Catalyst-Zero RS256 public key. Written to
  # /var/lib/catalyst/handover-jwt-public.jwk (mode 0600) on the new
  # Sovereign control-plane. Agent-C (auth/handover) reads this file to
  # verify the one-time handover JWT without a cross-cluster RPC.
  #
  # Per INVIOLABLE-PRINCIPLES.md #10 (credential hygiene): mode 0600 so
  # the file is readable only by root. Even though RSA public keys are
  # technically non-secret, tightening the permission costs nothing and
  # avoids any future confusion about sensitivity.
```

## block-44-line-482

Formerly `cloudinit-control-plane.tftpl:482-512` (G107 #2702 strip).

```
  # ── Crossplane provider-hcloud + ProviderConfig (issue #425) ────────
  #
  # Phase 0→Day-2 handover. After Flux installs Crossplane core (via
  # bp-crossplane in the bootstrap-kit), this Provider package + its
  # ProviderConfig come up in the cluster and become the seam for ALL
  # subsequent Hetzner Cloud mutations.
  #
  # Per ADR-0001 §11.3 + INVIOLABLE-PRINCIPLES #3:
  #   - OpenTofu provisions Phase 0 EXACTLY ONCE per Sovereign.
  #   - Crossplane is the only Day-2 cloud-API mutation seam.
  #   - Flux is the only GitOps reconciler.
  #   - Blueprints (`bp-<name>:<semver>` OCI) are the only install unit.
  #   - NEVER bespoke Go cloud-API calls. NEVER `exec.Command("helm",
  #     ...)`. NEVER direct `kubectl apply` of production manifests.
  #
  # Once `provider-hcloud` reaches `Healthy=True` (event the Catalyst
  # control plane observes via the Crossplane status conditions), the
  # catalyst-api's bespoke Hetzner-API calls for any RUNTIME-scaling
  # concern (additional Floating IPs, additional buckets, scaling
  # LoadBalancers, ...) MUST be retired in favour of XRC writes against
  # this Provider. Provisioning Phase 0 (the very first server, network,
  # firewall, LB, bucket) stays in this Tofu module by design — that's
  # the bootstrap exception that lets the Provider exist in the first
  # place.
  #
  # Package version pin: v0.4.0 of `crossplane-contrib/provider-hcloud`
  # is the latest stable as of 2026-05. Per INVIOLABLE-PRINCIPLES #4
  # (never hardcode), the version is operator-bumpable via PR; future
  # rotations land here in the same commit that bumps the
  # `bp-crossplane-claims` Composition referencing the new Provider
  # types.
```

## block-45-line-462

Formerly `cloudinit-control-plane.tftpl:462-477` (G107 #2702 strip).

```
        # Issue #1778 — Hetzner network + firewall + ssh-key names so the
        # cluster-autoscaler attaches scale-up Pods to the SAME private
        # network + firewall the Phase-0 workers landed in. Without
        # `HCLOUD_NETWORK`, the autoscaler-spawned VMs only receive public
        # IPs; the worker cloud-init runs
        # `K3S_URL=https://10.0.1.2:6443` (CP private IP) which is
        # unreachable from a node without the 10.0.0.0/16 attachment → the
        # k3s agent join silently fails → node never registers → autoscaler
        # times out at 15m → backoff. The Hetzner provider docs at
        # https://github.com/kubernetes/autoscaler/tree/master/cluster-
        # autoscaler/cloudprovider/hetzner#environment-variables list
        # HCLOUD_NETWORK / HCLOUD_FIREWALL / HCLOUD_SSH_KEY as the env
        # vars that attach the spawned servers to existing resources by
        # name. Names match the Phase-0 Tofu resource names verbatim
        # (catalyst-<sov-fqdn-with-dashes>-{net,fw} + catalyst-<sov-fqdn-
        # with-dashes>).
```

## block-46-line-451

Formerly `cloudinit-control-plane.tftpl:451-460` (G107 #2702 strip).

```
        # Issue #921 — base64-encoded cloud-init payload for cluster-
        # autoscaler's HCLOUD_CLOUD_INIT env var. The bp-cluster-autoscaler-
        # hcloud HelmRelease's `valuesFrom` lifts this key into
        # `clusterAutoscalerHcloud.cloudInit`; the chart's
        # extraEnvSecrets.HCLOUD_CLOUD_INIT then maps it onto the autoscaler
        # Pod's env. Without this, cluster-autoscaler 1.32.x's Hetzner
        # provider exits at startup with FATAL "HCLOUD_CLUSTER_CONFIG or
        # HCLOUD_CLOUD_INIT is not specified". Same content as the Phase-0
        # worker servers' user_data so autoscaler-spawned workers join the
        # cluster identically.
```

## block-47-line-422

Formerly `cloudinit-control-plane.tftpl:422-439` (G107 #2702 strip).

```
  # ── flux-system/cloud-credentials Secret (issue #425, OpenTofu→Crossplane) ─
  #
  # Bootstrap of the Crossplane Hetzner Cloud provider (planted further
  # below in this cloud-init). Carries the operator's hcloud API token —
  # the same token Tofu used to provision Phase 0 — under a single key
  # `hcloud-token`. Per ADR-0001 §11.3 + INVIOLABLE-PRINCIPLES #3,
  # Day-2 cloud-resource changes (additional Floating IPs, additional
  # buckets, scaling LoadBalancers, firewall rule edits, ...) flow
  # through Crossplane XRC writes against this Provider — NEVER through
  # bespoke Go cloud-API calls in catalyst-api, NEVER through manual
  # Tofu re-runs.
  #
  # The Secret name is vendor-agnostic (`cloud-credentials`); the
  # `hcloud-token` key name encodes the cloud-specific shape of the
  # credential. A future AWS Sovereign would write
  # `aws-access-key-id`/`aws-secret-access-key` keys into the same
  # Secret name; the matching Crossplane Provider/ProviderConfig
  # (added in the same Tofu module's cloud-init) reads them.
```

## block-48-line-352

Formerly `cloudinit-control-plane.tftpl:352-394` (G107 #2702 strip).

```
  # ── flux-system/object-storage Secret (issue #371, vendor-agnostic since #425) ─
  #
  # The Sovereign's per-cluster S3 credentials, materialised as a stock
  # Kubernetes Secret in the `flux-system` namespace. Harbor (#383) and
  # Velero (#384) consume this Secret via the canonical `secretRef` field
  # in their respective HelmRelease values blocks, e.g.
  #
  #   harbor:
  #     persistence:
  #       imageChartStorage:
  #         type: s3
  #         s3:
  #           existingSecret: object-storage
  #
  # Per #425 the Secret name is vendor-AGNOSTIC (`object-storage`, no
  # `hetzner-` prefix). A future AWS / Azure / GCP / OCI Sovereign
  # provisions the same Secret name with the same key set via its own
  # `infra/<provider>/` Tofu module — every existing chart Just Works
  # without renaming.
  #
  # The Secret is namespace-bound to flux-system so the helm-controller can
  # rewrite it into the workload namespaces at chart install time — that's
  # the same boundary `ghcr-pull` already uses, so the apply ordering in
  # runcmd: below stays a single sequenced step.
  #
  # Why pre-populated by cloud-init rather than a SealedSecret committed to
  # git: ADR-0001 §9.2 forbids bespoke cloud-API calls and Hetzner exposes
  # NO Cloud API for S3 credential issuance — they're operator-issued in
  # the Hetzner Console exactly once. Therefore catalyst-api receives the
  # plaintext from the wizard, validates it, and forwards it to the new
  # Sovereign via the same encrypted-PVC + cloud-init channel as the GHCR
  # pull token. The credentials never land in git; the only durable copies
  # are the per-deployment OpenTofu workdir (mode 0600, wiped on tofu
  # destroy) and inside the new Sovereign's etcd (encrypted at rest by
  # k3s default).
  #
  # Token rotation policy: per Hetzner's docs, the secret half is shown
  # exactly once at issue time. To rotate, the operator issues a fresh
  # credential pair in the Hetzner Console, updates the wizard payload
  # for the next provisioning, OR for an existing Sovereign uses a
  # day-2 Crossplane XRC write (the Provider+ProviderConfig planted
  # below makes this possible without a Tofu re-run; out of scope for
  # the initial bootstrap).
```

## block-49-line-298

Formerly `cloudinit-control-plane.tftpl:298-327` (G107 #2702 strip).

```
  # ── flux-system/pdm-basicauth Secret (issue #879 Bug 2) ──────────────
  #
  # The Sovereign-side catalyst-api Pod (api-deployment.yaml) reads
  # CATALYST_PDM_BASIC_AUTH_USER + CATALYST_PDM_BASIC_AUTH_PASS via
  # secretKeyRef into `pdm-basicauth` (in the same namespace
  # catalyst-api lives — catalyst-system). Reflector mirrors this
  # Secret out of flux-system to sme,catalyst,catalyst-system,gitea,
  # harbor (same canonical pattern flux-system/ghcr-pull and
  # flux-system/harbor-robot-token already use).
  #
  # The Pod adds `Authorization: Basic …` to every PDM call so the
  # Traefik basicAuth Middleware in front of pool.openova.io accepts
  # the request — pdmFlipNS in parent_domains.go is the call site.
  # Without this Secret + Reflector mirror, every Day-2 add-parent-
  # domain POST returns 401 from PDM (caught live on otech103,
  # 2026-05-05 — issue #879).
  #
  # optional=true on the secretKeyRef in api-deployment.yaml so:
  #   - Catalyst-Zero pods (contabo's catalyst-api) start cleanly
  #     when the Secret is absent. Contabo uses the in-cluster
  #     Service path which bypasses the ingress entirely.
  #   - CI / older Sovereigns that pre-date this provisioning seam
  #     start cleanly. POSTs without auth get 401 from PDM with a
  #     clear log line, instead of crashlooping on Pod start.
  #
  # Per Inviolable Principle #10: the credentials never enter a
  # logged struct, a deployment record, or any committed git file.
  # Plaintext only ever lives in the per-deployment OpenTofu workdir
  # (mode 0600, wiped on tofu destroy) and inside the Sovereign's
  # encrypted etcd.
```

## block-50-line-268

Formerly `cloudinit-control-plane.tftpl:268-285` (G107 #2702 strip).

```
  # ── cert-manager/powerdns-api-credentials Secret (PR #681 followup) ──────
  #
  # The bp-cert-manager-powerdns-webhook Pod reads X-API-Key from this
  # Secret to authenticate against contabo's authoritative PowerDNS for
  # the omani.works zone. Without this, DNS-01 challenges hang and the
  # wildcard cert never issues — caught live on otech47.
  #
  # PR #681 dropped bp-cert-manager-dynadot-webhook in favour of the
  # contabo-targeted webhook but left the cloud-init Secret block on the
  # old shape. This block replaces it with the powerdns-api-credentials
  # Secret pointed at by ClusterIssuer letsencrypt-dns01-prod-powerdns
  # (apiKeySecretRef.name: powerdns-api-credentials, key: api-key).
  #
  # Namespace: cert-manager — same as the HelmRelease's targetNamespace.
  # Source on contabo: openova-system/powerdns-api-credentials Secret;
  # mirrored into the catalyst namespace via Reflector annotations
  # (platform/powerdns/chart/templates/api-secret.yaml) so catalyst-api
  # can mount it as env CATALYST_POWERDNS_API_KEY.
```

## block-51-line-216

Formerly `cloudinit-control-plane.tftpl:216-246` (G107 #2702 strip).

```
  # ── flux-system/harbor-robot-token Secret (issue #557 follow-up) ─────
  #
  # The catalyst-api Pod template (products/catalyst/chart/templates/
  # api-deployment.yaml) references a Secret named `harbor-robot-token`
  # via a REQUIRED (non-optional) secretKeyRef on every Sovereign. The
  # token authenticates pulls from the central harbor.openova.io
  # proxy-cache (proxy-dockerhub, proxy-gcr, proxy-quay, proxy-k8s,
  # proxy-ghcr) — the same value already interpolated into
  # /etc/rancher/k3s/registries.yaml below.
  #
  # Without this Secret the catalyst-api Pod stays in
  # CreateContainerConfigError indefinitely. Caught live on otech43,
  # otech45, otech46 — the operator workaround was hand-creating a
  # placeholder Secret on each iteration, which is a workaround, not
  # a fix.
  #
  # Why this Secret lives in flux-system + uses Reflector auto-mirror:
  # the same canonical pattern as `ghcr-pull` above — bp-reflector
  # (slot 05a) propagates the Secret to every namespace via
  # `reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"`,
  # so catalyst-system (and any other namespace that needs the token)
  # picks it up event-driven on first reconcile.
  #
  # The Secret carries one key (`token`) which catalyst-api reads as
  # CATALYST_HARBOR_ROBOT_TOKEN and re-stamps onto every grandchild
  # Sovereign provision request — the Sovereign's own Sovereigns
  # (post-handover) inherit the same central proxy-cache auth.
  #
  # Token rotation: yearly, see docs/SECRET-ROTATION.md. Rotation flows
  # through `var.harbor_robot_token` → re-render cloud-init → re-apply
  # this Secret. The plaintext NEVER lives in git.
```

## block-52-line-173

Formerly `cloudinit-control-plane.tftpl:173-199` (G107 #2702 strip).

```
          # bp-reflector (slot 05a) mirrors this secret to every namespace
          # so all workloads can pull from ghcr.io/openova-io without
          # per-namespace manual creation. reflection-auto-enabled means
          # Reflector creates the copy in new namespaces as they appear.
          #
          # ALLOWED + AUTO namespaces — explicitly enumerated.
          # Issue #879 Bonus Bug 6: the previous values left both fields
          # as empty strings, which Reflector interprets ambiguously
          # depending on version. On otech103 (2026-05-05) catalyst-api
          # POD failed to pull the SHA-pinned image until an operator
          # manually created the Secret in the `catalyst-system`
          # namespace. The fix here lists every namespace catalyst-api
          # and SME services land in: sme, catalyst, catalyst-system,
          # gitea, harbor — paired with `auto-namespaces` so a
          # later-created namespace (the bp-* HelmReleases land in their
          # own namespaces over time) still gets the mirror automatically
          # the moment it appears. The list is the SUPERSET of what
          # otech103 verified live. Future namespaces added to the
          # bootstrap-kit (a new bp-* slot) only need an addition here
          # plus a Pod restart to pick up the new mirror.
          #
          # Issue #952 (2026-05-05): added `newapi` — the bp-newapi
          # bootstrap-kit slot 80 lands its Deployment in this namespace
          # and pulls PRIVATE `newapi-mirror` + `services-metering-sidecar`
          # images. Without `newapi` on this list the Pod stalls in
          # ImagePullBackOff with 403 Forbidden, blocking alice signup
          # gate 5 (LLM). Caught live on otech113.
```

## block-53-line-141

Formerly `cloudinit-control-plane.tftpl:141-163` (G107 #2702 strip).

```
  # ── flux-system/ghcr-pull Secret ─────────────────────────────────────
  #
  # Every HelmRepository CR in clusters/_template/bootstrap-kit/
  # references `secretRef: name: ghcr-pull` because the bp-* OCI artifacts
  # at `ghcr.io/openova-io/` are PRIVATE. Without this Secret, the
  # source-controller logs:
  #
  #   failed to get authentication secret 'flux-system/ghcr-pull':
  #     secrets "ghcr-pull" not found
  #
  # …and Phase 1 stalls at bp-cilium. The operator workaround (kubectl
  # apply the Secret by hand after Flux installs) is not durable across
  # re-provisioning of the same Sovereign — every fresh control-plane
  # boots without the Secret.
  #
  # We write the Secret into flux-system at cloud-init time, BEFORE
  # /var/lib/catalyst/flux-bootstrap.yaml is applied, so the GitRepository +
  # Kustomization land into a cluster that already has working GHCR creds.
  # The apply step is in runcmd: below; the manifest itself lives here.
  #
  # Token rotation policy: yearly, stored in 1Password under
  # "Catalyst — GHCR pull token (catalyst-ghcr-pull-token)". See
  # docs/SECRET-ROTATION.md. The token NEVER lives in git.
```

## block-54-line-54

Formerly `cloudinit-control-plane.tftpl:54-66` (G107 #2702 strip).

```
  # ── Kernel inotify limits — k3s + Flux + CNPG + bao + Helm exhaust Ubuntu defaults ──
  # Default Hetzner Ubuntu 24.04 ships fs.inotify.max_user_instances=128
  # and fs.inotify.max_user_watches=524288 — but every Helm controller,
  # CNPG operator, k3s kubelet, file-watching admin tool grabs an
  # instance slot. On a 35-component bootstrap-kit the slots run out
  # mid-install and the next process to ask gets:
  #   failed to create fsnotify watcher: too many open files
  # Diagnosed live during otech35 — bp-openbao's `bao operator init`
  # crash-looped 4× with that exact error, which Flux escalated to
  # InstallFailed/RetriesExceeded — masking the real OS-level root cause.
  #
  # Bump well above k8s/k3s production guidance so future blueprint
  # additions don't tickle the same wall.
```

## block-55-line-1

Formerly `cloudinit-control-plane.tftpl:1-19` (G107 #2702 strip).

```
#cloud-config
# Catalyst Sovereign control-plane bootstrap.
# Sovereign: ${sovereign_fqdn}
# Provisioned by: catalyst-provisioner (https://console.openova.io/sovereign)
#
# This script:
#   1. Installs OS hardening (SSH password-auth off, fail2ban, unattended-upgrades).
#   2. Installs k3s with --flannel-backend=none (Cilium replaces it).
#   3. Installs Flux + bootstraps the GitRepository pointing at the shared
#      clusters/_template/ tree in the public OpenOva monorepo. The
#      Sovereign's FQDN is interpolated into the template manifests via
#      Flux postBuild.substitute ($${SOVEREIGN_FQDN}) at apply time, so
#      no per-Sovereign directory needs to be committed before
#      provisioning. From this point Flux is the GitOps reconciler and
#      installs the 11-component bootstrap kit (Cilium → cert-manager →
#      Crossplane → ... → bp-catalyst-platform) in dependency order via
#      Kustomizations the _template directory ships.
#   4. Touches /var/lib/catalyst/cloud-init-complete so the catalyst-api
#      provisioner can detect cloud-init has finished.
```

