package ingest

import (
	"context"
	"crypto/hmac"
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
	"regexp"
	"sort"
	"strings"
	"time"
)

// Objects holds the originals. Bucket is the one implementation.
type Objects interface {
	// Put stores size bytes of content under key; sha256 is the hex digest of the content.
	Put(ctx context.Context, key string, content io.Reader, size int64, sha256 string) error
	// Get returns the content stored under key, and its length; ErrNoObject when there is none.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// Delete removes key; a key that does not exist is not an error.
	Delete(ctx context.Context, key string) error
}

// ErrNoObject is returned by Get for a key that holds nothing.
var ErrNoObject = errors.New("ingest: no such object")

// emptySHA256 is the digest of no bytes, the payload digest of a GET and a DELETE.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var (
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	objectKey  = regexp.MustCompile(`^[0-9a-f-]{36}/[0-9a-f]{64}$`)
)

// Bucket is the binding of contract object-storage v1: one bucket on an S3-protocol endpoint.
// OpenBucket reads the binding's files in a folder: endpoint (https://host[:port]), region,
// bucket, path-style (true or false), and ca.crt when the endpoint's certificate is not
// publicly trusted. The two keys, access-key-id and secret-access-key, are read from their
// files on every call, so a rotated key is used as soon as its file changes; neither is ever a
// flag, an environment value or a log field. Requests are signed with AWS Signature Version 4.
// A key is an object name this package makes: <document id>/<SHA-256 of the content>.
type Bucket struct {
	dir    string
	base   string // https://host[:port]/bucket or https://bucket.host[:port]
	region string
	http   *http.Client
	now    func() time.Time
}

// OpenBucket reads the binding in dir.
func OpenBucket(dir string) (*Bucket, error) {
	values := map[string]string{}
	for _, name := range []string{"endpoint", "region", "bucket", "path-style"} {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if err != nil {
			return nil, fmt.Errorf("ingest: the binding has no readable %s", name)
		}
		values[name] = strings.TrimSpace(string(b))
	}
	u, err := url.Parse(values["endpoint"])
	switch {
	case err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || strings.Trim(u.Path, "/") != "":
		return nil, fmt.Errorf("ingest: the endpoint %q is not https://host[:port]", values["endpoint"])
	case !bucketName.MatchString(values["bucket"]):
		return nil, fmt.Errorf("ingest: %q is not a bucket name", values["bucket"])
	case values["region"] == "":
		return nil, errors.New("ingest: the region is empty")
	case values["path-style"] != "true" && values["path-style"] != "false":
		return nil, fmt.Errorf("ingest: path-style is %q, not true or false", values["path-style"])
	}
	base := "https://" + values["bucket"] + "." + u.Host
	if values["path-style"] == "true" {
		base = "https://" + u.Host + "/" + values["bucket"]
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if pem, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "ca.crt"))); err == nil {
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("ingest: ca.crt holds no certificate")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("ingest: ca.crt cannot be read")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return &Bucket{dir: dir, base: base, region: values["region"], http: &http.Client{Transport: tr}, now: time.Now}, nil
}

// Put stores content under key.
func (b *Bucket) Put(ctx context.Context, key string, content io.Reader, size int64, sum string) error {
	resp, err := b.do(ctx, http.MethodPut, key, content, size, sum)
	if err != nil {
		return err
	}
	return b.done(resp, key, http.StatusOK)
}

// Get returns the content stored under key.
func (b *Bucket) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := b.do(ctx, http.MethodGet, key, nil, 0, emptySHA256)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("%w: %s", ErrNoObject, key)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, 0, b.done(resp, key, http.StatusOK)
	}
	return resp.Body, resp.ContentLength, nil
}

// Delete removes key.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	resp, err := b.do(ctx, http.MethodDelete, key, nil, 0, emptySHA256)
	if err != nil {
		return err
	}
	return b.done(resp, key, http.StatusNoContent, http.StatusOK, http.StatusNotFound)
}

// done closes the answer and returns an error unless its status is one of ok. The error holds
// the status, never the body.
func (b *Bucket) done(resp *http.Response, key string, ok ...int) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	for _, s := range ok {
		if resp.StatusCode == s {
			return nil
		}
	}
	return fmt.Errorf("ingest: the bucket answered %d to %s", resp.StatusCode, key)
}

func (b *Bucket) do(ctx context.Context, method, key string, body io.Reader, size int64, sum string) (*http.Response, error) {
	if !objectKey.MatchString(key) {
		return nil, fmt.Errorf("ingest: %q is not an object key", key)
	}
	var keys [2]string
	for i, name := range []string{"access-key-id", "secret-access-key"} {
		v, err := os.ReadFile(filepath.Clean(filepath.Join(b.dir, name)))
		if err != nil || len(strings.TrimSpace(string(v))) == 0 {
			return nil, fmt.Errorf("ingest: the binding has no readable %s", name)
		}
		keys[i] = strings.TrimSpace(string(v))
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+"/"+key, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	req.Header.Set("x-amz-content-sha256", sum)
	sign(req, keys[0], keys[1], b.region, b.now())
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ingest: the bucket cannot be reached: %w", err)
	}
	return resp, nil
}

// sign adds the headers x-amz-date and Authorization of AWS Signature Version 4 for the
// service s3. It signs host and every header named range or x-amz-*; the header
// x-amz-content-sha256 must hold the payload's digest. The request has no query.
func sign(req *http.Request, accessKey, secretKey, region string, t time.Time) {
	stamp := t.UTC().Format("20060102T150405Z")
	req.Header.Set("x-amz-date", stamp)
	names := []string{"host"}
	for k := range req.Header {
		if lk := strings.ToLower(k); lk == "range" || strings.HasPrefix(lk, "x-amz-") {
			names = append(names, lk)
		}
	}
	sort.Strings(names)
	var headers strings.Builder
	for _, n := range names {
		v := req.URL.Host
		if n != "host" {
			v = strings.TrimSpace(req.Header.Get(n))
		}
		headers.WriteString(n + ":" + v + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, headers.String(), signed,
		req.Header.Get("x-amz-content-sha256")}, "\n")
	scope := stamp[:8] + "/" + region + "/s3/aws4_request"
	digest := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	k := []byte("AWS4" + secretKey)
	for _, part := range []string{stamp[:8], region, "s3", "aws4_request", toSign} {
		m := hmac.New(sha256.New, k)
		m.Write([]byte(part))
		k = m.Sum(nil)
	}
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+",SignedHeaders="+signed+
		",Signature="+hex.EncodeToString(k))
}
