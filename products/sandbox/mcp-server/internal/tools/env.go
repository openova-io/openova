// env.go — per-process Env construction from OS environment.
//
// The Sandbox controller (Wave 4) injects these env vars on the
// `openova-sandbox-mcp` Deployment. Field naming follows the same
// `SANDBOX_*` convention the controller is targetting in
// products/sandbox/docs/newapi-proxy-contract.md §1 plus
// products/sandbox/docs/architecture.md §6.
//
// Wire contract (the controller fills these; the binary reads them):
//
//	SANDBOX_ORG_ID              = "acme"
//	SANDBOX_ID                  = "emrah"
//	SANDBOX_NAMESPACE           = "sandbox-<owner-uid>"
//	SANDBOX_SOVEREIGN_FQDN      = "t39.omani.works"
//	SANDBOX_REPOS               = "acme/eventforge,acme/internal-tools"
//	SANDBOX_TOKEN               = "<long-lived PAT>"   (fallback bearer)
//	SANDBOX_JWT_SECRET          = "<HS256 secret>"     (validates bearers)
//	SANDBOX_GITEA_BASE_URL      = "http://gitea-http.gitea.svc.cluster.local:3000"
//	SANDBOX_GITEA_TOKEN         = "<machine-account token>"
//	SANDBOX_KUBECONFIG          = ""                   (empty → in-cluster)
//	SANDBOX_OWNER_UID           = "emrah-baysal-at-openova-io"
//	KEYCLOAK_ADMIN_URL          = "http://keycloak.keycloak.svc.cluster.local:8080"
//	KEYCLOAK_ADMIN_TOKEN        = "<admin bearer>" (sandbox-controller-injected)
//	KEYCLOAK_PARENT_REALM       = "master"  (default; controller may override)
//	SANDBOX_DOMAIN_API_URL      = "http://domain.org-services.svc.cluster.local:8086"
//	SANDBOX_MARKETPLACE_API_URL = "http://marketplace-api.marketplace.svc.cluster.local:8082"
//	SANDBOX_TENANT_ID           = "<tenant-uuid>" (scopes domain/byod calls)
//
// SANDBOX_JWT_SECRET empty AND SANDBOX_ORG_ID empty = test mode
// (the registry skips its auth gate so unit tests don't need to mint a
// JWT per call).
//
// D31 active-hot-standby — three additional env vars threaded by the
// sandbox-controller from its chart values (platform/sandbox/chart/
// values.yaml `cnpg.activeHotStandby.*`). When the Sovereign opts in,
// sandbox.db.provision emits a primary + replica Cluster CR pair
// instead of a single Cluster (matching the bp-cnpg-pair pattern under
// platform/cnpg-pair/chart/templates/). Default-off keeps existing
// Sandbox DBs on the single-Cluster shape (zero regression).
//
//	SOVEREIGN_ENABLE_HOT_STANDBY = "true"               (default empty/false)
//	SOVEREIGN_PRIMARY_REGION     = "hz-fsn-rtz-prod"    (openova.io/region label)
//	SOVEREIGN_REPLICA_REGION     = "hz-hel-rtz-prod"    (openova.io/region label)
//
// Wave-12 sandbox.storage.* — SeaweedFS S3 endpoint + per-Sandbox
// credentials, all sourced from the host cluster's platform/seaweedfs
// deployment and a per-Sandbox IAM identity the sandbox-controller mints
// at first bind:
//
//	SANDBOX_STORAGE_S3_ENDPOINT   = "seaweedfs.storage.svc:8333" (unified S3 API)
//	SANDBOX_STORAGE_S3_ACCESS_KEY = "<per-Sandbox IAM access key>"
//	SANDBOX_STORAGE_S3_SECRET_KEY = "<per-Sandbox IAM secret>"
//	SANDBOX_STORAGE_S3_USE_TLS    = "true|false" (default: false; in-cluster)
//	SANDBOX_STORAGE_S3_REGION     = "us-east-1"  (default; SeaweedFS opaque)
package tools

import (
	"os"
	"strings"
)

// NewEnvFromOS reads the canonical SANDBOX_* env vars and returns a
// populated Env. Always succeeds — fields the operator didn't set
// stay zero-valued and the affected tool families surface a clear
// "not configured" error at call time rather than aborting startup.
//
// The MCP server's `main()` calls this once and hands the *Env to
// NewRegistry().
func NewEnvFromOS() *Env {
	env := &Env{
		OrgID:               os.Getenv("SANDBOX_ORG_ID"),
		SandboxID:           os.Getenv("SANDBOX_ID"),
		SandboxNamespace:    os.Getenv("SANDBOX_NAMESPACE"),
		SovereignFQDN:       os.Getenv("SANDBOX_SOVEREIGN_FQDN"),
		SandboxToken:        os.Getenv("SANDBOX_TOKEN"),
		GiteaBaseURL:        os.Getenv("SANDBOX_GITEA_BASE_URL"),
		GiteaToken:          os.Getenv("SANDBOX_GITEA_TOKEN"),
		KubeconfigPath:      os.Getenv("SANDBOX_KUBECONFIG"),
		OwnerUID:            os.Getenv("SANDBOX_OWNER_UID"),
		KeycloakAdminURL:    os.Getenv("KEYCLOAK_ADMIN_URL"),
		KeycloakAdminToken:  os.Getenv("KEYCLOAK_ADMIN_TOKEN"),
		KeycloakParentRealm: os.Getenv("KEYCLOAK_PARENT_REALM"),
		DomainAPIURL:        os.Getenv("SANDBOX_DOMAIN_API_URL"),
		MarketplaceAPIURL:   os.Getenv("SANDBOX_MARKETPLACE_API_URL"),
		// TenantAPIURL — root URL of the Organization tenant-service. The
		// marketplace.app.install MCP tool POSTs `/orgs/<TenantID>/apps`
		// against this URL with the Sandbox HS256 bearer to invoke the
		// canonical install path (tenant-service publishes the
		// `tenant.app_install_requested` NATS event which provisioning
		// consumes — see core/services/tenant/handlers/apps.go:195 and
		// core/services/provisioning/handlers/consumer.go::handleAppInstallRequested).
		// Default unset; sandbox-controller injects via SANDBOX_TENANT_API_URL
		// pointing at the Organization gateway `http://gateway.org-services.svc.cluster.local:8080/api/tenant`.
		// Empty → marketplace.app.install surfaces a clear "not configured" error.
		TenantAPIURL:        os.Getenv("SANDBOX_TENANT_API_URL"),
		TenantID:            os.Getenv("SANDBOX_TENANT_ID"),
		StorageS3Endpoint:   os.Getenv("SANDBOX_STORAGE_S3_ENDPOINT"),
		StorageS3AccessKey:  os.Getenv("SANDBOX_STORAGE_S3_ACCESS_KEY"),
		StorageS3SecretKey:  os.Getenv("SANDBOX_STORAGE_S3_SECRET_KEY"),
		StorageS3Region:     os.Getenv("SANDBOX_STORAGE_S3_REGION"),
		StorageS3UseTLS:     strings.EqualFold(strings.TrimSpace(os.Getenv("SANDBOX_STORAGE_S3_USE_TLS")), "true"),
	}
	if env.KeycloakParentRealm == "" {
		env.KeycloakParentRealm = "master"
	}
	if secret := os.Getenv("SANDBOX_JWT_SECRET"); secret != "" {
		env.JWTSecret = []byte(secret)
	}
	if csv := os.Getenv("SANDBOX_REPOS"); csv != "" {
		parts := strings.Split(csv, ",")
		env.SandboxRepos = make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				env.SandboxRepos = append(env.SandboxRepos, p)
			}
		}
	}
	// D31 active-hot-standby. Toggle parses truthy on {true, 1, yes, on}
	// (case-insensitive); every other value (including empty) leaves the
	// flag false. PrimaryRegion / ReplicaRegion are stored verbatim;
	// sandbox.db.provision applies the same defence-in-depth rule the
	// Organization-tenant gitops writer does (block is rendered only when toggle
	// is true AND both regions are non-empty AND distinct).
	switch strings.ToLower(strings.TrimSpace(os.Getenv("SOVEREIGN_ENABLE_HOT_STANDBY"))) {
	case "true", "1", "yes", "on":
		env.EnableHotStandby = true
	}
	env.PrimaryRegion = strings.TrimSpace(os.Getenv("SOVEREIGN_PRIMARY_REGION"))
	env.ReplicaRegion = strings.TrimSpace(os.Getenv("SOVEREIGN_REPLICA_REGION"))
	return env
}
