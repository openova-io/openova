// Package agentcatalog is the single source of truth for the
// agent-slug -> binary mapping inside pty-server.
//
// The slugs MUST stay in lock-step with three sibling sites:
//
//   - FE catalogue:    products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts
//     (SANDBOX_AGENTS)
//   - catalyst-api:    products/catalyst/bootstrap/api/internal/handler/
//     sandbox_sessions.go (sandboxAllowedAgents)
//   - Chart CRD enum:  spec.agentCatalogue.items.enum
//
// Adding an agent to pty-server WITHOUT also adding it to the three
// upstream sites will surface as a 400 invalid-agent from catalyst-api
// long before the request reaches pty-server, so the four-site update
// is a single PR by convention.
//
// The hardcoded Builtin table can be augmented at runtime by mounting a
// JSON override at /etc/openova/sandbox-agents.json (path overridable
// via the OPENOVA_SANDBOX_AGENTS_PATH env var); entries in the override
// supersede builtins by Slug. The override is a deployment escape
// hatch for one-off experiments — production should always extend
// Builtin instead so the four-site invariant is auditable in git.
//
// Design source: tracked in TBD-P4 #1986 sub-break B3.
package agentcatalog

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
)

// ErrUnknownAgent is returned by Lookup when the slug is not present
// in either the builtin table or the override file.
var ErrUnknownAgent = errors.New("agentcatalog: unknown agent slug")

// defaultOverridePath is where the optional JSON override is read
// from. Operators can re-point it via OPENOVA_SANDBOX_AGENTS_PATH.
const defaultOverridePath = "/etc/openova/sandbox-agents.json"

// Agent is one row in the catalogue — the canonical record of how to
// spawn a particular agent slug.
type Agent struct {
	// Slug is the wire-level identifier. Must match the FE catalogue.
	Slug string `json:"slug"`
	// Binary is the absolute path inside the pty-server / agent-bundle
	// image. The B1 agent-bundle image installs the canonical binaries
	// under /usr/local/bin/<slug>.
	Binary string `json:"binary"`
	// DefaultArgs is appended verbatim after Binary; ExtraArgs from the
	// caller are appended after these.
	DefaultArgs []string `json:"defaultArgs,omitempty"`
	// DefaultCwd is the child's working directory. "" = inherit
	// pty-server's CWD (typically /workspace per the StatefulSet).
	DefaultCwd string `json:"defaultCwd,omitempty"`
	// RequiredEnv lists environment variables whose ABSENCE returns a
	// clean 400 at create() time — a fail-fast guard against the
	// black-screen pattern when the controller hasn't wired the agent's
	// required gateway / API-key env yet.
	RequiredEnv []string `json:"requiredEnv,omitempty"`
	// Notes is free-form; surfaced in /healthz/agents (future) and
	// useful for code-reviewers.
	Notes string `json:"notes,omitempty"`
}

// Builtin is the compiled-in registry. Operator overrides via
// /etc/openova/sandbox-agents.json supersede entries here by Slug.
//
// Order is significant only as a coding convention (lexicographic).
// The 6 real-agent rows match the four upstream sites; `sovereign-shell`
// is an extra rescue row guaranteed to be present so a broken agent
// bundle never leaves the operator staring at a black screen.
var Builtin = []Agent{
	{
		Slug:        "aider",
		Binary:      "/usr/local/bin/aider",
		DefaultArgs: []string{"--yes-always", "--no-auto-commits"},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
	},
	{
		Slug:        "claude-code",
		Binary:      "/usr/local/bin/claude",
		DefaultArgs: []string{"--dangerously-skip-permissions"},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"LLM_GATEWAY_URL"},
		Notes:       "Anthropic Claude Code CLI. --dangerously-skip-permissions is safe inside the sandbox PTY (single attack surface).",
	},
	{
		Slug:        "cursor-agent",
		Binary:      "/usr/local/bin/cursor-agent",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"LLM_GATEWAY_URL"},
	},
	{
		Slug:        "little-coder",
		Binary:      "/usr/local/bin/little-coder",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
	},
	{
		Slug:        "opencode",
		Binary:      "/usr/local/bin/opencode",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
	},
	{
		Slug:        "qwen-code",
		Binary:      "/usr/local/bin/qwen-code",
		DefaultArgs: []string{},
		DefaultCwd:  "/workspace",
		RequiredEnv: []string{"OPENAI_BASE_URL", "OPENAI_API_KEY"},
		Notes:       "Reads OpenAI-compatible base URL; bp-newapi exposes that.",
	},
	{
		Slug:        "sovereign-shell",
		Binary:      "/bin/sh",
		DefaultArgs: []string{"-l"},
		DefaultCwd:  "/workspace",
		RequiredEnv: nil,
		Notes:       "Rescue shell. Always present even if the agent bundle is broken — black-screen prevention. No third-party binary needed for smoke tests.",
	},
}

// tableMu guards the lazily-loaded table cache. We load once per
// process; an operator who edits the override file must restart
// pty-server (same lifecycle contract as the Helm-rendered StatefulSet
// env vars).
var (
	tableMu    sync.Mutex
	tableCache map[string]Agent
	// overridePathOverride lets tests point at a temp file without
	// touching the real /etc/openova path. Empty = use env var or
	// default.
	overridePathOverride string
)

// setOverridePath is a test seam. Production callers leave this empty.
func setOverridePath(path string) {
	tableMu.Lock()
	defer tableMu.Unlock()
	overridePathOverride = path
	tableCache = nil
}

// reset is a test seam — drops the cache so the next Lookup re-reads.
func reset() {
	tableMu.Lock()
	defer tableMu.Unlock()
	tableCache = nil
}

// overridePath returns the effective override file path: explicit test
// override > OPENOVA_SANDBOX_AGENTS_PATH env > defaultOverridePath.
func overridePath() string {
	if overridePathOverride != "" {
		return overridePathOverride
	}
	if p := os.Getenv("OPENOVA_SANDBOX_AGENTS_PATH"); p != "" {
		return p
	}
	return defaultOverridePath
}

// load builds the effective catalogue: Builtin layered with the
// optional override file. Cached process-lifetime after first call.
func load() map[string]Agent {
	tableMu.Lock()
	defer tableMu.Unlock()
	if tableCache != nil {
		return tableCache
	}
	out := make(map[string]Agent, len(Builtin)+2)
	for _, a := range Builtin {
		out[a.Slug] = a
	}
	if extras, ok := loadOverride(overridePath()); ok {
		// Each entry in the override is a complete row by design — no
		// partial merge, to keep diffability obvious for ops review.
		for _, a := range extras {
			if a.Slug == "" || a.Binary == "" {
				// Skip malformed rows rather than panic at startup;
				// log to stderr so operators can spot the misconfig.
				continue
			}
			out[a.Slug] = a
		}
	}
	tableCache = out
	return out
}

// loadOverride attempts to read + parse the override file. Returns
// (nil, false) on any error or missing file — those cases simply fall
// back to the builtin table.
func loadOverride(path string) ([]Agent, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var rows []Agent
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, false
	}
	return rows, true
}

// Lookup returns the catalogue entry for slug, or ErrUnknownAgent.
func Lookup(slug string) (Agent, error) {
	table := load()
	if a, ok := table[slug]; ok {
		return a, nil
	}
	return Agent{}, ErrUnknownAgent
}

// AllSlugs returns the catalogue keyset, sorted lexicographically.
// Surfaced in the invalid-agent 400 detail string so operators know
// what they should have asked for.
func AllSlugs() []string {
	table := load()
	out := make([]string, 0, len(table))
	for slug := range table {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}

// Resolve builds the exec argv + final env slice for spawning the
// agent. The returned argv is [Binary, DefaultArgs..., extraArgs...].
// The returned env is os.Environ() with the entries from envOverride
// applied on top (later wins on key collision).
//
// session.New owns the actual exec.Command call; Resolve is just the
// data-shaping step.
func (a Agent) Resolve(extraArgs []string, envOverride map[string]string) ([]string, []string) {
	argv := make([]string, 0, 1+len(a.DefaultArgs)+len(extraArgs))
	argv = append(argv, a.Binary)
	argv = append(argv, a.DefaultArgs...)
	argv = append(argv, extraArgs...)

	base := os.Environ()
	if len(envOverride) == 0 {
		return argv, base
	}
	// Apply override on top: keys present in envOverride replace
	// matching keys in base; new keys are appended in sorted order so
	// the result is deterministic for tests.
	idx := make(map[string]int, len(base))
	for i, kv := range base {
		eq := indexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		idx[kv[:eq]] = i
	}
	overrideKeys := make([]string, 0, len(envOverride))
	for k := range envOverride {
		overrideKeys = append(overrideKeys, k)
	}
	sort.Strings(overrideKeys)
	out := make([]string, len(base), len(base)+len(envOverride))
	copy(out, base)
	for _, k := range overrideKeys {
		v := envOverride[k]
		if i, ok := idx[k]; ok {
			out[i] = k + "=" + v
		} else {
			out = append(out, k+"="+v)
		}
	}
	return argv, out
}

// indexByte is a tiny stdlib-free helper to avoid pulling strings just
// for the equals split.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
