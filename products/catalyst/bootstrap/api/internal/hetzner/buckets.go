// buckets.go — Hetzner Object Storage bucket purge for the wizard's
// Cancel & Wipe path (issue #706).
//
// Background: tofu provisions a per-Sovereign Hetzner Object Storage
// bucket (resource `minio_s3_bucket` in infra/hetzner/main.tf) named
// `catalyst-<sovereign-fqdn-with-dashes>` (e.g.
// `catalyst-otech50-omani-works`). The bucket holds Harbor + Velero
// backing data plus the occasional CNPG test artefact. tofu's bucket
// resource has `force_destroy=false` semantics and Hetzner's S3 endpoint
// rejects DELETE Bucket while objects are present, so `tofu destroy`
// silently skips it — the bucket lives forever, costing money.
//
// This file adds an explicit, idempotent S3-API path that:
//
//  1. Lists every object version + delete marker in the bucket and
//     batch-deletes them (1000 at a time, the AWS S3 protocol limit).
//  2. Aborts every in-progress multipart upload (otherwise the next
//     DELETE Bucket fails with `BucketNotEmpty` even after object
//     deletion succeeds).
//  3. DELETEs the bucket itself.
//
// The function is invoked from wipe.go AFTER `tofu destroy` completes,
// so we never fight tofu state. A 404 (bucket already gone) is treated
// as success — callers see BucketsRemoved=0 with no error, which is the
// correct shape for an idempotent re-run.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 (OpenTofu owns Phase 0 / Crossplane
// is the only Day-2 IaC seam): this file is the recovery fallback. The
// canonical CREATE path remains tofu's `minio_s3_bucket` resource. The
// DELETE path lives here because Hetzner Object Storage exposes no
// Crossplane provider as of issue #706 and tofu's `force_destroy` is
// not safe for production tenants — a separate PR can wire this through
// Crossplane once an upstream provider exists.

package hetzner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// bucketSuffixLen is the number of hex characters appended to the
// FQDN-derived stem to disambiguate buckets across deployments. 8
// hex chars = 32 bits of entropy = ~1-in-4-billion collision
// probability for two deployments of the SAME Sovereign FQDN — more
// than enough for any realistic re-provision cadence on a single
// Hetzner Object Storage tenant.
const bucketSuffixLen = 8

// BucketNameForSovereign returns the deterministic Object Storage
// bucket name for a (Sovereign FQDN, deployment ID) pair. Mirrors
// the default in handler/deployments.go (which is the same string
// tofu's bucket resource consumes via `object_storage_bucket_name`).
//
// Pattern: `catalyst-<fqdn-with-dots-replaced-by-dashes>-<8-hex>`.
// e.g. `omantel.omani.works` + deployment-id `b3b837a22d7a8e5c` ->
// `catalyst-omantel-omani-works-b3b837a2`.
//
// Why the suffix (Fix #111, 2026-05-10): Hetzner Object Storage
// bucket names share a GLOBAL namespace across every tenant. Two
// Sovereigns at the same FQDN (e.g. a re-provision after wipe, or
// two operators racing with the same domain) would collide on
// `catalyst-<fqdn>` and the second `tofu apply` would fail with
// `BucketAlreadyExists`. The deployment-id-derived suffix is unique
// per CreateDeployment call so collisions only happen if the same
// 16-byte random ID is generated twice, which is cryptographically
// negligible.
//
// Backward-compat: when deploymentID is empty (legacy on-disk
// records pre-dating this change), the suffix is derived from
// sha256(fqdn) so the name remains deterministic per-FQDN — wipes
// of legacy Sovereigns still target the right bucket. New
// deployments always carry deploymentID via Request.DeploymentID
// (stamped in handler.CreateDeployment before validation).
//
// Exposed so the wipe handler can call PurgeBuckets() without
// re-deriving the name itself, and so unit tests can pin both halves
// of the contract from one place.
func BucketNameForSovereign(fqdn, deploymentID string) string {
	stem := "catalyst-" + strings.ReplaceAll(fqdn, ".", "-")
	suffix := bucketSuffix(fqdn, deploymentID)
	return stem + "-" + suffix
}

// bucketSuffix returns the 8-hex-char disambiguation suffix.
//
// When deploymentID is non-empty and at least bucketSuffixLen long,
// we use its leading characters directly — newID() in
// handler/deployments.go already returns 16 cryptographically-random
// hex chars, so taking the first 8 is equivalent to taking 32 bits
// of fresh entropy. Cheaper than hashing and keeps the suffix
// trivially traceable back to the deployment record.
//
// When deploymentID is empty/short (legacy records), fall back to
// first 8 chars of sha256(fqdn) so the name remains deterministic
// per-FQDN. This branch only fires for wipes of Sovereigns provisioned
// before Fix #111 — by definition those buckets pre-date the suffix
// scheme and were named `catalyst-<fqdn>` (no suffix at all). The
// fallback won't match those buckets, but that's correct: PurgeBuckets()
// returns 0/nil on NoSuchBucket and the operator's runbook
// (feedback_idempotent_iac_purge.md) covers the manual sweep.
func bucketSuffix(fqdn, deploymentID string) string {
	if id := strings.TrimSpace(strings.ToLower(deploymentID)); len(id) >= bucketSuffixLen && isHex(id[:bucketSuffixLen]) {
		return id[:bucketSuffixLen]
	}
	sum := sha256.Sum256([]byte(fqdn))
	return hex.EncodeToString(sum[:])[:bucketSuffixLen]
}

// isHex reports whether s is non-empty and consists only of
// lowercase hex digits. Used to validate the deployment-id prefix
// before splicing it into a bucket name (defence-in-depth: keeps
// the bucket name within the S3 lexical rules even if a future
// caller passes something that isn't pure hex).
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// HetznerObjectStorageEndpoint returns the canonical Hetzner Object
// Storage endpoint hostname for a region. Mirrors the same lookup in
// internal/objectstorage/hetzner — duplicated here (not imported) to
// avoid an import cycle: objectstorage/hetzner imports
// internal/objectstorage which is loaded at init() time, while purge
// runs at request time. The two functions MUST stay in sync; a
// regression test asserts that.
func HetznerObjectStorageEndpoint(region string) string {
	switch region {
	case "fsn1", "nbg1", "hel1":
		return region + ".your-objectstorage.com"
	default:
		return ""
	}
}

// PurgeBucketsConfig holds the credentials + region needed to delete
// the per-Sovereign bucket. Sourced from the wizard's wipe payload
// (which itself surfaces from the on-disk Deployment record's request
// body, where the operator entered the keys at provision time).
type PurgeBucketsConfig struct {
	AccessKey string
	SecretKey string
	Region    string
}

// PurgeBuckets empties + deletes the per-Sovereign Hetzner Object
// Storage bucket. The bucket name is derived deterministically from
// (sovereignFQDN, deploymentID) — see BucketNameForSovereign.
//
// deploymentID is the catalyst-api's per-deployment ID (16-hex from
// newID()). It is used as the bucket-name suffix to disambiguate
// across the global Hetzner Object Storage namespace. Pass empty
// only for legacy records where the on-disk Deployment pre-dates
// Fix #111; the function falls back to a fqdn-derived sha256 prefix
// in that case (see bucketSuffix).
//
// Returns the number of buckets actually removed (0 or 1) and an
// error only on a non-recoverable failure (network, auth, partial
// delete that left the bucket non-empty). A 404 NoSuchBucket is
// idempotent success: BucketsRemoved=0, err=nil.
//
// progress is called for human-readable status updates to feed the
// SSE log. Pass nil to silence.
func PurgeBuckets(ctx context.Context, cfg PurgeBucketsConfig, sovereignFQDN, deploymentID string, progress func(msg string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}
	if strings.TrimSpace(cfg.AccessKey) == "" {
		return 0, errors.New("object-storage access key is empty")
	}
	if strings.TrimSpace(cfg.SecretKey) == "" {
		return 0, errors.New("object-storage secret key is empty")
	}
	endpoint := HetznerObjectStorageEndpoint(cfg.Region)
	if endpoint == "" {
		return 0, fmt.Errorf("unknown Hetzner Object Storage region %q (must be fsn1, nbg1, or hel1)", cfg.Region)
	}
	if strings.TrimSpace(sovereignFQDN) == "" {
		return 0, errors.New("sovereign fqdn is empty")
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: true,
		Region: cfg.Region,
	})
	if err != nil {
		return 0, fmt.Errorf("construct minio client: %w", err)
	}

	bucketName := BucketNameForSovereign(sovereignFQDN, deploymentID)
	return purgeBucket(ctx, client, bucketName, progress)
}

// purgeBucket runs the empty + delete sequence against a single
// bucket. Split out so tests can drive a real minio testserver
// without depending on Hetzner's region table.
//
// Sequence (must run in this order; AWS S3 semantics):
//
//  1. List all object versions (including delete markers) in pages of
//     up to 1000 keys. Hetzner's Object Storage is versioned-by-default
//     in the catalyst tenant (see infra/hetzner/main.tf §minio_s3_bucket
//     versioning block) so a non-versioned ListObjects loop would miss
//     historic versions and DELETE Bucket would fail with BucketNotEmpty.
//  2. Batch-delete each window via RemoveObjects channel.
//  3. Abort every multipart upload still recorded for the bucket.
//  4. DELETE the bucket.
//
// Each step's error is wrapped with context so the wizard's SSE log
// shows operators where exactly the purge stalled.
func purgeBucket(ctx context.Context, client *minio.Client, bucketName string, progress func(string)) (int, error) {
	if progress == nil {
		progress = func(string) {}
	}

	// Fast 404 path: if the bucket doesn't exist we're done.
	exists, err := client.BucketExists(ctx, bucketName)
	if err != nil {
		// 404 surfaces as an error in some minio-go versions; treat the
		// canonical NoSuchBucket code as idempotent success.
		var errResp minio.ErrorResponse
		if errors.As(err, &errResp) && (errResp.Code == "NoSuchBucket" || errResp.StatusCode == 404) {
			progress(fmt.Sprintf("bucket %s already gone (404)", bucketName))
			return 0, nil
		}
		return 0, fmt.Errorf("BucketExists %s: %w", bucketName, err)
	}
	if !exists {
		progress(fmt.Sprintf("bucket %s already gone", bucketName))
		return 0, nil
	}

	// Step 1+2 — list all versions + delete markers, stream them into
	// RemoveObjects which batches at 1000 per request internally.
	objectsCh := make(chan minio.ObjectInfo)
	listCtx, listCancel := context.WithCancel(ctx)
	defer listCancel()
	listErrCh := make(chan error, 1)
	go func() {
		defer close(objectsCh)
		for obj := range client.ListObjects(listCtx, bucketName, minio.ListObjectsOptions{
			WithVersions: true,
			Recursive:    true,
		}) {
			if obj.Err != nil {
				listErrCh <- obj.Err
				return
			}
			select {
			case objectsCh <- obj:
			case <-listCtx.Done():
				return
			}
		}
		listErrCh <- nil
	}()

	removeErrCh := client.RemoveObjects(ctx, bucketName, objectsCh, minio.RemoveObjectsOptions{})
	deleted := 0
	var removeErrs []string
	for re := range removeErrCh {
		if re.Err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s@%s: %s", re.ObjectName, re.VersionID, re.Err.Error()))
			continue
		}
		deleted++
	}
	if listErr := <-listErrCh; listErr != nil {
		return 0, fmt.Errorf("list versions in %s: %w", bucketName, listErr)
	}
	if deleted > 0 {
		progress(fmt.Sprintf("emptied bucket %s — deleted %d object version(s)", bucketName, deleted))
	}
	if len(removeErrs) > 0 {
		// Surface a bounded summary; full list would flood SSE.
		const cap = 5
		head := removeErrs
		if len(head) > cap {
			head = head[:cap]
		}
		return 0, fmt.Errorf("delete %d object(s) in %s failed: %s%s",
			len(removeErrs), bucketName, strings.Join(head, "; "),
			func() string {
				if len(removeErrs) > cap {
					return fmt.Sprintf("; …and %d more", len(removeErrs)-cap)
				}
				return ""
			}())
	}

	// Step 3 — abort any in-flight multipart uploads. RemoveIncompleteUpload
	// is the canonical AbortMultipartUpload wrapper in minio-go.
	aborted := 0
	for u := range client.ListIncompleteUploads(ctx, bucketName, "", true) {
		if u.Err != nil {
			return 0, fmt.Errorf("list incomplete uploads in %s: %w", bucketName, u.Err)
		}
		if err := client.RemoveIncompleteUpload(ctx, bucketName, u.Key); err != nil {
			return 0, fmt.Errorf("abort multipart %s in %s: %w", u.Key, bucketName, err)
		}
		aborted++
	}
	if aborted > 0 {
		progress(fmt.Sprintf("aborted %d in-progress multipart upload(s) in %s", aborted, bucketName))
	}

	// Step 4 — DELETE the bucket itself.
	if err := client.RemoveBucket(ctx, bucketName); err != nil {
		var errResp minio.ErrorResponse
		if errors.As(err, &errResp) && (errResp.Code == "NoSuchBucket" || errResp.StatusCode == 404) {
			progress(fmt.Sprintf("bucket %s already gone after empty step", bucketName))
			return 0, nil
		}
		return 0, fmt.Errorf("delete bucket %s: %w", bucketName, err)
	}
	progress(fmt.Sprintf("deleted bucket %s", bucketName))
	return 1, nil
}
