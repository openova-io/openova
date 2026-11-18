// objectstorage.go — the Object Storage credential check of the Hetzner
// adapter (providers.ObjectStorageValidator). The code came unchanged from
// the package internal/objectstorage/hetzner, deleted by CLD-112: minio-go
// ListBuckets against `<region>.your-objectstorage.com`.
//
// Hetzner exposes no Cloud API to mint these keys: the operator issues them
// once in the Hetzner Console (Object Storage → Manage Credentials). The
// wizard captures both halves and this check runs before `tofu apply`, so a
// typo'd key surfaces at the wizard step. It is read-only.

package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
)

var _ providers.ObjectStorageValidator = (*Provider)(nil)

// ObjectStorageEndpoint returns the Hetzner Object Storage endpoint hostname
// (no scheme) of a region, `<region>.your-objectstorage.com`, or the empty
// string for a region without Object Storage. Object Storage is in fsn1,
// nbg1 and hel1 only; for ash and hil compute the operator picks one of them.
func (p *Provider) ObjectStorageEndpoint(region string) string {
	switch region {
	case "fsn1", "nbg1", "hel1":
		return region + ".your-objectstorage.com"
	default:
		return ""
	}
}

// ValidateObjectStorage issues an S3 ListBuckets call against Hetzner Object
// Storage with the operator-supplied pair, the permission the module needs to
// create the Sovereign's bucket.
//
//	(true,  nil) — the keys authenticate and can list buckets
//	(false, nil) — the keys are rejected (AccessDenied, InvalidAccessKeyId, …)
//	(false, err) — the endpoint cannot be reached (timeout, DNS, 5xx)
//
// The keys are never logged.
func (p *Provider) ValidateObjectStorage(ctx context.Context, region, accessKey, secretKey string) (bool, error) {
	if strings.TrimSpace(accessKey) == "" {
		return false, errors.New("access key is empty")
	}
	if strings.TrimSpace(secretKey) == "" {
		return false, errors.New("secret key is empty")
	}
	endpoint := p.ObjectStorageEndpoint(region)
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

	// Only whether the call is refused matters, not the bucket list (a new
	// tenant has none).
	_, err = client.ListBuckets(ctx)
	if err == nil {
		return true, nil
	}
	var errResp minio.ErrorResponse
	if errors.As(err, &errResp) {
		switch errResp.Code {
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidSecurity":
			return false, nil
		}
	}
	return false, err
}
