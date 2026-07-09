package main

import (
	"log"
	"net/http"
	"os"

	"github.com/openova-io/openova-private/website/marketplace-api/handlers"
	"github.com/openova-io/openova-private/website/marketplace-api/store"
)

// SMTP defaults (#4919). defaultSMTPHost is intentionally EMPTY: marketplace-api
// must NOT fall back to the in-cluster Stalwart Service
// (stalwart-mail.stalwart.svc.cluster.local) — no Stalwart runs on a fresh
// Sovereign, so that former default silently black-holed the signup PIN. The
// real relay coordinates arrive via SMTP_HOST/PORT/FROM_EMAIL/SMTP_USER/SMTP_PASS
// injected from the durable catalyst-system/sovereign-smtp-credentials seed
// (#4748/#4749 → mail.openova.io:587) — the same seed the org-services auth
// service (#934) and catalyst-api CATALYST_SMTP_* read. An empty host means "no
// send attempt", which is the safe no-regression posture (never dial a dead host).
const (
	defaultSMTPHost  = ""
	defaultSMTPPort  = "587"
	defaultFromEmail = "marketplace@openova.io"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	jwtSecret := getEnv("JWT_SECRET", "dev-secret-change-me")
	allowOrigin := getEnv("CORS_ORIGIN", "https://openova.io")
	gitRepoPath := getEnv("GIT_REPO_PATH", "/data/repo")
	smtpHost := getEnv("SMTP_HOST", defaultSMTPHost)
	smtpPort := getEnv("SMTP_PORT", defaultSMTPPort)
	fromEmail := getEnv("FROM_EMAIL", defaultFromEmail)
	smtpUser := getEnv("SMTP_USER", "")
	smtpPass := getEnv("SMTP_PASS", "")

	// Surface the resolved SMTP wiring at startup (never the password) so an
	// operator debugging "signup PIN never arrived" can confirm at a glance
	// whether the durable relay seed reached the pod — the exact diagnostic
	// signal missing during the hw233 walk (#4919). Empty host ⇒ disabled.
	if smtpHost == "" {
		log.Println("marketplace-api: SMTP not configured (SMTP_HOST empty) — signup email notifications disabled")
	} else {
		authMode := "none"
		if smtpUser != "" && smtpPass != "" {
			authMode = "plain"
		}
		log.Printf("marketplace-api: SMTP relay %s:%s from=%q auth=%s", smtpHost, smtpPort, fromEmail, authMode)
	}

	s := store.NewMemoryStore()

	h := &handlers.Handler{
		Store:       s,
		JWTSecret:   []byte(jwtSecret),
		AllowOrigin: allowOrigin,
		GitRepoPath: gitRepoPath,
		SMTPHost:    smtpHost,
		SMTPPort:    smtpPort,
		FromEmail:   fromEmail,
	}

	mux := http.NewServeMux()

	// Catalog (public, cacheable)
	mux.HandleFunc("/api/marketplace/apps", h.CORS(h.ListApps))
	mux.HandleFunc("/api/marketplace/apps/", h.CORS(h.GetApp))
	mux.HandleFunc("/api/marketplace/bundles", h.CORS(h.ListBundles))

	// Provisioning
	mux.HandleFunc("/api/marketplace/provisions", h.CORS(h.CreateProvision))
	mux.HandleFunc("/api/marketplace/provisions/", h.CORS(h.GetProvisionStatus))

	// Tenant management (authenticated)
	mux.HandleFunc("/api/marketplace/tenants/", h.CORS(h.TenantRouter))

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Println("marketplace-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
