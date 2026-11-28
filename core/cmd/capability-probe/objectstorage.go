package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// bindingKey is one key of a contract version: one file in the binding directory.
type bindingKey struct {
	name     string
	required bool
}

// objectStorageV1Keys are the keys of contract v1 of platform/_capabilities/object-storage.yaml.
var objectStorageV1Keys = []bindingKey{
	{"type", true},
	{"provider", true},
	{"endpoint", true},
	{"region", true},
	{"bucket", true},
	{"path-style", true},
	{"access-key-id", true},
	{"secret-access-key", true},
	{"ca.crt", false},
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// readBinding reads one file per key. A required key must be a file with a value; an optional key
// may be absent or empty (an empty file stands for an absent key: the producer writes every key).
func readBinding(dir string, keys []bindingKey) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	fsys := os.DirFS(dir)
	for _, k := range keys {
		raw, err := fs.ReadFile(fsys, k.name)
		switch {
		case errors.Is(err, fs.ErrNotExist) && !k.required:
			continue
		case errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("key %s missing", k.name)
		case err != nil:
			return nil, fmt.Errorf("key %s unreadable: %v", k.name, err)
		}
		v := strings.TrimSpace(string(raw))
		if v == "" && k.required {
			return nil, fmt.Errorf("key %s empty", k.name)
		}
		values[k.name] = v
	}
	return values, nil
}

// objectStorageBinding is a checked binding of contract object-storage v1.
type objectStorageBinding struct {
	endpoint  string
	host      string
	secure    bool
	region    string
	bucket    string
	pathStyle bool
	accessKey string
	secretKey string
	roots     *x509.CertPool
}

// checkObjectStorage checks that every key is well formed. It names the key and never its value
// when the key is a credential.
func checkObjectStorage(v map[string]string) (objectStorageBinding, error) {
	b := objectStorageBinding{region: v["region"], accessKey: v["access-key-id"], secretKey: v["secret-access-key"]}
	if v["type"] != "s3" {
		return b, fmt.Errorf("key type is %q, want s3", v["type"])
	}
	u, err := url.Parse(v["endpoint"])
	if err != nil {
		return b, fmt.Errorf("key endpoint %q is not a URL", v["endpoint"])
	}
	switch {
	case u.Host == "" || u.Hostname() == "":
		return b, fmt.Errorf("key endpoint %q has no host", v["endpoint"])
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		return b, fmt.Errorf("key endpoint %q must be scheme://host[:port] only", v["endpoint"])
	case u.Scheme == "https":
		b.secure = true
	case u.Scheme == "http" && clusterLocal(u.Hostname()):
		b.secure = false
	case u.Scheme == "http":
		return b, fmt.Errorf("key endpoint %q: http is allowed only for a service name of the cluster (*.svc, *.svc.cluster.local)", v["endpoint"])
	default:
		return b, fmt.Errorf("key endpoint %q: scheme must be https", v["endpoint"])
	}
	b.endpoint, b.host = v["endpoint"], u.Host
	if !bucketName.MatchString(v["bucket"]) {
		return b, fmt.Errorf("key bucket %q is not a bucket name", v["bucket"])
	}
	b.bucket = v["bucket"]
	switch v["path-style"] {
	case "true":
		b.pathStyle = true
	case "false":
	default:
		return b, fmt.Errorf("key path-style is %q, want true or false", v["path-style"])
	}
	if pem := v["ca.crt"]; pem != "" {
		b.roots = x509.NewCertPool()
		if !b.roots.AppendCertsFromPEM([]byte(pem)) {
			return b, errors.New("key ca.crt holds no PEM certificate")
		}
	}
	return b, nil
}

func clusterLocal(host string) bool {
	return strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}

// objectStorageV1 puts an object, gets it back, compares it and deletes it, with the binding's own
// key. It removes the exact version it wrote, so a versioned bucket keeps nothing either.
func objectStorageV1(ctx context.Context, dir string, keysOnly bool) (string, error) {
	v, err := readBinding(dir, objectStorageV1Keys)
	if err != nil {
		return "", err
	}
	b, err := checkObjectStorage(v)
	if err != nil {
		return "", err
	}
	if keysOnly {
		return fmt.Sprintf("keys of bucket %s at %s present and well formed (provider %s)", b.bucket, b.endpoint, v["provider"]), nil
	}
	client, err := newS3Client(b)
	if err != nil {
		return "", err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("random object name: %v", err)
	}
	key := "capability-probe/" + hex.EncodeToString(suffix)
	body := []byte("capability-probe object-storage-v1 " + key + "\n")

	put, err := client.PutObject(ctx, b.bucket, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "text/plain"})
	if err != nil {
		return "", fmt.Errorf("put object %s into bucket %s at %s: %v", key, b.bucket, b.endpoint, err)
	}
	remove := func(ctx context.Context) error {
		return client.RemoveObject(ctx, b.bucket, key, minio.RemoveObjectOptions{VersionID: put.VersionID})
	}
	// After the put every failure removes the object first, with its own short deadline.
	undo := func(cause error) (string, error) {
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := remove(cctx); err != nil {
			return "", fmt.Errorf("%v; and removing object %s failed: %v", cause, key, err)
		}
		return "", cause
	}
	obj, err := client.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{VersionID: put.VersionID})
	if err != nil {
		return undo(fmt.Errorf("get object %s: %v", key, err))
	}
	got, err := io.ReadAll(obj)
	_ = obj.Close()
	if err != nil {
		return undo(fmt.Errorf("get object %s: %v", key, err))
	}
	if !bytes.Equal(got, body) {
		return undo(fmt.Errorf("object %s read back (%d bytes) differs from the object written (%d bytes)", key, len(got), len(body)))
	}
	if err := remove(ctx); err != nil {
		return "", fmt.Errorf("delete object %s: %v", key, err)
	}
	_, err = client.StatObject(ctx, b.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return "", fmt.Errorf("object %s still exists after its delete", key)
	}
	if code := minio.ToErrorResponse(err).Code; code != "NoSuchKey" {
		return "", fmt.Errorf("stat of object %s after its delete: %v", key, err)
	}
	return fmt.Sprintf("put, got and deleted object %s in bucket %s at %s (provider %s)", key, b.bucket, b.endpoint, v["provider"]), nil
}

func newS3Client(b objectStorageBinding) (*minio.Client, error) {
	transport, err := minio.DefaultTransport(b.secure)
	if err != nil {
		return nil, fmt.Errorf("S3 client transport: %v", err)
	}
	if b.roots != nil && transport.TLSClientConfig != nil {
		transport.TLSClientConfig.RootCAs = b.roots
	}
	lookup := minio.BucketLookupDNS
	if b.pathStyle {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(b.host, &minio.Options{
		Creds:        credentials.NewStaticV4(b.accessKey, b.secretKey, ""),
		Secure:       b.secure,
		Region:       b.region,
		BucketLookup: lookup,
		Transport:    transport,
		MaxRetries:   2,
	})
	if err != nil {
		return nil, fmt.Errorf("S3 client for %s: %v", b.endpoint, err)
	}
	return client, nil
}
