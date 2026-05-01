// Package hetzner is the Hetzner Object Storage Provider impl for the
// vendor-agnostic objectstorage package (issue #425).
//
// Migrated from internal/hetzner/objectstorage.go in the same PR — the
// behaviour is unchanged (minio-go ListBuckets against
// `<region>.your-objectstorage.com`), only the package shape changes
// to plug into objectstorage.Provider.
//
// Why minio-go vs. aws-sdk-go-v2: minio-go is the canonical client for
// S3-compatible storage and is what Hetzner officially recommends in
// their docs at https://docs.hetzner.com/storage/object-storage/
// getting-started/using-s3-api-tools/. It pulls ~5 small modules vs.
// aws-sdk-go-v2's dozens, and its API is shaped for S3-compatible (not
// just AWS S3) scenarios — the constructor takes an explicit endpoint
// URL rather than deriving one from a region literal.
//
// Why Hetzner exposes no Cloud API to mint these credentials: per
// Hetzner docs, S3 access keys are operator-issued ONCE in the Hetzner
// Console (Object Storage → Manage Credentials). The wizard captures
// both halves; this package validates them BEFORE `tofu apply` so a
// typo'd key surfaces at the wizard step instead of 5 minutes into
// provisioning.
package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/objectstorage"
)

func init() {
	// Register the Hetzner Provider under the canonical name. The
	// wizard payload's `provider` field maps 1:1 onto this name.
	objectstorage.Register("hetzner", &Provider{})
}

// Provider implements objectstorage.Provider for Hetzner Object Storage.
type Provider struct{}

// Endpoint composes the canonical Hetzner Object Storage endpoint
// hostname (no scheme) for a region. Hetzner's published format is
// `<region>.your-objectstorage.com` per
// https://docs.hetzner.com/storage/object-storage/getting-started/
// using-s3-api-tools/. Returns the empty string for unrecognised
// regions so callers can surface "unknown region" before constructing
// a doomed HTTPS request.
//
// Region must be one of the European-only Object Storage availability
// zones: fsn1 / nbg1 / hel1. The Hetzner Cloud regions ash and hil do
// NOT have Object Storage as of 2026-04 — for ash/hil compute Sovereigns
// the operator picks a European Object Storage region in the wizard.
func (Provider) Endpoint(region string) string {
	switch region {
	case "fsn1", "nbg1", "hel1":
		return region + ".your-objectstorage.com"
	default:
		return ""
	}
}

// Validate issues an S3 ListBuckets call against Hetzner Object Storage
// with the operator-supplied access/secret pair. A successful 200 means
// the keys authenticate AND have permission to list buckets in the
// tenant — the same permission the `aminueza/minio` Terraform provider
// needs to create the per-Sovereign bucket in main.tf. A 403/401
// surfaces as (false, nil) so the wizard renders a "rejected" failure
// card. Network errors return (false, err) so the wizard renders the
// "unreachable" card, distinct from "rejected".
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the keys are never logged. The
// minio-go client uses TLS-pinned default transport so a man-in-the-
// middle on a hostile network cannot downgrade the connection.
func (p Provider) Validate(ctx context.Context, region, accessKey, secretKey string) (bool, error) {
	if strings.TrimSpace(accessKey) == "" {
		return false, errors.New("access key is empty")
	}
	if strings.TrimSpace(secretKey) == "" {
		return false, errors.New("secret key is empty")
	}
	endpoint := p.Endpoint(region)
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

	// Cleanly distinguish auth failure ("rejected") from network
	// failure ("unreachable") so the wizard renders the right hint.
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		switch errResp.Code {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidSecurity":
			// Authenticated but not authorized, OR keys are wrong.
			// Either way unusable — wizard treats this as "rejected".
			return false, nil
		}
	}
	// Anything else (timeout, DNS failure, 5xx) is a network/upstream
	// failure — surface to the wizard's "unreachable" failure card.
	return false, err
}
