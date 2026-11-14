// agentcatalog_test.go — unit coverage for the pty-server agent
// catalogue. We assert:
//
//   - All 7 builtin slugs resolve and have a non-empty Binary +
//     DefaultCwd of /workspace.
//   - Unknown slugs return ErrUnknownAgent.
//   - The optional /etc/openova/sandbox-agents.json override layers on
//     top of Builtin (and a malformed row is silently skipped).
//   - Resolve composes argv as [Binary, DefaultArgs..., extraArgs...]
//     and merges envOverride onto os.Environ() with later-wins
//     semantics + a deterministic, sorted append order for new keys.
//   - AllSlugs is sorted, exhaustive, and includes every builtin
//     plus any override slug.
package agentcatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// realAgentSlugs is the 6-slug set that the FE / catalyst-api / chart
// CRD all agree on. `sovereign-shell` is the 7th (rescue) slug carried
// only by pty-server.
var realAgentSlugs = []string{
	"aider", "claude-code", "cursor-agent",
	"little-coder", "opencode", "qwen-code",
}

func TestLookup_KnownSlugs(t *testing.T) {
	reset()
	for _, slug := range append(append([]string{}, realAgentSlugs...), "sovereign-shell") {
		got, err := Lookup(slug)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", slug, err)
		}
		if got.Binary == "" {
			t.Errorf("Lookup(%q): Binary empty", slug)
		}
		if got.DefaultCwd != "/workspace" {
			t.Errorf("Lookup(%q): DefaultCwd=%q want /workspace", slug, got.DefaultCwd)
		}
		if got.Slug != slug {
			t.Errorf("Lookup(%q): Slug=%q want %q", slug, got.Slug, slug)
		}
	}
}

func TestLookup_UnknownSlug(t *testing.T) {
	reset()
	if _, err := Lookup("goose"); err != ErrUnknownAgent {
		t.Errorf("Lookup(goose): err=%v want ErrUnknownAgent", err)
	}
}

func TestLookup_OverrideFile(t *testing.T) {
	reset()
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	body := []byte(`[
		{"slug":"goose","binary":"/usr/local/bin/goose","defaultCwd":"/workspace"},
		{"slug":"","binary":"/should/be/skipped"},
		{"slug":"malformed-no-binary"}
	]`)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	setOverridePath(path)
	defer func() { setOverridePath(""); reset() }()

	got, err := Lookup("goose")
	if err != nil {
		t.Fatalf("Lookup(goose) after override: %v", err)
	}
	if got.Binary != "/usr/local/bin/goose" {
		t.Errorf("Lookup(goose): Binary=%q want /usr/local/bin/goose", got.Binary)
	}
	// Malformed rows must be silently skipped (not crash startup).
	if _, err := Lookup("malformed-no-binary"); err != ErrUnknownAgent {
		t.Errorf("malformed-no-binary should have been skipped, got err=%v", err)
	}
	// Builtin still works through the override layer.
	if _, err := Lookup("sovereign-shell"); err != nil {
		t.Errorf("Lookup(sovereign-shell) after override: %v", err)
	}
}

func TestLookup_OverrideSupersedesBuiltinBySlug(t *testing.T) {
	reset()
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	body := []byte(`[
		{"slug":"sovereign-shell","binary":"/usr/local/bin/custom-shell","defaultArgs":["-i"]}
	]`)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	setOverridePath(path)
	defer func() { setOverridePath(""); reset() }()

	got, err := Lookup("sovereign-shell")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.Binary != "/usr/local/bin/custom-shell" {
		t.Errorf("override did not supersede builtin: Binary=%q", got.Binary)
	}
	if !reflect.DeepEqual(got.DefaultArgs, []string{"-i"}) {
		t.Errorf("override DefaultArgs not applied: %v", got.DefaultArgs)
	}
}

func TestResolve_ArgvShape(t *testing.T) {
	reset()
	ag, err := Lookup("aider")
	if err != nil {
		t.Fatalf("Lookup(aider): %v", err)
	}
	argv, _ := ag.Resolve([]string{"--model", "gpt-4o"}, nil)
	want := []string{
		"/usr/local/bin/aider",
		"--yes-always", "--no-auto-commits", // DefaultArgs
		"--model", "gpt-4o", // extraArgs
	}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("Resolve argv mismatch:\n got=%v\nwant=%v", argv, want)
	}
}

func TestResolve_EnvMergePrecedence(t *testing.T) {
	reset()
	// Seed an env var so we can verify override precedence.
	const probeKey = "AGENTCATALOG_TEST_PROBE"
	t.Setenv(probeKey, "from-os-environ")
	ag, err := Lookup("sovereign-shell")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	_, env := ag.Resolve(nil, map[string]string{
		probeKey:                 "from-override",
		"AGENTCATALOG_TEST_NEW1": "v1",
		"AGENTCATALOG_TEST_NEW2": "v2",
	})

	// The override value must win for the existing key, and it must
	// REPLACE the existing entry rather than appending a duplicate.
	probeCount := 0
	for _, kv := range env {
		if kv == probeKey+"=from-override" {
			probeCount++
		}
		if kv == probeKey+"=from-os-environ" {
			t.Errorf("os.Environ value leaked through: %s", kv)
		}
	}
	if probeCount != 1 {
		t.Errorf("override probe key occurs %d times, want exactly 1", probeCount)
	}

	// New keys are appended; their relative order is sorted for
	// determinism.
	new1Idx, new2Idx := -1, -1
	for i, kv := range env {
		if kv == "AGENTCATALOG_TEST_NEW1=v1" {
			new1Idx = i
		}
		if kv == "AGENTCATALOG_TEST_NEW2=v2" {
			new2Idx = i
		}
	}
	if new1Idx == -1 || new2Idx == -1 {
		t.Fatalf("new keys missing from env: new1=%d new2=%d", new1Idx, new2Idx)
	}
	if new1Idx >= new2Idx {
		t.Errorf("new keys not in sorted order: new1=%d new2=%d", new1Idx, new2Idx)
	}
}

func TestResolve_EnvOverrideEmptyKeepsOsEnviron(t *testing.T) {
	reset()
	ag, err := Lookup("sovereign-shell")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	_, env := ag.Resolve(nil, nil)
	if len(env) == 0 {
		t.Errorf("expected os.Environ() passthrough, got empty env")
	}
}

func TestAllSlugs_SortedAndExhaustive(t *testing.T) {
	reset()
	got := AllSlugs()
	// Must include all 7 builtins.
	want := append(append([]string{}, realAgentSlugs...), "sovereign-shell")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllSlugs mismatch:\n got=%v\nwant=%v", got, want)
	}
	// Must be sorted ascending.
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("AllSlugs not sorted: %v", got)
			break
		}
	}
}

// Sanity guard: the 6 "real" slugs in pty-server must exactly match
// the canonical catalyst-api allowlist. If this test fails, the
// upstream allowlist diverged and the four sites need re-syncing.
func TestBuiltin_RealAgentSlugsMatchUpstreamCatalogue(t *testing.T) {
	reset()
	got := AllSlugs()
	gotReal := make([]string, 0, len(got))
	for _, s := range got {
		if s == "sovereign-shell" {
			continue
		}
		gotReal = append(gotReal, s)
	}
	want := append([]string{}, realAgentSlugs...)
	sort.Strings(want)
	if !reflect.DeepEqual(gotReal, want) {
		t.Errorf("real-agent set drifted from upstream:\n got=%v\nwant=%v", gotReal, want)
	}
}
