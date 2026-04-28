package main

import (
	"log"
	"net/http"
	"os"

	"github.com/openova-io/openova-private/website/marketplace-api/handlers"
	"github.com/openova-io/openova-private/website/marketplace-api/store"
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
	smtpHost := getEnv("SMTP_HOST", "stalwart-mail.stalwart.svc.cluster.local")
	smtpPort := getEnv("SMTP_PORT", "25")
	fromEmail := getEnv("FROM_EMAIL", "marketplace@openova.io")

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
