package runner

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// The files of the object-storage binding (contract object-storage v1) that the runner
// reads. The key pair is required; ca.crt is present when the endpoint's certificate is
// not publicly trusted.
const (
	BindingAccessKeyID     = "access-key-id"
	BindingSecretAccessKey = "secret-access-key"
	BindingCA              = "ca.crt"
)

// binding is the key pair of the evidence bucket and the optional certificate bundle.
type binding struct {
	accessKeyID     string
	secretAccessKey string
	ca              []byte
}

// readBinding reads the binding files in dir. A missing or empty key file is an error.
func readBinding(dir string) (binding, error) {
	var b binding
	for _, f := range []struct {
		name string
		to   *string
	}{{BindingAccessKeyID, &b.accessKeyID}, {BindingSecretAccessKey, &b.secretAccessKey}} {
		data, err := os.ReadFile(filepath.Join(dir, f.name)) //nolint:gosec // a fixed name in the mounted binding
		if err != nil {
			return binding{}, fmt.Errorf("the binding file %s cannot be read", f.name)
		}
		*f.to = strings.TrimSpace(string(data))
		if *f.to == "" {
			return binding{}, fmt.Errorf("the binding file %s is empty", f.name)
		}
	}
	ca, err := os.ReadFile(filepath.Join(dir, BindingCA)) //nolint:gosec // a fixed name in the mounted binding
	switch {
	case err == nil:
		b.ca = ca
	case !errors.Is(err, os.ErrNotExist):
		return binding{}, fmt.Errorf("the binding file %s cannot be read: %v", BindingCA, err)
	}
	return b, nil
}

// store puts each file of the work folder into the evidence bucket under the key
// <prefix><file name>, over HTTPS with certificate validation, and returns the evidence
// entries in the order of files.
func store(ctx context.Context, cfg Config, env Env, files []file) ([]runresult.Evidence, error) {
	b, err := readBinding(env.BindingDir)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if len(b.ca) > 0 && !pool.AppendCertsFromPEM(b.ca) {
		return nil, fmt.Errorf("the binding file %s holds no certificate", BindingCA)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	endpoint, err := url.Parse(cfg.Evidence.Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(endpoint.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(b.accessKeyID, b.secretAccessKey, ""),
		Secure:    true,
		Region:    cfg.Evidence.Region,
		Transport: transport,
	})
	if err != nil {
		return nil, err
	}
	var out []runresult.Evidence
	for _, f := range files {
		e, err := put(ctx, client, cfg, filepath.Join(env.WorkDir, f.name), f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// put stores one file and returns its evidence entry.
func put(ctx context.Context, client *minio.Client, cfg Config, path string, f file) (runresult.Evidence, error) {
	h, err := os.Open(path) //nolint:gosec // a fixed name in the work folder
	if err != nil {
		return runresult.Evidence{}, err
	}
	defer func() { _ = h.Close() }()
	sum := sha256.New()
	size, err := io.Copy(sum, h)
	if err != nil {
		return runresult.Evidence{}, err
	}
	if _, err := h.Seek(0, io.SeekStart); err != nil {
		return runresult.Evidence{}, err
	}
	contentType := "application/json"
	if strings.HasSuffix(f.name, ".xml") {
		contentType = "application/xml"
	}
	key := cfg.Evidence.Prefix + f.name
	if _, err := client.PutObject(ctx, cfg.Evidence.Bucket, key, h, size,
		minio.PutObjectOptions{ContentType: contentType, DisableMultipart: true}); err != nil {
		return runresult.Evidence{}, err
	}
	return runresult.Evidence{Kind: f.kind, Key: key, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}
