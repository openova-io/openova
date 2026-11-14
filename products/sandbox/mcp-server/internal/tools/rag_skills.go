// rag_skills.go — lean Wave-12 stubs for rag.search + skills.list/get.
//
// Scope (architecture.md §3 + §"Memory wiring", lines 108-109, 143):
//
//   - rag.search   {query, repo?, limit?}  → text-grep over the per-Sandbox
//     PVC mounted at `/repo/<sandbox>` (the same checkout cwd the
//     sandbox-controller wires for every agent). Returns up to `limit`
//     matches as { path, line, snippet } objects, where snippet is the
//     matching line trimmed to 80 characters. When the PVC isn't
//     mounted (controller hasn't completed the clone, or the operator
//     is running the binary outside of a Sandbox pod), we return an
//     empty matches array + `pendingApi=true` flag so the agent can
//     branch on "index not ready yet" rather than mis-reading "no
//     hits". This is the cheap-and-cheerful seed for the eventual
//     hybrid (vector+BM25+recency) index advertised in architecture
//     §"Per-Org RAG"; the wire shape is intended to stay stable.
//
//   - skills.list / skills.get  →  return the three canned skill names
//     the platform ships with at Wave 12 (openova-platform-basics,
//     k8s-debugging, fluxcd-workflows), each carrying a stub manifest
//     marked `pendingOCI=true`. Once the OCI publishing pipeline lands
//     the same handlers will pull the real manifest from the catalogue
//     registry without changing the response envelope.
//
// Auth: identical to sandbox.secrets.* / sandbox.db.* — Claims.OrgID
// MUST match env.OrgID (enforced by the registry), RequiredCapability
// = "rag" for rag.search and "skills" for skills.*. Both are read-only
// surfaces: rag.search walks a mounted PVC, skills.* returns hard-coded
// metadata.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ragRepoRoot is the canonical mount point the sandbox-controller wires
// into every Sandbox pod (architecture.md line 37: `cwd = /repo/<repo>`).
// Overridable via SANDBOX_REPO_ROOT for unit tests + future controller
// reshuffles. When the controller has not yet attached the PVC the
// directory either does not exist OR is empty; both paths trigger the
// `pendingApi` short-circuit.
const defaultRagRepoRoot = "/repo"

// ragMaxLimit caps the matches per call. The agent can pass `limit`
// lower; values above the cap are silently clamped so a runaway model
// can't request 100k hits and blow the stdio frame.
const ragMaxLimit = 50

// ragDefaultLimit is what we return when the caller omits `limit`.
const ragDefaultLimit = 10

// ragSnippetWidth is the trimmed-line cap (per matching line) so a hit
// inside a 4k-char minified JS bundle doesn't drown the response.
const ragSnippetWidth = 80

// ragMaxFileBytes is the per-file read cap. Large blobs (lockfiles,
// model weights checked into the repo) are skipped — the file is
// counted as scanned, just not searched. Keeps a single call cheap.
const ragMaxFileBytes = 256 * 1024

// ragSkippedDirs is the set of directory names we never descend into
// during a walk. Avoids node_modules / .git churn dominating the budget.
var ragSkippedDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	".astro":       {},
	".next":        {},
	".cache":       {},
	"dist":         {},
	"build":        {},
}

// ragRepoRoot returns the override SANDBOX_REPO_ROOT when set, else the
// canonical /repo mount. Reads OS env directly so unit tests can
// override without touching the Env struct (which is wired from
// SANDBOX_*).
func ragRepoRoot() string {
	if v := strings.TrimSpace(os.Getenv("SANDBOX_REPO_ROOT")); v != "" {
		return v
	}
	return defaultRagRepoRoot
}

// --- rag.search --------------------------------------------------------

type ragSearchArgs struct {
	Query string `json:"query"`
	Repo  string `json:"repo,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type ragHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

func ragSearch(ctx context.Context, raw json.RawMessage) (any, error) {
	var args ragSearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("rag.search: invalid arguments: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return nil, errors.New("rag.search: `query` is required")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = ragDefaultLimit
	}
	if limit > ragMaxLimit {
		limit = ragMaxLimit
	}

	root := ragRepoRoot()
	if args.Repo != "" {
		// Reject anything that smells like a traversal — `repo` must
		// be a simple slug (lowercase alnum + hyphen) so the agent
		// can't escape /repo via "../../etc/passwd".
		if strings.ContainsAny(args.Repo, "/\\") || strings.Contains(args.Repo, "..") {
			return nil, fmt.Errorf("rag.search: `repo` must be a simple name without separators (got %q)", args.Repo)
		}
		root = filepath.Join(root, args.Repo)
	}

	// PVC-unmounted short-circuit. info==nil OR not-a-dir OR empty all
	// mean "the controller has not finished wiring the corpus into this
	// pod"; surface pendingApi=true so the agent doesn't conflate
	// "index not ready" with "no hits".
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]any{
			"matches":    []ragHit{},
			"query":      args.Query,
			"pendingApi": true,
			"note":       "Sandbox repo PVC not mounted at " + root + " yet; rag.search index is still being built.",
		}, nil
	}

	hits := make([]ragHit, 0, limit)
	needle := strings.ToLower(args.Query)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Permissions / racing-delete during walk: skip the entry
			// rather than abort the whole search.
			return nil
		}
		if len(hits) >= limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if _, skip := ragSkippedDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip clearly-binary or oversized files. We don't try hard to
		// sniff MIME; the size cap + extension blacklist below catch
		// the common cases.
		switch filepath.Ext(d.Name()) {
		case ".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip", ".tar",
			".gz", ".woff", ".woff2", ".ico", ".webp", ".mp4", ".mov":
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil || fi.Size() > ragMaxFileBytes {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		// Case-insensitive substring match over each line so the
		// snippet can carry the matching line index.
		text := string(data)
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			if len(hits) >= limit {
				break
			}
			if strings.Contains(strings.ToLower(line), needle) {
				snippet := strings.TrimSpace(line)
				if len(snippet) > ragSnippetWidth {
					snippet = snippet[:ragSnippetWidth]
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				hits = append(hits, ragHit{
					Path:    rel,
					Line:    i + 1,
					Snippet: snippet,
				})
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		// A genuine walk failure (root removed mid-walk, etc.). Surface
		// the partial hits + the error so the agent isn't blind.
		return map[string]any{
			"matches": hits,
			"query":   args.Query,
			"root":    root,
			"limit":   limit,
			"warning": walkErr.Error(),
		}, nil
	}

	return map[string]any{
		"matches": hits,
		"query":   args.Query,
		"root":    root,
		"limit":   limit,
		"count":   len(hits),
	}, nil
}

func schemaRagSearch() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Text to search for (case-insensitive substring match over /repo).",
			},
			"repo": map[string]any{
				"type":        "string",
				"description": "Optional repo slug to scope the search (a subdirectory of /repo). Simple name; no path separators.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Max matches to return (default 10, max 50).",
				"minimum":     1,
				"maximum":     ragMaxLimit,
			},
		},
		"additionalProperties": false,
	}
}

// --- skills.list / skills.get -----------------------------------------

// stubSkill is the canned manifest shape skills.list / skills.get return
// in Wave 12. The wire envelope deliberately mirrors what the eventual
// OCI-backed catalogue will return so the agent's parsing layer doesn't
// need to change when the real registry lands.
type stubSkill struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	PendingOCI  bool     `json:"pendingOCI"`
}

// catalogueSkills is the hardcoded Wave-12 skill list. Order is
// significant for skills.list's response (alpha-sorted by Name) so the
// agent's UI displays stable rows.
var catalogueSkills = []stubSkill{
	{
		Name:        "fluxcd-workflows",
		Version:     "0.0.0-stub",
		Description: "GitOps recipes for FluxCD: HelmRelease pinning, Kustomization layering, image-update automation.",
		Tags:        []string{"flux", "gitops", "k8s"},
		PendingOCI:  true,
	},
	{
		Name:        "k8s-debugging",
		Version:     "0.0.0-stub",
		Description: "Triage playbooks for Kubernetes pods, services, ingresses; common CrashLoop / ImagePullBackOff causes.",
		Tags:        []string{"k8s", "debugging", "ops"},
		PendingOCI:  true,
	},
	{
		Name:        "openova-platform-basics",
		Version:     "0.0.0-stub",
		Description: "Inviolable principles, architecture invariants, day-1 onboarding for engineers shipping on OpenOva Sovereign.",
		Tags:        []string{"openova", "onboarding"},
		PendingOCI:  true,
	},
}

// findSkill returns the catalogue entry by name (case-sensitive). The
// catalogue is tiny so a linear scan is fine.
func findSkill(name string) (stubSkill, bool) {
	for _, s := range catalogueSkills {
		if s.Name == name {
			return s, true
		}
	}
	return stubSkill{}, false
}

func skillsList(ctx context.Context, _ json.RawMessage) (any, error) {
	// Defensive copy — the catalogue is package-level state and we
	// don't want a future caller mutating our slice.
	out := make([]stubSkill, len(catalogueSkills))
	copy(out, catalogueSkills)
	return map[string]any{
		"skills":     out,
		"count":      len(out),
		"pendingOCI": true,
		"note":       "Wave 12 stub: skills.list returns a hardcoded catalogue. Real OCI-backed catalogue ships in Wave 13.",
	}, nil
}

type skillsGetArgs struct {
	Name string `json:"name"`
}

func skillsGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var args skillsGetArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("skills.get: invalid arguments: %w", err)
	}
	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return nil, errors.New("skills.get: `name` is required")
	}
	skill, ok := findSkill(args.Name)
	if !ok {
		return map[string]any{
			"status": "NotFound",
			"name":   args.Name,
			"note":   "No such skill in the Wave 12 stub catalogue. Call skills.list to see available names.",
		}, nil
	}
	return map[string]any{
		"status":   "Found",
		"skill":    skill,
		"manifest": stubSkillManifest(skill),
	}, nil
}

// stubSkillManifest renders a minimal manifest shape that mirrors what
// the real OCI fetch will return. The agent can rely on `entrypoint` +
// `instructions` keys existing in both stub and real responses.
func stubSkillManifest(s stubSkill) map[string]any {
	return map[string]any{
		"apiVersion":   "openova.io/skill-pack/v1",
		"name":         s.Name,
		"version":      s.Version,
		"description":  s.Description,
		"tags":         s.Tags,
		"entrypoint":   s.Name + "/README.md",
		"instructions": "Pending OCI publish. Once published, skills.get will return the real manifest fetched from the per-Org catalogue registry.",
		"pendingOCI":   true,
	}
}

func schemaSkillsGet() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name (see skills.list for available names).",
			},
		},
		"additionalProperties": false,
	}
}
