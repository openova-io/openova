// buckets_test.go — regression tests for issue #706 bucket-purge path.
//
// Coverage:
//
//   - TestBucketNameForSovereign — the deterministic name we must
//     produce stays in lockstep with the wizard / tofu derivation.
//   - TestHetznerObjectStorageEndpoint — region table.
//   - TestBucketPurge_NotFound_404 — bucket already gone is idempotent
//     success (BucketsRemoved=0, no error).
//   - TestBucketPurge_Empty_Bucket — bucket exists with 0 objects;
//     RemoveBucket succeeds, BucketsRemoved=1.
//   - TestBucketPurge_With_Versions — bucket with 1500 versions across
//     3 keys; assert all are deleted via RemoveObjects (which the
//     minio-go client batches at 1000 internally), bucket then
//     deleted.
//   - TestBucketPurge_Multipart_Aborted — bucket with 1 in-progress
//     multipart upload; assert AbortMultipartUpload is called before
//     bucket delete succeeds.
//   - TestPurgeBuckets_RejectsEmptyCreds — defensive validation paths.
//   - TestPurgeBuckets_UnknownRegion — defensive validation paths.
//
// Strategy: instead of bringing up a real minio binary, we stub the
// subset of S3 endpoints minio-go calls during a bucket purge. Each
// test hands a different state to the fake server and exercises the
// real PurgeBuckets()/purgeBucket() code paths.

package hetzner

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestBucketNameForSovereign(t *testing.T) {
	cases := []struct {
		fqdn string
		want string
	}{
		{"omantel.omani.works", "catalyst-omantel-omani-works"},
		{"otech50.omani.works", "catalyst-otech50-omani-works"},
		{"acme.example.com", "catalyst-acme-example-com"},
	}
	for _, c := range cases {
		if got := BucketNameForSovereign(c.fqdn); got != c.want {
			t.Errorf("BucketNameForSovereign(%q) = %q, want %q", c.fqdn, got, c.want)
		}
	}
}

func TestHetznerObjectStorageEndpoint(t *testing.T) {
	for _, region := range []string{"fsn1", "nbg1", "hel1"} {
		got := HetznerObjectStorageEndpoint(region)
		want := region + ".your-objectstorage.com"
		if got != want {
			t.Errorf("HetznerObjectStorageEndpoint(%q) = %q, want %q", region, got, want)
		}
	}
	for _, bad := range []string{"", "us-east-1", "eu-west-1", "ash"} {
		if got := HetznerObjectStorageEndpoint(bad); got != "" {
			t.Errorf("HetznerObjectStorageEndpoint(%q) = %q, want empty (unknown region)", bad, got)
		}
	}
}

func TestPurgeBuckets_RejectsEmptyAccessKey(t *testing.T) {
	_, err := PurgeBuckets(context.Background(),
		PurgeBucketsConfig{AccessKey: "", SecretKey: "x", Region: "fsn1"},
		"otech50.omani.works", nil)
	if err == nil || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("expected access-key error, got %v", err)
	}
}

func TestPurgeBuckets_RejectsEmptySecretKey(t *testing.T) {
	_, err := PurgeBuckets(context.Background(),
		PurgeBucketsConfig{AccessKey: "x", SecretKey: "", Region: "fsn1"},
		"otech50.omani.works", nil)
	if err == nil || !strings.Contains(err.Error(), "secret key") {
		t.Fatalf("expected secret-key error, got %v", err)
	}
}

func TestPurgeBuckets_UnknownRegion(t *testing.T) {
	_, err := PurgeBuckets(context.Background(),
		PurgeBucketsConfig{AccessKey: "x", SecretKey: "y", Region: "us-east-1"},
		"otech50.omani.works", nil)
	if err == nil || !strings.Contains(err.Error(), "Hetzner Object Storage region") {
		t.Fatalf("expected region error, got %v", err)
	}
}

// fakeS3 is a minimal S3-compatible HTTP stub for bucket-purge tests.
//
// Endpoint shape it implements (only what minio-go calls during a
// purgeBucket run):
//
//   - HEAD /<bucket> — bucket exists / not found
//   - GET  /<bucket>?versions — ListObjectVersions (paginated by us)
//   - POST /<bucket>?delete   — DeleteObjects (Multi-Object Delete)
//   - GET  /<bucket>?uploads  — ListMultipartUploads
//   - DELETE /<bucket>/<key>?uploadId=... — AbortMultipartUpload
//   - DELETE /<bucket> — DeleteBucket
//
// Server behaviour is driven by mutable maps the test seeds before
// running PurgeBuckets.
type fakeS3 struct {
	mu sync.Mutex

	bucket      string
	exists      bool
	versions    []fakeS3Version  // pending versions to return on Versions list
	uploads     []fakeS3Upload   // pending multipart uploads
	deletedKeys []fakeS3Deleted  // versionId-aware deletes received
	abortedUps  []fakeS3Upload   // multipart aborts received
	bucketDel   atomic.Bool      // RemoveBucket received
	versionsCnt atomic.Int32     // count of ListObjectVersions calls
}

type fakeS3Version struct {
	Key       string
	VersionID string
	IsDelete  bool // delete-marker
}

type fakeS3Upload struct {
	Key      string
	UploadID string
}

type fakeS3Deleted struct {
	Key       string
	VersionID string
}

// XML payload types matching the AWS S3 protocol that minio-go expects.

type listVersionsResult struct {
	XMLName             xml.Name        `xml:"ListVersionsResult"`
	Name                string          `xml:"Name"`
	Prefix              string          `xml:"Prefix"`
	MaxKeys             int             `xml:"MaxKeys"`
	IsTruncated         bool            `xml:"IsTruncated"`
	KeyMarker           string          `xml:"KeyMarker"`
	NextKeyMarker       string          `xml:"NextKeyMarker"`
	NextVersionIDMarker string          `xml:"NextVersionIdMarker"`
	Versions            []listVersion   `xml:"Version"`
	DeleteMarkers       []listDelMarker `xml:"DeleteMarker"`
}

type listVersion struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
	IsLatest  bool   `xml:"IsLatest"`
}

type listDelMarker struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
	IsLatest  bool   `xml:"IsLatest"`
}

type listMultipartUploadsResult struct {
	XMLName     xml.Name        `xml:"ListMultipartUploadsResult"`
	Bucket      string          `xml:"Bucket"`
	IsTruncated bool            `xml:"IsTruncated"`
	Uploads     []multipartItem `xml:"Upload"`
}

type multipartItem struct {
	Key      string `xml:"Key"`
	UploadID string `xml:"UploadId"`
}

type deleteRequest struct {
	XMLName xml.Name      `xml:"Delete"`
	Objects []deleteEntry `xml:"Object"`
	Quiet   bool          `xml:"Quiet"`
}

type deleteEntry struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type deleteResult struct {
	XMLName xml.Name       `xml:"DeleteResult"`
	Deleted []deletedEntry `xml:"Deleted"`
	Errors  []errorEntry   `xml:"Error"`
}

type deletedEntry struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
}

type errorEntry struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// handler returns an http.Handler that implements the subset of S3 the
// purge code calls. The bucket name is single-tenant per fake.
func (f *fakeS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// minio-go uses path-style requests when the bucket isn't a valid
		// DNS host (which is true for our test endpoints). Path-style
		// shape: /<bucket>[/<key>][?<query>].
		// First strip leading slash.
		p := strings.TrimPrefix(r.URL.Path, "/")
		// Health check / root probe used by some minio-go paths.
		if p == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// First segment is bucket; rest (after first '/') is key.
		var bucket, key string
		if i := strings.IndexByte(p, '/'); i >= 0 {
			bucket = p[:i]
			key = p[i+1:]
		} else {
			bucket = p
		}
		if bucket != f.bucket {
			w.WriteHeader(http.StatusNotFound)
			_ = xmlError(w, "NoSuchBucket", "bucket does not exist")
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		// Method+query routing — matters for which S3 op this is.
		switch {
		case r.Method == http.MethodHead && key == "":
			// BucketExists
			if f.exists {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
			return

		case r.Method == http.MethodGet && key == "" && r.URL.Query().Has("versions"):
			// ListObjectVersions — paginate to assert the loop iterates.
			f.versionsCnt.Add(1)
			f.serveListVersions(w, r)
			return

		case r.Method == http.MethodGet && key == "" && r.URL.Query().Has("uploads"):
			// ListMultipartUploads
			f.serveListMultipart(w, r)
			return

		case r.Method == http.MethodPost && key == "" && r.URL.Query().Has("delete"):
			// Multi-Object Delete
			f.serveMultiDelete(w, r)
			return

		case r.Method == http.MethodDelete && key != "" && r.URL.Query().Has("uploadId"):
			// AbortMultipartUpload
			id := r.URL.Query().Get("uploadId")
			f.abortedUps = append(f.abortedUps, fakeS3Upload{Key: key, UploadID: id})
			w.WriteHeader(http.StatusNoContent)
			return

		case r.Method == http.MethodDelete && key == "":
			// DeleteBucket
			f.exists = false
			f.bucketDel.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return

		default:
			// Catch-all: respond 200 to GET, 405 to anything else, so
			// surprise calls fail loudly in tests.
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ok/>`))
				return
			}
			http.Error(w, "unexpected fake-s3 call: "+r.Method+" "+r.URL.String(), http.StatusMethodNotAllowed)
		}
	})
}

func (f *fakeS3) serveListVersions(w http.ResponseWriter, r *http.Request) {
	// Paginate at 1000 entries to match S3 default. We split f.versions
	// based on the keyMarker / version-id-marker.
	q := r.URL.Query()
	keyMarker := q.Get("key-marker")
	verMarker := q.Get("version-id-marker")

	// Find offset based on markers. Markers are exclusive — return
	// entries strictly after them in our list order.
	start := 0
	if keyMarker != "" || verMarker != "" {
		for i, v := range f.versions {
			if v.Key == keyMarker && v.VersionID == verMarker {
				start = i + 1
				break
			}
		}
	}
	const pageSize = 1000
	end := start + pageSize
	if end > len(f.versions) {
		end = len(f.versions)
	}
	page := f.versions[start:end]
	resp := listVersionsResult{
		Name:        f.bucket,
		MaxKeys:     pageSize,
		KeyMarker:   keyMarker,
		IsTruncated: end < len(f.versions),
	}
	if resp.IsTruncated && end > start {
		last := page[len(page)-1]
		resp.NextKeyMarker = last.Key
		resp.NextVersionIDMarker = last.VersionID
	}
	for _, v := range page {
		if v.IsDelete {
			resp.DeleteMarkers = append(resp.DeleteMarkers, listDelMarker{
				Key: v.Key, VersionID: v.VersionID,
			})
		} else {
			resp.Versions = append(resp.Versions, listVersion{
				Key: v.Key, VersionID: v.VersionID,
			})
		}
	}
	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	_ = enc.Encode(resp)
}

func (f *fakeS3) serveListMultipart(w http.ResponseWriter, r *http.Request) {
	resp := listMultipartUploadsResult{Bucket: f.bucket}
	for _, u := range f.uploads {
		resp.Uploads = append(resp.Uploads, multipartItem{Key: u.Key, UploadID: u.UploadID})
	}
	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	_ = enc.Encode(resp)
}

func (f *fakeS3) serveMultiDelete(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var dr deleteRequest
	if err := xml.Unmarshal(body, &dr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := deleteResult{}
	for _, o := range dr.Objects {
		f.deletedKeys = append(f.deletedKeys, fakeS3Deleted{Key: o.Key, VersionID: o.VersionID})
		// Remove from f.versions so subsequent ListObjectVersions calls
		// surface the empty state (relevant for paginated runs).
		newVersions := f.versions[:0]
		for _, v := range f.versions {
			if v.Key == o.Key && v.VersionID == o.VersionID {
				continue
			}
			newVersions = append(newVersions, v)
		}
		f.versions = newVersions
		resp.Deleted = append(resp.Deleted, deletedEntry{Key: o.Key, VersionID: o.VersionID})
	}
	w.Header().Set("Content-Type", "application/xml")
	enc := xml.NewEncoder(w)
	_ = enc.Encode(resp)
}

func xmlError(w http.ResponseWriter, code, msg string) error {
	w.Header().Set("Content-Type", "application/xml")
	_, err := fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
	return err
}

// newTestMinio returns a minio.Client pointing at the given httptest
// URL with V4 path-style addressing (so the fake's path parser works).
func newTestMinio(t *testing.T, srv *httptest.Server) *minio.Client {
	t.Helper()
	// Strip "http://" prefix; minio-go expects bare host:port for the
	// endpoint argument.
	endpoint := strings.TrimPrefix(srv.URL, "http://")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4("test-access", "test-secret", ""),
		Secure:       false,
		Region:       "fsn1",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}
	return client
}

func TestBucketPurge_NotFound_404(t *testing.T) {
	fake := &fakeS3{bucket: "catalyst-otech-not-found", exists: false}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := newTestMinio(t, srv)

	removed, err := purgeBucket(context.Background(), client, fake.bucket, nil)
	if err != nil {
		t.Fatalf("purgeBucket on non-existent bucket: %v (want nil err)", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 (bucket already gone)", removed)
	}
	if fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was called against a non-existent bucket")
	}
}

func TestBucketPurge_Empty_Bucket(t *testing.T) {
	fake := &fakeS3{bucket: "catalyst-otech-empty", exists: true}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := newTestMinio(t, srv)

	removed, err := purgeBucket(context.Background(), client, fake.bucket, nil)
	if err != nil {
		t.Fatalf("purgeBucket on empty bucket: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called on empty bucket")
	}
}

func TestBucketPurge_With_Versions(t *testing.T) {
	fake := &fakeS3{bucket: "catalyst-otech-versions", exists: true}
	// Seed 1500 versions split across 3 keys; AWS S3 caps multi-delete
	// at 1000 per call so the purge code must batch.
	for i := 0; i < 1500; i++ {
		fake.versions = append(fake.versions, fakeS3Version{
			Key:       "key-" + strconv.Itoa(i%3),
			VersionID: "v-" + strconv.Itoa(i),
		})
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := newTestMinio(t, srv)

	removed, err := purgeBucket(context.Background(), client, fake.bucket, nil)
	if err != nil {
		t.Fatalf("purgeBucket on versioned bucket: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1 (bucket should be deleted after empty)", removed)
	}
	if got := len(fake.deletedKeys); got != 1500 {
		t.Errorf("deletedKeys count = %d, want 1500", got)
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called after objects emptied")
	}
}

func TestBucketPurge_Multipart_Aborted(t *testing.T) {
	fake := &fakeS3{
		bucket:  "catalyst-otech-multipart",
		exists:  true,
		uploads: []fakeS3Upload{{Key: "stuck-upload", UploadID: "abc-123"}},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := newTestMinio(t, srv)

	removed, err := purgeBucket(context.Background(), client, fake.bucket, nil)
	if err != nil {
		t.Fatalf("purgeBucket with in-progress multipart: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if len(fake.abortedUps) != 1 {
		t.Errorf("abortedUps = %v, want one entry (AbortMultipartUpload should fire before DeleteBucket)", fake.abortedUps)
	} else if fake.abortedUps[0].Key != "stuck-upload" || fake.abortedUps[0].UploadID != "abc-123" {
		t.Errorf("abortedUps[0] = %+v, want stuck-upload/abc-123", fake.abortedUps[0])
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called after aborts")
	}
}

// TestBucketPurge_Progress_Receives_Messages ensures the operator-facing
// SSE log gets at least one progress callback during a non-trivial
// purge — the wizard banner relies on this.
func TestBucketPurge_Progress_Receives_Messages(t *testing.T) {
	fake := &fakeS3{
		bucket: "catalyst-otech-progress",
		exists: true,
		versions: []fakeS3Version{
			{Key: "k1", VersionID: "v1"},
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	client := newTestMinio(t, srv)

	var msgs []string
	progress := func(s string) { msgs = append(msgs, s) }
	if _, err := purgeBucket(context.Background(), client, fake.bucket, progress); err != nil {
		t.Fatalf("purgeBucket: %v", err)
	}
	foundDeleted := false
	for _, m := range msgs {
		if strings.Contains(m, "deleted bucket") {
			foundDeleted = true
		}
	}
	if !foundDeleted {
		t.Errorf("progress callback never emitted 'deleted bucket' message; got %v", msgs)
	}
}
