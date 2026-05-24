package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/client-go/dynamic"
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
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/openbao"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/powerdns"
	// Side-effect import: registers every CloudProvider adapter
	// (hetzner + huawei stub) against internal/providers/registry
	// at init() time. Wave 2 — the registrations are passive; no
	// handler yet calls providers.Get(). Wave 3 wires the handlers
	// through the registry once Huawei has a concrete impl.
	_ "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/all"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func main() {
	port := env("PORT", "8080")
	corsOrigin := env("CORS_ORIGIN", "*")

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{corsOrigin},
		// PUT enabled for the cloud-init kubeconfig postback (issue
		// #183) — the new Sovereign's cloud-init writes its own
		// kubeconfig to /api/v1/deployments/{id}/kubeconfig with a
		// bearer token. CORS is irrelevant for that caller (curl
		// from the new VM, not a browser), but enabling PUT here
		// keeps the policy consistent for any future browser-side
		// resume flow that re-uses the same endpoint.
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		// Cookie is required for SameSite=Strict session cookies to be
		// forwarded across the Traefik ingress path (issue #608).
		// X-User-Email + X-User-Sub are injected by the RequireSession
		// middleware into downstream requests.
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization", "Cookie"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	h := handler.New(log)

	// OpenBao client — wired ONLY when CATALYST_OPENBAO_ADDR +
	// CATALYST_OPENBAO_TOKEN are both set. Production catalyst-api Pods
	// running on a Sovereign cluster set both (the new Sovereign IS the
	// handover target — see issue #317). Catalyst-Zero leaves them
	// unset and ReceiveTofuArchive returns 503 ("not handover target")
	// for any misrouted POST.
	if addr := os.Getenv("CATALYST_OPENBAO_ADDR"); addr != "" {
		if token := os.Getenv("CATALYST_OPENBAO_TOKEN"); token != "" {
			h.SetOpenBao(openbao.New(addr, token))
			log.Info("openbao: handover-archive receiver enabled",
				"addr", addr,
			)
		} else {
			log.Warn("openbao: CATALYST_OPENBAO_ADDR set but CATALYST_OPENBAO_TOKEN missing; receiver disabled")
		}
	}

	// Handover JWT signer (issue #605) — RSA-2048 keypair lifecycle.
	// CATALYST_HANDOVER_KEY_PATH must point at a writable path on the PVC;
	// if absent the signer is nil and MintHandoverToken returns 503.
	// Sovereign clusters leave this env unset; Catalyst-Zero sets it.
	if keyPath := os.Getenv("CATALYST_HANDOVER_KEY_PATH"); keyPath != "" {
		pubKeyPath := env("CATALYST_HANDOVER_PUBKEY_PATH", keyPath+".pub.jwk")
		issuer := env("CATALYST_HANDOVER_JWT_ISSUER", "https://console.openova.io")
		signer, err := handoverjwt.LoadOrGenerate(keyPath, pubKeyPath, issuer, 5*time.Minute)
		if err != nil {
			log.Warn("handoverjwt: keypair init failed; MintHandoverToken will return 503",
				"err", err,
			)
		} else {
			h.SetHandoverSigner(signer)
			log.Info("handoverjwt: signer ready", "issuer", issuer)
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
	if envBool("KEYCLOAK_BOOTSTRAP_TIER_ROLES", false) {
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

	// TBD-D35b / #1776 — Sandbox-create NATS publish. Wires a
	// `catalyst.tenant.*` publisher when CATALYST_NATS_URL is set so
	// every Sandbox CR Create leaves a corresponding
	// `catalyst.tenant.sandbox_requested` event on the audit stream.
	// Returns nil today (placeholder mirroring newRBACAuditPublisher
	// FromEnv); the real nats.go-importing publisher lands in the
	// same follow-up slice that swaps the RBAC stub. Nil is safe:
	// the sandbox_sessions handler treats a nil publisher as a no-op.
	h.SetTenantEventPublisher(newTenantEventPublisherFromEnv(log))

	// CATALYST_SME_JWT_SECRET — bridge secret for /api/v1/sme/* proxies
	// (PR #1625 follow-up). The chart's api-deployment.yaml feeds this
	// via secretKeyRef on `sme-secrets/JWT_SECRET`, mirrored from the
	// `sme` namespace into `catalyst-system` by emberstack/reflector
	// (annotation block on chart/templates/sme-services/sme-secrets.yaml).
	// proxySMEVoucher uses these bytes to mint a short-lived HS256
	// token the SME gateway will accept (the operator's RS256 session
	// is rejected by the gateway's HMAC-only validator).
	//
	// Empty env on a Sovereign without marketplace
	// (ingress.marketplace.enabled=false) — proxySMEVoucher surfaces
	// 503 `sme-jwt-bridge-unwired` so the FE renders an actionable
	// message rather than the silent 401 the pre-bridge state produced.
	if smeSecret := os.Getenv("CATALYST_SME_JWT_SECRET"); smeSecret != "" {
		h.SetSMEJWTSecret([]byte(smeSecret))
		log.Info("sme: HS256 bridge secret wired",
			// NEVER log the secret value (INVIOLABLE-PRINCIPLES.md #10).
			"bytes", len(smeSecret),
		)
	} else {
		log.Info("sme: HS256 bridge secret unset — /api/v1/sme/* proxies return 503 until sme-secrets is reflected into catalyst-system")
	}

	// /healthz is LIVENESS — always 200 if the process is up and the
	// HTTP server is serving. /readyz is READINESS — 200 only when
	// the primary Sovereign's informers are synced (or no Sovereigns
	// registered yet). The chart wires livenessProbe → /healthz and
	// readinessProbe → /readyz; failing readiness pulls the Pod out
	// of the Service rotation without restarting it. See the Health
	// + Ready handler comments and issue #530 for the crashloop
	// failure mode this split prevents.
	r.Get("/healthz", h.Health)
	r.Get("/readyz", h.Ready)
	r.Handle("/metrics", promhttp.Handler())

	// /api/v1/version — build identification (git SHA + chart version).
	// Always 200, no auth gate (probe-friendly). Operators + the QA
	// matrix probe this to confirm "what version is live right now"
	// without scraping the Pod spec. See handler/version.go for the
	// truth-resolution rules + wire shape.
	r.Get("/api/v1/version", h.HandleVersion)

	// Unauthenticated auth endpoints — 6-digit PIN issue + verify (#688)
	// and logout. These MUST remain outside the session gate (the user is
	// not yet authenticated when they hit /pin/issue and /pin/verify).
	//
	// PIN auth replaces the magic-link flow rejected by the founder
	// (looks like phishing, 2026-05-03):
	//   POST /api/v1/auth/pin/issue   — generate + email a 6-digit code
	//   POST /api/v1/auth/pin/verify  — validate code + set session cookie
	//
	// The legacy /auth/callback and /auth/magic routes are kept as 302
	// redirects to /login so any cached Keycloak redirect_uri bookmarks
	// or stale magic-link emails degrade gracefully.
	r.Post("/api/v1/auth/pin/issue", h.HandlePinIssue)
	r.Post("/api/v1/auth/pin/verify", h.HandlePinVerify)

	// QA-loop iter-11 Cluster-A: tier-scoped session minting.
	// POST /api/v1/auth/test-session?tier=<viewer|developer|operator|admin|owner>
	// Gated by env CATALYST_TEST_SESSION_ENABLED — returns 404 to
	// the public when unset (production-safe). On QA Sovereigns
	// (chroot, omantel-style) the env is set to "true" so the
	// 5-agent QA executor can mint per-tier session cookies and
	// assert the tier-boundary 403/200 contract on every privileged
	// endpoint. See handler/auth_test_session.go for the rationale.
	r.Post("/api/v1/auth/test-session", h.HandleAuthTestSession)

	// /api/v1/deployments/in-flight-count — public, read-only count of
	// deployments in any Phase-0 in-flight status (pending /
	// provisioning / tofu-applying / flux-bootstrapping). The CI
	// deploy-bot (.github/workflows/catalyst-build.yaml) polls this
	// before pushing a values.yaml image-SHA bump, to avoid rolling the
	// catalyst-api Pod mid-tofu-apply (the OpenTofu workdir lives on a
	// /tmp emptyDir that dies with the Pod, abandoning the prov and
	// leaking Hetzner resources). MUST live outside RequireSession —
	// the deploy-bot has no session cookie and runs from a GHA runner.
	// Same posture as /healthz, /readyz, /api/v1/version. The response
	// is count+IDs only; no FQDNs or owner emails. See handler/
	// deployments_in_flight_count.go for the full rationale and the
	// t13/t17/t21 incident history that motivated this gate.
	r.Get("/api/v1/deployments/in-flight-count", h.InFlightCount)

	// /api/v1/subdomains/check — public, read-only availability query.
	// Same model as a username-availability check on a signup form: an
	// anonymous visitor lands on the wizard's Domain step BEFORE they
	// authenticate (PIN issue happens AFTER they pick a subdomain), so
	// requiring a session cookie here would block the only flow that
	// matters. The handler routes the call to PDM (managed pool) or to
	// a DNS lookup (BYO) — both are read-only with no state change and
	// negligible information disclosure ("is this subdomain taken?"
	// is the same answer DNS itself surfaces to anyone).
	r.Post("/api/v1/subdomains/check", h.CheckSubdomain)

	// Wizard pre-submit credential validators — also pre-auth surfaces.
	// The operator types Hetzner / S3 / Dynadot creds in the wizard
	// BEFORE PIN auth (so a typo surfaces at the prompt, not 5 minutes
	// into `tofu apply`). Each endpoint is read-only — it probes the
	// credential against the upstream API and returns 200/400. No state
	// change on success. Same pre-auth treatment as /subdomains/check.
	r.Post("/api/v1/credentials/validate", h.ValidateCredentials)
	r.Post("/api/v1/credentials/object-storage/validate", h.ValidateObjectStorageCredentials)
	r.Post("/api/v1/sshkey/generate", h.GenerateSSHKey)
	r.Post("/api/v1/registrar/{registrar}/validate", h.ValidateRegistrar)
	r.Get("/api/v1/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?error=flow_changed", http.StatusFound)
	})
	r.Get("/api/v1/auth/magic", func(w http.ResponseWriter, r *http.Request) {
		// Legacy magic-link callback — replaced by PIN auth in #688.
		// Redirect to login so cached email links degrade gracefully.
		http.Redirect(w, r, "/login?error=flow_changed", http.StatusFound)
	})
	r.Delete("/api/v1/auth/session", h.HandleAuthLogout)
	// POST /auth/session — SPA-driven logout (qa-loop iter-4
	// auth-session-logout-405). DELETE remains for backwards-compat;
	// POST is the canonical SPA path because some proxies strip
	// body+credentials from DELETE on cross-origin XHR.
	r.Post("/api/v1/auth/session", h.HandleAuthSessionLogout)

	// Unauthenticated tenant-discovery endpoint (issue #802) — the SPA
	// calls this on bootstrap to learn which Keycloak realm to OIDC
	// against for the current `window.location.host`. Returns 404 for
	// unknown hosts (the SPA renders a generic landing) and never
	// exposes admin URLs or credentials. Wired BEFORE the session-cookie
	// gate because the SPA hasn't authenticated yet at the moment it
	// calls this.
	r.Get("/api/v1/tenant/discover", h.HandleTenantDiscover)

	// /api/v1/sovereign/self — Sovereign self-discovery (deployment id +
	// FQDN) used by the catalyst-ui SovereignConsoleRedirect React
	// component to map clean `/console/<page>` URLs on a Sovereign to the
	// canonical deployment-scoped `/provision/<self-id>/<page>` pages
	// that render the same byte-byte data the mothership serves at
	// console.openova.io/sovereign/provision/<id>/<page>.
	//
	// Outside RequireSession: the response carries only public
	// identifiers (deployment id + FQDN — both visible in URLs anyway).
	// Bypassing the session gate keeps the redirect helper usable on
	// the very first browser hit before the operator has logged in.
	// Mothership returns 404 (CATALYST_OTECH_FQDN unset) — the UI hook
	// silently falls back to URL params there. See sovereign_self.go.
	r.Get("/api/v1/sovereign/self", h.HandleSovereignSelf)

	// /api/v1/internal/deployments/import — Sovereign-side receiver for
	// the full deployment record POSTed by the contabo mother at
	// handover time. Mother's fireHandover() ships the record here after
	// the JWT mint completes; child persists it locally so the child's
	// own /api/v1/deployments/{id}/* endpoints answer byte-byte-identical
	// to the mother's view. Closes the data half of the mother→child
	// contract (PR #976 closed the URL routing half).
	//
	// Outside RequireSession: cross-cluster ingress at handover happens
	// before any operator session exists on the child. Validation is
	// instead done by FQDN match against CATALYST_OTECH_FQDN env — a
	// record claiming a different FQDN is rejected.
	r.Post("/api/v1/internal/deployments/import", h.HandleDeploymentImport)

	// D16 PR F (2026-05-17 t138 bug fix): mothership POSTs each
	// secondary-region kubeconfig here at handover. Same auth model as
	// /api/v1/internal/deployments/import — no operator session exists
	// on the child yet, validation is by depID+regionKey safe-id regex.
	// Earlier registration inside the auth group (rg) caused mothership
	// POSTs to 401, suppressing the D16 fan-out silently. Bytes never
	// leave the chroot disk or enter logged structs (INVIOLABLE-PRINCIPLES #10).
	r.Post("/api/v1/sovereign/secondary-kubeconfig", h.HandleSovereignSecondaryKubeconfig)

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
		// same directory; a missing dir is non-fatal — the SME user
		// endpoints surface 503 instead.
		if ups, err := store.NewUserProvisionStore(dir); err != nil {
			log.Warn("user-provision-store: init failed; /sme/users will return 503",
				"err", err,
			)
		} else {
			deps := handler.SMEDeps{
				UserProvisionStore: ups,
				SecretBaseURLTemplate: env(
					"CATALYST_SME_NEWAPI_BASE_URL_TEMPLATE",
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
					log.Info("sme-users: NewAPI admin client wired", "addr", addr)
				} else {
					log.Info("sme-users: CATALYST_NEWAPI_ADMIN_TOKEN unset; NewAPI hook step 2 will fail-closed")
				}
			}
			// SME-realm Keycloak client — uses the SA token from the
			// existing bp-keycloak ExternalSecret pipeline.
			if saToken := os.Getenv("CATALYST_SME_KC_SA_TOKEN"); saToken != "" {
				deps.KeycloakClient = handler.SMEKeycloakDirectClient{SAToken: saToken}
				log.Info("sme-users: SME-realm Keycloak client wired")
			}
			// K8s Secret applier — uses the catalyst-api Pod's own
			// in-cluster client. main.go already builds homeCore above.
			if homeCore != nil {
				deps.SecretApplier = handler.K8sSecretApplier{Client: homeCore}
				log.Info("sme-users: K8s SSA Secret applier wired")
			}
			h.SetSMEDeps(deps)
		}

		// SME tenant provisioning pipeline (issue #804) — same
		// directory as the user-provision store. Wires the GitOps
		// overlay writer (uses CATALYST_GITOPS_* env), DNS provisioner
		// (PowerDNS for free-subdomain, net.LookupCNAME for BYO), and
		// the chart-bootstrap-aware Keycloak client verifier.
		if smeTenantStore, err := store.NewSMETenantProvisionStore(dir); err != nil {
			log.Warn("sme-tenant-store: init failed; /sme/tenants will return 503",
				"err", err,
			)
		} else {
			tdeps := handler.SMETenantDeps{
				Store:            smeTenantStore,
				TenantRegistry:   nil, // overwritten below from h.tenantRegistry
				OTECHFQDN:        os.Getenv("CATALYST_OTECH_FQDN"),
				OTECHIngressIPv4: os.Getenv("CATALYST_OTECH_INGRESS_IPV4"),
				// Multi-domain Sovereign pool (epic #825 / MD-3 #828).
				// Sourced from CATALYST_SME_POOL_DOMAINS env (stub) until
				// MD-1 (#826) lands the Deployment.ParentDomains[] field
				// — at which point this wiring switches to read from the
				// deployment record. The data shape is forward-compatible.
				ParentDomains: handler.LoadSMETenantParentDomainsFromEnv(),
				MaxRetryCount: 5,
			}
			// GitOps overlay writer — chart versions read from env
			// per Inviolable Principle 4. Empty values fall back to "*".
			tdeps.GitOps = handler.DefaultSMETenantGitOpsWriter{
				Log: log,
				ChartVersions: handler.SMETenantChartVersions{
					Keycloak:  os.Getenv("CATALYST_SME_BP_KEYCLOAK_VER"),
					CNPG:      os.Getenv("CATALYST_SME_BP_CNPG_VER"),
					WordPress: os.Getenv("CATALYST_SME_BP_WORDPRESS_VER"),
					OpenClaw:  os.Getenv("CATALYST_SME_BP_OPENCLAW_VER"),
					Stalwart:  os.Getenv("CATALYST_SME_BP_STALWART_VER"),
					NewAPI:    os.Getenv("CATALYST_SME_BP_NEWAPI_VER"),
				},
			}
			// DNS provisioner — wraps PowerDNS for free-subdomain
			// writes; falls back to a no-op when env unset.
			pdnsURL := os.Getenv("CATALYST_POWERDNS_URL")
			pdnsKey := os.Getenv("CATALYST_POWERDNS_API_KEY")
			if writer := handler.NewPowerDNSWriter(pdnsURL, pdnsKey); writer != nil {
				tdeps.DNS = handler.DefaultSMETenantDNSProvisioner{Writer: writer}
				log.Info("sme-tenant: powerdns writer wired", "url", pdnsURL)
			} else {
				tdeps.DNS = handler.NoopSMETenantDNSProvisioner{}
				log.Info("sme-tenant: powerdns env unset; using no-op DNS provisioner")
			}
			// Keycloak client verifier — uses the same SA token as the
			// user-create hook (CATALYST_SME_KC_SA_TOKEN).
			tdeps.KeycloakClients = handler.ChartBootstrapKeycloakProvisioner{
				Log:     log,
				SAToken: os.Getenv("CATALYST_SME_KC_SA_TOKEN"),
			}
			// Pull the tenant registry the SME-user wiring just set so
			// the pipeline can register console.<host> on completion.
			h.SetSMETenantDeps(tdeps)
			// Re-wire registry now that the Handler has it.
			if reg, err := store.NewTenantRegistry(dir); err == nil {
				tdeps.TenantRegistry = reg
				h.SetTenantRegistry(reg)
				h.SetSMETenantDeps(tdeps)
			}
			log.Info("sme-tenant: pipeline wired",
				"otech_fqdn", tdeps.OTECHFQDN,
				"max_retry", tdeps.MaxRetryCount,
			)
		}
	}

	// Unauthenticated cloud-init postback (issue #183, Option D + #634).
	// The new Sovereign's control plane PUTs its rewritten kubeconfig
	// here with `Authorization: Bearer <postback-token>`. PutKubeconfig
	// has its own SHA-256-hash-vs-stored-hash compare — it MUST live
	// outside the session-cookie middleware because cloud-init has no
	// browser cookies. Putting this inside the RequireSession group
	// rejected every postback with 401 {"error":"unauthenticated"} and
	// stuck Phase-1 in PENDING forever (caught live on otech23).
	r.Put("/api/v1/deployments/{id}/kubeconfig", h.PutKubeconfig)

	// Sovereign-side handover receiver (issue #606). The operator's
	// browser arrives at GET /auth/handover?token=<jwt> from the
	// Catalyst-Zero wizard. The JWT IS the auth — there is NO
	// catalyst_session cookie yet (the handler MINTS one). Therefore
	// this route MUST live outside RequireSession. Putting it inside
	// rejected every handover with 401 {"error":"unauthenticated"}
	// before AuthHandover ever ran (caught live on otech49 2026-05-03).
	// AuthHandover validates the RS256 JWT against the public-key file,
	// creates the user in the Sovereign Keycloak, exchanges for a
	// session, sets HttpOnly Secure SameSite=Lax cookies, and redirects
	// to /sovereign/dashboard. The cookies it sets are what gates every
	// subsequent /sovereign/api/v1/* call inside the RequireSession group.
	r.Get("/auth/handover", h.AuthHandover)

	// In-cluster Self-Sovereignty Cutover trigger (issue #935 Bug 2).
	// The bp-self-sovereign-cutover chart's auto-trigger Helm
	// post-install Job (templates/10-auto-trigger-job.yaml, added in
	// chart 0.1.16 per issue #933) hits this endpoint to fire the
	// cutover automatically — there is no human session, no browser
	// cookie. The Job authenticates via its projected ServiceAccount
	// token (Authorization: Bearer <SA token bytes>) and the handler
	// validates the token via the apiserver's TokenReview API + checks
	// the resolved username matches the canonical
	// `bp-self-sovereign-cutover-runner` SA the chart authored for this
	// purpose. Same blast radius as the chart's RBAC, expressed at the
	// HTTP edge instead of only at the K8s API edge.
	//
	// MUST live outside RequireSession because the in-cluster Job has
	// no `catalyst_session` cookie. The session-cookie group below
	// rejects every Job request with 401 `unauthenticated`, which is
	// what was hanging cutover on otech113 2026-05-05 (issue #935).
	r.Post("/api/v1/internal/cutover/trigger", h.HandleCutoverInternalTrigger)

	// Auth-gated wizard endpoints — RequireSession validates the
	// HMAC-signed catalyst_session cookie on every request. When
	// cfg is nil (Sovereign clusters, CI without CATALYST_KC_ADDR)
	// the middleware is a transparent passthrough and all requests
	// proceed without any auth check, preserving existing behaviour.
	r.Group(func(rg chi.Router) {
		rg.Use(auth.RequireSession(h.GetAuthConfig(), log))

		// Whoami — UI auth guard polls this; 401 → redirect to /login.
		rg.Get("/api/v1/whoami", h.HandleWhoami)

		// Sandbox BYOS — Claude Code "Bring Your Own Subscription"
		// (Wave 1b scaffold; design in
		// products/sandbox/docs/claude-code-byos.md).
		//
		// Lets a user attach their personal Anthropic Max / Pro
		// subscription so Sandbox-Pod Claude Code sessions bypass
		// the Sovereign newapi gateway. All four endpoints sit
		// inside RequireSession — every BYOS action is bound to
		// the authenticated user's `sub` claim.
		rg.Post("/api/v1/sandbox/byos/claude-code/start", h.HandleByosClaudeCodeStart)
		rg.Get("/api/v1/sandbox/byos/claude-code/callback", h.HandleByosClaudeCodeCallback)
		rg.Delete("/api/v1/sandbox/byos/claude-code", h.HandleByosClaudeCodeDisconnect)
		rg.Get("/api/v1/sandbox/byos/claude-code/status", h.HandleByosClaudeCodeStatus)
		// /config — FE pre-flight: reports whether the chart's OAuth
		// client_id is the placeholder (PR #1619). The SandboxSettings
		// card uses this to disable the Connect button + show an amber
		// "pending operator setup" tooltip instead of letting the user
		// click a button whose OAuth URL would 400 at Anthropic.
		rg.Get("/api/v1/sandbox/byos/claude-code/config", h.HandleByosClaudeCodeConfig)

		// Sandbox sessions — Wave 7 CRUD on the Sandbox CRD
		// (sandbox.openova.io/v1). The FE in
		// products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts
		// (PR #1621) calls getSandboxes() + createSandbox() + a
		// per-row delete; the BE handler lives in
		// products/catalyst/bootstrap/api/internal/handler/
		// sandbox_sessions.go. All four endpoints sit inside
		// RequireSession — Sandbox ops are scoped to the operator's
		// org_id claim per PR #1619.
		rg.Get("/api/v1/sandbox/sessions", h.HandleListSandboxSessions)
		rg.Post("/api/v1/sandbox/sessions", h.HandleCreateSandboxSession)
		rg.Get("/api/v1/sandbox/sessions/{id}", h.HandleGetSandboxSession)
		rg.Delete("/api/v1/sandbox/sessions/{id}", h.HandleDeleteSandboxSession)

		// K8s data-plane endpoints — list + SSE stream + sync map per
		// Sovereign cluster (issue #321). Per ADR-0001 §5 the catalyst-api
		// is the consolidator; reads flow off the in-process Indexer,
		// never directly from the apiserver.
		rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}", h.HandleK8sList)
		rg.Get("/api/v1/sovereigns/{id}/k8s/stream", h.HandleK8sStream)
		rg.Get("/api/v1/sovereigns/{id}/k8s/sync", h.HandleK8sSync)
		// EPIC-4 X1 (#1099) — Pod-log WebSocket. Same chi.Group as the
		// rest of /k8s/* so RequireSession gates the upgrade handshake
		// the same way it gates the SSE/REST surface.
		rg.Get("/api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}", h.HandleK8sLogs)
		// EPIC-4 X2+E (#1099) — exec session creation (Guacamole),
		// exec WebSocket fallback (X1-style), session list + replay.
		// Tier-developer or higher for create + WS fallback;
		// tier-admin or higher for list; admin/owner for replay.
		rg.Post("/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session", h.HandleK8sExecSession)
		rg.Get("/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}", h.HandleK8sExecWebSocket)
		rg.Get("/api/v1/sovereigns/{id}/sessions", h.HandleK8sSessionsList)
		rg.Get("/api/v1/sovereigns/{id}/sessions/{sessionId}/replay", h.HandleK8sSessionReplay)
		// qa-loop iter-7 Fix #39 — canonical UAT-matrix vocabulary
		// surface for "issue a remote-shell session". Same business
		// logic as POST /k8s/exec/.../session but the matrix-canonical
		// query-param + response-field shape (`sessionId`,
		// `guacamoleUrl`, `recordingPath`). See
		// internal/handler/shells_issue.go for the full contract.
		// Tier-developer or higher (same gate as the underlying
		// HandleK8sExecSession).
		rg.Post("/api/v1/sovereigns/{id}/shells/issue", h.HandleShellsIssue)
		// qa-loop iter-9 Fix #43, Cluster-B (TC-231): canonical
		// kubectl-style alias for /sessions — items envelope of
		// recorded shell sessions on the Sovereign. Same handler.
		rg.Get("/api/v1/sovereigns/{id}/shells/sessions", h.HandleK8sSessionsList)
		// qa-loop iter-9 Fix #43, Cluster-A (TC-376): canonical
		// kubectl-style alias for POST /k8s/exec/.../session — issues
		// a Guacamole session against the named pod's default
		// container. The handler resolves a default container name
		// when the URL omits it (matches `kubectl exec POD --`
		// behaviour).
		rg.Post("/api/v1/sovereigns/{id}/k8s/pods/{ns}/{pod}/exec", h.HandleK8sExecSession)
		// qa-loop iter-9 Fix #43, Cluster-B (TC-265): canonical
		// search-across-kinds endpoint — items envelope of K8s
		// resources whose name matches `?q=`.
		rg.Get("/api/v1/sovereigns/{id}/k8s/search", h.HandleK8sSearch)
		// EPIC-4 R1+R2+R3+R5+R6 (#1099) — Resource browser drill-down,
		// resource tree, YAML edit (apply / dry-run), per-row actions
		// (scale / restart / delete), metrics. Tier-admin gate is enforced
		// inside each mutation handler (UI hides the buttons too, but the
		// server is the authoritative gate per INVIOLABLE-PRINCIPLES #5).
		// /metrics has its own static path-prefix so chi resolves it
		// correctly against /k8s/{kind} (kind="metrics" would otherwise
		// shadow it). Static must come BEFORE dynamic.
		rg.Get("/api/v1/sovereigns/{id}/k8s/metrics/{kind}/{ns}/{name}", h.HandleK8sResourceMetrics)
		rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourceGet)
		rg.Get("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree", h.HandleK8sResourceTree)
		rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", h.HandleK8sResourceScale)
		// PUT /scale alias (qa-loop iter-7 Cluster-C, #1227): the
		// Sovereign Console UI + qa-loop matrix use PUT to align with
		// REST verb-resource semantics ("update the scale subresource").
		// The handler is identical — both verbs land on the same
		// MergePatch.
		rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", h.HandleK8sResourceScale)
		rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/restart", h.HandleK8sResourceRestart)
		// PUT alias for ConfigMap / Secret update (qa-loop iter-7
		// Cluster-C, #1227). Mirrors the YAML-editor's apply path but
		// accepts a structured body so the matrix + UI can update a
		// single field without round-tripping the whole YAML.
		rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourceApply)
		rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/dry-run", h.HandleK8sResourceDryRun)
		rg.Post("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/apply", h.HandleK8sResourceApply)
		rg.Delete("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourceDelete)
		// QA-loop iter-7 Fix #34 — vocab widening: PUT alias for
		// /scale and direct resource Update via PUT on the bare path.
		// The matrix (TC-215, TC-243, TC-206, TC-244, TC-247) and
		// kubectl-style muscle memory both surface PUT for "edit
		// replicas" and "edit object yaml". The handlers underneath
		// are the same as the POST shapes; the chi router needs both
		// verbs registered explicitly because chi.Group.Post does NOT
		// silently match PUT.
		rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/scale", h.HandleK8sResourceScale)
		rg.Put("/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}", h.HandleK8sResourcePut)
		// QA-loop iter-7 Fix #34 — multi-resource server-side apply.
		// Body: {yaml: "<one-or-more-docs>"}. Returns one entry per
		// document with `created` vs `updated`. Matrix TC-271.
		rg.Post("/api/v1/sovereigns/{id}/k8s/apply", h.HandleK8sMultiApply)
		// NOTE: wizard pre-submit validation endpoints
		// (/credentials/validate, /credentials/object-storage/validate,
		// /sshkey/generate, /registrar/{r}/validate, /subdomains/check)
		// are registered OUTSIDE the session group at the top of this
		// function — they are called from the wizard BEFORE the operator
		// authenticates and gating them on a session cookie produces a
		// 401 every time, blocking the only flow that matters.
		rg.Post("/api/v1/deployments", h.CreateDeployment)
		// List endpoint (issue #747) — wizard auto-redirect to /provision/<id>
		// when the signed-in operator already has an in-flight deployment.
		// Filtered server-side by the X-User-Email header injected by
		// RequireSession; the ?owner= query param is a client hint that is
		// silently overridden when a session is present.
		rg.Get("/api/v1/deployments", h.ListDeployments)
		rg.Get("/api/v1/deployments/{id}", h.GetDeployment)
		// Record-only delete (issue #178). Removes the deployment from
		// the in-memory map + on-disk store + kubeconfig file. Does NOT
		// touch Hetzner — for the "kill the kid" path the operator's UI
		// POSTs /wipe instead (which already chains record-delete on
		// success). Refuses adopted (422) + in-flight (409) deployments
		// to keep the customer breadcrumb + Commit safety intact.
		rg.Delete("/api/v1/deployments/{id}", h.DeleteDeployment)
		rg.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
		// Buffered event history endpoint (issue #180). Returns the full event
		// slice + state JSON so the wizard's ProvisionPage can render history
		// for a deployment that already finished — the SSE replay-on-connect
		// covers the same path, but the GET is a stateless fast-path test
		// + reconnect target.
		rg.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
		// Kubeconfig endpoint — wizard StepSuccess "Download kubeconfig"
		// button + Sovereign Admin break-glass download + the source the
		// internal/helmwatch HelmRelease watcher reads from when the
		// catalyst-api Pod cold-starts mid-Phase-1 and has to reattach
		// to a deployment whose kubeconfig is on the PVC.
		rg.Get("/api/v1/deployments/{id}/kubeconfig", h.GetKubeconfig)
		// (PUT /kubeconfig is registered ABOVE the session group — see
		// the cloud-init postback comment near r.Delete /auth/session.)
		// Registrar proxy — wizard's BYO Flow B (#169). /validate is OUTSIDE
		// session group (pre-auth wizard surface). /set-ns is called from
		// CreateDeployment when domainMode == byo-api (post-auth).
		rg.Post("/api/v1/registrar/{registrar}/set-ns", h.SetNSRegistrar)
		// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
		// Phase 0 retries re-run `tofu apply` against the existing workdir;
		// Phase 1 retries emit operator instructions per the architectural
		// contract (Flux owns Phase 1 reconciliation).
		rg.Post("/api/v1/deployments/{id}/phases/{phase}/retry", h.RetryPhase)
		// Cancel & Wipe endpoint (issue #318). Operator-triggered purge of a
		// failed or abandoned deployment: tofu destroy + Hetzner orphan purge
		// + PDM release + local state cleanup. Idempotent. Returns 200 with a
		// PurgeReport summary. The wizard's failed-state banner renders the
		// operator confirmation modal that POSTs here.
		rg.Post("/api/v1/deployments/{id}/wipe", h.WipeDeployment)
		// Handover JWT — issue #605 (minting) + issue #606 (consumption).
		// MintHandoverToken: Catalyst-Zero operator finalises a deployment;
		// wizard StepSuccess POSTs here to get a one-time RS256 JWT, then
		// redirects the operator's browser to the Sovereign console URL.
		// GetHandoverPublicKey: Sovereigns fetch the JWK at boot to seed
		// their CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH (or via cloud-init).
		// AuthHandover: Sovereign-side receiver — validates the JWT, creates
		// the operator in Keycloak, exchanges for a session, sets cookies,
		// and redirects to /console/dashboard.
		rg.Post("/api/v1/deployments/{id}/mint-handover-token", h.MintHandoverToken)
		rg.Get("/api/v1/handover/public-key", h.GetHandoverPublicKey)
		// Subdomain-only release endpoint (issue #489). Releases the PDM
		// allocation row for a failed-or-abandoned deployment WITHOUT
		// requiring the operator to re-enter their HetznerToken. Lets a
		// franchise customer retry under the same pool subdomain after a
		// botched provision instead of being forced to pick acmeN+1. Does
		// NOT touch Hetzner — the Cancel & Wipe flow remains the canonical
		// path for live cloud cleanup. Refuses on in-flight deployments
		// (409), wiped deployments (410), or adopted Sovereigns (422).
		rg.Delete("/api/v1/deployments/{id}/release-subdomain", h.ReleaseSubdomain)
		// Handover finalisation (issue #317). Catalyst-Zero side: stops the
		// helmwatch informer, ships the OpenTofu state to the new Sovereign's
		// catalyst-api, and purges every local trace once the new side
		// confirms the archive is sealed in its OpenBao. Sovereign side:
		// receives the archive on /handover/tofu-archive and writes it to
		// `secret/catalyst/tofu-phase0-archive`. The two endpoints live on
		// the same binary; Catalyst-Zero leaves CATALYST_OPENBAO_ADDR unset,
		// so a misrouted archive POST hits 503 instead of 200.
		rg.Post("/api/v1/handover/finalise/{id}", h.FinaliseHandover)
		rg.Post("/api/v1/handover/tofu-archive", h.ReceiveTofuArchive)
		// Jobs/Executions REST surface — the canvas + per-job detail
		// pages read this in parallel to the existing SSE events feed.
		// All endpoints are read-only; every mutation flows through the
		// helmwatch bridge in internal/jobs. Each Job carries parentId +
		// childIds so the FE can render the recursive Job tree without
		// any batch-specific endpoint (issue #351).
		rg.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
		rg.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
		rg.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
		// Backfill endpoints — give the FE an explicit handshake to
		// re-attach the helmwatch goroutine after a Pod restart and to
		// snapshot the in-memory informer cache. The bridge seeds a Job
		// per HR observed on initial-list so HRs that have been
		// Ready=True for an hour materialise rows immediately rather
		// than only on state transitions.
		rg.Post("/api/v1/deployments/{depId}/refresh-watch", h.RefreshWatch)
		rg.Get("/api/v1/deployments/{depId}/components/state", h.GetComponentsState)
		// OpenovaFlow proxy (Agent #3 integration — PR #1389/#1390
		// follow-up). Proxies the FlowPage canvas's snapshot/stream/
		// ingest path to the bp-openova-flow-server inside the
		// Sovereign's catalyst-system namespace. flowId == deploymentId
		// (the openova-flow-server treats flowId as an opaque key). The
		// SSE pass-through is unbuffered (canonical pattern: see
		// internal/handler/deployments.go StreamLogs lines 1208-1287).
		// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the
		// upstream URL is sourced from env OPENOVA_FLOW_SERVER_URL
		// (Sovereign-side defaults to the in-cluster Service DNS).
		rg.Get("/api/v1/flows/{deploymentId}/snapshot", h.HandleFlowSnapshot)
		rg.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
		rg.Post("/api/v1/flows/{deploymentId}/events", h.HandleFlowEvents)
		// Sovereign Dashboard treemap (resource utilisation). Read-only.
		// V1 emits a static placeholder shape — see dashboard.go header
		// for the metrics-server upgrade plan.
		rg.Get("/api/v1/dashboard/treemap", h.GetDashboardTreemap)
		// Compliance score aggregator (EPIC-1 #1096 slice S) — joins
		// Kyverno PolicyReports + 5 custom evaluators + EnvironmentPolicy
		// weights into per-resource + per-Application + per-Org +
		// per-Sovereign weighted scores. SSE for live updates, REST for
		// snapshots, Prometheus /metrics + NATS JetStream KV
		// `policy-rollup` for replayable history.
		rg.Get("/api/v1/sovereigns/{id}/compliance/scorecard", h.HandleComplianceScorecard)
		rg.Get("/api/v1/sovereigns/{id}/compliance/policies", h.HandleCompliancePolicies)
		rg.Get("/api/v1/sovereigns/{id}/compliance/policies/{name}", h.HandleCompliancePolicyByName)
		rg.Get("/api/v1/sovereigns/{id}/compliance/violations", h.HandleComplianceViolations)
		rg.Get("/api/v1/sovereigns/{id}/compliance/stream", h.HandleComplianceStream)
		// Wave-2 Family-E (#1583/Family-E): runtime + supply-chain
		// compliance aggregators. Falco runtime alerts (C11-008),
		// Trivy SBOM + CVE reports (C11-010), per-Pod + cluster-wide.
		rg.Get("/api/v1/sovereigns/{id}/compliance/falco", h.HandleComplianceFalco)
		rg.Get("/api/v1/sovereigns/{id}/compliance/sbom", h.HandleComplianceSBOMPod)
		rg.Get("/api/v1/sovereigns/{id}/compliance/sbom/summary", h.HandleComplianceSBOMSummary)
		// QA-loop iter-11 Fix #48 — Networking page surface. Each
		// endpoint joins live K8s objects from the in-process k8scache
		// Indexer (Cilium NetworkPolicies, ClusterMesh ConfigMaps,
		// NetBird Deployments, DMZ vClusters, Hubble relay/UI) into
		// the wire shape the UI's NetworkingPage subscribes to.
		// Per docs/INVIOLABLE-PRINCIPLES.md #2 every byte traces back
		// to a real K8s object — no fixture data, no stub rows.
		rg.Get("/api/v1/sovereigns/{id}/networking/policies", h.HandleNetworkingPolicies)
		rg.Get("/api/v1/sovereigns/{id}/networking/clustermesh", h.HandleNetworkingClusterMesh)
		rg.Get("/api/v1/sovereigns/{id}/networking/netbird", h.HandleNetworkingNetBird)
		rg.Get("/api/v1/sovereigns/{id}/networking/dmz", h.HandleNetworkingDMZ)
		rg.Get("/api/v1/sovereigns/{id}/networking/hubble", h.HandleNetworkingHubble)
		// EPIC-1 #1096 slice X — EnvironmentPolicy mode toggle backend
		// for the slice U PolicyModeToggle widget. Writes
		// EnvironmentPolicy.spec.compliance.modes; the EnvironmentPolicy
		// controller (separately reconciled) flips Kyverno's per-namespace
		// validationFailureAction. Requires tier-admin or higher per
		// INVIOLABLE-PRINCIPLES #5.
		rg.Put("/api/v1/sovereigns/{id}/environments/{env}/policy", h.HandleEnvironmentPolicyMode)
		// Sovereign Infrastructure surface — unified topology read +
		// Day-2 CRUD via Crossplane XRC writes (issue #227 + Day-2 IaC).
		// Read endpoints compose from the deployment record + live
		// cluster informer cache; mutation endpoints write Composite
		// Resource Claims to the Sovereign cluster's kubeconfig per
		// docs/INVIOLABLE-PRINCIPLES.md #3 (Crossplane is the ONLY
		// Day-2 IaC seam). Every mutation also commits a Job entry to
		// the existing /jobs surface for full audit-trail.
		rg.Get("/api/v1/deployments/{depId}/infrastructure/topology", h.GetInfrastructureTopology)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/compute", h.GetInfrastructureCompute)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/storage", h.GetInfrastructureStorage)
		rg.Get("/api/v1/deployments/{depId}/infrastructure/network", h.GetInfrastructureNetwork)

		// CRUD — every endpoint writes a Crossplane XRC + a mutation Job.
		// The third-sibling chart authors the matching Compositions; until
		// they land Crossplane sits the claim Pending and the catalyst-api
		// surfaces "Awaiting Composition for <kind>" in the audit log.
		rg.Post("/api/v1/deployments/{depId}/infrastructure/regions", h.CreateInfrastructureRegion)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/regions/{id}", h.PatchInfrastructureRegion)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/regions/{id}/clusters", h.CreateInfrastructureCluster)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/clusters/{id}", h.PatchInfrastructureCluster)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/vclusters", h.CreateInfrastructureVCluster)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/vclusters/{id}", h.PatchInfrastructureVCluster)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/pools", h.CreateInfrastructurePool)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/pools/{id}", h.PatchInfrastructurePool)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/clusters/{id}/nodes", h.CreateInfrastructureWorkerNode)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/nodes/{id}", h.PatchInfrastructureWorkerNode)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/loadbalancers", h.CreateInfrastructureLoadBalancer)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/loadbalancers/{id}", h.PatchInfrastructureLoadBalancer)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/networks", h.CreateInfrastructureNetwork)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/networks/{id}", h.PatchInfrastructureNetwork)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/pvcs", h.CreateInfrastructurePVC)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/pvcs/{id}", h.PatchInfrastructurePVC)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/buckets", h.CreateInfrastructureBucket)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/buckets/{id}", h.PatchInfrastructureBucket)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/volumes", h.CreateInfrastructureVolume)
		rg.Patch("/api/v1/deployments/{depId}/infrastructure/volumes/{id}", h.PatchInfrastructureVolume)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/peerings", h.CreateInfrastructurePeering)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/firewalls/{id}/rules", h.CreateInfrastructureFirewallRule)
		rg.Post("/api/v1/deployments/{depId}/infrastructure/nodes/{id}/{action}", h.CreateInfrastructureNodeAction)
		rg.Delete("/api/v1/deployments/{depId}/infrastructure/{kind}/{id}", h.DeleteInfrastructureResource)

		// Marketplace mode toggle (issue #710 wave 3b). The Sovereign
		// Settings → Marketplace page POSTs here to enable/disable the
		// marketplace HTTPRoutes + storefront branding on a live
		// Sovereign. The handler edits the per-Sovereign overlay file
		// at clusters/<fqdn>/bootstrap-kit/13-bp-catalyst-platform.yaml
		// in the GitOps repo and pushes the commit; Flux on the target
		// Sovereign reconciles within ~1 min and the chart re-renders.
		// Per the founder's 2026-05-04 GitOps rule, NO ConfigMap-shortcut
		// path exists — every change is a git commit on the audit trail.
		rg.Post("/api/v1/sovereigns/{id}/marketplace", h.HandleSetMarketplace)
		rg.Get("/api/v1/sovereigns/{id}/marketplace", h.HandleGetMarketplace)

		// Sovereign IAM — UserAccess CR editor (issue #323). The UI's
		// /sovereign/users page calls these endpoints to list / create /
		// update / delete UserAccess CRs against the Sovereign cluster.
		// The CRD shape (`access.openova.io/v1alpha1`) is shipped by
		// issue #322's chart; catalyst-api consumes it via dynamic
		// client so there's no Go-type build dependency between the
		// two PRs.
		rg.Get("/api/v1/deployments/{depId}/admin/user-access", h.ListUserAccess)
		rg.Post("/api/v1/deployments/{depId}/admin/user-access", h.CreateUserAccess)
		rg.Put("/api/v1/deployments/{depId}/admin/user-access/{name}", h.UpdateUserAccess)
		rg.Delete("/api/v1/deployments/{depId}/admin/user-access/{name}", h.DeleteUserAccess)

		// EPIC-3 (#1098) slice A1+A2 — find-or-create role assignment
		// + access-matrix endpoints. The /rbac/assign endpoint is the
		// ergonomic wrapper the multi-grant editor (slice U1) calls
		// when an operator picks a tier + scope combination; idempotent
		// re-assigns no-op or update the existing UserAccess. The
		// access-matrix endpoint feeds the EPIC-3 U7 access-matrix UI
		// with one pre-computed users × applications × tier grid.
		rg.Post("/api/v1/sovereigns/{id}/rbac/assign", h.HandleRBACAssign)
		rg.Get("/api/v1/sovereigns/{id}/rbac/access-matrix", h.HandleRBACAccessMatrix)

		// EPIC-3 (#1098) slice U8 — RBAC audit trail endpoints. List is
		// paginated; stream is SSE. Both filter to the rbac-* audit-type
		// namespace and the requested SovereignID. Backed by the
		// in-process audit.Bus (also forwarding to NATS catalyst.audit
		// when CATALYST_NATS_URL is set per ADR-0001 §3).
		rg.Get("/api/v1/sovereigns/{id}/audit/rbac", h.HandleRBACAuditList)
		rg.Get("/api/v1/sovereigns/{id}/audit/rbac/stream", h.HandleRBACAuditStream)

		// EPIC-3 (#1098) slice U2/U3/U4 — Keycloak proxy endpoints
		// powering the multi-grant editor's user picker (U2),
		// sovereign-admin group browser (U3), and realm/role browser
		// (U4). All proxy to the Sovereign realm's KC Admin REST API
		// via the wired h.kc client. U2 requires tier-admin or higher
		// (mirrors /rbac/assign); U3 + U4 require sovereign-admin
		// (admin or owner) per INVIOLABLE-PRINCIPLES #5.
		rg.Get("/api/v1/sovereigns/{id}/keycloak/users", h.HandleKeycloakUsersSearch)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsList)
		rg.Post("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsCreate)
		rg.Put("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsUpdate)
		rg.Delete("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsDelete)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/roles", h.HandleKeycloakRolesList)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/roles/{name}/members", h.HandleKeycloakRoleMembers)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/clients/{clientId}/roles", h.HandleKeycloakClientRolesList)

		// qa-loop iter-1 Fix #104 — Keycloak admin proxy for the
		// matrix-asserted endpoints (TC-124 / TC-125 / TC-159 / TC-160 /
		// TC-161 / TC-176 / TC-190 / TC-285). Keycloak is NOT externally
		// exposed on the chroot Sovereign and the matrix runner cannot
		// `kubectl exec`, so without this proxy these 8 TCs were stuck
		// at FAIL/BLOCKED. The proxy gates every endpoint with the
		// sovereign-admin tier (admin or owner) — same gate as the U3/U4
		// browser endpoints — and uses the catalyst-api SA credential to
		// perform the privileged Keycloak Admin call in-cluster (the
		// operator's password / SA secret never leaves the cluster).
		rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles", h.HandleKeycloakAdminRealmRolesList)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles/{role}/composites", h.HandleKeycloakAdminRoleComposites)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances", h.HandleKeycloakAdminIdPList)
		rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances", h.HandleKeycloakAdminIdPCreate)
		rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances/{alias}/mappers", h.HandleKeycloakAdminIdPMapperCreate)
		rg.Post("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/protocol/openid-connect/token", h.HandleKeycloakAdminTokenMint)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients", h.HandleKeycloakAdminClientsByClientID)
		rg.Get("/api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients/{client}/service-account-user/role-mappings/realm", h.HandleKeycloakAdminClientServiceAccountRoles)

		// EPIC-2 (#1097) slice I — live install flow. Operators submit
		// Application install requests here; the handler validates
		// parameters against Blueprint.spec.configSchema (via the
		// promoted core/controllers/pkg/validate package) and creates
		// the Application CR per ADR-0001 §2.7. The application-
		// controller (slice C4 #1133) reconciles the rest. The
		// preview endpoint runs the SAME renderer the controller uses
		// (core/controllers/pkg/render, promoted in this slice) so a
		// "looks-good in preview" cannot diverge from the actual
		// install. Both endpoints require tier-admin or higher per
		// docs/INVIOLABLE-PRINCIPLES.md #5.
		rg.Post("/api/v1/sovereigns/{id}/applications", h.HandleApplicationInstall)
		rg.Post("/api/v1/sovereigns/{id}/applications/preview", h.HandleApplicationPreview)
		rg.Get("/api/v1/sovereigns/{id}/applications/{name}/status", h.HandleApplicationStatus)
		rg.Get("/api/v1/sovereigns/{id}/applications/{name}/stream", h.HandleApplicationStream)
		// qa-loop iter-11 Fix #45 Cluster-C: full Application detail
		// (identity + spec + status) so the Sovereign Console SPA's
		// AppDetail page can synthesise an ApplicationDescriptor on the
		// fly when the Application isn't part of the wizard's
		// `selectedComponents` (the typical chroot Sovereign case —
		// Apps installed via `kubectl apply -f application.yaml` or
		// the catalyst-api install endpoint, NOT via the wizard).
		// Without this route the SPA fell into the "App not found"
		// surface for every chroot-installed Application.
		rg.Get("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationGet)
		// qa-loop iter-9 Fix #43, Cluster-B (TC-104): canonical items
		// envelope listing of installed Applications across all Org
		// namespaces on the Sovereign cluster.
		rg.Get("/api/v1/sovereigns/{id}/applications", h.HandleApplicationList)

		// qa-loop iter-3 — catalog proxy. Slice-L originally exposed
		// catalyst-catalog only via a Gateway HTTPRoute on the
		// `api.<sovereign>` hostname; the Sovereign Console at
		// `console.<sovereign>` proxies its `/api/*` calls through
		// catalyst-api's ingress, so without these registrations the
		// UI's `/api/v1/catalog*` fetches 404'd at chi's NotFound
		// handler. See internal/handler/catalog_proxy.go for the
		// architectural rationale (Fix #5's HTTPRoute template
		// explicitly flagged this as the in-tier follow-up). Routes
		// are session-gated like the rest of /api/v1/* — anonymous
		// catalog browsing is not a UI surface and the catalog client
		// forwards the caller's token so AnonymousReads policy on
		// catalyst-catalog still applies if the upstream is configured
		// for it.
		rg.Get("/api/v1/catalog", h.HandleCatalogList)
		rg.Get("/api/v1/catalog/{name}", h.HandleCatalogGet)
		rg.Get("/api/v1/catalog/{name}/versions/{version}", h.HandleCatalogGetVersion)

		// EPIC-6 (#1101) slice U-Fleet — multi-Sovereign fleet view.
		// Read-only aggregator that backs the new live DashboardPage,
		// per-Sovereign card detail rollup, and cross-Sovereign
		// Applications table. Per ADR-0001 §2.7 the data is read
		// LIVE from each Sovereign's CRs (no separate fleet DB);
		// per INVIOLABLE-PRINCIPLES #5 the per-tier visibility gate
		// is centralised in fleetCallerVisibility() on the handler
		// side. See internal/handler/fleet.go for the contract +
		// per-Sov timeout (4s) so a slow cluster doesn't stall the
		// whole dashboard.
		rg.Get("/api/v1/fleet/sovereigns", h.HandleFleetSovereigns)
		rg.Get("/api/v1/fleet/sovereigns/{id}/summary", h.HandleFleetSovereignSummary)
		rg.Get("/api/v1/fleet/applications", h.HandleFleetApplications)
		// TBD-E14 — fleet-wide treemap surface (mothership only).
		// One cell per Sovereign; layer 2+ deep-links to the per-Sov
		// /dashboard treemap. See handler/fleet_treemap.go.
		rg.Get("/api/v1/fleet/treemap", h.HandleFleetTreemap)

		// EPIC-2 (#1097) slice T+O+P — Application page bundle.
		// PUT/DELETE on the Application CR + topology / upgrade preview
		// + Blueprint publishing (per-Org) + Curate (sovereign-admin).
		// Per ADR-0001 §2.7 the CR is still the source of truth; PUT/
		// DELETE simply patches/removes it and the application-controller
		// (slice C4 #1133) reconciles. Preview endpoints REUSE the
		// install-preview renderer so "looks-good" is byte-identical to
		// the actual write. Blueprint publishing flows through the
		// unified Gitea client per ADR-0001 §4.3.
		rg.Put("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationUpdate)
		rg.Delete("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationDelete)
		rg.Post("/api/v1/sovereigns/{id}/applications/{name}/topology/preview", h.HandleApplicationTopologyPreview)
		rg.Post("/api/v1/sovereigns/{id}/applications/{name}/upgrade/preview", h.HandleApplicationUpgradePreview)
		rg.Post("/api/v1/sovereigns/{id}/blueprints/publish", h.HandleBlueprintPublish)
		rg.Post("/api/v1/sovereigns/{id}/blueprints/curate", h.HandleBlueprintCurate)
		rg.Get("/api/v1/sovereigns/{id}/blueprints/curatable", h.HandleBlueprintListCuratable)
		// Slice Z3 follow-up: YamlEditor's flux-managed Apply path
		// routes through here. The handler creates a branch + commits
		// new content + opens a PR on `<org>/shared-blueprints` so the
		// edit lands via the GitOps flow rather than side-stepping flux.
		rg.Post("/api/v1/sovereigns/{id}/blueprints/edit-pr", h.HandleBlueprintEditPR)

		// EPIC-6 (#1101) slice U-DR-1 — Continuum DR UI surface.
		// GET surfaces the CR for the AppDetail Topology DR section.
		// POST switchover/failback patches spec — the K-Cont-2
		// reconciler picks up the change and runs the 7-step Sequencer
		// + emits the 9 reserved continuum-* audit events on NATS.
		// /audit/continuum mirrors the rbac audit endpoints (slice
		// U5-U8 #1098) but filters on the continuum-* type prefix so
		// future audit-type additions (slice F-1 may add 3 more)
		// require zero handler-side change. Per ADR-0001 §2.7 the CR
		// is the source of truth; per INVIOLABLE-PRINCIPLES #5 the
		// switchover + failback gates enforce owner tier on the
		// Application server-side, the approve gate enforces
		// sovereign-admin server-side, the audit endpoints enforce
		// tier-admin or higher.
		rg.Get("/api/v1/sovereigns/{id}/continuums/{name}", h.HandleContinuumGet)
		rg.Post("/api/v1/sovereigns/{id}/continuums/{name}/switchover", h.HandleContinuumSwitchoverRequest)
		rg.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback", h.HandleContinuumFailbackRequest)
		rg.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback/approve", h.HandleContinuumFailbackApprove)
		rg.Get("/api/v1/sovereigns/{id}/audit/continuum", h.HandleContinuumAuditList)
		rg.Get("/api/v1/sovereigns/{id}/audit/continuum/stream", h.HandleContinuumAuditStream)

		// EPIC-6 iter-6 target-state — singular `/continuum/` path aliases
		// + the rest of the matrix-required surface (PUT for spec
		// updates, switchover preview, status SSE, fleet aggregate, per-
		// Sovereign DR summary). The original plural routes above stay
		// live for back-compat. See continuum_extras.go for handler
		// docs. Per ADR-0001 §2.7 the Continuum CR remains the source
		// of truth; PUT patches spec.rpoSeconds + spec.rtoSeconds and
		// the controller reconciles. Per INVIOLABLE-PRINCIPLES #5 PUT
		// gates on operator tier (REUSES applicationInstallCallerAuthorized).
		rg.Get("/api/v1/sovereigns/{id}/continuum/{name}", h.HandleContinuumGetEnriched)
		rg.Put("/api/v1/sovereigns/{id}/continuum/{name}", h.HandleContinuumPut)
		rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/stream", h.HandleContinuumStream)
		rg.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover", h.HandleContinuumSwitchoverRequest)
		rg.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover/preview", h.HandleContinuumSwitchoverPreview)
		rg.Post("/api/v1/sovereigns/{id}/continuum/{name}/failback", h.HandleContinuumFailbackRequest)
		rg.Post("/api/v1/sovereigns/{id}/continuum/{name}/failback/approve", h.HandleContinuumFailbackApprove)
		rg.Get("/api/v1/fleet/continuum", h.HandleFleetContinuum)
		rg.Get("/api/v1/fleet/sovereigns/{id}/dr-summary", h.HandleFleetSovereignDRSummary)

		// qa-loop iter-1 prefetch Fix #110 (Continuum DR third batch):
		// adds the rest of the DR contract the SovereignConsole renders +
		// the matrix is expected to assert on going forward —
		// replication-status, switchover-history, runbook preflight +
		// playback, quorum status, sovereign-wide replication roll-up,
		// per-Application DR settings GET/PUT. See
		// continuum_dr_extras.go for shapes + fallback semantics.
		// Per INVIOLABLE-PRINCIPLES #5 the playback POST + settings PUT
		// gate on owner tier (REUSE applicationInstallCallerAuthorized);
		// preflight + GET endpoints gate on viewer.
		rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/replication-status", h.HandleContinuumReplicationStatus)
		rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/switchover/history", h.HandleContinuumSwitchoverHistory)
		rg.Get("/api/v1/sovereigns/{id}/continuum/{name}/settings", h.HandleContinuumSettingsGet)
		rg.Put("/api/v1/sovereigns/{id}/continuum/{name}/settings", h.HandleContinuumSettingsPut)
		rg.Post("/api/v1/sovereigns/{id}/dr/runbook/preflight", h.HandleDRRunbookPreflight)
		rg.Post("/api/v1/sovereigns/{id}/dr/runbook/playback", h.HandleDRRunbookPlayback)
		rg.Get("/api/v1/sovereigns/{id}/dr/quorum/status", h.HandleDRQuorumStatus)
		rg.Get("/api/v1/sovereigns/{id}/dr/replication-status", h.HandleDRReplicationStatus)

		// SME-tier user CRUD + role mapping (issue #802, ADR-0003).
		// Owned by the unified-rbac slice of catalyst-api. Tenant
		// scoping is by X-Tenant-Host header (sent by the SPA from
		// window.location.host); the tenant must be registered with
		// tenant_kind=sme. Each create fires the 3-step user-create
		// hook (Keycloak → NewAPI → K8s Secret) per ADR-0003 §3.
		// State is persisted in a flat-file user_provision_state
		// store; on partial failure the response carries the partial
		// state and the steps[] field so the SPA can render progress.
		rg.Post("/api/v1/sme/users", h.HandleCreateSMEUser)
		rg.Get("/api/v1/sme/users", h.HandleListSMEUsers)
		rg.Delete("/api/v1/sme/users/{uuid}", h.HandleDeleteSMEUser)

		// SME tenant provisioning pipeline (issue #804). Marketplace
		// signup → vCluster + bp-* charts + DNS + cert + SSO clients
		// + tenant registry. State machine surfaced as steps[] in the
		// response so the SPA can render a progress timeline. The
		// reconciler is event-driven (NATS heartbeat-to-self per
		// ADR-0003 §3.5) so a Pod restart never strands a half-
		// provisioned tenant. See sme_tenant.go for the full state
		// machine and sme_tenant_gitops.go for the GitOps overlay
		// generator.
		rg.Post("/api/v1/sme/tenants", h.HandleCreateSMETenant)
		rg.Get("/api/v1/sme/tenants", h.HandleListSMETenants)
		rg.Get("/api/v1/sme/tenants/{id}", h.HandleGetSMETenant)
		rg.Post("/api/v1/sme/tenants/{id}/reconcile", h.HandleReconcileSMETenant)
		rg.Delete("/api/v1/sme/tenants/{id}", h.HandleDeleteSMETenant)

		// BSS landing KPI rollup (Refs #1949, TBD-A58). Read-only feed
		// for the /console/bss landing surface (BssLandingPage.tsx →
		// getBssOverview() in ui/src/lib/bss.api.ts). Pre-fix the path
		// returned 404 and the FE flipped `pendingApi=true`, so every
		// tile rendered the "API pending" pill instead of real zeros.
		// Today the handler returns a zero-filled payload — the FE
		// renders the full target-state surface (0 revenue / 0
		// customers — truthful on a fresh Sovereign) from first paint
		// (INVIOLABLE-PRINCIPLES.md #1). The non-zero projection lands
		// with the marketplace/billing wire (siblings:
		// /api/v1/sme/billing/revenue, /api/v1/sme/orders,
		// /api/v1/sme/billing/vouchers/list, /api/v1/sme/tenants).
		rg.Get("/api/v1/sme/bss/overview", h.HandleGetSMEBssOverview)

		// BSS Orders rollup (Wave 6 PR 3). Read-only feed for the
		// /console/bss/orders native React table. Today the handler
		// returns an empty list — the FE renders its full empty-state
		// chrome so the operator sees the target-state surface from
		// first paint (INVIOLABLE-PRINCIPLES.md #1). The non-empty
		// projection lands with the marketplace/billing wire.
		rg.Get("/api/v1/sme/orders", h.HandleListSMEOrders)

		// BSS Revenue rollup (Wave 6 PR 4). Read-only feed for the
		// /console/bss/revenue native React surface (KPI strip + line
		// chart + per-plan breakdown table). Today the handler returns
		// a zero-filled payload — the FE renders its full target-state
		// chrome so the operator sees the surface from first paint
		// (INVIOLABLE-PRINCIPLES.md #1). The non-zero projection lands
		// with the marketplace/billing wire.
		rg.Get("/api/v1/sme/billing/revenue", h.HandleGetSMEBillingRevenue)

		// BSS Vouchers proxy (Wave 6 PR 5 — follow-up to FE PR #1609).
		// Forwards to the SME gateway (gateway.sme.svc.cluster.local:8080)
		// which proxies to the billing service's
		// `/billing/vouchers/{list,issue,revoke}` handlers
		// (core/services/billing/handlers/vouchers.go, gated by
		// requireVoucherIssuer — superadmin OR sovereign-admin per
		// docs/FRANCHISE-MODEL.md §3). The FE bss.api.ts
		// listVouchers/issueVoucher/revokeVoucher all hit these paths.
		// Revoke is registered for BOTH POST (task spec) and DELETE (FE
		// wire) — the handler always forwards as DELETE so the billing
		// service's DELETE /billing/vouchers/revoke/{code} route matches.
		rg.Get("/api/v1/sme/billing/vouchers/list", h.HandleListSMEBillingVouchers)
		rg.Post("/api/v1/sme/billing/vouchers/issue", h.HandleIssueSMEBillingVoucher)
		rg.Post("/api/v1/sme/billing/vouchers/revoke/{code}", h.HandleRevokeSMEBillingVoucher)
		rg.Delete("/api/v1/sme/billing/vouchers/revoke/{code}", h.HandleRevokeSMEBillingVoucher)

		// BSS Purchase proxy (TBD-C15 / #1750). Mirrors the vouchers proxy
		// shape above — catalyst-api forwards to the SME gateway which
		// proxies to the billing service's `POST /billing/purchase`
		// alias (see core/services/billing/handlers/routes.go).
		//
		// Two paths are registered for symmetry with the DoD validator
		// vocabulary on console.<sov-fqdn>:
		//
		//   POST /api/v1/billing/purchase     — operator-visible alias
		//   POST /api/v1/sme/billing/purchase — sme-namespaced (matches
		//                                       /api/v1/sme/billing/{revenue,vouchers/*})
		//
		// Both call the same handler — the upstream is identical. The
		// canonical UI surface remains the marketplace's
		// /api/billing/checkout (CheckoutStep.svelte); these console-side
		// routes exist so the close-audit DoD validator on the Sovereign
		// host stops 404'ing during the marketplace customer-journey
		// re-walk (Step 15 — purchase button).
		rg.Post("/api/v1/billing/purchase", h.HandleSMEBillingPurchase)
		rg.Post("/api/v1/sme/billing/purchase", h.HandleSMEBillingPurchase)

		// Sovereign Console populated views (issue #933). Read-only
		// endpoints the Console pages on console.<sov-fqdn>/console/*
		// hit to render LIVE local-cluster data (HelmReleases, Jobs,
		// catalog, nodes/namespaces/ingresses). Without these the
		// freshly-handed-over Sovereign Console renders only
		// placeholders — useless on day one.
		//
		// All four endpoints use rest.InClusterConfig + the
		// catalyst-api ServiceAccount on the Sovereign cluster, so
		// they continue to function after the Self-Sovereignty
		// Cutover (issue #792) severs every mothership-side tether.
		// On the mothership these endpoints also work (catalyst-api
		// runs in-cluster on contabo too), but the mothership
		// Console serves the per-deployment endpoints instead — the
		// /api/v1/sovereign/* surface is the customer-side Console's
		// data plane.
		rg.Get("/api/v1/sovereign/status", h.HandleSovereignStatus)
		rg.Get("/api/v1/sovereign/jobs", h.HandleSovereignJobs)
		rg.Get("/api/v1/sovereign/apps", h.HandleSovereignApps)
		rg.Get("/api/v1/sovereign/cloud", h.HandleSovereignCloud)
		// PATCH /api/v1/sovereign/apps/{slug}/publish — operator-admin
		// toggle to publish/unpublish a SaaS app on this Sovereign's
		// marketplace. Replaces the deleted /catalog page (PR #1058);
		// chip lives on each AppsPage card, proxies to the in-cluster
		// SME catalog service.
		rg.Patch("/api/v1/sovereign/apps/{slug}/publish", h.HandleSovereignAppPublish)

		// EPIC-3 (#1098) — Sovereign-prefix RBAC access-matrix surface
		// (TBD-F4 / C6-007). Chroot-friendly mirror of
		// /api/v1/sovereigns/{id}/rbac/access-matrix; the id is resolved
		// server-side from SOVEREIGN_FQDN + the catalyst_session cookie
		// (same chain as HandleSovereignSelf). Same query-param filters
		// and response shape as the per-deployment endpoint, so the
		// AccessMatrixPage in the chroot can call this path without
		// first round-tripping to /sovereign/self.
		rg.Get("/api/v1/sovereign/rbac/matrix", h.HandleSovereignRBACMatrix)

		// C6-006 / #1739 — Sovereign-prefix RBAC assign surface. Same
		// wire contract as /api/v1/sovereigns/{id}/rbac/assign but the
		// active deployment id is resolved server-side from
		// SOVEREIGN_FQDN + the catalyst_session cookie (mirrors
		// HandleSovereignRBACMatrix one line above). The Sovereign
		// Console's Members UI hits this seam directly so it doesn't
		// have to round-trip /sovereign/self → /sovereigns/{id}/rbac/assign
		// per click; without it every chroot-side assign 404'd or
		// stamped a generic 500 `rbac-assign-failed: resource not
		// found` when the FE constructed the path with an empty id.
		rg.Post("/api/v1/sovereign/rbac/assign", h.HandleSovereignRBACAssign)

		// TBD-E16 — Sovereign-prefix UserAccess listing. Chroot-friendly
		// mirror of /api/v1/deployments/{depId}/admin/user-access; the
		// depId is resolved server-side from SOVEREIGN_FQDN +
		// catalyst_session cookie (same chain as HandleSovereignSelf).
		// Operator-side smoke probes / external monitors can hit a
		// single stable URL without first round-tripping
		// /sovereign/self. Same response shape as the per-deployment
		// endpoint (userAccessListResponse).
		rg.Get("/api/v1/sovereign/users", h.HandleSovereignUsers)

		// Self-Sovereignty Cutover (issue #792 — parent epic #790). The
		// post-handover step that severs a Sovereign's remaining
		// tethers to the openova-io mothership: gitea-mirror,
		// harbor-projects, harbor-prewarm, registry-pivot DaemonSet,
		// flux-gitrepository-patch, helmrepository-patches,
		// catalyst-api-env-patch, and an egress-block self-test.
		// PodSpec ConfigMaps are shipped by bp-self-sovereign-cutover
		// (issue #791); catalyst-api creates the actual Jobs.
		// Operator-admin gating is provided by RequireSession above —
		// only authenticated console operators can fire this.
		rg.Post("/api/v1/sovereign/cutover/start", h.HandleCutoverStart)
		rg.Get("/api/v1/sovereign/cutover/status", h.HandleCutoverStatus)
		rg.Get("/api/v1/sovereign/cutover/events", h.HandleCutoverEvents)

		// Multi-domain Sovereign — admin "Add another parent domain" flow
		// + live DNS propagation status panel (issue #829, parent #825).
		// LIST returns the operator's parent-domain pool (primary +
		// sme-pool entries). POST queues a new domain through the
		// NS-flip → PowerDNS-zone-create → cert-issue pipeline. Per
		// issue #827 (this PR) the zone-create step now invokes the
		// real PowerDNS REST API via internal/powerdns.Client when the
		// catalyst-api Pod has CATALYST_POWERDNS_API_URL +
		// CATALYST_POWERDNS_API_KEY wired (Sovereign-side); otherwise
		// the call falls back to the stub no-op so contabo-side
		// installs stay green.
		// /propagation fans out to 5 public DNS resolvers via Go's
		// net.Resolver and reports per-resolver convergence so the
		// operator can see the gTLD 48h NS TTL window settle.
		rg.Get("/api/v1/sovereign/parent-domains", h.ListParentDomains)
		rg.Post("/api/v1/sovereign/parent-domains", h.AddParentDomain)
		rg.Delete("/api/v1/sovereign/parent-domains/{name}", h.DeleteParentDomain)
		rg.Get("/api/v1/sovereign/parent-domains/{name}/propagation", h.GetPropagation)
		// D16 secondary-kubeconfig moved OUT of auth group in PR F
		// (2026-05-17). Now at top-level r.Post (alongside
		// /api/v1/internal/deployments/import) so mothership handover
		// POSTs aren't 401'd before any operator session exists.
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

	log.Info("catalyst api listening", "port", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// newTenantEventPublisherFromEnv constructs the cross-process forwarder
// for the `catalyst.tenant.*` event taxonomy used by the
// sandbox_sessions handler (TBD-D35b / #1776). When CATALYST_NATS_URL
// is set, dials NATS and returns the concrete natspub.Publisher; the
// handler's tenantEvents field then publishes
// `catalyst.tenant.sandbox_requested` on every successful Sandbox CR
// Create. When CATALYST_NATS_URL is unset OR the dial fails, returns
// nil so the handler runs in publish-disabled mode — the Sandbox CR
// Create still succeeds + the FE still observes the new sandbox, but
// no audit envelope is emitted. Dial failure is logged + non-fatal so
// a Pod cold-start doesn't crash-loop on a transiently unreachable
// broker (the chroot-Sovereign cold-start sequence brings nats-system
// up after catalyst-system).
//
// Per ADR-0001 §6 the subject taxonomy is `catalyst.tenant.<event>`.
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is env-driven.
//
// TBD-D35c — wired concrete nats.go binding (this PR). The previous
// scaffold returned nil unconditionally because catalyst-api shipped
// without the `nats.go` SDK in its go.mod (see PR #1918 placeholder).
func newTenantEventPublisherFromEnv(log *slog.Logger) handler.TenantEventPublisher {
	url := os.Getenv("CATALYST_NATS_URL")
	if url == "" {
		log.Info("tenant-events: CATALYST_NATS_URL unset — running in publish-disabled mode")
		return nil
	}
	pub, err := natspub.Dial(url, log)
	if err != nil {
		// Non-fatal: log + return nil. The handler's nil-tolerant
		// publish guard keeps the Sandbox-create hot path working.
		log.Warn("tenant-events: NATS dial failed — running in publish-disabled mode",
			"url", url, "err", err,
		)
		return nil
	}
	return pub
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
	// landed in catalyst-api's go.mod via TBD-D35c PR #1918 (the same
	// dependency that backs sandbox_publisher.go).
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
