package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-secret-value-never-printed"

// validBinding is a well-formed binding of object-storage v1 whose endpoint nothing listens on.
func validBinding() map[string]string {
	return map[string]string{
		"type":              "s3",
		"provider":          "bp-object-bucket",
		"endpoint":          "https://127.0.0.1:1",
		"region":            "us-east-1",
		"bucket":            "probe-bucket",
		"path-style":        "true",
		"access-key-id":     "TESTACCESSKEY",
		"secret-access-key": testSecret,
		"ca.crt":            "",
	}
}

func writeBinding(t *testing.T, keys map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for k, v := range keys {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runProbe(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(context.Background(), args, &out)
	return code, out.String()
}

func TestRunRefusesAMalformedBinding(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]string)
		want string
	}{
		{"type missing", func(m map[string]string) { delete(m, "type") }, "key type missing"},
		{"provider missing", func(m map[string]string) { delete(m, "provider") }, "key provider missing"},
		{"endpoint missing", func(m map[string]string) { delete(m, "endpoint") }, "key endpoint missing"},
		{"region missing", func(m map[string]string) { delete(m, "region") }, "key region missing"},
		{"bucket missing", func(m map[string]string) { delete(m, "bucket") }, "key bucket missing"},
		{"path-style missing", func(m map[string]string) { delete(m, "path-style") }, "key path-style missing"},
		{"access key missing", func(m map[string]string) { delete(m, "access-key-id") }, "key access-key-id missing"},
		{"secret key missing", func(m map[string]string) { delete(m, "secret-access-key") }, "key secret-access-key missing"},
		{"secret key empty", func(m map[string]string) { m["secret-access-key"] = "\n" }, "key secret-access-key empty"},
		{"region empty", func(m map[string]string) { m["region"] = "" }, "key region empty"},
		{"type not s3", func(m map[string]string) { m["type"] = "gcs" }, `key type is "gcs", want s3`},
		{"endpoint ftp", func(m map[string]string) { m["endpoint"] = "ftp://s3.example.com" }, `key endpoint "ftp://s3.example.com": scheme must be https`},
		{"endpoint http outside the cluster", func(m map[string]string) { m["endpoint"] = "http://s3.example.com" },
			`key endpoint "http://s3.example.com": http is allowed only for a service name of the cluster (*.svc, *.svc.cluster.local)`},
		{"endpoint with a path", func(m map[string]string) { m["endpoint"] = "https://s3.example.com/bucket" },
			`key endpoint "https://s3.example.com/bucket" must be scheme://host[:port] only`},
		{"endpoint without a host", func(m map[string]string) { m["endpoint"] = "https://" }, `key endpoint "https://" has no host`},
		{"bucket not a name", func(m map[string]string) { m["bucket"] = "Probe_Bucket" }, `key bucket "Probe_Bucket" is not a bucket name`},
		{"path-style not a boolean", func(m map[string]string) { m["path-style"] = "yes" }, `key path-style is "yes", want true or false`},
		{"ca.crt not PEM", func(m map[string]string) { m["ca.crt"] = "not a certificate" }, "key ca.crt holds no PEM certificate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			keys := validBinding()
			c.edit(keys)
			code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", writeBinding(t, keys))
			if code != 1 {
				t.Errorf("exit code %d, want 1", code)
			}
			if want := "PROBE fail object-storage-v1 " + c.want + "\n"; out != want {
				t.Errorf("output %q, want %q", out, want)
			}
			if strings.Contains(out, testSecret) {
				t.Errorf("output %q prints the secret key", out)
			}
		})
	}
}

func TestRunAcceptsAnOptionalKeyAbsentAndAClusterLocalHTTPEndpoint(t *testing.T) {
	keys := validBinding()
	delete(keys, "ca.crt")
	keys["endpoint"] = "http://seaweedfs-s3.seaweedfs.svc.cluster.local:1"
	b, err := readBinding(writeBinding(t, keys), objectStorageV1Keys)
	if err != nil {
		t.Fatalf("readBinding: %v", err)
	}
	got, err := checkObjectStorage(b)
	if err != nil {
		t.Fatalf("checkObjectStorage: %v", err)
	}
	if got.secure || got.host != "seaweedfs-s3.seaweedfs.svc.cluster.local:1" || !got.pathStyle || got.bucket != "probe-bucket" || got.roots != nil {
		t.Errorf("binding %+v: want secure false, that host, path style, bucket probe-bucket, no extra roots", got)
	}
}

func TestRunFailsWhenTheServiceDoesNotAnswer(t *testing.T) {
	code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", writeBinding(t, validBinding()))
	if code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if want := "PROBE fail object-storage-v1 put object capability-probe/"; !strings.HasPrefix(out, want) ||
		!strings.Contains(out, " into bucket probe-bucket at https://127.0.0.1:1: ") || strings.Count(out, "\n") != 1 {
		t.Errorf("output %q, want one line starting %q that names the bucket and the endpoint", out, want)
	}
	if strings.Contains(out, testSecret) {
		t.Errorf("output %q prints the secret key", out)
	}
}

func TestKeysOnlyChecksTheKeysAndCallsNoService(t *testing.T) {
	code, out := runProbe(t, "--probe", "object-storage-v1", "--binding-dir", writeBinding(t, validBinding()), "--keys-only")
	if want := "PROBE pass object-storage-v1 keys of bucket probe-bucket at https://127.0.0.1:1 present and well formed (provider bp-object-bucket)\n"; code != 0 || out != want {
		t.Errorf("exit code %d and output %q, want 0 and %q (nothing listens on that endpoint)", code, out, want)
	}
	keys := validBinding()
	delete(keys, "bucket")
	code, out = runProbe(t, "--probe", "object-storage-v1", "--binding-dir", writeBinding(t, keys), "--keys-only")
	if want := "PROBE fail object-storage-v1 key bucket missing\n"; code != 1 || out != want {
		t.Errorf("a missing key with --keys-only: exit code %d and output %q, want 1 and %q", code, out, want)
	}
}

func TestRunRefusesABadCommandLine(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, "PROBE fail - usage: --probe is required\n"},
		{[]string{"--probe", "nope", "--binding-dir", "/x"}, "PROBE fail nope unknown probe\n"},
		{[]string{"--probe", "object-storage-v1"}, "PROBE fail object-storage-v1 usage: --binding-dir is required\n"},
		{[]string{"--probe", "object-storage-v1", "--binding-dir", "/x", "extra"}, "PROBE fail - usage: unexpected argument extra\n"},
		{[]string{"--idle", "--probe", "object-storage-v1"}, "PROBE fail - usage: --idle takes no other flag\n"},
		{[]string{"--idle", "--keys-only"}, "PROBE fail - usage: --idle takes no other flag\n"},
		{[]string{"--no-such-flag"}, "PROBE fail - usage: flag provided but not defined: -no-such-flag\n"},
	}
	for _, c := range cases {
		code, out := runProbe(t, c.args...)
		if code != 1 || out != c.want {
			t.Errorf("args %q: exit code %d and output %q, want 1 and %q", c.args, code, out, c.want)
		}
	}
}

func TestIdleWaitsUntilStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"--idle"}, &out) }()
	select {
	case code := <-done:
		t.Fatalf("--idle returned %d before it was stopped", code)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 || out.Len() != 0 {
			t.Errorf("--idle after stop: exit code %d and output %q, want 0 and nothing", code, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("--idle did not return after it was stopped")
	}
}
