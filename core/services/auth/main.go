package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/openova-io/openova/core/services/auth/handlers"
	"github.com/openova-io/openova/core/services/auth/store"
	"github.com/openova-io/openova/core/services/shared/db"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/health"
	"github.com/openova-io/openova/core/services/shared/middleware"
)

func main() {
	// Configuration from environment.
	databaseURL := getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/auth?sslmode=disable")
	valkeyAddr := getEnv("VALKEY_ADDR", "localhost:6379")
	redpandaBrokers := strings.Split(getEnv("REDPANDA_BROKERS", "localhost:9092"), ",")
	jwtSecret := []byte(getEnv("JWT_SECRET", ""))
	jwtRefreshSecret := []byte(getEnv("JWT_REFRESH_SECRET", ""))
	googleClientID := getEnv("GOOGLE_CLIENT_ID", "")
	googleClientSecret := getEnv("GOOGLE_CLIENT_SECRET", "")
	baseURL := getEnv("BASE_URL", "http://localhost:8081")
	smtpHost := getEnv("SMTP_HOST", "localhost")
	smtpPort := getEnv("SMTP_PORT", "587")
	smtpFrom := getEnv("SMTP_FROM", "noreply@openova.io")
	smtpUser := getEnv("SMTP_USER", "")
	smtpPass := getEnv("SMTP_PASS", "")
	corsOrigin := getEnv("CORS_ORIGIN", "*")
	port := getEnv("PORT", "8081")

	// Connect to PostgreSQL.
	pgDB := db.MustConnect(databaseURL)
	defer pgDB.Close()
	slog.Info("connected to PostgreSQL")

	// Connect to Valkey.
	valkeyClient, err := db.ConnectValkey(valkeyAddr)
	if err != nil {
		slog.Error("failed to connect to Valkey", "error", err)
		os.Exit(1)
	}
	defer valkeyClient.Close()
	slog.Info("connected to Valkey")

	// Create events producer.
	producer, err := events.NewProducer(redpandaBrokers)
	if err != nil {
		slog.Error("failed to create events producer", "error", err)
		os.Exit(1)
	}
	defer producer.Close()
	slog.Info("connected to RedPanda")

	// Initialize store and handler.
	authStore := store.New(pgDB)

	// Seed superadmin account (only creates if missing).
	adminEmail := getEnv("ADMIN_EMAIL", "admin@openova.io")
	adminPassword := getEnv("ADMIN_PASSWORD", "")
	if adminPassword != "" {
		if created := authStore.SeedSuperadmin(context.Background(), adminEmail, "Admin", adminPassword); created {
			slog.Info("superadmin seeded", "email", adminEmail)
		}
	}

	h := &handlers.Handler{
		Store:              authStore,
		Valkey:             valkeyClient,
		Producer:           producer,
		JWTSecret:          jwtSecret,
		JWTRefreshSecret:   jwtRefreshSecret,
		GoogleClientID:     googleClientID,
		GoogleClientSecret: googleClientSecret,
		BaseURL:            baseURL,
		SMTPHost:           smtpHost,
		SMTPPort:           smtpPort,
		FromEmail:          smtpFrom,
		SMTPUser:           smtpUser,
		SMTPPass:           smtpPass,
	}

	// Build the main mux.
	mux := http.NewServeMux()

	// Health check — no middleware.
	mux.HandleFunc("GET /healthz", health.Handler())

	// Auth routes — JWT middleware applied only to /auth/me.
	authRoutes := h.Routes()
	jwtMiddleware := middleware.JWTAuth(jwtSecret)

	// Endpoints that identify "the current user" from the Bearer token need
	// JWT middleware. Everything else (login, verify, refresh, etc.) runs
	// without it — they authenticate via their own request body.
	needsAuth := func(r *http.Request) bool {
		if r.Method == http.MethodGet && r.URL.Path == "/auth/me" {
			return true
		}
		if r.Method == http.MethodPost && r.URL.Path == "/auth/logout-all" {
			return true
		}
		// Admin/service endpoints under /auth/admin/ read role from JWT
		// claims and enforce superadmin — must be authenticated.
		if strings.HasPrefix(r.URL.Path, "/auth/admin/") {
			return true
		}
		return false
	}
	mux.Handle("/auth/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if needsAuth(r) {
			jwtMiddleware(authRoutes).ServeHTTP(w, r)
			return
		}
		authRoutes.ServeHTTP(w, r)
	}))

	// Apply global middleware chain.
	handler := middleware.Chain(
		mux,
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.CORS(corsOrigin),
	)

	slog.Info("starting auth service", "port", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
