// secondary_kubeconfig_completeness_test.go — the guard for the hw293
// credential-less region-B kubeconfig.
//
// Pinned to the LIVE artefact, not to a paraphrase of it. hw293StubKubeconfig
// below is a byte-for-byte reconstruction of what sat on the chroot's PVC at
// /var/lib/catalyst/kubeconfigs/a0077ba47e3720e5-me-east-215-b-1.yaml:
// 95 bytes, ending mid-token on `  name: c` with no trailing newline, while
// the healthy region-a file beside it measured 1109 bytes and carried all
// five top-level keys.
//
// Every arm here fails against the pre-fix handler, which persisted the
// document first and only discovered it was unusable 21 lines later at
// AddCluster — returning 500 while leaving the file exactly where it had put
// it.
//
// Refs #6015, #6040.

package handler

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hw293StubKubeconfig is the measured live artefact. Do not reformat: the
// byte count is an assertion, and the absent trailing newline is part of the
// evidence that the document was cut mid-token rather than generated short.
const hw293StubKubeconfig = "apiVersion: v1\n" +
	"kind: Config\n" +
	"clusters:\n" +
	"- cluster:\n" +
	"    server: https://212.72.24.6:6443\n" +
	"  name: c"

// hw293StubBytes — the size measured on the chroot PVC (`wc -c`).
const hw293StubBytes = 95

// completeKubeconfigSameCluster is the CONTROL that shares the suspect
// property. It keeps the stub's exact cluster block — same single entry,
// same `server:` URL, same `name: c` — and adds ONLY the three sections the
// stub is missing. If it were the server URL, the cluster count, or the
// document's brevity that made the stub unusable, this control would be
// refused too. It is accepted, which isolates contexts/users/current-context
// as the discriminator.
const completeKubeconfigSameCluster = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
  name: c
contexts:
- name: c
  context:
    cluster: c
    user: c
current-context: c
users:
- name: c
  user:
    token: fake-token
`

func newCompletenessTestHandler(t *testing.T, dir string) *Handler {
	t.Helper()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)
	t.Setenv("SOVEREIGN_FQDN", "") // mothership: no dial-based self-heal in unit tests
	return &Handler{
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		k8sCache: newK8sCacheWithClusters(t, nil),
	}
}

// TestHw293Stub_ShapeIsPinned states the defect in numbers: 95 bytes, and
// exactly which top-level sections are absent versus a complete document.
func TestHw293Stub_ShapeIsPinned(t *testing.T) {
	if got := len(hw293StubKubeconfig); got != hw293StubBytes {
		t.Fatalf("stub fixture drifted from the live artefact: %d bytes, want %d", got, hw293StubBytes)
	}
	if strings.HasSuffix(hw293StubKubeconfig, "\n") {
		t.Error("stub fixture gained a trailing newline; the live file ended mid-token")
	}

	got := strings.Join(secondaryKubeconfigDefects(hw293StubKubeconfig), ",")
	const want = "contexts,current-context,users"
	if got != want {
		t.Fatalf("defects = %q, want %q", got, want)
	}

	// The control shares the cluster block and is usable — so brevity and
	// the server URL are not what disqualifies the stub.
	if d := secondaryKubeconfigDefects(completeKubeconfigSameCluster); len(d) != 0 {
		t.Fatalf("control with the SAME cluster block was refused: %v", d)
	}
	if len(completeKubeconfigSameCluster) <= hw293StubBytes {
		t.Fatal("control must be the larger document for the comparison to mean anything")
	}
}

// TestSecondaryKubeconfig_RefusesIncompleteBeforeWriting is the core guard.
// Pre-fix this POST returned 500 AND left the 95-byte file on disk.
func TestSecondaryKubeconfig_RefusesIncompleteBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	h := newCompletenessTestHandler(t, dir)

	rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "a0077ba47e3720e5",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": hw293StubKubeconfig,
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "kubeconfig-incomplete") {
		t.Errorf("response does not name the refusal: %s", body)
	}

	path := filepath.Join(dir, "a0077ba47e3720e5-me-east-215-b-1.yaml")
	if st, err := os.Stat(path); err == nil {
		t.Fatalf("unusable kubeconfig was persisted anyway (%d bytes at %s)", st.Size(), path)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error: %v", err)
	}
}

// TestSecondaryKubeconfig_IncompleteCannotDisplaceGoodFile is the property
// that actually kept hw293's region B dark: whatever the sender does, a
// kubeconfig that already works must survive a later malformed delivery.
func TestSecondaryKubeconfig_IncompleteCannotDisplaceGoodFile(t *testing.T) {
	dir := t.TempDir()
	h := newCompletenessTestHandler(t, dir)
	path := filepath.Join(dir, "a0077ba47e3720e5-me-east-215-b-1.yaml")

	// A healthy delivery lands first.
	if rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "a0077ba47e3720e5",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": secondaryKubeconfigFixture,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("seed POST status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("seed kubeconfig not on disk: %v", err)
	}

	// The malformed delivery follows, exactly as it did live (twice).
	if rec := postSecondaryKubeconfig(t, h, map[string]string{
		"deploymentId":   "a0077ba47e3720e5",
		"regionKey":      "me-east-215-b-1",
		"kubeconfigYaml": hw293StubKubeconfig,
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("stub POST status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("good kubeconfig disappeared: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("good kubeconfig was displaced: %d bytes -> %d bytes", len(before), len(after))
	}
	if len(after) == hw293StubBytes {
		t.Fatalf("region slot now holds the %d-byte stub", hw293StubBytes)
	}
}

// TestSecondaryKubeconfig_CompleteStillAccepted is the vacuity arm. Without
// it the guard above could be satisfied by a handler that refuses
// everything.
func TestSecondaryKubeconfig_CompleteStillAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
	}{
		{"iac fixture", secondaryKubeconfigFixture},
		{"stub cluster block plus the three missing sections", completeKubeconfigSameCluster},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := newCompletenessTestHandler(t, dir)

			rec := postSecondaryKubeconfig(t, h, map[string]string{
				"deploymentId":   "a0077ba47e3720e5",
				"regionKey":      "me-east-215-b-1",
				"kubeconfigYaml": tc.yaml,
			})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
			}
			onDisk, err := os.ReadFile(filepath.Join(dir, "a0077ba47e3720e5-me-east-215-b-1.yaml"))
			if err != nil {
				t.Fatalf("complete kubeconfig was not persisted: %v", err)
			}
			if len(onDisk) <= hw293StubBytes {
				t.Fatalf("persisted %d bytes, no larger than the %d-byte stub", len(onDisk), hw293StubBytes)
			}
		})
	}
}

// TestSecondaryKubeconfigDefects_NamesEachGapIndividually keeps the 422's
// `missing` field honest: each section is reported on its own evidence, so
// an operator reading the response learns which part of the document the
// sender dropped.
func TestSecondaryKubeconfigDefects_NamesEachGapIndividually(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "   ", "empty"},
		{"not yaml", "\tthis: is: not: a: kubeconfig\n\t\t- [", "unparseable"},
		{
			"cluster present but server empty — the KEY exists, the VALUE does not",
			"apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: \"\"\n  name: c\ncontexts:\n- name: c\n  context:\n    cluster: c\n    user: c\ncurrent-context: c\nusers:\n- name: c\n  user:\n    token: t\n",
			"clusters",
		},
		{"the hw293 artefact", hw293StubKubeconfig, "contexts,current-context,users"},
		{"complete", completeKubeconfigSameCluster, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(secondaryKubeconfigDefects(tc.yaml), ",")
			if got != tc.want {
				t.Fatalf("defects = %q, want %q", got, tc.want)
			}
			if usable := secondaryKubeconfigUsable(tc.yaml); usable != (tc.want == "") {
				t.Fatalf("secondaryKubeconfigUsable = %v, want %v", usable, tc.want == "")
			}
		})
	}
}
