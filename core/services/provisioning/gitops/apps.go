package gitops

// AppSpec defines how to deploy an app.
type AppSpec struct {
	Image       string
	Port        int
	EnvVars     map[string]string // static env vars
	NeedsDB     string            // "postgres", "mysql", or ""
	RAMMI       string            // resource request memory
	CPUMilli    string            // resource request cpu
	// DBEnvStyle selects the env var shape used for the wired DB secret.
	// "wordpress" → WORDPRESS_DB_* (WordPress, BookStack, InvoiceShelf, default).
	// "ghost"     → database__client + database__connection__{host,user,password,database}.
	// "" (empty) keeps the legacy WordPress shape for backwards compatibility.
	DBEnvStyle string
	// ContentPath, when set, mounts a PVC ("app-<slug>-data", 2Gi) at this
	// path inside the container — needed for Ghost's /var/lib/ghost/content.
	ContentPath string
	// InitCommand, when non-empty, runs as an initContainer BEFORE the main
	// container starts, sharing the same image and env vars. Used for apps
	// whose binary ships a --install flag that must be invoked once to
	// bootstrap schema (listmonk — issue #101). The command is executed via
	// `sh -c` so shell constructs (|| true, &&, 2>&1) are available.
	InitCommand string
}

// KnownApps maps catalog app slugs to their deployment specs.
var KnownApps = map[string]AppSpec{
	"wordpress": {
		Image: "wordpress:6-apache", Port: 80,
		NeedsDB: "mysql",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"umami": {
		Image: "ghcr.io/umami-software/umami:postgresql-latest", Port: 3000,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"cal-com": {
		Image: "calcom/cal.com:latest", Port: 3000,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{
			"NEXT_PUBLIC_WEBAPP_URL": "https://TENANT.omani.rest/calcom",
			"NEXTAUTH_URL":          "https://TENANT.omani.rest/calcom",
		},
	},
	"chatwoot": {
		Image: "chatwoot/chatwoot:latest", Port: 3000,
		NeedsDB: "postgres",
		RAMMI: "512Mi", CPUMilli: "200m",
		EnvVars: map[string]string{
			"RAILS_ENV":       "production",
			"REDIS_URL":       "redis://redis:6379",
		},
	},
	"invoiceshelf": {
		Image: "invoiceshelf/invoiceshelf:latest", Port: 8080,
		NeedsDB: "mysql",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"ghost": {
		Image: "ghost:5-alpine", Port: 2368,
		NeedsDB: "mysql",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{
			"NODE_ENV": "production",
			"url":      "https://TENANT.omani.rest/ghost",
		},
		DBEnvStyle:  "ghost",
		ContentPath: "/var/lib/ghost/content",
	},
	"nextcloud": {
		Image: "nextcloud:29-apache", Port: 80,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"gitea": {
		Image: "gitea/gitea:1-rootless", Port: 3000,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"uptime-kuma": {
		Image: "louislam/uptime-kuma:1", Port: 3001,
		NeedsDB: "",
		RAMMI: "128Mi", CPUMilli: "50m",
		EnvVars: map[string]string{},
	},
	"vaultwarden": {
		Image: "vaultwarden/server:latest", Port: 80,
		NeedsDB: "",
		RAMMI: "128Mi", CPUMilli: "50m",
		EnvVars: map[string]string{},
	},
	"bookstack": {
		Image: "lscr.io/linuxserver/bookstack:latest", Port: 80,
		NeedsDB: "mysql",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"nocodb": {
		Image: "nocodb/nocodb:latest", Port: 8080,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"listmonk": {
		Image: "listmonk/listmonk:latest", Port: 9000,
		NeedsDB: "postgres",
		RAMMI: "128Mi", CPUMilli: "50m",
		EnvVars: map[string]string{},
		// listmonk reads config.toml and only honours LISTMONK_db__* envs —
		// DATABASE_URL is ignored. Issue #101.
		DBEnvStyle: "listmonk",
		// Bootstrap schema on first run. --yes skips prompts, --idempotent
		// makes --install a no-op if the schema already exists. Falling
		// through to --upgrade handles the in-place-upgrade case when
		// listmonk's image version bumps. `|| true` at the end ensures
		// the init container always succeeds so a restarted pod doesn't
		// get stuck on an already-migrated DB.
		InitCommand: "./listmonk --install --yes --idempotent 2>&1 || ./listmonk --upgrade --yes 2>&1 || true",
	},
	"rocket-chat": {
		Image: "rocket.chat:latest", Port: 3000,
		NeedsDB: "",
		RAMMI: "512Mi", CPUMilli: "200m",
		EnvVars: map[string]string{},
	},
	"formbricks": {
		Image: "formbricks/formbricks:latest", Port: 3000,
		NeedsDB: "postgres",
		RAMMI: "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
}

// LookupAppSpec returns (spec, true) if the slug has a real template, or
// (zero AppSpec, false) if not. Callers that need a hard guarantee — the
// InstallApp handler in particular — MUST check the bool before proceeding
// so we never silently deploy an nginx placeholder under a real app's name.
//
// See issue #102 — before this change, GetAppSpec silently returned an
// nginx placeholder for unknown slugs, which meant a tenant could install
// "plane" and get an nginx welcome page while the UI reported "installed OK".
func LookupAppSpec(slug string) (AppSpec, bool) {
	spec, ok := KnownApps[slug]
	return spec, ok
}

// GetAppSpec returns the deployment spec for an app slug. Kept for
// backwards-compat with callers that intentionally want the nginx fallback
// (placeholder-for-demo cases only) — prefer LookupAppSpec in new code.
//
// Nginx is ONLY returned when the caller passes the literal slug
// "placeholder"; unknown slugs return an empty spec so the generator
// emits a pod that never starts (fail loud rather than silently succeed).
func GetAppSpec(slug string) AppSpec {
	if spec, ok := KnownApps[slug]; ok {
		return spec
	}
	if slug == "placeholder" {
		return AppSpec{
			Image:    "nginx:1-alpine",
			Port:     80,
			NeedsDB:  "",
			RAMMI:    "64Mi",
			CPUMilli: "25m",
			EnvVars:  map[string]string{},
		}
	}
	return AppSpec{}
}
