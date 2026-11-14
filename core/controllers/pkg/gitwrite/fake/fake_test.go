package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

// TestFakeCommitReadList: a commit through the fake is read back, listed,
// and recorded with the scope's robot as committer.
func TestFakeCommitReadList(t *testing.T) {
	w := fake.New()
	defer w.Close()
	var _ gitwrite.Writer = w
	ctx := context.Background()
	repo := gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"}
	scope := gitwrite.OrgIntent("acme")
	res, err := w.Commit(ctx, gitwrite.Change{
		Scope: scope, Repo: repo, Summary: "add site", ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaConsole,
		Author: gitwrite.Identity{Name: "Ann Example", Email: "ann@example.org"},
		Files:  []gitwrite.FileChange{{Path: "intent/applications/site.yaml", Op: gitwrite.OpUpsert, Content: []byte("kind: Application\n")}},
	})
	if err != nil || !res.Committed {
		t.Fatalf("Commit: %+v %v", res, err)
	}
	content, sha, err := w.Read(ctx, scope, repo, "intent/applications/site.yaml")
	if err != nil || string(content) != "kind: Application\n" || sha != res.Files[0].BlobSHA {
		t.Fatalf("Read: %q %q %v", content, sha, err)
	}
	entries, err := w.List(ctx, scope, repo, "intent/applications")
	if err != nil || len(entries) != 1 || entries[0].Path != "intent/applications/site.yaml" {
		t.Fatalf("List: %+v %v", entries, err)
	}
	commits := w.Commits()
	if len(commits) != 1 || commits[0].SHA != res.Commit || commits[0].Committer != (gitwrite.Identity{Name: "acme-iac-bot", Email: "acme-iac-bot@" + fake.RobotDomain}) ||
		commits[0].Author.Email != "ann@example.org" || commits[0].Repo.Branch != "main" || commits[0].Paths[0] != "intent/applications/site.yaml" {
		t.Fatalf("Commits: %+v", commits)
	}
}

// TestFakeSameRules: the fake refuses what the library refuses and sees
// a file that Put wrote.
func TestFakeSameRules(t *testing.T) {
	w := fake.New()
	defer w.Close()
	ctx := context.Background()
	repo := gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"}
	base := gitwrite.Change{Scope: gitwrite.OrgIntent("acme"), Repo: repo, Summary: "s", ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaAPI}

	outside := base
	outside.Files = []gitwrite.FileChange{{Path: "regions/r1/apps/a.yaml", Op: gitwrite.OpUpsert, Content: []byte("a: 1\n")}}
	if _, err := w.Commit(ctx, outside); !errors.Is(err, gitwrite.ErrForbiddenPath) {
		t.Errorf("outside the scope: got %v", err)
	}
	secret := base
	secret.Files = []gitwrite.FileChange{{Path: "intent/s.yaml", Op: gitwrite.OpUpsert, Content: []byte("apiVersion: v1\nkind: Secret\n")}}
	if _, err := w.Commit(ctx, secret); !errors.Is(err, gitwrite.ErrSecretContent) {
		t.Errorf("secret: got %v", err)
	}
	w.Put(repo, "intent/a.yaml", []byte("a: 1\n"))
	taken := base
	taken.Files = []gitwrite.FileChange{{Path: "intent/a.yaml", Op: gitwrite.OpUpsert, Content: []byte("a: 2\n"), IfMatch: gitwrite.AbsentSHA}}
	if _, err := w.Commit(ctx, taken); !gitwrite.IsConflict(err) {
		t.Errorf("AbsentSHA on an existing file: got %v", err)
	}
	if len(w.Commits()) != 0 {
		t.Fatalf("Put or a refused change recorded a commit: %+v", w.Commits())
	}
}
