package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	clientgofeatures "k8s.io/client-go/features"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/natspub"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/newapi"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/oidcprovider"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	// Side-effect import: registers every CloudProvider adapter
	// (hetzner, huawei) against internal/providers/registry at
	// init() time. The wipe handler resolves the adapter of a
	// deployment with providers.Get().
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func main() {
	port := env("PORT", "8080")
	corsOrigin := env("CORS_ORIGIN", "*")

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// #4071 — disable client-go's WatchListClient feature gate BEFORE any
	// client / informer is constructed.
	//
	// Root cause: client-go v0.36 ships WatchListClient as a Beta gate that
	// is DEFAULT-ON from v1.35 (see k8s.io/client-go/features). With it on,
	// the reflector's primary path is the streaming watch-list
	// (`?sendInitialEvents=true&resourceVersionMatch=NotOlderThan&watch=true`)
	// instead of the classic LIST+WATCH. On the Sovereign chroot's k3s
	// apiserver this streaming-list path intermittently returns
	// `the server could not find the requested resource` for freshly-served
	// CRDs (apps.openova.io/applications, orgs.openova.io/organizations,
	// catalyst.openova.io/{blueprints,environments}, dr.openova.io/{continuums,
	// cnpgpairs}, sandbox.openova.io/sandboxes). The reflector logs
	// `reflector.go "Failed to watch"` and retries the SAME failing streaming
	// path forever — it never falls through to the plain LIST+WATCH that the
	// apiserver serves perfectly (verified live on omantel.biz: a direct
	// `GET /apis/apps.openova.io/v1/applications` returns 200 with items, yet
	// the in-process informer 404s on every retry). A pod restart does NOT fix
	// it because every fresh reflector re-takes the same broken streaming path.
	// Net effect: the Application/Org/Blueprint/Environment informers never
	// sync → the console Apps view shows every platform app PENDING, and
	// user-installed apps (agenity/WordPress) never reach Ready through the
	// k8scache SSE stream either.
	//
	// Disabling the gate forces the reflector back to LIST+WATCH (the path
	// proven working against this apiserver), recovering WITHOUT a restart on
	// the next reflector relist. Set programmatically so the fix travels with
	// the binary and protects every fresh prov regardless of chart env wiring;
	// an operator can still re-enable via KUBE_FEATURE_WatchListClient=true.
	disableWatchListClient(log)

	h := handler.New(log)

	// #4706 — turn on the external console-reachability gate: markPhase1Done
	// will not flip a converged deployment to "ready" until its console URL
	// actually answers, so the operator is never redirected to a TCP-dead
	// console under a false-green "ready" pill.
	h.EnableConsoleReachabilityGate()

	// OpenBao client — wired whenever CATALYST_OPENBAO_ADDR is set.
	//
	// #3374: the client serves TWO consumers with different credential
	// needs:
	//   (a) the bare-URL SSO shim (HandleOpenBaoSSOInit) calls ONLY the
	//       unauthenticated /v1/auth/<mount>/oidc/auth_url login-flow
	//       endpoint — an address alone is sufficient. On-Sovereign
	//       Pods set just CATALYST_OPENBAO_ADDR (bp-catalyst-platform
	//       api.openbaoAddr); without it the shim silently degraded to
	//       the one-click deep-link on every Sovereign (hw130 measured).
	//   (b) the handover-archive receiver (ReceiveTofuArchive) seals to
	//       KV-v2 and additionally requires CATALYST_OPENBAO_TOKEN; it
	//       still returns 503 ("not handover target") when the token is
	//       absent — same external semantics as before.
	// Catalyst-Zero leaves both unset: client nil, receiver 503, shim
	// falls back to the deep-link.
	if addr := os.Getenv("CATALYST_OPENBAO_ADDR"); addr != "" {
		token := os.Getenv("CATALYST_OPENBAO_TOKEN")
		obClient := openbao.New(addr, token)

		// #3376 — Sovereign handover-archive receiver auth. On a Sovereign
		// no long-lived static token is injected (that would be a secret
		// at rest with no rotation); instead bp-openbao's auth-bootstrap
		// Job binds a `catalyst-api` Kubernetes-auth role + a write policy
		// on `secret/catalyst/*` to this pod's ServiceAccount. When
		// CATALYST_OPENBAO_K8S_AUTH_ROLE is set we log in via that role to
		// mint a short-lived Vault token, then re-login on a TTL-driven
		// cadence so ReceiveTofuArchive can seal the phase0 archive at
		// handover. Without this the receiver 503s and the cutover (and
		// therefore the tenant-Org funnel) never un-gates.
		if role := strings.TrimSpace(os.Getenv("CATALYST_OPENBAO_K8S_AUTH_ROLE")); role != "" && token == "" {
			authMount := os.Getenv("CATALYST_OPENBAO_K8S_AUTH_MOUNT") // "" → "kubernetes"
			saPath := os.Getenv("CATALYST_OPENBAO_K8S_SA_TOKEN_PATH") // "" → default
			ttl, lerr := obClient.LoginKubernetes(context.Background(), authMount, role, saPath)
			if lerr != nil {
				// Non-fatal: the SSO auth_url shim still works ADDR-only,
				// and a transient OpenBao-not-yet-ready window self-heals
				// on the next re-login tick. Log loudly per PRINCIPLES #3.
				log.Error("openbao: kubernetes login failed (handover-archive receiver stays 503 until re-login succeeds)",
					"addr", addr, "role", role, "err", lerr,
				)
			} else {
				log.Info("openbao: kubernetes login succeeded — handover-archive receiver enabled",
					"addr", addr, "role", role, "leaseTTLSeconds", ttl,
				)
			}
			// Re-login loop: refresh the token before the lease expires.
			// A zero/short TTL falls back to a 5-minute cadence; we always
			// refresh at min(ttl/2, ...) so a missed tick never strands the
			// receiver with an expired token.
			go func() {
				for {
					sleep := 5 * time.Minute
					if ttl > 60 {
						sleep = time.Duration(ttl/2) * time.Second
					}
					time.Sleep(sleep)
					nttl, rerr := obClient.LoginKubernetes(context.Background(), authMount, role, saPath)
					if rerr != nil {
						log.Error("openbao: kubernetes re-login failed (will retry)",
							"addr", addr, "role", role, "err", rerr,
						)
						continue
					}
					ttl = nttl
				}
			}()
		}

		h.SetOpenBao(obClient)
		if strings.TrimSpace(obClient.Token) != "" {
			log.Info("openbao: handover-archive receiver + SSO auth_url enabled",
				"addr", addr,
			)
		} else {
			log.Info("openbao: SSO auth_url enabled (no token — handover-archive receiver disabled)",
				"addr", addr,
			)
		}
	}

	// Handover JWT signer (issue #605) — RSA-2048 keypair lifecycle.
	// CATALYST_HANDOVER_SIGNER_PATH must point at a writable path on the
	// PVC; if absent the signer is nil and MintHandoverToken returns 503.
	// Sovereign clusters leave this env unset; Catalyst-Zero sets it.
	//
	// Backwards-compat: legacy env name CATALYST_HANDOVER_KEY_PATH is read
	// as a fallback. Renamed (Kyverno deny-plaintext-secret-env, 2026-06-03)
	// because the substring "KEY" matches Kyverno's secret-shaped regex
	// `(?i)(PASSWORD|TOKEN|KEY|SECRET)`, and a plaintext `value:` on such
	// an env name registers as a policy violation even though the value is
	// just a filesystem path. New chart emits only CATALYST_HANDOVER_SIGNER_PATH.
	keyPath := os.Getenv("CATALYST_HANDOVER_SIGNER_PATH")
	if keyPath == "" {
		keyPath = os.Getenv("CATALYST_HANDOVER_KEY_PATH")
	}
	if keyPath != "" {
		pubKeyPath := env("CATALYST_HANDOVER_PUBKEY_PATH", keyPath+".pub.jwk")
		// #2940 (Pillar 5): the mothership-default literal lives in exactly one
		// place — handoverjwt.DefaultIssuer() — which reads
		// CATALYST_HANDOVER_JWT_ISSUER and falls back to the Catalyst-Zero
		// origin only when unset. Passing the resolved value (rather than a
		// second hardcoded "https://console.openova.io" here) keeps the
		// per-Sovereign override seam honoured and avoids a drift-prone
		// duplicate tether literal. Empty env → DefaultIssuer() → mothership
		// origin (Catalyst-Zero byte-unchanged).
		issuer := handoverjwt.DefaultIssuer()
		signer, err := handoverjwt.LoadOrGenerate(keyPath, pubKeyPath, issuer, 5*time.Minute)
		if err != nil {
			log.Warn("handoverjwt: keypair init failed; MintHandoverToken will return 503",
				"err", err,
			)
		} else {
			h.SetHandoverSigner(signer)
			log.Info("handoverjwt: signer ready", "issuer", issuer)
			// #4114 — publish the runtime signer's ACTUAL public JWK into
			// the catalyst-handover-jwt-public Secret so every off-pod
			// consumer (the agenity openova-mcp's RS256 bearer verifier,
			// any future federation peer) verifies session bearers against
			// the SAME key catalyst-api signs with. LoadOrGenerate may
			// have minted a fresh keypair (cloud-init seeded only the
			// public Secret, not the private PEM at keyPath), leaving the
			// published Secret stale + mismatched → openova-mcp rejected
			// every genuine bearer. Self-healing: PATCH on each boot.
			if pubJWK, jwkErr := signer.PublicJWK(); jwkErr == nil {
				publishHandoverPubkey(context.Background(), log, pubJWK)
			} else {
				log.Warn("handoverjwt: PublicJWK failed; published Secret may be stale (#4114)", "err", jwkErr)
			}
		}
	}

	// Keycloak auth config (issue #608, Phase-8b) — PKCE + HMAC session
	// cookies for the Catalyst-Zero wizard. Wired only when
	// CATALYST_KC_ADDR is set; Sovereign clusters and CI leave it unset
	// and the RequireSession middleware becomes a transparent passthrough.
	if kcAddr := os.Getenv("CATALYST_KC_ADDR"); kcAddr != "" {
		realm := env("CATALYST_KC_REALM", "openova")
		clientID := env("CATALYST_KC_CLIENT_ID", "catalyst-zero-ui")
		redirectURI := os.Getenv("CATALYST_KC_REDIRECT_URI")
		cookieSecret := os.Getenv("CATALYST_SESSION_COOKIE_SECRET")
		jwksURL := kcAddr + "/realms/" + realm + "/protocol/openid-connect/certs"
		authCfg := &auth.Config{
			KeycloakAddr: kcAddr,
			Realm:        realm,
			ClientID:     clientID,
			RedirectURI:  redirectURI,
			CookieSecret: cookieSecret,
			JWKSCache:    auth.NewJWKSCache(jwksURL, 10*time.Minute),
		}
		// Wire the handover signer's public key as a fallback for self-signed
		// session JWTs (KC 24.7+ removed legacy token-exchange).
		if signer := h.GetHandoverSigner(); signer != nil {
			if pub, err := signer.PublicRSAKey(); err == nil {
				authCfg.LocalPublicKey = pub
				log.Info("auth: local public key wired into session validator")
			}
		}
		h.SetAuthConfig(authCfg)
		log.Info("auth: Keycloak session gate enabled",
			"addr", kcAddr,
			"realm", realm,
		)
	}

	// PIN-auth openova-realm Keycloak client (issue #688, replaces #614 magic-link).
	// Wired only when CATALYST_OPENOVA_KC_SA_CLIENT_SECRET is set.
	// Catalyst-Zero always sets this; Sovereign clusters leave it unset.
	if secret := os.Getenv("CATALYST_OPENOVA_KC_SA_CLIENT_SECRET"); secret != "" {
		addr := env("CATALYST_OPENOVA_KC_ADDR", env("CATALYST_KC_ADDR", "http://keycloak-zero.keycloak-zero.svc.cluster.local"))
		realm := env("CATALYST_OPENOVA_KC_REALM", "openova")
		clientID := env("CATALYST_OPENOVA_KC_SA_CLIENT_ID", "catalyst-zero-server")
		h.SetOpenovaKC(keycloak.New(addr, realm, clientID, secret))
		log.Info("pin-auth: openova KC client ready",
			"addr", addr,
			"realm", realm,
		)
	}

	// EPIC-3 slice T2 (#1098) — Keycloak composite catalog-tier
	// realm-role bootstrap. Gated by KEYCLOAK_BOOTSTRAP_TIER_ROLES
	// (default false) so existing Sovereigns + the contabo mothership
	// continue to start unchanged; opt in via the chart's catalyst-api
	// Deployment env on the Sovereigns where the catalog-tier system
	// is the source of UX truth.
	//
	// Why a goroutine: Keycloak may not be ready when catalyst-api
	// starts (Flux reconciles bp-keycloak in parallel with
	// bp-catalyst-platform). Blocking the HTTP listener on a Keycloak
	// availability probe violates ADR-0001 §2 event-driven principle.
	// Instead the bootstrap polls for KC readiness with capped backoff
	// (5 attempts, ~30s total) and gives up — the next catalyst-api
	// restart picks the bootstrap up again. Re-runs are no-ops once
	// the realm-role chain is in place (idempotency anchor in
	// EnsureTierRealmRoles).
	// KC_BOOTSTRAP_TIER_ROLES (preferred) / KEYCLOAK_BOOTSTRAP_TIER_ROLES
	// (legacy, kept for backward-compat). Renamed (Kyverno
	// deny-plaintext-secret-env, 2026-06-03) — the substring "KEY" in
	// KEYCLOAK matches Kyverno's secret-shaped regex
	// `(?i)(PASSWORD|TOKEN|KEY|SECRET)` and made the boolean
	// `value: "false"` register as a plaintext-secret PolicyViolation.
	if envBool("KC_BOOTSTRAP_TIER_ROLES", envBool("KEYCLOAK_BOOTSTRAP_TIER_ROLES", false)) {
		// Reuse the same SA credentials wired above for /auth/handover
		// (CATALYST_KC_ADDR + CATALYST_KC_REALM + CATALYST_KC_SA_CLIENT_ID
		// + CATALYST_KC_SA_CLIENT_SECRET). If any are missing the
		// goroutine logs and exits — the operator opted into the
		// bootstrap but didn't wire credentials, that's a config bug
		// not an infra outage and shouldn't crashloop catalyst-api.
		kcAddr := os.Getenv("CATALYST_KC_ADDR")
		kcRealm := env("CATALYST_KC_REALM", "sovereign")
		kcSAClient := os.Getenv("CATALYST_KC_SA_CLIENT_ID")
		kcSASecret := os.Getenv("CATALYST_KC_SA_CLIENT_SECRET")
		if kcAddr == "" || kcSAClient == "" || kcSASecret == "" {
			log.Warn("kc-bootstrap: KEYCLOAK_BOOTSTRAP_TIER_ROLES=true but CATALYST_KC_ADDR/SA_CLIENT_ID/SA_CLIENT_SECRET incomplete; skipping tier-role bootstrap")
		} else {
			bootstrapKC := keycloak.New(kcAddr, kcRealm, kcSAClient, kcSASecret)
			// Tied to a fresh background context — the goroutine's
			// lifetime is the catalyst-api process. ctx (line ~213
			// below) is reserved for the k8scache informer wiring.
			go runTierRoleBootstrap(context.Background(), log, bootstrapKC, kcRealm)
		}
	}

	// Multi-zone PowerDNS client (issue #827, parent epic #825). Wired
	// only on Sovereign-side catalyst-api Pods that have
	// CATALYST_POWERDNS_API_URL + CATALYST_POWERDNS_API_KEY set. Used
	// by POST /api/v1/sovereign/parent-domains/{name}/zone (the
	// admin-console add-domain flow #829). Catalyst-Zero (contabo)
	// leaves both env vars unset; the endpoint then returns 503
	// "powerdns_not_wired" and the admin console UI hides the
	// add-domain button.
	//
	// CATALYST_POWERDNS_API_URL — defaults to the in-cluster Service
	// FQDN of the Sovereign's own PowerDNS so a stock catalyst-api Pod
	// running in catalyst-system reaches powerdns.powerdns.svc:8081
	// without per-pod URL configuration. CATALYST_POWERDNS_API_KEY
	// MUST come from the Reflector-mirrored powerdns-api-credentials
	// Secret (see clusters/_template/bootstrap-kit/05a-reflector.yaml).
	if pdnsKey := os.Getenv("CATALYST_POWERDNS_API_KEY"); pdnsKey != "" {
		pdnsURL := env("CATALYST_POWERDNS_API_URL", "http://powerdns.powerdns.svc.cluster.local:8081")
		pdnsServerID := env("CATALYST_POWERDNS_SERVER_ID", "localhost")
		h.SetPowerDNSZoneClient(powerdns.New(pdnsURL, pdnsKey, pdnsServerID))
		log.Info("powerdns: zone client ready",
			"addr", pdnsURL,
			"serverID", pdnsServerID,
		)
	}

	// K8s data-plane (issue #321) — informer cache + SSE + disk
	// snapshot. Wired only when at least one kubeconfig is mountable;
	// in test/CI without a real cluster the catalyst-api still
	// serves every other endpoint and /healthz returns plain "ok"
	// per the legacy contract.
	ctx := context.Background()
	homeCore := mustHomeCoreClient(log)
	k8sFactory, err := k8scache.FactoryFromEnv(ctx, log, homeCore)
	if err != nil {
		log.Warn("k8scache: factory init failed; data plane disabled",
			"err", err,
		)
	} else if k8sFactory != nil {
		if err := k8sFactory.Start(ctx); err != nil {
			log.Warn("k8scache: factory start failed; data plane disabled",
				"err", err,
			)
		} else {
			sar := k8scache.NewSARCache()
			h.SetK8sCache(k8sFactory, sar, env("CATALYST_K8SCACHE_USER_HEADER", "X-Forwarded-User"))
			log.Info("k8scache: data plane started",
				"sovereigns", len(k8sFactory.Clusters()),
			)
			// #4000 — durable self-heal of secondary-region kubeconfigs.
			// FactoryFromEnv just loaded every <depID>-<region>.yaml from
			// disk VERBATIM (LoadClustersFromDir applies no rewrite). On a
			// chroot whose secondary kubeconfig still carries the unroutable
			// PUBLIC EIP (cloud-init predating #3991, or a restart re-reading
			// the un-rewritten on-disk file), the secondary region's
			// informers never sync → every per-app Topology placement
			// collapses to a false `singleton`. Probe each secondary, and
			// where the server host is an unreachable EIP, heal it to a
			// reachable private SAN read off the apiserver cert, persist, and
			// re-register. Async so the dial probes never delay readiness;
			// a no-op on a healthy/mothership env.
			go h.SelfHealSecondaryKubeconfigsOnDisk(log)
			// G95.1 (Refs #2642) — wire the ExecStreamFactory so the
			// /api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}
			// WebSocket handler can actually dial the apiserver's
			// pods/exec subresource. Pre-G95.1 SetExecStreamFactory was
			// only called from tests; the production handler's
			// execStreamFactoryGet() returned nil → handler responded
			// 503 → browser saw an empty terminal (the symptom founder
			// reported). The factory builds a remotecommand SPDY
			// executor per call against the cluster's rest.Config
			// captured by k8scache at registration time + bridges its
			// duplex stdin/stdout into the io.ReadWriteCloser the
			// pump-loop expects. Stderr is multiplexed into the same
			// stdout pipe (xterm.js renders both streams in the same
			// terminal — the convention the chepherd reference
			// implementation uses, mirrored here).
			h.SetExecStreamFactory(newExecStreamFactory(k8sFactory, log))
			// EPIC-1 #1096 slice S — compliance score aggregator. Wired
			// AFTER k8sCache because runWatch subscribes to the
			// Factory's SSE fanout. Nil publisher + nil resolver are
			// fine: the handler runs in best-effort mode (SSE +
			// Prometheus only). Wiring NATS KV + EnvironmentPolicy
			// resolver is operator-overridable via env vars per
			// docs/INVIOLABLE-PRINCIPLES.md #4.
			compliance := handler.NewComplianceHandler(
				handler.ComplianceConfig{},
				log,
				k8sFactory,
				newCompliancePolicyRollupPublisherFromEnv(log),
				newComplianceEnvironmentPolicyResolverFromEnv(log),
			)
			compliance.Start(ctx)
			h.SetComplianceHandler(compliance)
			log.Info("compliance: aggregator started")

			// Wave 5.65b (#2337, #1096) — custom evaluator Engine.
			// HPA-effective / OTel-injected / Hubble-flows-seen /
			// image-via-Harbor-proxy / Crossplane-managed-by-flux —
			// emit synthetic PolicyReport-like rows alongside Kyverno
			// PolicyReports so the scorecard aggregator treats them
			// uniformly. Engine subscribes to Factory SSE for trigger
			// re-evaluations + ticks for full sweeps.
			if err := wireEvaluatorEngine(ctx, log, k8sFactory); err != nil {
				log.Warn("evaluators: engine wire-up failed; custom evaluators disabled",
					"err", err,
				)
			} else {
				log.Info("evaluators: engine started",
					"evaluators", []string{"hpa", "otel", "hubble", "harbor", "flux"},
				)
			}
		}
	}

	// EPIC-2 #1097 slice I — live install flow. The catalog client is
	// the proxy hop catalyst-api uses to fetch Blueprints from
	// catalyst-catalog (slice L, #1148). Per
	// docs/INVIOLABLE-PRINCIPLES.md #4 the URL is env-overridable
	// (CATALYST_CATALOG_URL); the in-cluster service FQDN is the
	// production default. Wiring here is unconditional — when the
	// catalog upstream is down the handlers surface 502/503 with a
	// clear "catalog upstream" detail rather than 500.
	// Wrap the upstream HTTP catalog client with an in-cluster Blueprint
	// CR fallback (qa-loop iter-8 Fix #40). The wrapper consults
	// catalyst-catalog first (paths a + b: public mirror, sovereign
	// mirror) and falls back to in-cluster `blueprints.catalyst.openova.io`
	// CR lookups (path c: chart-shipped fixtures, qa-fixtures Blueprint
	// CRs, operator-curated CRs). Per
	// docs/INVIOLABLE-PRINCIPLES.md #1 (target-state) all three paths are
	// first-class — the chart's Blueprint CRs are not a "stub for now",
	// they are the canonical seam for chart-shipped fixtures the operator
	// expects to install with `bp-<name>` literally.
	upstreamCatalog := handler.NewCatalogClientFromEnv()
	homeDyn := mustHomeDynamicClient(log)
	h.SetCatalogClient(handler.NewChainedCatalogClient(upstreamCatalog, homeDyn))
	log.Info("catalog: client wired",
		"url", env("CATALYST_CATALOG_URL", "http://catalyst-catalog.openova-system.svc.cluster.local:8080"),
		"clusterFallback", homeDyn != nil,
	)

	// TBD-A34 (#1891) — bake-time owner UserAccess seed (D21).
	//
	// auth_handover.go's seedOwnerUserAccess() was the ONLY caller of
	// EnsureOwnerUserAccess before this slice; D21 therefore only
	// converged after a PIN-login + handover. Zero-touch verification
	// probes (the convergence verifier) cannot drive a live PIN-login
	// from CI, so D21 was reported RED on every fresh prov until the
	// operator manually logged in — even though all the inputs
	// (SOVEREIGN_FQDN + OPERATOR_EMAIL + the UserAccess CRD) were
	// stable on the chroot from bake-time onward.
	//
	// This goroutine closes that gap. When the catalyst-api boots on a
	// chroot Sovereign (SOVEREIGN_FQDN env set per the chart's
	// sovereign-fqdn ConfigMap) AND the operator email is wired
	// (OPERATOR_EMAIL env set per the same ConfigMap's orgEmail key,
	// stamped by the orchestrator's overlay writer at handover-prepare),
	// we call EnsureOwnerUserAccess against the in-cluster dynamic
	// client with capped backoff so the UserAccess CRD has time to
	// roll behind us.
	//
	// Idempotency: EnsureOwnerUserAccess folds AlreadyExists to nil,
	// so this is safe to run before, after, or alongside the existing
	// handover-fired path. A subsequent /auth/handover for the same
	// operator is also a no-op (re-handover for the same email).
	//
	// Skip conditions (each becomes a single Info log + return):
	//   - homeDyn nil (out-of-cluster — CI / smoke / local dev)
	//   - SOVEREIGN_FQDN unset (mother mode — contabo Catalyst-Zero)
	//   - OPERATOR_EMAIL unset (chroot but orgEmail not yet stamped;
	//     bp-catalyst-platform's sovereign-fqdn ConfigMap renders the
	//     key empty until the operator's email is wired — next Pod
	//     restart picks it up once the overlay writer commits).
	//
	// Per docs/INVIOLABLE-PRINCIPLES.md #10 the email is logged at
	// Info; nothing else (no JWTs, no secrets) is logged.
	go runBakeTimeOwnerSeed(context.Background(), log, homeDyn)

	// Issue #2470 — periodic janitor that purges abandoned wreckage from
	// the catalyst-api-deployments PVC. Without this, failed deployments
	// leave kubeconfigs + tofu workdirs + records on disk, which causes
	// the mothership's helmwatch.Bridge to keep presenting OLD CA-signed
	// client certs to (potentially-recycled-IP) Sovereign apiservers,
	// flooding them with x509 cert-auth errors and breaking watch event
	// delivery cluster-wide. Full RCA in handler/janitor.go header +
	// session-2026-05-27 record.
	tofuWorkDir := env("CATALYST_TOFU_WORKDIR", "/var/lib/catalyst/tofu")
	h.StartJanitor(context.Background(), tofuWorkDir)

	// #4635 — level-triggered sovereignty-cutover reconciler. Edge-triggered
	// fires (the cutover Helm hook + the handover-seal path) are fire-once; if
	// one loses its timing race or the engine wedges, nothing re-fires it and
	// the Sovereign sits sealed-but-uncut. This reconciler idempotently keeps
	// the engine running while the handover seal exists and the durable
	// cutover-complete seal does not — no give-up deadline. Dormant on
	// operator-gated Sovereigns (fireCutoverOnHandover=false, #4061).
	h.StartCutoverReconciler(context.Background())

	// Issue #1753 (slice G3b) — bp-mimir (slot 23) query-frontend URL
	// for the Pod metrics sparkline. Per docs/INVIOLABLE-PRINCIPLES.md
	// #4 the URL is env-overridable; empty disables the mimir path and
	// the Pod metrics handler falls back to the metrics-server single
	// point so clusters without bp-mimir still render (degraded).
	mimirURL := env("CATALYST_MIMIR_URL", "http://mimir-query-frontend.mimir.svc.cluster.local:8080")
	h.SetMimirURL(mimirURL)
	log.Info("mimir: query-frontend url wired",
		"url", mimirURL,
	)

	// EPIC-2 #1097 slice T+O+P — Blueprint publishing + Curate.
	// Per ADR-0001 §4.3 Gitea is the source-of-truth for Blueprints;
	// the publish + curate handlers are thin wrappers over the unified
	// Gitea client. Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL +
	// token are env-overridable. When either is unset (CI without a
	// Gitea backend) the wired client stays nil and the handlers
	// surface 503 ("gitea-not-wired") rather than panic.
	if gc := handler.NewGiteaClientFromEnv(); gc != nil {
		h.SetGiteaClient(gc)
		log.Info("gitea: client wired",
			"url", env("CATALYST_GITEA_URL", ""),
		)
	} else {
		log.Info("gitea: client not wired (CATALYST_GITEA_URL or CATALYST_GITEA_TOKEN unset); /blueprints/* will return 503")
	}

	// ── G117.3 (#2742) — endpoint read + Application-instance wiring.
	// Wires the EndpointPrecheckDeps bundle the endpoint_handler.go
	// handlers consume.
	// G117.3d #2780 — RegionsCounter wires Sovereign.spec.regions as the
	// truth-source for multi-region topology selection. Falls back to
	// SOVEREIGN_REGIONS env when the dynamic client is unwired or the
	// Sovereign CRD is absent (chroot dev surface).
	var regionsCounter func(ctx context.Context) (int, error)
	if dynCfg, derr := rest.InClusterConfig(); derr == nil {
		if dynClient, dcerr := dynamic.NewForConfig(dynCfg); dcerr == nil {
			regionsCounter = func(ctx context.Context) (int, error) {
				return handler.CountSovereignRegions(ctx, dynClient)
			}
		}
	}
	h.SetEndpointPrecheckDeps(handler.EndpointPrecheckDeps{
		SovereignFQDN:  env("SOVEREIGN_FQDN", ""),
		RegionsCounter: regionsCounter,
	})
	log.Info("g117.3: endpoint deps wired",
		"sovereignFQDN", env("SOVEREIGN_FQDN", ""),
		"regionsCounter", regionsCounter != nil,
	)

	// EPIC-3 #1098 slice U8 — RBAC audit Bus. Owns:
	//   • the in-process ring buffer the GET /audit/rbac listing reads
	//   • the SSE fan-out the GET /audit/rbac/stream subscribes to
	//   • optional cross-process forwarding to NATS catalyst.audit
	//
	// Per ADR-0001 §3 the canonical audit transport is the
	// `catalyst.audit` JetStream subject; the Bus mirrors locally so
	// the audit-trail UI works even when NATS is briefly unreachable.
	// Per docs/INVIOLABLE-PRINCIPLES.md #4 the ring capacity is
	// env-overridable (CATALYST_AUDIT_RING_CAPACITY); default 1000.
	auditRingCap := envInt("CATALYST_AUDIT_RING_CAPACITY", audit.DefaultRingCapacity)
	auditBus := audit.NewBus(audit.BusConfig{
		RingCapacity: auditRingCap,
		Publisher:    newRBACAuditPublisherFromEnv(log),
	})
	h.SetAuditBus(auditBus)
	log.Info("audit: bus wired", "ringCapacity", auditRingCap)

	// CATALYST_ORG_JWT_SECRET — bridge secret for /api/v1/org/* proxies
	// (PR #1625 follow-up). The chart's api-deployment.yaml feeds this
	// via secretKeyRef on `org-services-secrets/JWT_SECRET`, mirrored from the
	// `org-services` namespace into `catalyst-system` by emberstack/reflector
	// (annotation block on chart/templates/org-services/org-services-secrets.yaml).
	// proxyOrgVoucher uses these bytes to mint a short-lived HS256
	// token the Organization gateway will accept (the operator's RS256 session
	// is rejected by the gateway's HMAC-only validator).
	//
	// Empty env on a Sovereign without marketplace
	// (ingress.marketplace.enabled=false) — proxyOrgVoucher surfaces
	// 503 `org-jwt-bridge-unwired` so the FE renders an actionable
	// message rather than the silent 401 the pre-bridge state produced.
	if orgSecret := os.Getenv("CATALYST_ORG_JWT_SECRET"); orgSecret != "" {
		h.SetOrgJWTSecret([]byte(orgSecret))
		log.Info("org: HS256 bridge secret wired",
			// NEVER log the secret value (INVIOLABLE-PRINCIPLES.md #10).
			"bytes", len(orgSecret),
		)
	} else {
		log.Info("org: HS256 bridge secret unset — /api/v1/org/* proxies return 503 until org-services-secrets is reflected into catalyst-system")
	}

	// #5352 — pprof diagnostics, gated behind CATALYST_PPROF_ENABLED (default off).
	// catalyst-api OOMs periodically on some Sovereigns (RSS grows to the 4Gi
	// limit → OOMKilled, observed 28× on hw288). pprof isn't exposed, so the
	// unbounded allocation can't be located. Enable this flag TRANSIENTLY
	// (kubectl set env / values override) to capture `go tool pprof .../debug/pprof/heap`,
	// find the growth (k8scache Indexer / SSE fanout buffers / deployment cache),
	// then turn it back off. Do NOT leave enabled in production — it exposes
	// process internals on /debug/pprof.
	pprofEnabled := env("CATALYST_PPROF_ENABLED", "false") == "true"
	if pprofEnabled {
		log.Warn("pprof diagnostics ENABLED at /debug/pprof (CATALYST_PPROF_ENABLED=true) — transient OOM diagnosis only; do not leave on in production")
	}

	// G113 #2725 Option B Step 2 (2026-06-02): OIDC IDP provider routes.
	// Keycloak's identity-broker federates catalyst-api as an external
	// OIDC IdP into the sovereign realm under alias `catalyst-pin`.
	// After PIN-verify drops `catalyst_session`, catalyst-ui redirects
	// the browser through the broker — which performs an OIDC roundtrip
	// against THESE 5 endpoints — and KC drops a realm session cookie
	// on auth.<sov-fqdn>. Every subsequent per-app SSO redirect then
	// succeeds silently. See internal/oidcprovider/provider.go.
	//
	// Wired only when CATALYST_OIDC_PROVIDER_ENABLED=true AND the
	// handoverSigner is loaded AND the catalyst-pin client_secret env
	// is set; otherwise routes are absent and KC's broker call would
	// 404 (the realm-config IDP entry is gated by the same env, so
	// install-order isn't a problem).
	var oidcProv *oidcprovider.Provider
	if os.Getenv("CATALYST_OIDC_PROVIDER_ENABLED") == "true" {
		issuerURL := env("CATALYST_OIDC_PROVIDER_ISSUER_URL", "")
		// G113-followup: chart used to template the issuer from
		// .Values.sovereign.fqdn which is nil (the path doesn't exist
		// in values.yaml — the real Sovereign FQDN arrives via the
		// sovereign-fqdn ConfigMap on the SOVEREIGN_FQDN env). When
		// CATALYST_OIDC_PROVIDER_ISSUER_URL is empty but SOVEREIGN_FQDN
		// is set, derive `https://api.<fqdn>` here so the chart can
		// drop the broken templated value entirely.
		if issuerURL == "" {
			if fqdn := os.Getenv("SOVEREIGN_FQDN"); fqdn != "" {
				issuerURL = "https://api." + fqdn
			}
		}
		clientSecret := os.Getenv("CATALYST_PIN_BROKER_CLIENT_SECRET")
		if signer := h.GetHandoverSigner(); signer != nil && issuerURL != "" && clientSecret != "" {
			oidcProv = &oidcprovider.Provider{
				IssuerURL:                issuerURL,
				ExpectedClientID:         env("CATALYST_PIN_BROKER_CLIENT_ID", "catalyst-pin"),
				ExpectedClientSecret:     clientSecret,
				Signer:                   signer,
				SessionSubjectFromCookie: makeSessionValidator(signer, log),
			}
			log.Info("oidc-provider: routes wired", "issuer", issuerURL)
		} else {
			log.Warn("oidc-provider: env set but signer / issuer / secret missing — routes NOT wired",
				"has_signer", signer != nil, "has_issuer", issuerURL != "", "has_secret", clientSecret != "")
		}
	}

	// Wire the tenant registry — flat-file store at
	// CATALYST_DEPLOYMENTS_DIR/-tenant-registry.json. Per ADR-0001 §6
	// the catalyst-api is the host process for the unified-rbac slice
	// until it is split out as its own deployable unit.
	{
		dir := os.Getenv("CATALYST_DEPLOYMENTS_DIR")
		if dir == "" {
			dir = "/var/lib/catalyst/deployments"
		}
		if reg, err := store.NewTenantRegistry(dir); err != nil {
			log.Warn("tenant-registry: init failed; /tenant/discover will return 503",
				"err", err,
			)
		} else {
			h.SetTenantRegistry(reg)
			log.Info("tenant-registry: loaded",
				"hosts", len(reg.List()),
			)
		}

		// User-provision-state store (ADR-0003 §3.4). Hosted in the
		// same directory; a missing dir is non-fatal — the Organization user
		// endpoints surface 503 instead.
		if ups, err := store.NewUserProvisionStore(dir); err != nil {
			log.Warn("user-provision-store: init failed; /org/users will return 503",
				"err", err,
			)
		} else {
			deps := handler.OrgDeps{
				UserProvisionStore: ups,
				SecretBaseURLTemplate: env(
					"CATALYST_ORG_NEWAPI_BASE_URL_TEMPLATE",
					"https://newapi.{otech_fqdn}",
				),
				OTECHFQDN: os.Getenv("CATALYST_OTECH_FQDN"),
			}
			// NewAPI admin client — wired only when both env vars are
			// present (the bp-newapi blueprint provisions the
			// catalyst-newapi-admin-token ExternalSecret per #799).
			//
			// Default URL fixed in TBD-V15 / #2021 (2026-05-20): pre-fix
			// default `http://newapi.newapi.svc` was NXDOMAIN — same
			// root cause as TBD-V14 / #2017 (bp-newapi.fullname helper
			// renders `<Release.Name>-<Chart.Name>` = `newapi-bp-newapi`
			// when releaseName=newapi against chart=bp-newapi per
			// bootstrap-kit slot 80). Canonical in-cluster URL is
			// `http://newapi-bp-newapi.newapi.svc.cluster.local:3000`.
			// The bp-catalyst-platform chart now also exports
			// CATALYST_NEWAPI_ADDR explicitly so the literal lives in
			// values rather than this code default (belt-and-braces).
			if addr := env("CATALYST_NEWAPI_ADDR", "http://newapi-bp-newapi.newapi.svc.cluster.local:3000"); addr != "" {
				if token := os.Getenv("CATALYST_NEWAPI_ADMIN_TOKEN"); token != "" {
					deps.NewAPIClient = newapi.New(addr, token)
					log.Info("org-users: NewAPI admin client wired", "addr", addr)
				} else {
					log.Info("org-users: CATALYST_NEWAPI_ADMIN_TOKEN unset; NewAPI hook step 2 will fail-closed")
				}
			}
			// Organization realm Keycloak client — uses the SA token from the
			// existing bp-keycloak ExternalSecret pipeline.
			if saToken := os.Getenv("CATALYST_ORG_KC_SA_TOKEN"); saToken != "" {
				deps.KeycloakClient = handler.OrgKeycloakDirectClient{SAToken: saToken}
				log.Info("org-users: Organization realm Keycloak client wired")
			}
			// K8s Secret applier — uses the catalyst-api Pod's own
			// in-cluster client. main.go already builds homeCore above.
			if homeCore != nil {
				deps.SecretApplier = handler.K8sSecretApplier{Client: homeCore}
				log.Info("org-users: K8s SSA Secret applier wired")
			}
			h.SetOrgDeps(deps)
		}

		// Organization tenant provisioning pipeline (issue #804) — same
		// directory as the user-provision store. Wires the GitOps
		// overlay writer (uses CATALYST_GITOPS_* env), DNS provisioner
		// (PowerDNS for free-subdomain, net.LookupCNAME for BYO), and
		// the chart-bootstrap-aware Keycloak client verifier.
		if orgTenantStore, err := store.NewOrganizationProvisionStore(dir); err != nil {
			log.Warn("org-tenant-store: init failed; /org/tenants will return 503",
				"err", err,
			)
		} else {
			tdeps := handler.OrganizationDeps{
				Store:            orgTenantStore,
				TenantRegistry:   nil, // overwritten below from h.tenantRegistry
				OTECHFQDN:        os.Getenv("CATALYST_OTECH_FQDN"),
				OTECHIngressIPv4: os.Getenv("CATALYST_OTECH_INGRESS_IPV4"),
				// Multi-domain Sovereign pool (epic #825 / MD-3 #828).
				// Sourced from CATALYST_ORG_POOL_DOMAINS env (stub) until
				// MD-1 (#826) lands the Deployment.ParentDomains[] field
				// — at which point this wiring switches to read from the
				// deployment record. The data shape is forward-compatible.
				ParentDomains: handler.LoadOrganizationParentDomainsFromEnv(),
				MaxRetryCount: 5,
			}
			// GitOps overlay writer — chart versions read from env
			// per Inviolable Principle 4. Empty values fall back to "*".
			tdeps.GitOps = handler.DefaultOrganizationGitOpsWriter{
				Log: log,
				ChartVersions: handler.OrganizationChartVersions{
					Keycloak:  os.Getenv("CATALYST_ORG_BP_KEYCLOAK_VER"),
					CNPG:      os.Getenv("CATALYST_ORG_BP_CNPG_VER"),
					WordPress: os.Getenv("CATALYST_ORG_BP_WORDPRESS_VER"),
					OpenClaw:  os.Getenv("CATALYST_ORG_BP_OPENCLAW_VER"),
					Stalwart:  os.Getenv("CATALYST_ORG_BP_STALWART_VER"),
					NewAPI:    os.Getenv("CATALYST_ORG_BP_NEWAPI_VER"),
					Agenity:   os.Getenv("CATALYST_ORG_BP_AGENITY_VER"),
				},
			}
			// DNS provisioner — wraps PowerDNS for free-subdomain
			// writes; falls back to a no-op when env unset.
			//
			// #4215: read CATALYST_POWERDNS_API_URL — the canonical name
			// used by every other PowerDNS reader (main.go:353,
			// parent_domains.go, the chart api-deployment templates) and
			// the only name the deployment actually sets. The legacy
			// CATALYST_POWERDNS_URL was never wired, so this org-tenant DNS
			// provisioner was permanently no-op'd → new Orgs never got their
			// console.<slug>.<pool> A-record → ERR_NAME_NOT_RESOLVED on the
			// post-signup redirect (the #4179 funnel landing).
			pdnsURL := os.Getenv("CATALYST_POWERDNS_API_URL")
			pdnsKey := os.Getenv("CATALYST_POWERDNS_API_KEY")
			localWriter := handler.NewPowerDNSWriter(pdnsURL, pdnsKey)
			// #4218: pool-domain (omani.*) console A-records must be
			// written to the CENTRAL authoritative PowerDNS (pdns.openova.io)
			// with the CENTRAL pool key — NOT the Sovereign-local PowerDNS,
			// which hosts only the Sovereign's own FQDN zone and has no
			// omani.works zone (PATCH 404). On a production Sovereign the
			// marketplace runs on the Sovereign domain while Orgs provision
			// on separate omani.* pool domains, so the free-subdomain write
			// (console.<slug>.<pool>) targets the central server. The pool
			// env pair is independent of the local CATALYST_POWERDNS_API_*
			// (which stays pointed at the local server for the #827/#829
			// multi-zone admin flow). When the pool env is unset (a
			// single-PowerDNS Sovereign where the pool IS the local zone)
			// the provisioner falls back to localWriter automatically.
			poolURL := os.Getenv("CATALYST_POOL_POWERDNS_API_URL")
			poolKey := os.Getenv("CATALYST_POOL_POWERDNS_API_KEY")
			poolWriter := handler.NewPowerDNSWriter(poolURL, poolKey)
			if localWriter != nil || poolWriter != nil {
				tdeps.DNS = handler.DefaultOrganizationDNSProvisioner{
					Writer:     localWriter,
					PoolWriter: poolWriter,
				}
				switch {
				case poolWriter != nil:
					log.Info("org-tenant: powerdns writer wired (pool-domain free-subdomain writes target central pdns)",
						"local_url", pdnsURL, "pool_url", poolURL)
				default:
					log.Info("org-tenant: powerdns writer wired (local pdns only; no pool env — single-PowerDNS Sovereign)",
						"local_url", pdnsURL)
				}
			} else {
				tdeps.DNS = handler.NoopOrganizationDNSProvisioner{}
				log.Info("org-tenant: powerdns env unset; using no-op DNS provisioner")
			}
			// Keycloak client verifier — uses the same SA token as the
			// user-create hook (CATALYST_ORG_KC_SA_TOKEN).
			tdeps.KeycloakClients = handler.ChartBootstrapKeycloakProvisioner{
				Log:     log,
				SAToken: os.Getenv("CATALYST_ORG_KC_SA_TOKEN"),
			}
			// Pull the tenant registry the Organization-user wiring just set so
			// the pipeline can register console.<host> on completion.
			h.SetOrganizationDeps(tdeps)
			// Re-wire registry now that the Handler has it.
			if reg, err := store.NewTenantRegistry(dir); err == nil {
				tdeps.TenantRegistry = reg
				h.SetTenantRegistry(reg)
				h.SetOrganizationDeps(tdeps)
			}
			log.Info("org-tenant: pipeline wired",
				"otech_fqdn", tdeps.OTECHFQDN,
				"max_retry", tdeps.MaxRetryCount,
			)
		}
	}

	// #4179 (criterion 4d) — reconcile the tenant registry from Organization
	// CRs so a MARKETPLACE-funnel-created Org's console host
	// (console.<slug>.<parentDomain>) is a registered tenant_kind=org host.
	// Without this row, /auth/org-handover → resolveOrgScope refuses the
	// session with `invalid audience: not an org console host` and the
	// customer never lands signed-in. The BSS pipeline writes this row at
	// Step 6, but the marketplace funnel (tenant-service → provisioning-service
	// → Org CR) never runs that pipeline — so catalyst-api reconciles its own
	// registry FROM the Org CRs (the one object both doors create), covering
	// both doors with a single source of truth (same consolidation as #4236
	// DNS + #4242 TLS). Async + best-effort: never blocks readiness, retries
	// on a ticker, and a no-op when the registry/dynamic-client isn't wired.
	go h.ReconcileTenantRegistryFromOrgCRs(context.Background())

	// #5635 — the SAME consolidation for the per-Org console GATEWAY surface
	// (wildcard Certificate + console-https-<slug>/console-http-<slug>
	// listener pair + the console HTTPRoute). That surface also has two
	// producers, and only the BSS door (provisionOrgConsoleTLS) fans it out to
	// every region — the marketplace-funnel Org's surface is written by the
	// org-controller, which has no per-region client and never converges the
	// second region. One shared EIP round-robins every region's envoy, so a
	// host only one region can serve resets ~1/N of all customer connections
	// (the split-write class #5394/#5406/#5414/#5416/#5511). This loop lists
	// the Organization CRs — the one object BOTH doors create — and re-runs
	// the multi-region emitter for each, making the fan-out unconditional
	// (creation-path independent) and reconciled (an Org missing from a region
	// converges rather than staying however it was first written). Every write
	// is idempotent, so a healthy Sovereign sees zero object churn.
	go h.ReconcileOrgConsoleTLSFromOrgCRs(context.Background())

	// #6027 — the CREDENTIAL half of the loop above. Both that emitter and the
	// organization-controller can only write into a peer region if a kubeconfig
	// for it exists in-cluster, as
	// `<cutover-ns>/cutover-secondary-kubeconfigs`. Until this loop existed the
	// only thing that ever wrote that Secret was runCutover — one call site, in
	// one file — so a 2-region Sovereign that had not run the operator-gated
	// self-sovereignty cutover held no credential for its own peer region, and
	// every per-Org console host answered only the share of the shared EIP's
	// round-robin that landed in region A. Measured on hw293: the Secret
	// NotFound in both regions, region B carrying zero per-Org listeners
	// against five pairs in region A. Same shape as #6015 one layer down, and
	// the same remedy: give the step its own level-triggered owner instead of
	// hanging it off an unrelated milestone. Chroot-only, no-op on a
	// single-region Sovereign, and idempotent to the point of writing nothing
	// once converged.
	go h.RunSecondaryKubeconfigSecretMaterializer(context.Background())

	// #4877 — level-triggered self-heal for the three OpenBao seed seams
	// (newapi-admin-token / anthropic / per-Org mcp-bearer). Those seeds fire
	// from EXACTLY ONE place (runOrganizationPipeline, once per HTTP Org-create,
	// no retry). If that single write misses — catalyst-api OOM mid-pipeline
	// (#4811) or a prereq not yet reflected — the OpenBao paths are never
	// written and the ExternalSecrets reading them stay SecretSyncedError
	// forever (unified-rbac 401s; agenity's agent gets no Anthropic cred / no
	// Catalyst MCP bearer). This reconciler re-runs the seeds on startup + a
	// periodic tick, seeding ONLY paths that are absent/empty (read-before-write
	// → zero churn on healthy paths). Idempotent + non-fatal; dormant on the
	// orchestrator (nil OpenBao) and behind CATALYST_SEED_RECONCILER_ENABLED.
	// Wired here, after SetOpenBao (#189) + SetHandoverSigner (#233) +
	// SetOrganizationDeps (all seed prerequisites) are in place.
	h.StartSovereignSeedReconciler(context.Background())

	// The routes are registered in routes.go and the routes_<concern>.go files.
	r := buildRouter(routeDeps{
		corsOrigin:   corsOrigin,
		log:          log,
		h:            h,
		pprofEnabled: pprofEnabled,
		oidcProv:     oidcProv,
	})

	// TBD-V13 (issue #2016) — on-startup cutover resume.
	//
	// The Self-Sovereignty Cutover engine runs as an in-process goroutine
	// spawned by HandleCutoverStart / HandleCutoverInternalTrigger. If
	// catalyst-api restarts mid-cutover (Pod evict, OOM, image bump), the
	// goroutine dies; the durable status ConfigMap records the in-flight
	// state but nothing auto-fires the engine on the fresh Pod. The
	// chart's Helm auto-trigger Job only runs on post-install/post-upgrade
	// hooks — it does NOT re-fire on a catalyst-api restart after the
	// chart is already installed.
	//
	// ResumeInterruptedCutover reads the status ConfigMap and, if a
	// cutover is in-flight, resets the in-flight step row and spawns
	// runCutover again. It is a no-op when the cutover is complete,
	// never started, or the chart is not installed.
	//
	// Wired BEFORE ListenAndServe so a startup-resume race against a
	// stale auto-trigger Job retry hitting the HTTP edge is serialised
	// through the in-process running flag (tryStartRun).
	h.ResumeInterruptedCutover(ctx)

	// #5642 — always-on, loopback-ONLY pprof. catalyst-api OOMKills on a
	// ~60-minute metronome on hw292 and the retaining allocation cannot be
	// named without a heap profile of the LEAKING process. The pre-existing
	// CATALYST_PPROF_ENABLED flag above cannot supply one: it is read at
	// process start, so turning it on rolls the Pod and destroys the heap
	// that was about to be profiled. This listener is up from boot, is a
	// separate mux on its own listener (unreachable through the public
	// router on :8080), and binds 127.0.0.1 — no Service port, no ingress,
	// no NodePort. `kubectl port-forward` into the Pod's netns is the only
	// way in. See cmd/api/pprof_loopback.go for the full rationale.
	if ln, pprofErr := startLoopbackPprof(log, env("CATALYST_PPROF_LOOPBACK_ADDR", defaultPprofLoopbackAddr)); pprofErr != nil {
		log.Warn("pprof loopback listener not started — heap profiles unavailable on this Pod (#5642)", "err", pprofErr)
	} else if ln != nil {
		log.Info("pprof loopback listener ready — capture with: kubectl -n catalyst-system port-forward pod/$POD 6060:6060 (#5642)",
			"addr", ln.Addr().String())
	}

	// #5767 — graceful shutdown. This was a bare http.ListenAndServe, so
	// SIGTERM killed the process where it stood. That is not a cosmetic
	// gap: restoreFromStore's orphan-release goroutine calls pdm.Release,
	// THEN clears the dep's PDM pointers, THEN persists. Killed between the
	// first and last step it leaves an on-disk record still claiming a
	// reservation that no longer exists in PDM — the exact #489
	// "every Pod restart permanently locks a subdomain" symptom that
	// goroutine exists to prevent. A rolling restart is the most common
	// event in this platform's life, so the fix for #489 was itself
	// killable by the routine case.
	//
	// Order matters: Shutdown FIRST (stop accepting, drain in-flight
	// requests), then join the orphan releases. Draining first would let a
	// new request spawn work after we had already waited.
	srv := &http.Server{Addr: ":" + port, Handler: r}
	drained := make(chan struct{})

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		defer close(drained)
		drainOnSignal(sigCh, srv, h, defaultDrainBudget, log)
	}()

	log.Info("catalyst api listening", "port", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
	// ErrServerClosed is the normal path out of a signalled shutdown, not a
	// failure — wait for the drain goroutine before returning so the process
	// does not exit while it is still joining.
	<-drained
	log.Info("catalyst api stopped cleanly")
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// pubkeyPublishDecision is the outcome of the guard that decides whether the
// runtime signer's pubkey may overwrite the catalyst-handover-jwt-public
// Secret. Pure + table-testable (no cluster) — see main_test.go.
type pubkeyPublishDecision int

const (
	// pubkeyPublishWrite — the Secret's current value differs from ours and it
	// is safe to overwrite (mothership / Catalyst-Zero, or the Sovereign Secret
	// is empty/absent so there is no injected key to clobber).
	pubkeyPublishWrite pubkeyPublishDecision = iota
	// pubkeyPublishAlreadyCurrent — the Secret already carries our exact JWK.
	pubkeyPublishAlreadyCurrent
	// pubkeyPublishPreserveInjected — this is a Sovereign and the Secret already
	// holds a DIFFERENT key (the mothership-injected inbound-handover key).
	// Preserve it; publishing our local-signer key here would break owner
	// handover (#4450).
	pubkeyPublishPreserveInjected
)

// decidePubkeyPublish is the pure core of publishHandoverPubkey's write guard
// (#4450). Given the runtime signer's JWK, the Secret's current value (nil when
// the key is absent / Secret missing) and whether this process is a Sovereign
// (SOVEREIGN_FQDN set), it decides whether to overwrite.
//
// Why the Sovereign guard exists (#4450 root cause):
//
//	cloud-init seeds catalyst-handover-jwt-public with the MOTHERSHIP's public
//	key (the key the inbound owner-handover JWT must verify against — read by
//	auth_handover.go). On a Sovereign the local handoverSigner is a SEPARATE,
//	self-generated keypair (the mothership never shares its private key —
//	sovereignty). #4114 added an unconditional per-boot PATCH that published the
//	LOCAL signer's pubkey over the injected mothership key, so every
//	mothership-signed owner handover 401'd "invalid token". The local signer's
//	pubkey already reaches its real consumer (agenity openova-mcp) via openbao
//	(seedMCPBearer → secret/catalyst/agenity/<slug>/mcp-bearer), so the Secret
//	mirror is not needed on a Sovereign — preserve the injected key there.
//
// On the mothership (SOVEREIGN_FQDN empty) the Secret IS the local signer's
// mirror, so the original #4114 self-heal still applies: overwrite when stale.
// handoverSignersJWKSKey is the ADDITIVE key on the same
// catalyst-handover-jwt-public Secret that carries the full SET of RS256 public
// keys able to sign a bearer on this Sovereign, as an RFC 7517 JWKS.
//
// Why a set exists at all (#5175, UAT rows 212/213 on hw293):
//
//	`public.jwk` holds exactly ONE key, and decidePubkeyPublish below
//	guarantees at most one signer's key ever occupies it on a Sovereign — the
//	mothership-injected inbound-handover key is deliberately preserved there
//	(#4450). But that Secret is also what off-pod verifiers read, and every
//	bearer a caller actually presents is signed by a catalyst-api's OWN
//	handoverjwt signer (HandlePinVerify / org_handover / org_enter_org /
//	seedMCPBearer all route through SignCustomClaims). Those signers are
//	self-generated by LoadOrGenerate from a key file on a per-Pod RWO PVC, so a
//	multi-region Sovereign has one per catalyst-api and none of them can take
//	`public.jwk` without breaking owner handover.
//
//	catalyst-api itself is immune because auth_handover.go verifies against a
//	CANDIDATE list — the mounted key, then its own (#4101, added after a single
//	pinned key rejected every Enter-org token with "crypto/rsa: verification
//	error"). Off-pod verifiers had no equivalent, so bp-openova-mcp refused
//	genuine Org-scoped bearers 3/3 with that same error while catalyst-api
//	answered 200 for the byte-identical string.
//
// This key is ADDITIVE and APPEND-ONLY-ish: it never touches `public.jwk`, and
// each catalyst-api Pod adds its own key if absent. Nothing that reads
// `public.jwk` changes behaviour.
const handoverSignersJWKSKey = "signers.jwks"

// maxPublishedSigners bounds the JWKS so repeated key regeneration (a wiped
// PVC, a re-provisioned region) cannot grow the Secret without limit. Oldest
// entries are dropped first; the set is ordered oldest→newest.
const maxPublishedSigners = 8

// mergeSignerJWKS folds `add` (one RSA JWK, as produced by
// handoverjwt.Signer.PublicJWK) into `existing` (a JWKS document, possibly
// empty/absent/garbage) and reports whether the document changed.
//
// Pure + table-testable, no cluster. Identity is the (n, e) pair, which is the
// key itself — `use`/`alg`/`kid` differences never create a duplicate entry.
// An unparseable `existing` is REPLACED rather than propagated: a corrupt
// mirror must not permanently wedge publication.
func mergeSignerJWKS(existing, add []byte) (updated []byte, changed bool, err error) {
	var addKey map[string]any
	if uErr := json.Unmarshal(add, &addKey); uErr != nil {
		return nil, false, fmt.Errorf("signers.jwks: added key is not JSON: %w", uErr)
	}
	n, _ := addKey["n"].(string)
	e, _ := addKey["e"].(string)
	if n == "" || e == "" {
		return nil, false, errors.New("signers.jwks: added key has no n/e")
	}

	var doc struct {
		Keys []map[string]any `json:"keys"`
	}
	if len(existing) > 0 {
		// A parse failure leaves doc.Keys nil — start clean rather than fail.
		_ = json.Unmarshal(existing, &doc)
	}
	for _, k := range doc.Keys {
		kn, _ := k["n"].(string)
		ke, _ := k["e"].(string)
		if kn == n && ke == e {
			return existing, false, nil
		}
	}
	doc.Keys = append(doc.Keys, addKey)
	if len(doc.Keys) > maxPublishedSigners {
		doc.Keys = doc.Keys[len(doc.Keys)-maxPublishedSigners:]
	}
	out, mErr := json.Marshal(doc)
	if mErr != nil {
		return nil, false, fmt.Errorf("signers.jwks: marshal: %w", mErr)
	}
	return out, true, nil
}

func decidePubkeyPublish(pubJWK, current []byte, isSovereign bool) pubkeyPublishDecision {
	if current != nil && string(current) == string(pubJWK) {
		return pubkeyPublishAlreadyCurrent
	}
	// A non-empty, DIFFERENT existing value on a Sovereign is the
	// mothership-injected inbound-handover key — never clobber it.
	if isSovereign && len(current) > 0 {
		return pubkeyPublishPreserveInjected
	}
	return pubkeyPublishWrite
}

// publishHandoverPubkey PATCHes the catalyst-handover-jwt-public Secret's
// public.jwk with the runtime signer's ACTUAL public JWK so off-pod
// consumers verify session bearers against the same key catalyst-api signs
// with (#4114). Best-effort: a failure (no in-cluster config, RBAC denied,
// Secret absent) is logged, never fatal — the handover signer still works
// in-process; only the published mirror lags. The Secret name/namespace are
// the chart defaults, overridable via env for non-standard installs.
//
// #4450: on a Sovereign the Secret holds the MOTHERSHIP-injected public key
// (the inbound owner-handover verification key), NOT a mirror of the local
// signer — so the write is gated by decidePubkeyPublish to preserve it. See
// that function's doc for the full root-cause.
func publishHandoverPubkey(ctx context.Context, log *slog.Logger, pubJWK []byte) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Warn("handoverjwt: no in-cluster config; skipping pubkey publish (#4114)", "err", err)
		return
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Warn("handoverjwt: clientset build failed; skipping pubkey publish (#4114)", "err", err)
		return
	}
	ns := env("CATALYST_HANDOVER_PUBKEY_SECRET_NAMESPACE", "")
	if ns == "" {
		if b, rErr := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); rErr == nil {
			ns = strings.TrimSpace(string(b))
		}
	}
	if ns == "" {
		ns = "catalyst-system"
	}
	name := env("CATALYST_HANDOVER_PUBKEY_SECRET_NAME", "catalyst-handover-jwt-public")
	key := env("CATALYST_HANDOVER_PUBKEY_SECRET_KEY", "public.jwk")

	// A Sovereign always carries SOVEREIGN_FQDN; the mothership / Catalyst-Zero
	// leaves it empty (see api-deployment.yaml line ~798).
	isSovereign := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != ""

	var current []byte
	var currentJWKS []byte
	if cur, getErr := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); getErr == nil {
		if existing, ok := cur.Data[key]; ok {
			current = existing
		}
		if existing, ok := cur.Data[handoverSignersJWKSKey]; ok {
			currentJWKS = existing
		}
	}

	// Publish the SIGNER SET first, and unconditionally — it is additive and
	// its correctness does not depend on which way the `public.jwk` decision
	// below goes. See handoverSignersJWKSKey for why one key is not enough.
	//
	// The set is seeded with whatever `public.jwk` currently holds (on a
	// Sovereign that is the preserved mothership-injected key, #4450) so the
	// document is COMPLETE from the first boot rather than describing only the
	// Pods that have restarted since this shipped.
	base := currentJWKS
	if len(current) > 0 {
		if seeded, seededChanged, sErr := mergeSignerJWKS(base, current); sErr == nil && seededChanged {
			base = seeded
		}
	}
	if merged, changed, mErr := mergeSignerJWKS(base, pubJWK); mErr != nil {
		log.Warn("handoverjwt: could not build the published signer set; off-pod verifiers keep the single-key view (#5175)",
			"secret", ns+"/"+name, "err", mErr)
	} else if changed || !bytes.Equal(base, currentJWKS) {
		patch := fmt.Sprintf(`{"stringData":{%q:%q}}`, handoverSignersJWKSKey, string(merged))
		if _, pErr := cs.CoreV1().Secrets(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); pErr != nil {
			log.Warn("handoverjwt: publish signer-set PATCH failed; off-pod verifiers may refuse bearers this Pod signs (#5175)",
				"secret", ns+"/"+name+"#"+handoverSignersJWKSKey, "err", pErr)
		} else {
			log.Info("handoverjwt: published this signer into the off-pod verify set (#5175)",
				"secret", ns+"/"+name+"#"+handoverSignersJWKSKey)
		}
	}

	switch decidePubkeyPublish(pubJWK, current, isSovereign) {
	case pubkeyPublishAlreadyCurrent:
		// Skip the write — avoids a needless write + Reloader bounce on every restart.
		log.Info("handoverjwt: published pubkey already current (#4114)", "secret", ns+"/"+name)
		return
	case pubkeyPublishPreserveInjected:
		// #4450: preserve the mothership-injected inbound-handover key on a
		// Sovereign — overwriting it 401's every owner-handover JWT. The local
		// signer's pubkey reaches openova-mcp via openbao (seedMCPBearer), not
		// this Secret, so no consumer is regressed.
		log.Info("handoverjwt: preserving mothership-injected handover pubkey; not publishing local signer key (#4450)",
			"secret", ns+"/"+name)
		return
	}

	// strategic-merge patch on the stringData field — server base64-encodes it.
	patch := fmt.Sprintf(`{"stringData":{%q:%q}}`, key, string(pubJWK))
	if _, pErr := cs.CoreV1().Secrets(ns).Patch(ctx, name, types.StrategicMergePatchType, []byte(patch), metav1.PatchOptions{}); pErr != nil {
		log.Warn("handoverjwt: publish pubkey PATCH failed; consumers may verify against a stale key (#4114)",
			"secret", ns+"/"+name, "err", pErr)
		return
	}
	log.Info("handoverjwt: published runtime signer pubkey to Secret (#4114)", "secret", ns+"/"+name)
}

// disableWatchListClient turns OFF the client-go WatchListClient feature gate
// so every reflector built by this process uses the classic LIST+WATCH path
// instead of the streaming watch-list (#4071 — see the call site in main for
// the full root-cause analysis).
//
// The default client-go feature-gate implementation (k8s.io/client-go/features
// FeatureGates()) is an *envVarFeatureGates, whose concrete type carries a
// `Set(Feature, bool) error` method that takes precedence over both the
// built-in default and the KUBE_FEATURE_* env var. We type-assert to that
// method set (the exported Gates interface only declares Enabled) and Set the
// gate to false. The Set MUST happen before the first Enabled() read — i.e.
// before any client/informer is constructed — because the env-var snapshot is
// loaded exactly once on the first Enabled() call. main calls this as its first
// real action, so the ordering holds.
//
// If the assertion ever fails (a future client-go swaps the default gate
// implementation), fall back to the supported env-var mechanism so the gate is
// still disabled; both are honoured by client-go's own resolution order.
func disableWatchListClient(log *slog.Logger) {
	type settableGates interface {
		Set(clientgofeatures.Feature, bool) error
	}
	if g, ok := clientgofeatures.FeatureGates().(settableGates); ok {
		if err := g.Set(clientgofeatures.WatchListClient, false); err != nil {
			log.Warn("client-go: failed to disable WatchListClient via Set; falling back to env var",
				"err", err)
			_ = os.Setenv("KUBE_FEATURE_WatchListClient", "false")
			return
		}
		log.Info("client-go: WatchListClient feature gate disabled (reflectors use classic LIST+WATCH) — #4071")
		return
	}
	// Default gate implementation not settable — use the env-var mechanism,
	// which client-go reads on the first Enabled() call.
	_ = os.Setenv("KUBE_FEATURE_WatchListClient", "false")
	log.Info("client-go: WatchListClient disabled via KUBE_FEATURE_WatchListClient env var — #4071")
}

// envInt returns the parsed integer value of an env var, or fallback
// when unset / unparseable / non-positive. Used for bounded numeric
// runtime knobs (ring sizes, page limits, etc.) where a non-positive
// or unparseable value is always wrong.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := fmt.Sscanf(v, "%d", new(int))
	if err != nil || n != 1 {
		return fallback
	}
	// Re-parse via Sscanf into a real int (Sscanf above only verified
	// parse-ability with a throwaway target).
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}

// newRBACAuditPublisherFromEnv constructs the cross-process forwarder
// for the RBAC audit Bus when CATALYST_NATS_URL is set. Currently
// returns nil (no Publisher) because catalyst-api ships without the
// `nats.go` SDK in its go.mod (mirrors the
// `newCompliancePolicyRollupPublisherFromEnv` pattern). Production
// adoption of NATS forwarding lands in a follow-up slice that imports
// `nats.go` directly. Until then, the Bus runs in in-process-only
// mode: ring buffer + SSE work normally, but events are not persisted
// across catalyst-api Pod restarts.
//
// Per ADR-0001 §3 the audit subject is `catalyst.audit`. Per
// docs/INVIOLABLE-PRINCIPLES.md #4 the URL is env-driven.
func newRBACAuditPublisherFromEnv(log *slog.Logger) audit.Publisher {
	url := os.Getenv("CATALYST_NATS_URL")
	if url == "" {
		return nil
	}
	subject := env("CATALYST_AUDIT_NATS_SUBJECT", "catalyst.audit")
	log.Info("audit: NATS publisher placeholder wired (forwarding will land in follow-up slice)",
		"url", url, "subject", subject,
	)
	// Returning nil keeps the bus in in-process-only mode until the
	// nats.go-importing follow-up slice replaces this stub with a
	// real publisher. The wiring contract (env vars + Bus.Publisher
	// field) is intact.
	return nil
}

// envBool returns the parsed boolean value of an env var, or fallback
// when unset / unparseable. Truthy values: 1, t, T, true, TRUE, True.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	switch v {
	case "1", "t", "T", "true", "TRUE", "True":
		return true
	case "0", "f", "F", "false", "FALSE", "False":
		return false
	default:
		return fallback
	}
}

// runTierRoleBootstrap waits for Keycloak to become reachable, then
// runs EnsureTierRealmRoles with capped backoff. Errors are logged and
// the goroutine exits — the next catalyst-api restart will pick the
// bootstrap up again. Re-runs are idempotent no-ops.
//
// Backoff: 5 attempts at 0s, 5s, 10s, 20s, 40s = ~75s total wall-clock
// at worst. Combined with the 30s Keycloak readiness wait, the bootstrap
// has up to ~105s to converge before the goroutine gives up. The
// Keycloak-config-cli Job (chart-install-time) is the orthogonal
// lifecycle and runs separately.
func runTierRoleBootstrap(ctx context.Context, log *slog.Logger, kc *keycloak.Client, realm string) {
	if err := waitForKeycloak(ctx, kc, 30*time.Second); err != nil {
		log.Error("kc-bootstrap: KC not reachable, skipping tier-role bootstrap",
			"err", err,
		)
		return
	}

	delays := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	var lastErr error
	for i, d := range delays {
		if d > 0 {
			select {
			case <-ctx.Done():
				log.Warn("kc-bootstrap: context cancelled during backoff",
					"attempt", i+1,
				)
				return
			case <-time.After(d):
			}
		}
		err := kc.EnsureTierRealmRoles(ctx, realm)
		if err == nil {
			log.Info("kc-bootstrap: tier-role bootstrap converged",
				"attempt", i+1,
				"realm", realm,
			)
			return
		}
		lastErr = err
		log.Warn("kc-bootstrap: tier-role ensure failed; will retry",
			"attempt", i+1,
			"err", err,
		)
	}
	log.Error("kc-bootstrap: tier-role bootstrap gave up after all retries",
		"realm", realm,
		"err", lastErr,
	)
}

// runBakeTimeOwnerSeed converges D21 (owner UserAccess CR) at bake-time
// when the catalyst-api boots on a chroot Sovereign. See the call site
// in main() above (TBD-A34 / issue #1891) for the full rationale.
//
// Skip conditions:
//   - dyn is nil (out-of-cluster catalyst-api; CI / smoke / local dev).
//   - SOVEREIGN_FQDN env is empty (mother mode; contabo Catalyst-Zero).
//   - OPERATOR_EMAIL env is empty (chroot but orgEmail not yet stamped
//     into the sovereign-fqdn ConfigMap; the next Pod restart picks
//     it up once the orchestrator overlay writer commits).
//
// Backoff: same shape as runTierRoleBootstrap — 0s, 5s, 10s, 20s, 40s.
// The UserAccess CRD comes from bp-crossplane-claims (claim XRD) which
// Flux reconciles in parallel with bp-catalyst-platform; the apiserver
// may return NotFound for several seconds on a freshly-rolled chroot.
// The capped backoff keeps us quiet behind the CRD roll while still
// converging in under ~75 s on the typical case.
//
// Idempotency: EnsureOwnerUserAccess folds AlreadyExists to nil, so
// this is safe to run before, after, or alongside the existing
// auth_handover.go-fired path.
func runBakeTimeOwnerSeed(ctx context.Context, log *slog.Logger, dyn dynamic.Interface) {
	if dyn == nil {
		log.Info("user-access: bake-time owner seed skipped — out-of-cluster (no dynamic client)")
		return
	}
	fqdn := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if fqdn == "" {
		log.Info("user-access: bake-time owner seed skipped — SOVEREIGN_FQDN unset (mother mode)")
		return
	}
	email := strings.TrimSpace(os.Getenv("OPERATOR_EMAIL"))
	if email == "" {
		log.Info("user-access: bake-time owner seed skipped — OPERATOR_EMAIL unset (orgEmail not yet stamped on chroot)",
			"sovereignFQDN", fqdn,
		)
		return
	}

	delays := []time.Duration{0, 5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	var lastErr error
	for i, d := range delays {
		if d > 0 {
			select {
			case <-ctx.Done():
				log.Warn("user-access: bake-time owner seed cancelled during backoff",
					"attempt", i+1,
				)
				return
			case <-time.After(d):
			}
		}
		err := handler.EnsureOwnerUserAccess(ctx, dyn, email, fqdn)
		if err == nil {
			// Per docs/INVIOLABLE-PRINCIPLES.md #10 the email is the
			// only operator-derived value logged here.
			log.Info("user-access: owner auto-seeded at bake-time (TBD-A34)",
				"attempt", i+1,
				"email", email,
				"sovereignFQDN", fqdn,
			)
			return
		}
		lastErr = err
		log.Warn("user-access: bake-time owner seed failed; will retry",
			"attempt", i+1,
			"err", err,
		)
	}
	log.Error("user-access: bake-time owner seed gave up after all retries",
		"email", email,
		"sovereignFQDN", fqdn,
		"err", lastErr,
	)
}

// waitForKeycloak polls the Keycloak service-account token endpoint
// until it returns a token (KC is up) or the timeout elapses. The
// catalyst-api may start before Keycloak's HelmRelease finishes
// reconciling on a fresh Sovereign — this poll keeps the bootstrap
// goroutine quiet until KC is reachable, avoiding a flood of 5xx logs.
func waitForKeycloak(ctx context.Context, kc *keycloak.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := 2 * time.Second
	var lastErr error
	for time.Now().Before(deadline) {
		// EnsureTierRealmRoles is idempotent on a populated realm but
		// expensive on a clean one — for the readiness check we only
		// need a single GET to confirm KC is up. The simplest path is
		// to call ListRealmRoles which exercises serviceAccountToken
		// then a single Admin GET.
		_, err := kc.ListRealmRoles(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(tick):
		}
	}
	if lastErr != nil {
		return fmt.Errorf("keycloak readiness timeout: %w", lastErr)
	}
	return fmt.Errorf("keycloak readiness timeout (no error captured)")
}

// #3985: the legacy `/api/v1/org/*` deprecatedAlias machinery (added by
// #3383 as a one-release bridge, Sunset 01 Jul 2026) has been REMOVED.
// Only the canonical `/api/v1/org/*` + `/api/v1/organizations` routes
// remain; the marketplace SPA + funnel walk now call the canonical paths
// directly.

// pathOnlyLogFormatter implements chi's middleware.LogFormatter so the
// access log line includes r.URL.Path but never the query string. The
// chroot Sovereign Console SPA appends `?access_token=<jwt>` to
// EventSource URLs (see auth/session.go ReadSessionToken) because the
// browser EventSource API cannot carry an Authorization header. Using
// chi's DefaultLogFormatter would emit r.RequestURI verbatim and leak
// the access token to stdout. Credential hygiene per CLAUDE.md §10.
type pathOnlyLogFormatter struct{}

// NewLogEntry captures the per-request fields we want to log AT request
// start time, mirroring chi's DefaultLogFormatter contract but stripping
// the query string entirely.
func (pathOnlyLogFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return &pathOnlyLogEntry{
		method: r.Method,
		path:   r.URL.Path,
		proto:  r.Proto,
		remote: r.RemoteAddr,
		start:  time.Now(),
	}
}

type pathOnlyLogEntry struct {
	method string
	path   string
	proto  string
	remote string
	start  time.Time
}

// Write is invoked by chi.middleware.RequestLogger when the response
// has been fully sent. The function signature matches the LogEntry
// interface exactly — extra args are intentionally discarded.
func (e *pathOnlyLogEntry) Write(status, bytes int, _ http.Header, elapsed time.Duration, _ interface{}) {
	slog.Default().Info("http",
		"method", e.method,
		"path", e.path,
		"proto", e.proto,
		"status", status,
		"bytes", bytes,
		"elapsedMs", elapsed.Milliseconds(),
		"remote", e.remote,
	)
}

// Panic is invoked by chi.middleware.Recoverer when a downstream handler
// panics; the path-only contract still applies.
func (e *pathOnlyLogEntry) Panic(v interface{}, _ []byte) {
	slog.Default().Error("http panic",
		"method", e.method,
		"path", e.path,
		"panic", v,
	)
}

// mustHomeCoreClient returns a typed kubernetes.Interface for the
// catalyst-api's own (home) cluster. Used to read the optional
// kinds-registry ConfigMap. A nil return value disables ConfigMap
// loading — the default kinds registry is sufficient.
//
// In production the catalyst-api Pod runs with a ServiceAccount that
// has `get` on ConfigMaps in the catalyst namespace; out-of-cluster
// (CI, smoke test) the in-cluster config build fails and we return
// nil + a warn log.
func mustHomeCoreClient(log *slog.Logger) kubernetes.Interface {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Info("k8scache: in-cluster config unavailable; kinds ConfigMap loading disabled",
			"err", err,
		)
		return nil
	}
	c, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Warn("k8scache: home core client build failed",
			"err", err,
		)
		return nil
	}
	return c
}

// mustHomeDynamicClient returns a dynamic.Interface for the catalyst-api's
// own (home / chroot) cluster. Used by the catalog cluster-fallback shim
// (qa-loop iter-8 Fix #40) to read in-cluster Blueprint CRs when the
// upstream catalyst-catalog returns 404. Returns nil on out-of-cluster
// (CI / smoke) where in-cluster config is unavailable; the chained
// catalog client degrades to upstream-only behaviour without panic.
func mustHomeDynamicClient(log *slog.Logger) dynamic.Interface {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Info("catalog: in-cluster dynamic client unavailable; cluster fallback disabled",
			"err", err,
		)
		return nil
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Warn("catalog: home dynamic client build failed; cluster fallback disabled",
			"err", err,
		)
		return nil
	}
	return c
}

// newCompliancePolicyRollupPublisherFromEnv returns a NATS JetStream
// KV publisher when CATALYST_NATS_URL is set, OR nil when it's unset
// (best-effort mode — the aggregator publishes to SSE + Prometheus
// only). The catalyst-api ships without a hard NATS dependency in
// the Go module so this slice does not import nats.go directly; the
// production wiring goes through a thin in-binary HTTP shim against
// the NATS REST API. When the env is unset, callers fall back to
// nil and the aggregator runs in best-effort mode.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every URL/credential is env-
// driven. Per ADR-0001 §3 the bucket name is fixed at
// `policy-rollup` (declared via slice H4 in bp-nats-jetstream).
func newCompliancePolicyRollupPublisherFromEnv(log *slog.Logger) handler.PolicyRollupPublisher {
	url := os.Getenv("CATALYST_NATS_URL")
	if url == "" {
		// No NATS wired — that's fine. The aggregator's other
		// outputs (SSE + Prometheus) still work, and an operator
		// can re-roll the Pod once NATS is reachable.
		return nil
	}
	bucket := env("CATALYST_NATS_KV_POLICY_ROLLUP_BUCKET", "policy-rollup")
	source := env("CATALYST_SOURCE_FQDN", env("HOSTNAME", "catalyst-api"))
	// Wave 5.44 (#2251) wired the real NATS JetStream KV publisher via
	// internal/natspub — replacing the prior nil-returning stub. nats.go
	// landed in catalyst-api's go.mod via TBD-D35c PR #1918.
	p, err := natspub.NewComplianceRollupPublisher(url, bucket, source, log,
		handler.IncrementComplianceNATSPublishFailure)
	if err != nil {
		log.Warn("compliance: NATS KV publisher init failed — falling back to best-effort (SSE+Prometheus only)",
			"url", url, "bucket", bucket, "err", err)
		return nil
	}
	log.Info("compliance: NATS KV publisher wired",
		"url", url, "bucket", bucket, "source", source)
	return p
}

// newComplianceEnvironmentPolicyResolverFromEnv builds a dynamic-
// client-backed EnvironmentPolicyResolver against the catalyst-api's
// own cluster (mother) or in-cluster (chroot). Returns nil when no
// in-cluster config is available — callers fall back to default
// equal-weights per the brief.
func newComplianceEnvironmentPolicyResolverFromEnv(log *slog.Logger) handler.EnvironmentPolicyResolver {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Info("compliance: in-cluster config unavailable; EnvironmentPolicy resolver disabled — falling back to default equal-weights")
		return nil
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Warn("compliance: dynamic client build failed; EnvironmentPolicy resolver disabled", "err", err)
		return nil
	}
	r, err := handler.NewDynamicEnvironmentPolicyResolver(dyn)
	if err != nil {
		log.Warn("compliance: resolver init failed", "err", err)
		return nil
	}
	return r
}
