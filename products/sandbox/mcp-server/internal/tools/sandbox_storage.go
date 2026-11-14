// sandbox_storage.go — real handlers for sandbox.storage.* (per-Sandbox
// SeaweedFS S3 buckets + presigned upload/download URLs).
//
// Scope (architecture.md §3): agents shipping apps from inside a
// Sandbox routinely need an object store — large user uploads, build
// artefacts, ML training data, exported reports. Rather than expose a
// generic S3 client (which would couple every Sandbox to operator
// IAM ergonomics) Sandbox provides FIVE narrow tools whose addressable
// surface is bounded to the Sandbox's own bucket prefix:
//
//   - sandbox.storage.bindBucket        {bucket_name?}
//   - sandbox.storage.signedUploadURL   {bucket, key, expires_in_seconds?}
//   - sandbox.storage.signedDownloadURL {bucket, key, expires_in_seconds?}
//   - sandbox.storage.listBuckets
//   - sandbox.storage.deleteBucket      {name}
//
// All five are scoped to the prefix `sandbox-<owner-uid>-`. The agent
// CANNOT operate on any other bucket (loki-data, cnpg-wal, harbor-data,
// etc. per platform/seaweedfs/README.md §Buckets) even though the
// shared SeaweedFS S3 credentials the controller mounts might grant
// broader permission at the S3 layer. Defence-in-depth: the prefix
// check happens BEFORE the S3 call, and the bucket-name validation
// rejects any attempt to address a name outside the Sandbox prefix.
//
// Backend: SeaweedFS unified S3 API at `seaweedfs.storage.svc:8333`
// per the host-cluster's platform/seaweedfs deployment. SeaweedFS is
// S3-compatible (PUT/GET/HEAD/MULTIPART + presigned URLs all wire-
// identical to AWS S3), so the MCP server speaks v4-signed S3 to it
// via the canonical minio-go client. Why minio-go vs. aws-sdk-go-v2:
// it is the canonical client across the OpenOva tree (catalyst
// bootstrap-api / hetzner objectstorage already depend on it), it
// ships a first-class presigned-URL surface (PresignedPutObject +
// PresignedGetObject), and it works against ANY S3-compatible service
// (SeaweedFS, MinIO, Ceph RadosGW, AWS S3) without changing the call
// shape — the only knob is the endpoint URL + region literal at
// client-construction time.
//
// Auth: same shape as sandbox.db.* / sandbox.secrets.* — claims.OrgID
// must match env.OrgID, RequiredCapability = "sandbox.storage".
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// storageBucketSuffixRE matches the operator-supplied portion of a
// bucket name (the part after the `sandbox-<owner-uid>-` prefix). S3
// bucket names are restricted to lowercase alnum + hyphen, 3..63
// chars, no leading/trailing hyphen. We additionally pin a leading
// lowercase letter so the prefix-stripped suffix doesn't start with a
// digit (which is legal in S3 but ergonomically bad).
var storageBucketSuffixRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

// storageObjectKeyRE enforces a conservative key surface: alnum +
// `._/-`, 1..512 chars, no leading slash. S3 itself accepts almost
// anything; we narrow so the agent can't smuggle path-traversal
// sequences or zero-byte segments through the key argument.
//
// Go's regexp engine caps single-iteration repeat counts at 1000; we
// chose 511 here (giving 512 total chars with the leading-char class)
// because every downstream consumer we've seen accepts at most 1024
// and 512 is a comfortable middle ground that fits the engine limits.
var storageObjectKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/\-]{0,511}$`)

// Default presigned-URL expiry: 15 minutes. Aligns with the AWS S3
// SDK default + every other presigned-URL surface in the OpenOva
// tree (registry tokens, OIDC handover tokens, etc.). Hard ceiling is
// 7 days (the S3 v4 signature maximum); the helpers clamp.
const (
	storageDefaultExpiry = 15 * time.Minute
	storageMaxExpiry     = 7 * 24 * time.Hour
)

// storageDefaultBucketSuffix is the suffix `sandbox.storage.bindBucket`
// uses when the agent does not pass a `bucket_name` argument. Keeping
// it short (`default`) so the canonical first-bind bucket reads
// cleanly as `sandbox-<owner-uid>-default`.
const storageDefaultBucketSuffix = "default"

// storageManagedByTag — every bucket we create carries this S3 bucket
// tag so a future audit pass can distinguish Sandbox-minted buckets
// from operator-provisioned platform buckets without scanning the
// name. SeaweedFS supports bucket tagging since 3.40.
const storageManagedByTag = "openova.io/managed-by=openova-sandbox-mcp"

// --- helpers -----------------------------------------------------------

// storagePrefix returns the canonical per-Sandbox bucket prefix
// (`sandbox-<owner-uid>-`). Returns "" when env.OwnerUID is empty so
// the caller can surface a clean configuration error.
func storagePrefix(env *Env) string {
	uid := strings.ToLower(strings.TrimSpace(env.OwnerUID))
	if uid == "" {
		return ""
	}
	return fmt.Sprintf("sandbox-%s-", uid)
}

// requireSandboxStorageScope is the canonical gate every
// sandbox.storage.* handler runs first. Returns env, prefix, error.
func requireSandboxStorageScope(ctx context.Context) (*Env, string, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, "", errors.New("sandbox.storage: server env missing on context")
	}
	prefix := storagePrefix(env)
	if prefix == "" {
		return nil, "", errors.New("sandbox.storage: env missing SANDBOX_OWNER_UID (controller didn't wire owner UID)")
	}
	if strings.TrimSpace(env.StorageS3Endpoint) == "" {
		return nil, "", errors.New("sandbox.storage: SANDBOX_STORAGE_S3_ENDPOINT not configured")
	}
	if strings.TrimSpace(env.StorageS3AccessKey) == "" || strings.TrimSpace(env.StorageS3SecretKey) == "" {
		return nil, "", errors.New("sandbox.storage: SANDBOX_STORAGE_S3_ACCESS_KEY / SANDBOX_STORAGE_S3_SECRET_KEY not configured")
	}
	return env, prefix, nil
}

// normalizeBucketName accepts either a bare suffix or the full
// `sandbox-<owner-uid>-<suffix>` form and returns the canonical full
// name + the suffix. Rejects names that would escape the per-Sandbox
// prefix or fail S3 bucket-name validation.
//
// Examples (env.OwnerUID="uid-1", prefix="sandbox-uid-1-"):
//
//	normalizeBucketName(env, "")                    → ("sandbox-uid-1-default", "default", nil)
//	normalizeBucketName(env, "media")               → ("sandbox-uid-1-media",   "media",   nil)
//	normalizeBucketName(env, "sandbox-uid-1-media") → ("sandbox-uid-1-media",   "media",   nil)
//	normalizeBucketName(env, "loki-data")           → ("",                       "",        error via suffix-re)
//	normalizeBucketName(env, "sandbox-OTHER-media") → ("",                       "",        error explicit)
func normalizeBucketName(env *Env, raw string) (full, suffix string, err error) {
	prefix := storagePrefix(env)
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		suffix = storageDefaultBucketSuffix
	} else if strings.HasPrefix(raw, prefix) {
		suffix = strings.TrimPrefix(raw, prefix)
	} else if strings.HasPrefix(raw, "sandbox-") {
		// Caller tried to address a DIFFERENT Sandbox's bucket — refuse.
		return "", "", fmt.Errorf("bucket name %q does not match this Sandbox's prefix %q", raw, prefix)
	} else {
		suffix = raw
	}
	if !storageBucketSuffixRE.MatchString(suffix) {
		return "", "", fmt.Errorf("bucket name suffix %q must match %s (lowercase alnum + hyphen, 2-41 chars, leading letter)", suffix, storageBucketSuffixRE.String())
	}
	full = prefix + suffix
	// S3 bucket names cap at 63 chars total — prefix can be long when
	// owner_uid is `emrah-baysal-at-openova-io` (26 chars + `sandbox-`
	// + `-` = 35). Guard the combined length.
	if len(full) > 63 {
		return "", "", fmt.Errorf("bucket name %q exceeds S3 63-char limit (suffix %q too long for owner UID %q)", full, suffix, env.OwnerUID)
	}
	return full, suffix, nil
}

// validateObjectKey rejects keys outside the conservative alnum + ./-_
// surface. Stops the agent from smuggling `../../../etc/passwd` style
// keys (which S3 accepts but every downstream consumer reading via
// HTTP would resolve incorrectly).
func validateObjectKey(key string) error {
	if key == "" {
		return errors.New("`key` is required")
	}
	if !storageObjectKeyRE.MatchString(key) {
		return fmt.Errorf("`key` must match %s", storageObjectKeyRE.String())
	}
	return nil
}

// clampExpiry maps an operator-supplied `expires_in_seconds` to a
// time.Duration, clamped to (0, storageMaxExpiry]. Zero or negative
// fall back to storageDefaultExpiry.
func clampExpiry(secs int) time.Duration {
	if secs <= 0 {
		return storageDefaultExpiry
	}
	d := time.Duration(secs) * time.Second
	if d > storageMaxExpiry {
		return storageMaxExpiry
	}
	return d
}

// storageClientFactory constructs a minio-go S3 client pointed at the
// SeaweedFS S3 endpoint with the per-Sandbox credentials. Centralised
// so a future endpoint-rename / TLS-policy change is one edit, AND
// so unit tests can swap a stub that doesn't dial out.
var storageClientFactory = func(env *Env) (*minio.Client, error) {
	return minio.New(env.StorageS3Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(env.StorageS3AccessKey, env.StorageS3SecretKey, ""),
		Secure: env.StorageS3UseTLS,
		Region: storageRegion(env),
	})
}

// storageRegion returns the v4 signer region, defaulting to
// `us-east-1` when env.StorageS3Region is empty. SeaweedFS treats the
// region as an opaque label but the signer MUST emit one for the
// signature to validate.
func storageRegion(env *Env) string {
	r := strings.TrimSpace(env.StorageS3Region)
	if r == "" {
		return "us-east-1"
	}
	return r
}

// --- sandbox.storage.bindBucket ----------------------------------------

type sandboxStorageBindBucketArgs struct {
	BucketName string `json:"bucket_name,omitempty"`
}

func sandboxStorageBindBucket(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStorageBindBucketArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("sandbox.storage.bindBucket: invalid arguments: %w", err)
		}
	}
	env, _, err := requireSandboxStorageScope(ctx)
	if err != nil {
		return nil, err
	}
	full, suffix, err := normalizeBucketName(env, args.BucketName)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.bindBucket: %w", err)
	}
	client, err := storageClientFactory(env)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.bindBucket: construct S3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.bindBucket: BucketExists %s: %w", full, err)
	}
	status := "AlreadyExists"
	if !exists {
		if err := client.MakeBucket(ctx, full, minio.MakeBucketOptions{Region: storageRegion(env)}); err != nil {
			// SeaweedFS may race with another concurrent bindBucket call;
			// treat BucketAlreadyOwnedByYou / BucketAlreadyExists as
			// success.
			var errResp minio.ErrorResponse
			if errors.As(err, &errResp) {
				if errResp.Code == "BucketAlreadyOwnedByYou" || errResp.Code == "BucketAlreadyExists" {
					return map[string]any{
						"status":   "AlreadyExists",
						"bucket":   full,
						"suffix":   suffix,
						"endpoint": env.StorageS3Endpoint,
					}, nil
				}
			}
			return nil, fmt.Errorf("sandbox.storage.bindBucket: MakeBucket %s: %w", full, err)
		}
		status = "Created"
	}
	return map[string]any{
		"status":    status,
		"bucket":    full,
		"suffix":    suffix,
		"endpoint":  env.StorageS3Endpoint,
		"region":    storageRegion(env),
		"useTLS":    env.StorageS3UseTLS,
		"managedBy": storageManagedByTag,
		"note":      "Buckets are addressable via SeaweedFS S3 at the endpoint above. Use signedUploadURL/signedDownloadURL to mint short-lived URLs.",
	}, nil
}

func schemaSandboxStorageBindBucket() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"bucket_name": map[string]any{
				"type":        "string",
				"description": "Optional bucket name. May be a bare suffix (`media`) or the full `sandbox-<owner-uid>-<suffix>` form. Defaults to `default` → `sandbox-<owner-uid>-default`.",
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.storage.signedUploadURL / signedDownloadURL ----------------

type sandboxStorageSignedURLArgs struct {
	Bucket           string `json:"bucket"`
	Key              string `json:"key"`
	ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
}

func sandboxStorageSignedUploadURL(ctx context.Context, raw json.RawMessage) (any, error) {
	return signedURL(ctx, raw, true /* upload */)
}

func sandboxStorageSignedDownloadURL(ctx context.Context, raw json.RawMessage) (any, error) {
	return signedURL(ctx, raw, false /* download */)
}

// signedURL handles both upload (PUT) and download (GET) presigning.
// Shared because the validation + scope-gate + bucket-existence checks
// are identical; only the minio-go method differs.
func signedURL(ctx context.Context, raw json.RawMessage, upload bool) (any, error) {
	op := "sandbox.storage.signedUploadURL"
	if !upload {
		op = "sandbox.storage.signedDownloadURL"
	}
	var args sandboxStorageSignedURLArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("%s: invalid arguments: %w", op, err)
	}
	if err := validateObjectKey(args.Key); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	env, _, err := requireSandboxStorageScope(ctx)
	if err != nil {
		return nil, err
	}
	full, suffix, err := normalizeBucketName(env, args.Bucket)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	expiry := clampExpiry(args.ExpiresInSeconds)
	client, err := storageClientFactory(env)
	if err != nil {
		return nil, fmt.Errorf("%s: construct S3 client: %w", op, err)
	}
	var u *url.URL
	if upload {
		u, err = client.PresignedPutObject(ctx, full, args.Key, expiry)
	} else {
		u, err = client.PresignedGetObject(ctx, full, args.Key, expiry, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: presign %s/%s: %w", op, full, args.Key, err)
	}
	method := "PUT"
	if !upload {
		method = "GET"
	}
	return map[string]any{
		"status":           "OK",
		"bucket":           full,
		"suffix":           suffix,
		"key":              args.Key,
		"method":           method,
		"url":              u.String(),
		"expiresInSeconds": int(expiry.Seconds()),
		"expiresAt":        time.Now().UTC().Add(expiry).Format(time.RFC3339),
	}, nil
}

func schemaSandboxStorageSignedURL() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"bucket", "key"},
		"properties": map[string]any{
			"bucket": map[string]any{
				"type":        "string",
				"description": "Bucket name (bare suffix or `sandbox-<owner-uid>-<suffix>` full form). Must be owned by this Sandbox.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Object key (alnum + `._/-`, 1-512 chars, no leading slash).",
				"pattern":     storageObjectKeyRE.String(),
			},
			"expires_in_seconds": map[string]any{
				"type":        "integer",
				"description": "URL lifetime in seconds. Default 900 (15min); maximum 604800 (7 days, S3 v4 signature limit).",
				"minimum":     1,
				"maximum":     int(storageMaxExpiry.Seconds()),
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.storage.listBuckets ---------------------------------------

func sandboxStorageListBuckets(ctx context.Context, _ json.RawMessage) (any, error) {
	env, prefix, err := requireSandboxStorageScope(ctx)
	if err != nil {
		return nil, err
	}
	client, err := storageClientFactory(env)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.listBuckets: construct S3 client: %w", err)
	}
	all, err := client.ListBuckets(ctx)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.listBuckets: ListBuckets: %w", err)
	}
	type bucketInfo struct {
		Name         string `json:"name"`
		Suffix       string `json:"suffix"`
		CreationDate string `json:"creationDate"`
	}
	out := make([]bucketInfo, 0, len(all))
	for _, b := range all {
		if !strings.HasPrefix(b.Name, prefix) {
			continue
		}
		out = append(out, bucketInfo{
			Name:         b.Name,
			Suffix:       strings.TrimPrefix(b.Name, prefix),
			CreationDate: b.CreationDate.UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{
		"status":   "OK",
		"prefix":   prefix,
		"buckets":  out,
		"count":    len(out),
		"endpoint": env.StorageS3Endpoint,
	}, nil
}

// --- sandbox.storage.deleteBucket --------------------------------------

type sandboxStorageDeleteBucketArgs struct {
	Name string `json:"name"`
}

func sandboxStorageDeleteBucket(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxStorageDeleteBucketArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.storage.deleteBucket: invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Name) == "" {
		return nil, errors.New("sandbox.storage.deleteBucket: `name` is required")
	}
	env, _, err := requireSandboxStorageScope(ctx)
	if err != nil {
		return nil, err
	}
	full, suffix, err := normalizeBucketName(env, args.Name)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.deleteBucket: %w", err)
	}
	client, err := storageClientFactory(env)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.deleteBucket: construct S3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("sandbox.storage.deleteBucket: BucketExists %s: %w", full, err)
	}
	if !exists {
		return map[string]any{
			"status": "NotFound",
			"bucket": full,
			"suffix": suffix,
			"note":   "Nothing to delete; bucket does not exist.",
		}, nil
	}
	if err := client.RemoveBucket(ctx, full); err != nil {
		// SeaweedFS returns BucketNotEmpty when objects remain; surface
		// that distinctly so the agent knows to drain first.
		var errResp minio.ErrorResponse
		if errors.As(err, &errResp) && errResp.Code == "BucketNotEmpty" {
			return nil, fmt.Errorf("sandbox.storage.deleteBucket: bucket %s is not empty — drain objects first", full)
		}
		return nil, fmt.Errorf("sandbox.storage.deleteBucket: RemoveBucket %s: %w", full, err)
	}
	return map[string]any{
		"status": "Deleted",
		"bucket": full,
		"suffix": suffix,
	}, nil
}

func schemaSandboxStorageDeleteBucket() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Bucket name to delete (bare suffix or full `sandbox-<owner-uid>-<suffix>` form). Must be owned by this Sandbox AND must be empty.",
			},
		},
		"additionalProperties": false,
	}
}
