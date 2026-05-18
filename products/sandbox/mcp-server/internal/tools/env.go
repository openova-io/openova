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
//	SANDBOX_SOVEREIGN_FQDN      = "acme.openova.io"
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
//
// SANDBOX_JWT_SECRET empty AND SANDBOX_ORG_ID empty = test mode
// (the registry skips its auth gate so unit tests don't need to mint a
// JWT per call).
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
	return env
}
