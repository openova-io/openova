package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// TestStoragePrefix_Format pins the canonical per-Sandbox bucket
// prefix so the controller, the agent docs, and this server agree on
// where the buckets live.
func TestStoragePrefix_Format(t *testing.T) {
	cases := map[string]struct {
		env  *Env
		want string
	}{
		"happy":   {&Env{OwnerUID: "uid-1"}, "sandbox-uid-1-"},
		"upper":   {&Env{OwnerUID: "UID-1"}, "sandbox-uid-1-"},
		"trimmed": {&Env{OwnerUID: "  uid-1 "}, "sandbox-uid-1-"},
		"missing": {&Env{}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := storagePrefix(tc.env); got != tc.want {
				t.Errorf("got=%q want=%q", got, tc.want)
			}
		})
	}
}

// TestRequireSandboxStorageScope covers the canonical gate every
// sandbox.storage.* handler runs first.
func TestRequireSandboxStorageScope(t *testing.T) {
	t.Run("no env", func(t *testing.T) {
		_, _, err := requireSandboxStorageScope(context.Background())
		if err == nil || !strings.Contains(err.Error(), "env missing") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("no owner uid", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{})
		_, _, err := requireSandboxStorageScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "SANDBOX_OWNER_UID") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("no endpoint", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{OwnerUID: "u1"})
		_, _, err := requireSandboxStorageScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "STORAGE_S3_ENDPOINT") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("no creds", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{
			OwnerUID:          "u1",
			StorageS3Endpoint: "seaweedfs.storage.svc:8333",
		})
		_, _, err := requireSandboxStorageScope(ctx)
		if err == nil || !strings.Contains(err.Error(), "ACCESS_KEY") {
			t.Errorf("err=%v", err)
		}
	})
	t.Run("populated", func(t *testing.T) {
		ctx := WithEnv(context.Background(), &Env{
			OwnerUID:           "u1",
			StorageS3Endpoint:  "seaweedfs.storage.svc:8333",
			StorageS3AccessKey: "a",
			StorageS3SecretKey: "s",
		})
		env, prefix, err := requireSandboxStorageScope(ctx)
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if env == nil || prefix != "sandbox-u1-" {
			t.Errorf("env=%v prefix=%q", env, prefix)
		}
	})
}

// TestNormalizeBucketName pins the prefix-enforcement rules. This is
// the core defence-in-depth gate that stops the agent from addressing
// platform buckets (`loki-data`, `cnpg-wal`, etc.) or another
// Sandbox's namespace.
func TestNormalizeBucketName(t *testing.T) {
	env := &Env{OwnerUID: "uid-1"}
	type tc struct {
		in     string
		full   string
		suffix string
		errSub string
	}
	cases := []tc{
		// Empty → default suffix.
		{in: "", full: "sandbox-uid-1-default", suffix: "default"},
		// Bare suffix.
		{in: "media", full: "sandbox-uid-1-media", suffix: "media"},
		// Already-prefixed full name.
		{in: "sandbox-uid-1-media", full: "sandbox-uid-1-media", suffix: "media"},
		// Bare suffix uppercase (gets lowercased).
		{in: "MEDIA", full: "sandbox-uid-1-media", suffix: "media"},
		// Different Sandbox's prefix — REFUSED.
		{in: "sandbox-other-media", errSub: "does not match this Sandbox's prefix"},
		// Reserved platform bucket — REFUSED via suffix re (digit-leading).
		{in: "1bad", errSub: "must match"},
		// Suffix with underscore — invalid char.
		{in: "media_x", errSub: "must match"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			full, suffix, err := normalizeBucketName(env, c.in)
			if c.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), c.errSub) {
					t.Errorf("in=%q err=%v want %q", c.in, err, c.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("in=%q err=%v", c.in, err)
			}
			if full != c.full || suffix != c.suffix {
				t.Errorf("in=%q got=(%q,%q) want=(%q,%q)", c.in, full, suffix, c.full, c.suffix)
			}
		})
	}
}

// TestNormalizeBucketName_LengthGuard pins the 63-char S3 cap.
func TestNormalizeBucketName_LengthGuard(t *testing.T) {
	env := &Env{OwnerUID: strings.Repeat("x", 40)}
	// prefix=sandbox-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx- = 49 chars,
	// suffix `media` = 5 chars → 54 chars total → OK.
	if _, _, err := normalizeBucketName(env, "media"); err != nil {
		t.Errorf("short suffix should pass: %v", err)
	}
	// 49 + 20-char suffix = 69 chars → over 63.
	if _, _, err := normalizeBucketName(env, "this-is-a-long-suff"); err == nil || !strings.Contains(err.Error(), "63") {
		t.Errorf("long combined name should fail with 63-char err: %v", err)
	}
}

// TestValidateObjectKey covers per-call key validation.
func TestValidateObjectKey(t *testing.T) {
	cases := map[string]string{
		"":                       "is required",
		"a":                      "",
		"foo/bar/baz.png":        "",
		"123-numeric-start.json": "",
		"/leading-slash":         "must match",
		"a:colon":                "must match",
		"a b":                    "must match",
	}
	for in, want := range cases {
		err := validateObjectKey(in)
		if want == "" {
			if err != nil {
				t.Errorf("%q: err=%v want nil", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v want %q", in, err, want)
		}
	}
}

// TestClampExpiry pins the (0, 7d] clamp.
func TestClampExpiry(t *testing.T) {
	if d := clampExpiry(0); d != storageDefaultExpiry {
		t.Errorf("zero: got=%v want=%v", d, storageDefaultExpiry)
	}
	if d := clampExpiry(-1); d != storageDefaultExpiry {
		t.Errorf("negative: got=%v want=%v", d, storageDefaultExpiry)
	}
	if d := clampExpiry(60); d.Seconds() != 60 {
		t.Errorf("60s: got=%v", d)
	}
	// 14 days → clamped to 7.
	if d := clampExpiry(14 * 24 * 60 * 60); d != storageMaxExpiry {
		t.Errorf("14d: got=%v want=%v", d, storageMaxExpiry)
	}
}

// TestStorageRegion pins the default + override.
func TestStorageRegion(t *testing.T) {
	if r := storageRegion(&Env{}); r != "us-east-1" {
		t.Errorf("default: got=%q", r)
	}
	if r := storageRegion(&Env{StorageS3Region: "eu-central-1"}); r != "eu-central-1" {
		t.Errorf("override: got=%q", r)
	}
}

// TestSandboxStorageBindBucket_ArgValidation confirms that arg-validation +
// scope-check errors surface BEFORE we attempt an S3 dial. This stops
// a misleading creds error from masking a bad bucket name.
func TestSandboxStorageBindBucket_ArgValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{
		OwnerUID:           "u1",
		StorageS3Endpoint:  "seaweedfs.storage.svc:8333",
		StorageS3AccessKey: "a",
		StorageS3SecretKey: "s",
	})
	// Bad bucket name — surfaces from normalizeBucketName BEFORE the
	// minio client tries to dial.
	_, err := sandboxStorageBindBucket(ctx, json.RawMessage(`{"bucket_name":"sandbox-other-media"}`))
	if err == nil || !strings.Contains(err.Error(), "does not match this Sandbox's prefix") {
		t.Errorf("err=%v want prefix-mismatch", err)
	}
}

func TestSandboxStorageSignedURL_ArgValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{
		OwnerUID:           "u1",
		StorageS3Endpoint:  "seaweedfs.storage.svc:8333",
		StorageS3AccessKey: "a",
		StorageS3SecretKey: "s",
	})
	cases := map[string]string{
		`{}`:                                   "is required",
		`{"bucket":"media"}`:                   "is required",
		`{"bucket":"media","key":"/leading"}`:  "must match",
		`{"bucket":"sandbox-other","key":"k"}`: "does not match",
	}
	for args, want := range cases {
		t.Run(args, func(t *testing.T) {
			_, err := sandboxStorageSignedUploadURL(ctx, json.RawMessage(args))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err=%v want %q", err, want)
			}
		})
	}
}

func TestSandboxStorageDeleteBucket_ArgValidation(t *testing.T) {
	ctx := WithEnv(context.Background(), &Env{
		OwnerUID:           "u1",
		StorageS3Endpoint:  "seaweedfs.storage.svc:8333",
		StorageS3AccessKey: "a",
		StorageS3SecretKey: "s",
	})
	// Missing name — surfaces early.
	_, err := sandboxStorageDeleteBucket(ctx, json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Errorf("missing: err=%v", err)
	}
	// Other-Sandbox bucket — surfaces from normalize.
	_, err = sandboxStorageDeleteBucket(ctx, json.RawMessage(`{"name":"sandbox-other-x"}`))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("other-sandbox: err=%v", err)
	}
}

// TestSandboxStorageCapabilityGate confirms the registry rejects
// sandbox.storage.* calls whose claims lack the `sandbox.storage` cap.
func TestSandboxStorageCapabilityGate(t *testing.T) {
	r := NewRegistry(&Env{
		OrgID:              "acme",
		JWTSecret:          []byte("x"),
		OwnerUID:           "u1",
		StorageS3Endpoint:  "seaweedfs.storage.svc:8333",
		StorageS3AccessKey: "a",
		StorageS3SecretKey: "s",
	})
	for _, name := range []string{
		"sandbox.storage.bindBucket",
		"sandbox.storage.signedUploadURL",
		"sandbox.storage.signedDownloadURL",
		"sandbox.storage.listBuckets",
		"sandbox.storage.deleteBucket",
	} {
		t.Run(name, func(t *testing.T) {
			cl := &sharedauth.Claims{OrgID: "acme", Capabilities: []string{"sandbox.db"}}
			args := json.RawMessage(`{"bucket":"media","key":"k","name":"media"}`)
			_, err := r.Call(context.Background(), name, args, CallOpts{Claims: cl})
			if err == nil || !strings.Contains(err.Error(), "forbidden") {
				t.Errorf("missing cap: err=%v want forbidden", err)
			}
		})
	}
}

// TestSandboxStorageCatalogueWiredIn ensures all five tools are in
// the catalogue with non-nil Handler + the right RequiredCapability.
func TestSandboxStorageCatalogueWiredIn(t *testing.T) {
	r := NewRegistry(&Env{OrgID: "acme", OwnerUID: "u1"})
	want := map[string]bool{
		"sandbox.storage.bindBucket":        false,
		"sandbox.storage.signedUploadURL":   false,
		"sandbox.storage.signedDownloadURL": false,
		"sandbox.storage.listBuckets":       false,
		"sandbox.storage.deleteBucket":      false,
	}
	for _, tool := range r.List() {
		if _, ok := want[tool.Name]; !ok {
			continue
		}
		if tool.Handler == nil {
			t.Errorf("%s: Handler is nil", tool.Name)
		}
		if tool.RequiredCapability != "sandbox.storage" {
			t.Errorf("%s: RequiredCapability=%q want sandbox.storage", tool.Name, tool.RequiredCapability)
		}
		want[tool.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s: missing from catalogue", name)
		}
	}
}
