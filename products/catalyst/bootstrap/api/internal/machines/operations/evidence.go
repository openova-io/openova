package operations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

// Store reads one file of the evidence bucket.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// MaxEvidenceBytes is the largest file the controller reads from the evidence bucket,
// and EvidenceTimeout the longest one read may take.
const (
	MaxEvidenceBytes = 16 << 20
	EvidenceTimeout  = time.Minute
)

// BucketStore reads the evidence bucket over HTTPS with certificate validation. The key
// pair is read from the object-storage binding mounted at BindingDir on every call, so a
// replaced key is used at once. No Kubernetes Secret is read.
type BucketStore struct {
	Endpoint   string // https://host[:port]
	Region     string
	Bucket     string
	BindingDir string
}

// Get returns the content of one object. It fails on an object longer than
// MaxEvidenceBytes and on a read that takes longer than EvidenceTimeout.
func (s BucketStore) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, EvidenceTimeout)
	defer cancel()
	var pair [2]string // the access key identifier and the secret access key
	for i, name := range []string{runner.BindingAccessKeyID, runner.BindingSecretAccessKey} {
		data, err := os.ReadFile(filepath.Join(s.BindingDir, name)) // #nosec G304 -- a fixed name in the mounted binding
		if pair[i] = strings.TrimSpace(string(data)); err != nil || pair[i] == "" {
			return nil, fmt.Errorf("the binding file %s cannot be read", name)
		}
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	ca, err := os.ReadFile(filepath.Join(s.BindingDir, runner.BindingCA)) // #nosec G304 -- a fixed name in the mounted binding
	if err == nil && !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("the binding file %s holds no certificate", runner.BindingCA)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("the binding file %s cannot be read", runner.BindingCA)
	}
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, fmt.Errorf("the evidence endpoint %q is not https://host[:port]", s.Endpoint)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	defer transport.CloseIdleConnections()
	c, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4(pair[0], pair[1], ""), Secure: true, Region: s.Region, Transport: transport})
	if err != nil {
		return nil, err
	}
	obj, err := c.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(io.LimitReader(obj, MaxEvidenceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s from the evidence bucket: %w", key, err)
	}
	if len(data) > MaxEvidenceBytes {
		return nil, fmt.Errorf("%s is longer than %d bytes", key, MaxEvidenceBytes)
	}
	return data, nil
}
