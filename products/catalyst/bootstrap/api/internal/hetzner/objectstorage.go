// objectstorage.go — credential validator for Hetzner Object Storage
// (Phase 0b, issue #371).
//
// Per ADR-0001 §9.2 #2 ("Crossplane is the only Day-2 cloud-API seam") and
// docs/INVIOLABLE-PRINCIPLES.md #3, catalyst-api avoids bespoke cloud-API
// calls for resource MUTATION. Validating an operator-supplied credential
// pair against an upstream API is NOT mutation — it's a read-only check
// that surfaces a typo or a permissions misconfig at the wizard step
// instead of 5 minutes into `tofu apply`. ValidateToken (this package's
// older sibling) operates on the same principle for the hcloud token.
//
// Why this validator is necessary
// -------------------------------
// Hetzner exposes NO Cloud API to manage Object Storage credentials —
// the operator issues them once in the Hetzner Console (Object Storage →
// Manage Credentials, secret half shown exactly once). The wizard
// therefore captures both halves directly. Without this validator a
// typo'd access key would surface inside `tofu apply`, ~5 minutes into
// provisioning, as a `minio_s3_bucket: 403 Forbidden` and the operator
// would have to wait for tofu's destroy + retry loop.
//
// Why minio-go vs. aws-sdk-go-v2
// ------------------------------
// minio-go is the canonical client for S3-compatible storage and is
// what Hetzner officially recommends in their docs at
// https://docs.hetzner.com/storage/object-storage/getting-started/
// using-s3-api-tools/. It pulls ~5 small modules vs. aws-sdk-go-v2's
// dozens, and its API is shaped for S3-compatible (not just AWS S3)
// scenarios — the constructor takes an explicit endpoint URL rather
// than deriving one from a region literal.
package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ObjectStorageEndpoint composes the canonical Hetzner Object Storage
// endpoint hostname (no scheme) for a region. Hetzner's published format
// is `<region>.your-objectstorage.com` per
// https://docs.hetzner.com/storage/object-storage/getting-started/
// using-s3-api-tools/. Returns the empty string for unrecognised regions
// so callers can surface "unknown region" before constructing a doomed
// HTTPS request.
//
// Region must be one of the European-only Object Storage availability
// zones: fsn1 / nbg1 / hel1. The Hetzner Cloud regions ash and hil do
// NOT have Object Storage as of 2026-04 — for ash/hil compute Sovereigns
// the operator picks a European Object Storage region in the wizard.
func ObjectStorageEndpoint(region string) string {
	switch region {
	case "fsn1", "nbg1", "hel1":
		return region + ".your-objectstorage.com"
	default:
		return ""
	}
}

// ValidateObjectStorageCredentials issues an S3 ListBuckets call against
// Hetzner Object Storage with the operator-supplied access/secret pair.
// A successful 200 means the keys authenticate AND have permission to
// list buckets in the tenant — the same permission the
// `aminueza/minio` Terraform provider needs to create the per-Sovereign
// bucket in main.tf. A 403/401 surfaces as (false, nil) so the wizard
// can render a "rejected" failure card with the standard remediation
// hint. Network errors return (false, err) so the wizard can render
// the "unreachable — Hetzner Object Storage may be down, try again"
// card, distinct from the "rejected" path.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the keys
// are never logged. The minio-go client uses TLS-pinned default
// transport so a man-in-the-middle on a hostile network cannot
// downgrade the connection.
func ValidateObjectStorageCredentials(ctx context.Context, region, accessKey, secretKey string) (bool, error) {
	if strings.TrimSpace(accessKey) == "" {
		return false, errors.New("access key is empty")
	}
	if strings.TrimSpace(secretKey) == "" {
		return false, errors.New("secret key is empty")
	}
	endpoint := ObjectStorageEndpoint(region)
	if endpoint == "" {
		return false, fmt.Errorf("unknown Hetzner Object Storage region %q (must be fsn1, nbg1, or hel1)", region)
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: true, // Hetzner Object Storage requires HTTPS
		Region: region,
	})
	if err != nil {
		return false, fmt.Errorf("construct minio client: %w", err)
	}

	// ListBuckets is the canonical "credentials work" probe for any
	// S3 service. We don't care about the bucket list itself (there
	// might be zero — a brand-new tenant) only that the call returned
	// without 401/403. Hetzner's S3 implementation returns the standard
	// AWS error codes for those statuses, which minio-go surfaces as
	// minio.ErrorResponse with a `Code` field we can switch on.
	_, err = client.ListBuckets(ctx)
	if err == nil {
		return true, nil
	}

	// Cleanly distinguish auth failure ("rejected") from network failure
	// ("unreachable") so the wizard renders the right hint card.
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		switch errResp.Code {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidSecurity":
			// Authenticated but not authorized, OR keys are wrong. Either
			// way the credentials are not usable — wizard treats this as
			// "rejected" with the standard remediation hint.
			return false, nil
		}
	}
	// Anything else (timeout, DNS failure, 5xx) is a network/upstream
	// failure — surface to the wizard's "unreachable" failure card.
	return false, err
}
