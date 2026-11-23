package intentroot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

const fqdn = "t01.omani.works"

var repo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions", Branch: "main"}

// fast is a backoff for tests: no real waiting.
var fast = wait.Backoff{Duration: time.Millisecond, Factor: 1, Steps: 4}

// flaky fails the first n commits with err, then hands every call to the fake.
type flaky struct {
	*fake.Writer
	n       int
	err     error
	commits int
}

func (f *flaky) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	f.commits++
	if f.commits <= f.n {
		return gitwrite.Result{}, f.err
	}
	return f.Writer.Commit(ctx, c)
}

func TestPath(t *testing.T) {
	if got, want := Path(fqdn), "clusters/t01.omani.works/intent/intent-root.yaml"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if !strings.HasPrefix(Path(fqdn), gitwrite.FleetIntent(fqdn).Prefixes[0]) {
		t.Fatalf("Path %q is outside the fleet-intent scope %q", Path(fqdn), gitwrite.FleetIntent(fqdn).Prefixes[0])
	}
}

func TestContentIsTheEmptyConfigMap(t *testing.T) {
	got, err := Content("01JCDZ7K9M3Q4T5V6W7X8Y9Z0A")
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    catalyst.openova.io/change-id: 01JCDZ7K9M3Q4T5V6W7X8Y9Z0A
  labels:
    app.kubernetes.io/managed-by: flux
  name: intent-root
  namespace: flux-system
`
	if string(got) != want {
		t.Fatalf("Content =\n%s\nwant\n%s", got, want)
	}
}

func TestEnsureCommitsThePlaceholderOnce(t *testing.T) {
	w := fake.New()
	defer w.Close()
	if err := Ensure(context.Background(), w, repo, fqdn); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	commits := w.Commits()
	if len(commits) != 1 || len(commits[0].Paths) != 1 || commits[0].Paths[0] != Path(fqdn) {
		t.Fatalf("commits = %+v, want one commit of %s", commits, Path(fqdn))
	}
	if commits[0].Committer.Name != gitwrite.FleetIntent(fqdn).Robot {
		t.Fatalf("committer = %q, want the fleet-intent robot %q", commits[0].Committer.Name, gitwrite.FleetIntent(fqdn).Robot)
	}
	if !strings.Contains(commits[0].Message, "Requested-Via: controller") || !strings.HasPrefix(commits[0].Message, Summary) {
		t.Fatalf("commit message = %q, want summary %q and Requested-Via: controller", commits[0].Message, Summary)
	}
	first, _, err := w.Read(context.Background(), gitwrite.FleetIntent(fqdn), repo, Path(fqdn))
	if err != nil {
		t.Fatalf("read the placeholder: %v", err)
	}
	var obj map[string]interface{}
	if err := yaml.Unmarshal(first, &obj); err != nil {
		t.Fatalf("placeholder is not YAML: %v", err)
	}
	md, _ := obj["metadata"].(map[string]interface{})
	if obj["kind"] != "ConfigMap" || md["name"] != Name || md["namespace"] != Namespace || obj["data"] != nil {
		t.Fatalf("placeholder = %s, want ConfigMap %s/%s with no data", first, Namespace, Name)
	}

	// A second start finds the file: success, no commit, same bytes.
	if err := Ensure(context.Background(), w, repo, fqdn); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if n := len(w.Commits()); n != 1 {
		t.Fatalf("second Ensure made a commit: %d commits", n)
	}
	again, _, err := w.Read(context.Background(), gitwrite.FleetIntent(fqdn), repo, Path(fqdn))
	if err != nil || string(again) != string(first) {
		t.Fatalf("second Ensure changed the placeholder: %v\n%s\nwas\n%s", err, again, first)
	}
}

func TestEnsureNeverReplacesAnExistingFile(t *testing.T) {
	w := fake.New()
	defer w.Close()
	other := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: intent-root\n  namespace: flux-system\n")
	w.Put(repo, Path(fqdn), other)
	if err := Ensure(context.Background(), w, repo, fqdn); err != nil {
		t.Fatalf("Ensure over an existing file: %v", err)
	}
	if n := len(w.Commits()); n != 0 {
		t.Fatalf("Ensure committed over an existing file: %d commits", n)
	}
	got, _, err := w.Read(context.Background(), gitwrite.FleetIntent(fqdn), repo, Path(fqdn))
	if err != nil || string(got) != string(other) {
		t.Fatalf("existing file changed: %v\n%s", err, got)
	}
}

func TestEnsureRefusesAMissingFQDNOrWriter(t *testing.T) {
	w := fake.New()
	defer w.Close()
	if err := Ensure(context.Background(), w, repo, ""); !errors.Is(err, gitwrite.ErrInvalid) {
		t.Fatalf("empty FQDN: err = %v, want ErrInvalid", err)
	}
	if err := Ensure(context.Background(), nil, repo, fqdn); !errors.Is(err, gitwrite.ErrInvalid) {
		t.Fatalf("nil writer: err = %v, want ErrInvalid", err)
	}
	if n := len(w.Commits()); n != 0 {
		t.Fatalf("a refused call committed: %d commits", n)
	}
}

func TestRunRetriesAnUnavailableServer(t *testing.T) {
	w := &flaky{Writer: fake.New(), n: 2, err: gitwrite.ErrUnavailable}
	defer w.Close()
	if err := Run(context.Background(), w, repo, fqdn, fast, logr.Discard()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w.commits != 3 || len(w.Commits()) != 1 {
		t.Fatalf("attempts = %d, commits = %d; want 3 attempts and 1 commit", w.commits, len(w.Commits()))
	}
}

func TestRunStopsOnAPermanentError(t *testing.T) {
	w := &flaky{Writer: fake.New(), n: 10, err: gitwrite.ErrForbiddenPath}
	defer w.Close()
	err := Run(context.Background(), w, repo, fqdn, fast, logr.Discard())
	if !errors.Is(err, gitwrite.ErrForbiddenPath) || w.commits != 1 {
		t.Fatalf("err = %v after %d attempts; want ErrForbiddenPath after 1", err, w.commits)
	}
}

func TestRunReturnsTheLastErrorWhenTheBackoffIsSpent(t *testing.T) {
	w := &flaky{Writer: fake.New(), n: 10, err: gitwrite.ErrUnavailable}
	defer w.Close()
	err := Run(context.Background(), w, repo, fqdn, fast, logr.Discard())
	if !errors.Is(err, gitwrite.ErrUnavailable) || w.commits != fast.Steps {
		t.Fatalf("err = %v after %d attempts; want ErrUnavailable after %d", err, w.commits, fast.Steps)
	}
	if n := len(w.Commits()); n != 0 {
		t.Fatalf("commits = %d, want 0", n)
	}
}
