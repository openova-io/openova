// gitea.go — real handlers for the gitea.* read tools.
//
// All four (gitea.repo.list / gitea.repo.get / gitea.pr.list /
// gitea.pr.get) delegate to the canonical Gitea client at
// `core/controllers/pkg/gitea`. We do NOT re-implement HTTP — the
// pkg/gitea client already has the correct error mapping
// (ErrOrgNotFound / ErrRepoNotFound / *HTTPError), retry posture, and
// JSON-decoder shape used across every controller in the system.
//
// Each handler:
//
//  1. Pulls the Sandbox's Gitea base URL + machine-account token from
//     the Env on the ctx (populated by Registry.Call before invoking).
//  2. Parses + validates the arguments JSON.
//  3. Scopes the request to claims.OrgID — for `gitea.repo.list` this
//     pins the listed Org to the bearer's Org; for repo/pr operations
//     it rejects any owner that doesn't match (so a bearer with
//     OrgID=acme cannot enumerate repos under `bankdhofar/...`).
//  4. Calls the pkg/gitea method, wraps the typed errors into a
//     short JSON-friendly shape ({"error":"...","status":404}).
//
// The wrapped output is the raw pkg/gitea struct (gitea.Repo /
// gitea.PullRequest) — its JSON tags already match what the MCP agent
// expects, so we don't need a separate translation layer.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	gitea "github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// giteaClient lazily builds a *gitea.Client from the Env on ctx.
// Centralised so every handler reports the same "gitea not configured"
// error when the operator forgot to mount the Sandbox-Gitea-Token Secret.
func giteaClient(ctx context.Context) (*gitea.Client, error) {
	env := EnvFrom(ctx)
	if env == nil || env.GiteaBaseURL == "" {
		return nil, errors.New("gitea: server env has no GITEA_BASE_URL (controller didn't wire sandbox-gitea-token)")
	}
	if env.GiteaToken == "" {
		return nil, errors.New("gitea: server env has no GITEA_TOKEN (controller didn't mount sandbox-gitea-token Secret)")
	}
	// 15s call timeout — gitea is in-cluster so anything slower is a
	// degraded backend; the MCP user wants a fast error.
	return gitea.NewWithHTTP(env.GiteaBaseURL, env.GiteaToken,
		&http.Client{Timeout: 15 * time.Second}), nil
}

// resolveOwner returns the Gitea Org slug to scope this call to. The
// MCP server's Env binds the pod to exactly one Org (env.OrgID); we use
// that as the default when the call arguments omit `owner`. If the
// caller passes an explicit `owner`, it MUST match env.OrgID — agents
// cannot cross-tenant by passing a different owner string.
func resolveOwner(env *Env, want string) (string, error) {
	if want == "" {
		want = env.OrgID
	}
	if want == "" {
		return "", errors.New("gitea: no owner supplied and Sandbox Env has no ORG_ID")
	}
	if env.OrgID != "" && want != env.OrgID {
		return "", fmt.Errorf("gitea: owner %q does not match Sandbox Org %q (cross-tenant access denied)", want, env.OrgID)
	}
	return want, nil
}

// --- gitea.repo.list ---------------------------------------------------

type giteaRepoListArgs struct {
	// Owner — the Gitea Org slug. Defaults to env.OrgID; if supplied,
	// must match.
	Owner string `json:"owner,omitempty"`
}

func giteaRepoList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args giteaRepoListArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("gitea.repo.list: invalid arguments: %w", err)
		}
	}
	env := EnvFrom(ctx)
	owner, err := resolveOwner(env, args.Owner)
	if err != nil {
		return nil, err
	}
	client, err := giteaClient(ctx)
	if err != nil {
		return nil, err
	}
	repos, err := client.ListOrgRepos(ctx, owner)
	if err != nil {
		if errors.Is(err, gitea.ErrOrgNotFound) {
			return map[string]any{"error": "org not found", "owner": owner, "status": http.StatusNotFound}, nil
		}
		return nil, fmt.Errorf("gitea.repo.list: %w", err)
	}
	return map[string]any{
		"owner": owner,
		"count": len(repos),
		"repos": repos,
	}, nil
}

func schemaGiteaRepoList() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"owner": map[string]any{"type": "string", "description": "Gitea Org slug. Defaults to the Sandbox's Org."},
		},
		"additionalProperties": false,
	}
}

// --- gitea.repo.get ----------------------------------------------------

type giteaRepoGetArgs struct {
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo"`
}

func giteaRepoGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var args giteaRepoGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("gitea.repo.get: invalid arguments: %w", err)
	}
	if args.Repo == "" {
		return nil, errors.New("gitea.repo.get: `repo` is required")
	}
	env := EnvFrom(ctx)
	owner, err := resolveOwner(env, args.Owner)
	if err != nil {
		return nil, err
	}
	client, err := giteaClient(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := client.GetRepo(ctx, owner, args.Repo)
	if err != nil {
		if errors.Is(err, gitea.ErrRepoNotFound) {
			return map[string]any{"error": "repo not found", "owner": owner, "repo": args.Repo, "status": http.StatusNotFound}, nil
		}
		return nil, fmt.Errorf("gitea.repo.get: %w", err)
	}
	return repo, nil
}

func schemaGiteaRepoGet() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"repo"},
		"properties": map[string]any{
			"owner": map[string]any{"type": "string"},
			"repo":  map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// --- gitea.pr.list -----------------------------------------------------

type giteaPRListArgs struct {
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo"`
	State string `json:"state,omitempty"` // "open" | "closed" | "all"
	Head  string `json:"head,omitempty"`  // "<branch>" or "<org>:<branch>"
	Base  string `json:"base,omitempty"`
}

func giteaPRList(ctx context.Context, raw json.RawMessage) (any, error) {
	var args giteaPRListArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("gitea.pr.list: invalid arguments: %w", err)
	}
	if args.Repo == "" {
		return nil, errors.New("gitea.pr.list: `repo` is required")
	}
	env := EnvFrom(ctx)
	owner, err := resolveOwner(env, args.Owner)
	if err != nil {
		return nil, err
	}
	client, err := giteaClient(ctx)
	if err != nil {
		return nil, err
	}
	state := gitea.PullRequestState(args.State)
	switch state {
	case "", gitea.PRStateOpen, gitea.PRStateClosed, gitea.PRStateAll:
		// ok
	default:
		return nil, fmt.Errorf("gitea.pr.list: invalid state %q (want open|closed|all)", args.State)
	}
	prs, err := client.ListPullRequests(ctx, owner, args.Repo, gitea.ListPRsOpts{
		State: state,
		Head:  args.Head,
		Base:  args.Base,
	})
	if err != nil {
		if errors.Is(err, gitea.ErrRepoNotFound) {
			return map[string]any{"error": "repo not found", "owner": owner, "repo": args.Repo, "status": http.StatusNotFound}, nil
		}
		return nil, fmt.Errorf("gitea.pr.list: %w", err)
	}
	return map[string]any{
		"owner": owner,
		"repo":  args.Repo,
		"state": string(state),
		"count": len(prs),
		"prs":   prs,
	}, nil
}

func schemaGiteaPRList() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"repo"},
		"properties": map[string]any{
			"owner": map[string]any{"type": "string"},
			"repo":  map[string]any{"type": "string"},
			"state": map[string]any{"type": "string", "enum": []string{"open", "closed", "all"}},
			"head":  map[string]any{"type": "string"},
			"base":  map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

// --- gitea.pr.get ------------------------------------------------------

type giteaPRGetArgs struct {
	Owner  string `json:"owner,omitempty"`
	Repo   string `json:"repo"`
	Number int64  `json:"number"`
}

func giteaPRGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var args giteaPRGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("gitea.pr.get: invalid arguments: %w", err)
	}
	if args.Repo == "" || args.Number <= 0 {
		return nil, errors.New("gitea.pr.get: `repo` and positive `number` are required")
	}
	env := EnvFrom(ctx)
	owner, err := resolveOwner(env, args.Owner)
	if err != nil {
		return nil, err
	}
	client, err := giteaClient(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := client.GetPullRequest(ctx, owner, args.Repo, args.Number)
	if err != nil {
		if gitea.IsNotFound(err) {
			return map[string]any{"error": "pr not found", "owner": owner, "repo": args.Repo, "number": args.Number, "status": http.StatusNotFound}, nil
		}
		return nil, fmt.Errorf("gitea.pr.get: %w", err)
	}
	return pr, nil
}

func schemaGiteaPRGet() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{"repo", "number"},
		"properties": map[string]any{
			"owner":  map[string]any{"type": "string"},
			"repo":   map[string]any{"type": "string"},
			"number": map[string]any{"type": "integer", "minimum": 1},
		},
		"additionalProperties": false,
	}
}
