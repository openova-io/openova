package main

import (
	"context"
	"crypto/x509"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// These tests need the S3 server of testdata/s3-server.sh; the workflow build-capability-probe.yaml
// starts it and sets PROBE_TEST_S3_DIR. Without it they fail: a skip would pass with nothing tested.
func s3Dir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("PROBE_TEST_S3_DIR")
	if dir == "" {
		t.Fatal("PROBE_TEST_S3_DIR is not set: run sh testdata/s3-server.sh start <dir> and set it to <dir>")
	}
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// copyBinding copies the binding directory and applies the edits; an empty value removes the key.
func copyBinding(t *testing.T, from string, edits map[string]string) string {
	t.Helper()
	keys := map[string]string{}
	for _, k := range objectStorageV1Keys {
		keys[k.name] = readFile(t, filepath.Join(from, k.name))
	}
	for k, v := range edits {
		if v == "" {
			delete(keys, k)
			continue
		}
		keys[k] = v
	}
	return writeBinding(t, keys)
}

// adminClient is the S3 client of the Admin identity, trusted with the server's CA.
func adminClient(t *testing.T, dir string) *minio.Client {
	t.Helper()
	binding := filepath.Join(dir, "binding")
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(readFile(t, filepath.Join(binding, "ca.crt")))) {
		t.Fatal("ca.crt of the test binding holds no certificate")
	}
	transport, err := minio.DefaultTransport(true)
	if err != nil {
		t.Fatal(err)
	}
	transport.TLSClientConfig.RootCAs = roots
	host := strings.TrimPrefix(readFile(t, filepath.Join(binding, "endpoint")), "https://")
	c, err := minio.New(host, &minio.Options{
		Creds: credentials.NewStaticV4(readFile(t, filepath.Join(dir, "admin", "access-key-id")),
			readFile(t, filepath.Join(dir, "admin", "secret-access-key")), ""),
		Secure: true, Region: "us-east-1", BucketLookup: minio.BucketLookupPath, Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func makeBucket(t *testing.T, c *minio.Client, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	exists, err := c.BucketExists(ctx, name)
	if err != nil {
		t.Fatalf("bucket %s: %v", name, err)
	}
	if !exists {
		if err := c.MakeBucket(ctx, name, minio.MakeBucketOptions{Region: "us-east-1"}); err != nil {
			t.Fatalf("make bucket %s: %v", name, err)
		}
	}
}

// leftovers counts every object and every version under the probe's prefix.
func leftovers(t *testing.T, c *minio.Client, bucket string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n := 0
	for o := range c.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: "capability-probe/", Recursive: true, WithVersions: true}) {
		if o.Err != nil {
			t.Fatalf("list %s: %v", bucket, o.Err)
		}
		n++
	}
	return n
}

var passLine = regexp.MustCompile(`^PROBE pass object-storage-v1 put, got and deleted object capability-probe/[0-9a-f]{16} in bucket (probe-bucket|probe-versioned) at https://localhost:[0-9]+ \(provider ci-seaweedfs\)\n$`)

func TestObjectStorageV1PassesAndLeavesNothing(t *testing.T) {
	dir := s3Dir(t)
	admin := adminClient(t, dir)
	makeBucket(t, admin, "probe-bucket")
	code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", filepath.Join(dir, "binding"))
	if code != 0 || !passLine.MatchString(out) || !strings.Contains(out, " in bucket probe-bucket ") {
		t.Fatalf("exit code %d and output %q, want 0 and a pass line for probe-bucket", code, out)
	}
	if n := leftovers(t, admin, "probe-bucket"); n != 0 {
		t.Errorf("probe-bucket holds %d objects of the probe after the run, want 0", n)
	}
}

func TestObjectStorageV1LeavesNoVersionInAVersionedBucket(t *testing.T) {
	dir := s3Dir(t)
	admin := adminClient(t, dir)
	makeBucket(t, admin, "probe-versioned")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := admin.EnableVersioning(ctx, "probe-versioned"); err != nil {
		t.Fatalf("enable versioning: %v", err)
	}
	cfg, err := admin.GetBucketVersioning(ctx, "probe-versioned")
	if err != nil || cfg.Status != "Enabled" {
		t.Fatalf("versioning of probe-versioned is %q (%v), want Enabled", cfg.Status, err)
	}
	binding := copyBinding(t, filepath.Join(dir, "binding"), map[string]string{"bucket": "probe-versioned"})
	code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", binding)
	if code != 0 || !passLine.MatchString(out) || !strings.Contains(out, " in bucket probe-versioned ") {
		t.Fatalf("exit code %d and output %q, want 0 and a pass line for probe-versioned", code, out)
	}
	if n := leftovers(t, admin, "probe-versioned"); n != 0 {
		t.Errorf("probe-versioned holds %d versions or delete markers of the probe after the run, want 0", n)
	}
}

func TestObjectStorageV1FailsOnARefusal(t *testing.T) {
	dir := s3Dir(t)
	admin := adminClient(t, dir)
	makeBucket(t, admin, "probe-bucket")
	makeBucket(t, admin, "probe-other")
	from := filepath.Join(dir, "binding")
	cases := []struct {
		name  string
		edits map[string]string
		want  string
	}{
		{"a wrong secret key", map[string]string{"secret-access-key": "0000000000wrong0000000000"}, " into bucket probe-bucket at https://localhost:"},
		{"a bucket the key is not scoped to", map[string]string{"bucket": "probe-other"}, " into bucket probe-other at https://localhost:"},
		{"no ca.crt for a private CA", map[string]string{"ca.crt": ""}, "certificate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", copyBinding(t, from, c.edits))
			if code != 1 || !strings.HasPrefix(out, "PROBE fail object-storage-v1 put object capability-probe/") ||
				!strings.Contains(out, c.want) || strings.Count(out, "\n") != 1 {
				t.Errorf("exit code %d and output %q, want 1 and one fail line of the put that contains %q", code, out, c.want)
			}
		})
	}
	if n := leftovers(t, admin, "probe-other"); n != 0 {
		t.Errorf("probe-other holds %d objects after a refused put, want 0", n)
	}
}
