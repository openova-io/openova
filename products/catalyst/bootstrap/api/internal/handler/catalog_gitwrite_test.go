// catalog_gitwrite_test.go — the catalogue writers commit through the
// commit library gitwrite, scope catalog, and the write routes answer 503
// git-unavailable when no commit library is configured.
package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

// readOnlyGitea hides the gitwrite.Writer of the fake: a Gitea client
// without a commit library, as NewGiteaClientFromEnv returns it when
// SOVEREIGN_FQDN is empty.
type readOnlyGitea struct{ GiteaBlueprintClient }

// TestCommitCatalogFile_RealLibrary runs the commit helper against the
// real library, so the Change it builds passes every check of gitwrite.
func TestCommitCatalogFile_RealLibrary(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	repo := gitwrite.Repo{Owner: "acme", Name: sharedBlueprintsRepo, Branch: blueprintBranch}
	committed, sha, err := commitCatalogFile(ctx, w, repo, "bp-a/blueprint.yaml", []byte("kind: Blueprint\n"),
		strings.Repeat("x", 80)+"\nsecond line", catalogEditCommitAuthor())
	if err != nil || !committed || sha == "" || len(w.Commits()) != 1 {
		t.Fatalf("first commit: committed=%v sha=%q err=%v commits=%d", committed, sha, err, len(w.Commits()))
	}
	c := w.Commits()[0]
	if !strings.HasPrefix(c.Message, strings.Repeat("x", 72)+"\n\n") || !strings.Contains(c.Message, "\nRequested-Via: api") ||
		c.Committer.Name != "catalyst-fleet-bot" || c.Author.Email != "ops@openova.io" {
		t.Errorf("want a 72-character summary, via api, scope catalog and the edit author; got %+v", c)
	}
	committed, _, err = commitCatalogFile(ctx, w, repo, "bp-a/blueprint.yaml", []byte("kind: Blueprint\n"), " ", gitwrite.Identity{})
	if err != nil || committed {
		t.Errorf("unchanged file: want no commit and no error; committed=%v err=%v", committed, err)
	}
	_, _, err = commitCatalogFile(ctx, w, repo, "s.yaml", []byte("apiVersion: v1\nkind: Secret\n"), "s", gitwrite.Identity{})
	if !errors.Is(err, gitwrite.ErrSecretContent) {
		t.Errorf("a Secret must be refused with ErrSecretContent; got %v", err)
	}
}

// TestCatalogWriters_CommitThroughGitwrite — publish hands one Change of
// scope catalog to the commit library.
func TestCatalogWriters_CommitThroughGitwrite(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)
	dep := installUserAccessDeployment(t, h, "dep-bp-gitwrite")
	body := blueprintPublishRequest{Org: "acme", Name: "bp-x", Version: "1.0.0", BlueprintYAML: validBlueprintYAML("bp-x", "1.0.0")}
	if rec := callUserAccess(t, h, http.MethodPost, "/api/v1/sovereigns/"+dep.ID+"/blueprints/publish", body, registerBlueprintRoutes); rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(fg.changes) != 1 {
		t.Fatalf("want 1 Change through gitwrite, got %d", len(fg.changes))
	}
	if c := fg.changes[0]; c.Scope.Name != "catalog" || c.Via != gitwrite.ViaAPI || len(c.ChangeID) != 26 ||
		c.Repo != (gitwrite.Repo{Owner: "acme", Name: sharedBlueprintsRepo, Branch: blueprintBranch}) ||
		len(c.Files) != 1 || c.Files[0].Path != "bp-x/blueprint.yaml" || c.Files[0].Op != gitwrite.OpUpsert {
		t.Errorf("unexpected Change: %+v", c)
	}
}

// TestCatalogWriteRoutes_503WithoutCommitLibrary — publish, curate, edit-pr
// and the full IaC edit answer 503 git-unavailable and write nothing; the
// card edit and the aggregator are refused with gitwrite.ErrUnavailable.
func TestCatalogWriteRoutes_503WithoutCommitLibrary(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	fg.files[giteaKey("acme", sharedBlueprintsRepo, blueprintBranch, "bp-x/blueprint.yaml")] = []byte(validBlueprintYAML("bp-x", "1.0.0"))
	h.SetGiteaClient(readOnlyGitea{fg})
	base := "/api/v1/sovereigns/" + installUserAccessDeployment(t, h, "dep-bp-no-gitwrite").ID + "/blueprints/"
	bodies := map[string]any{
		"publish": blueprintPublishRequest{Org: "acme", Name: "bp-x", Version: "1.0.0", BlueprintYAML: validBlueprintYAML("bp-x", "1.0.0")},
		"curate":  blueprintCurateRequest{SourceOrg: "acme", BlueprintName: "bp-x"},
		"edit-pr": blueprintEditPRRequest{Org: "acme", Path: "x.yaml", Content: "k: v"},
	}
	for route, body := range bodies {
		if rec := callUserAccess(t, h, http.MethodPost, base+route, body, registerBlueprintRoutes); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error":"git-unavailable"`) {
			t.Errorf("%s: got %d %s, want 503 git-unavailable", route, rec.Code, rec.Body.String())
		}
	}
	if rec := callCatalogIaCEdit(t, h, "bp-alloy", `{"blueprintYaml":`+jsonQuote(fullAlloyBlueprintYAML("1.0.2"))+`}`, adminClaims()); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"error":"git-unavailable"`) {
		t.Errorf("iac: got %d %s, want 503 git-unavailable", rec.Code, rec.Body.String())
	}
	if _, err := h.writeCatalogEditToGit(context.Background(), catalogEdit{Slug: "grafana", Name: "X"}); !errors.Is(err, gitwrite.ErrUnavailable) {
		t.Errorf("card edit: want gitwrite.ErrUnavailable, got %v", err)
	}
	if _, err := h.writeCatalogSovereignAggregatorBytes(context.Background(), "bp-x", []byte("kind: Blueprint\n")); !errors.Is(err, gitwrite.ErrUnavailable) {
		t.Errorf("aggregator: want gitwrite.ErrUnavailable, got %v", err)
	}
	if len(fg.files) != 1 || len(fg.changes) != 0 {
		t.Errorf("nothing may be written; files=%v changes=%d", fileKeys(fg), len(fg.changes))
	}
}

// TestNewGiteaClientFromEnv_WriterNeedsFQDN — the production client carries
// the commit library only when SOVEREIGN_FQDN is set.
func TestNewGiteaClientFromEnv_WriterNeedsFQDN(t *testing.T) {
	t.Setenv("CATALYST_GITEA_URL", "http://gitea.invalid:3000")
	t.Setenv("CATALYST_GITEA_TOKEN", "token")
	t.Setenv("SOVEREIGN_FQDN", "")
	if c := NewGiteaClientFromEnv(); c == nil || catalogWriterOf(c) != nil {
		t.Errorf("without SOVEREIGN_FQDN: want a Gitea client without a commit library, got %T", c)
	}
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	if c := NewGiteaClientFromEnv(); catalogWriterOf(c) == nil {
		t.Errorf("with SOVEREIGN_FQDN: want a commit library, got %T", c)
	}
}
