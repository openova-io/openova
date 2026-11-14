// session.go — sandbox.session.* tools.
//
// These two tools are the agent's self-discovery surface: "who am I"
// (which Claims does the MCP server see on my per-call bearer?) and
// "what is this Sandbox" (which Org, which namespace, which repos are
// attached). They are the first calls a freshly-bootstrapped agent
// will make — every other tool depends on the agent having a coherent
// picture of its scope.
//
// Neither tool requires a capability; they return only what the bearer's
// own already-validated claims authorise them to see. The org-scope
// check is also disabled for these two (registry.go::exemptFromOrgScope).
//
// Wire shape: plain map[string]any — JSON-RPC clients render the
// `content[0].text` blob agents already know how to parse.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func sessionWhoami(ctx context.Context, _ json.RawMessage) (any, error) {
	claims, ok := ClaimsFrom(ctx)
	env := EnvFrom(ctx)
	if !ok {
		// Unauthenticated mode (test env / a developer running the
		// server without a JWT secret) — surface the env we DO know so
		// the agent can still bootstrap.
		return map[string]any{
			"authenticated": false,
			"org_id":        envField(env, "OrgID"),
			"sandbox_id":    envField(env, "SandboxID"),
			"note":          "no bearer claims on request — running in unauthenticated mode",
		}, nil
	}
	out := map[string]any{
		"authenticated":  true,
		"sub":            claims.Subject,
		"email":          claims.Email,
		"email_verified": claims.EmailVerified,
		"role":           claims.Role,
		"org_id":         claims.OrgID,
		"groups":         claims.Groups,
		"capabilities":   claims.Capabilities,
		"typ":            claims.Typ,
		"sovereign_fqdn": claims.SovereignFQDN,
		"deployment_id":  claims.DeploymentID,
	}
	if claims.ExpiresAt != nil {
		out["expires_at"] = claims.ExpiresAt.Time.UTC().Format(time.RFC3339)
	}
	if claims.IssuedAt != nil {
		out["issued_at"] = claims.IssuedAt.Time.UTC().Format(time.RFC3339)
	}
	if claims.ID != "" {
		out["jti"] = claims.ID
	}
	return out, nil
}

func sessionInfo(ctx context.Context, _ json.RawMessage) (any, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, errors.New("sandbox.session.info: server env not populated")
	}
	return map[string]any{
		"sandbox_id":        env.SandboxID,
		"sandbox_namespace": env.SandboxNamespace,
		"org_id":            env.OrgID,
		"sovereign_fqdn":    env.SovereignFQDN,
		"repos":             env.SandboxRepos,
		"gitea_base_url":    env.GiteaBaseURL,
		"kubeconfig":        kubeconfigSource(env),
	}, nil
}

// kubeconfigSource reports where the k8s.read.* client picks up its
// REST config — useful for an agent debugging a "why can't I read pods"
// failure.
func kubeconfigSource(env *Env) string {
	if env.KubeconfigPath != "" {
		return "file:" + env.KubeconfigPath
	}
	return "in-cluster"
}

// envField is a small helper so whoami's unauthenticated branch can
// still surface fields without exploding on a nil Env.
func envField(env *Env, field string) string {
	if env == nil {
		return ""
	}
	switch field {
	case "OrgID":
		return env.OrgID
	case "SandboxID":
		return env.SandboxID
	}
	return ""
}
