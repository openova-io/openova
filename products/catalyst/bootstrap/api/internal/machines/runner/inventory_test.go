package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// writeSBOM is a fake trivy that writes a CycloneDX document to the path after --output.
const writeSBOM = `out=""; prev=""
for a in "$@"; do [ "$prev" = "--output" ] && out="$a"; prev="$a"; done
printf '{"bomFormat":"CycloneDX","specVersion":"1.6","components":[]}\n' > "$out"`

func TestInventoryStoresTheSBOMUnderTheRunPrefix(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	fakeProgram(t, env.Trivy, writeSBOM)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Succeeded || !doc.Completed || doc.Message != "" {
		t.Fatalf("got %+v, want Succeeded and completed", doc)
	}
	wantArgs := []string{"rootfs", "--format", "cyclonedx", "--skip-db-update", "--offline-scan",
		"--cache-dir", filepath.Join(env.WorkDir, "trivy-cache"),
		"--output", filepath.Join(env.WorkDir, FileSBOM), env.HostRoot}
	if got := args(t, env.Trivy); !reflect.DeepEqual(got, wantArgs) {
		t.Fatalf("trivy arguments %q, want %q", got, wantArgs)
	}
	content, err := os.ReadFile(filepath.Join(env.WorkDir, FileSBOM))
	must(t, err)
	sum := sha256.Sum256(content)
	want := []runresult.Evidence{{Kind: KindSBOM, Key: "acme/ledger-db-1/scan-1/sbom.cdx.json", SHA256: hex.EncodeToString(sum[:])}}
	if !reflect.DeepEqual(doc.Evidence, want) {
		t.Fatalf("evidence %+v, want %+v", doc.Evidence, want)
	}
	stored := s3.objects["/machine-evidence/acme/ledger-db-1/scan-1/sbom.cdx.json"]
	if string(stored) != string(content) {
		t.Fatalf("stored %q under keys %v, want the SBOM", stored, s3.keys())
	}
	if len(s3.keys()) != 1 || !strings.HasPrefix(s3.auth[0], "AWS4-HMAC-SHA256 Credential=AKTEST/") {
		t.Fatalf("keys %v, authorization %q", s3.keys(), s3.auth)
	}
}

func TestInventoryWithAKilledCollectorIsIncomplete(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	fakeProgram(t, env.Trivy, `printf '{"bomFormat":' > "`+filepath.Join(env.WorkDir, FileSBOM)+`"; kill -KILL $$`)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Incomplete || doc.Completed || len(doc.Evidence) != 0 {
		t.Fatalf("got %+v, want Incomplete, not completed, no evidence", doc)
	}
	if !strings.HasPrefix(doc.Message, "trivy was stopped: signal: killed") {
		t.Fatalf("message %q", doc.Message)
	}
	if s3.requests() != 0 {
		t.Fatalf("%d requests reached the evidence store, want 0", s3.requests())
	}
}

func TestInventoryWithAFailingCollectorIsFailed(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	fakeProgram(t, env.Trivy, `echo "cache dir is read-only" >&2; exit 3`)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Failed || doc.Completed || doc.Message != "trivy exited 3: cache dir is read-only" {
		t.Fatalf("got %+v", doc)
	}
	if s3.requests() != 0 {
		t.Fatalf("%d requests reached the evidence store, want 0", s3.requests())
	}
}

func TestInventoryWithoutACycloneDXDocumentIsFailed(t *testing.T) {
	for name, body := range map[string]string{
		"no file":       "exit 0",
		"not CycloneDX": `printf '{"SchemaVersion":2}' > "` + "$(dirname \"$0\")/../work/" + FileSBOM + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t, nil)
			fakeProgram(t, env.Trivy, body)
			doc := Run(context.Background(), config(TaskInventory, nil), env)
			if doc.Outcome != runresult.Failed || doc.Completed {
				t.Fatalf("got %+v, want Failed and not completed", doc)
			}
		})
	}
}

func TestAFailedStoreIsFailed(t *testing.T) {
	s3 := newFakeS3(t)
	s3.status = http.StatusForbidden
	env := testEnv(t, s3)
	fakeProgram(t, env.Trivy, writeSBOM)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Failed || !strings.HasPrefix(doc.Message, "evidence-upload-failed: sbom.cdx.json: ") || len(doc.Evidence) != 0 {
		t.Fatalf("got %+v", doc)
	}
}

func TestAStoreWithoutTheKeyPairIsFailed(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	must(t, os.Remove(filepath.Join(env.BindingDir, BindingSecretAccessKey)))
	fakeProgram(t, env.Trivy, writeSBOM)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Failed || doc.Message != "evidence-upload-failed: the binding file secret-access-key cannot be read" {
		t.Fatalf("got %+v", doc)
	}
	if s3.requests() != 0 {
		t.Fatalf("%d requests reached the evidence store, want 0", s3.requests())
	}
}

func TestAnEndpointWithAnUntrustedCertificateIsFailed(t *testing.T) {
	s3 := newFakeS3(t)
	env := testEnv(t, s3)
	must(t, os.Remove(filepath.Join(env.BindingDir, BindingCA)))
	fakeProgram(t, env.Trivy, writeSBOM)
	doc := Run(context.Background(), config(TaskInventory, s3), env)
	if doc.Outcome != runresult.Failed || !strings.Contains(doc.Message, "certificate") {
		t.Fatalf("got %+v, want Failed on the certificate", doc)
	}
	if len(s3.keys()) != 0 {
		t.Fatalf("objects %v were stored", s3.keys())
	}
}

func TestACancelledRunIsCancelled(t *testing.T) {
	env := testEnv(t, nil)
	started := filepath.Join(env.WorkDir, "started")
	fakeProgram(t, env.Trivy, `: > "`+started+`"; exec sleep 30`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	doc := Run(ctx, config(TaskInventory, nil), env)
	if doc.Outcome != runresult.Cancelled || doc.Completed {
		t.Fatalf("got %+v, want Cancelled", doc)
	}
}

func TestOtherModesAndTasksAreRefused(t *testing.T) {
	env := testEnv(t, nil)
	ssh := config(TaskInventory, nil)
	ssh.Mode = ModeSSH
	facts := config(TaskFacts, nil)
	for want, cfg := range map[string]Config{"not-supported: mode ssh": ssh, "not-supported: task facts": facts} {
		doc := Run(context.Background(), cfg, env)
		if doc.Outcome != runresult.Refused || doc.Message != want || doc.Completed {
			t.Errorf("got %+v, want Refused %q", doc, want)
		}
	}
	if args(t, env.Trivy) != nil {
		t.Fatal("trivy ran")
	}
}
