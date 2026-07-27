package gitops

import "strings"

// proxyImage re-tags an upstream image reference through the Sovereign-local
// Harbor proxy-cache so it passes the `harbor-proxy-pull` Kyverno
// ClusterPolicy (Enforce), which DENIES any image not matching the
// `*/proxy-*/*` glob (#3785, follow-up to #3761, Refs #3376).
//
// The per-tenant app Deployments are synced INTO the tenant vCluster, whose
// syncer schedules the BACKING Pod on the HOST cluster (in the
// `tenant-<slug>` host namespace) — where the host kyverno ClusterPolicy
// enforces. A raw `wordpress:6-apache` (Docker Hub) is therefore DENIED and
// the customer's purchased app never starts (the funnel's terminal
// acceptance). Re-tagging through the registry-appropriate Harbor proxy
// project (`proxy-dockerhub` / `proxy-ghcr` / `proxy-quay` / `proxy-gcr` /
// `proxy-k8s`) makes the reference match the glob.
//
// `mirror` is the Sovereign Harbor host (e.g. "harbor.openova.io",
// operator-overridable; cutover Step-04 flips it to harbor.<fqdn>). An empty
// mirror or an already-proxied reference (`<host>/proxy-*/...`) is returned
// unchanged. Registries without an established Harbor proxy project (e.g.
// lscr.io) are also returned unchanged — no regression for those apps, which
// keep their pre-#3785 behaviour; they are day-2 catalog entries, not the
// funnel terminal.
func proxyImage(image, mirror string) string {
	image = strings.TrimSpace(image)
	mirror = strings.TrimSpace(strings.TrimRight(mirror, "/"))
	if image == "" || mirror == "" {
		return image
	}
	// Already routed through a Harbor proxy project — leave as-is.
	if strings.HasPrefix(image, mirror+"/proxy-") {
		return image
	}

	// Split the optional registry host off the front. Docker Hub references
	// have no host (`wordpress:tag`, `chatwoot/chatwoot:tag`); explicit-
	// registry references start with a host containing a dot or colon
	// (`ghcr.io/...`, `lscr.io/...`).
	first := image
	if i := strings.IndexByte(image, '/'); i >= 0 {
		first = image[:i]
	}
	hasRegistryHost := strings.ContainsAny(first, ".:") && strings.Contains(image, "/")

	// registryToProxy maps an upstream registry host to its Harbor
	// proxy-cache project. Only registries with an established proxy project
	// (the live convention — see platform/**/values.yaml + chart comments)
	// are listed; the DockerHub project is `proxy-dockerhub` (NOT
	// `proxy-docker` — platform/openbao/chart/Chart.yaml).
	registryToProxy := map[string]string{
		"docker.io":       "proxy-dockerhub",
		"ghcr.io":         "proxy-ghcr",
		"quay.io":         "proxy-quay",
		"gcr.io":          "proxy-gcr",
		"registry.k8s.io": "proxy-k8s",
	}

	if !hasRegistryHost {
		// Bare Docker Hub reference → harbor/proxy-dockerhub/<repo>:<tag>.
		return mirror + "/proxy-dockerhub/" + image
	}
	proj, ok := registryToProxy[first]
	if !ok {
		// Unknown registry without a Harbor proxy project — leave unchanged
		// (no regression; not a funnel-terminal image).
		return image
	}
	return mirror + "/" + proj + "/" + strings.TrimPrefix(image, first+"/")
}

// AppSpec defines how to deploy an app.
type AppSpec struct {
	Image    string
	Port     int
	EnvVars  map[string]string // static env vars
	NeedsDB  string            // "postgres", "mysql", or ""
	RAMMI    string            // resource request memory
	CPUMilli string            // resource request cpu
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
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"umami": {
		Image: "ghcr.io/umami-software/umami:postgresql-v2.9.0", Port: 3000,
		NeedsDB: "postgres",
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"cal-com": {
		Image: "calcom/cal.com:v6.2.0", Port: 3000,
		NeedsDB: "postgres",
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{
			"NEXT_PUBLIC_WEBAPP_URL": "https://TENANT.omani.rest/calcom",
			"NEXTAUTH_URL":           "https://TENANT.omani.rest/calcom",
		},
	},
	"chatwoot": {
		Image: "chatwoot/chatwoot:v4.16.1", Port: 3000,
		NeedsDB: "postgres",
		RAMMI:   "512Mi", CPUMilli: "200m",
		EnvVars: map[string]string{
			"RAILS_ENV": "production",
			"REDIS_URL": "redis://redis:6379",
		},
	},
	"invoiceshelf": {
		Image: "invoiceshelf/invoiceshelf:2.4.1", Port: 8080,
		NeedsDB: "mysql",
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"ghost": {
		Image: "ghost:5-alpine", Port: 2368,
		NeedsDB: "mysql",
		RAMMI:   "256Mi", CPUMilli: "100m",
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
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"gitea": {
		Image: "gitea/gitea:1.27.0-rootless", Port: 3000,
		NeedsDB: "postgres",
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"uptime-kuma": {
		Image: "louislam/uptime-kuma:1.23.17", Port: 3001,
		NeedsDB: "",
		// #5410 — was 128Mi/50m, which OOMKilled forever. Live on hw290 Org
		// theta-corp: 49 restarts, lastState.terminated.reason=OOMKilled, at
		// exactly the declared 128Mi ceiling. Uptime Kuma is Node.js with an
		// embedded SQLite store; its baseline working set exceeds 128Mi before
		// it finishes booting, so it OOMs, restarts, and OOMs again — the app
		// installs, reports provisioned, and never once serves a request.
		//
		// This value is BOTH the request and the hard limit: qosResources()
		// returns it for both on every paid plan so the pod is Guaranteed QoS
		// (which the per-Org LimitRange's maxLimitRequestRatio {cpu:1,memory:1}
		// requires to admit it at all). So there is no burst headroom to absorb
		// an under-estimate — the declared number is the ceiling, full stop.
		//
		// 512Mi matches the tier already used for the other Node-heavy apps in
		// this map (chatwoot, rocket-chat). CPU 50m→100m because the liveness
		// probe was also failing during boot; 50m is 5% of a core, and Node
		// startup is CPU-hungry even when steady-state draw is small. Both are
		// deliberately modest — region-A already sits at 98-100% CPU *requests*
		// (#5393), so this is sized to stop a proven hard failure, not to be
		// generous.
		RAMMI: "512Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"vaultwarden": {
		Image: "vaultwarden/server:1.37.0", Port: 80,
		NeedsDB: "",
		RAMMI:   "128Mi", CPUMilli: "50m",
		EnvVars: map[string]string{},
	},
	"bookstack": {
		Image: "lscr.io/linuxserver/bookstack:26.05.2", Port: 80,
		NeedsDB: "mysql",
		RAMMI:   "256Mi", CPUMilli: "100m",
		// linuxserver/bookstack reads DB_HOST/DB_USER/DB_PASS/DB_DATABASE
		// (NOT WORDPRESS_DB_*) and refuses to start without APP_KEY. Without
		// the bookstack DBEnvStyle the manifest emitted only WordPress-shape
		// vars and the container halted in init with "The application key is
		// missing, halting init!" — pod stayed 1/1 Running with no
		// application listening, ingress returned 502.
		DBEnvStyle: "bookstack",
		EnvVars:    map[string]string{},
	},
	"nocodb": {
		Image: "nocodb/nocodb:2026.07.0", Port: 8080,
		NeedsDB: "postgres",
		RAMMI:   "256Mi", CPUMilli: "100m",
		EnvVars: map[string]string{},
	},
	"listmonk": {
		Image: "listmonk/listmonk:v6.2.0", Port: 9000,
		NeedsDB: "postgres",
		RAMMI:   "128Mi", CPUMilli: "50m",
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
		Image: "rocket.chat:8.5.1", Port: 3000,
		NeedsDB: "",
		RAMMI:   "512Mi", CPUMilli: "200m",
		EnvVars: map[string]string{},
	},
	"formbricks": {
		Image: "formbricks/formbricks:3.6.0", Port: 3000,
		NeedsDB: "postgres",
		RAMMI:   "256Mi", CPUMilli: "100m",
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
			Image:    "nginx:1.31.3-alpine",
			Port:     80,
			NeedsDB:  "",
			RAMMI:    "64Mi",
			CPUMilli: "25m",
			EnvVars:  map[string]string{},
		}
	}
	return AppSpec{}
}
