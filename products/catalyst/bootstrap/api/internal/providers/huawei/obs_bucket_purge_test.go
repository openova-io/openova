// obs_bucket_purge_test.go — regression tests for #4872's OBS bucket-cleanup
// path (emptyAndRemoveOBSBucket).
//
// The #4909 fix shipped a per-Sovereign OBS purge + a project-wide janitor
// sweep, but both share emptyAndRemoveOBSBucket, which originally used a serial
// per-object RemoveObject WITHOUT version awareness AND swallowed the delete
// error. That leaked buckets three ways (documented on the function): (1) a
// populated Harbor/Velero backup bucket could not be drained within the wipe
// budget by serial deletes → RemoveBucket 409'd BucketNotEmpty; (2) versioned
// objects + delete markers survived a non-versioned list; (3) a swallowed
// per-object error reported phantom success. This file pins the converged,
// Hetzner-equivalent behaviour so those leak modes cannot silently return.
//
// Strategy mirrors internal/hetzner/buckets_test.go: rather than run a real
// minio binary, stub the subset of S3 endpoints minio-go calls during a purge
// and drive the real emptyAndRemoveOBSBucket() code path against it.

package huawei

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

// obsFakeS3 is a minimal single-tenant S3-compatible stub for the OBS
// bucket-purge tests. It implements only what minio-go calls during an
// emptyAndRemoveOBSBucket run:
//
//   - HEAD /<bucket>                       — BucketExists
//   - GET  /<bucket>?versions              — ListObjectVersions (paginated)
//   - POST /<bucket>?delete                — DeleteObjects (multi-object)
//   - GET  /<bucket>?uploads               — ListMultipartUploads
//   - DELETE /<bucket>/<key>?uploadId=...  — AbortMultipartUpload
//   - DELETE /<bucket>                     — DeleteBucket
type obsFakeS3 struct {
	mu sync.Mutex

	bucket   string
	exists   bool
	versions []obsFakeVersion
	uploads  []obsFakeUpload
	// failDeleteKey, when non-empty, makes the multi-object delete report a
	// per-object Error for that key (never deletes it) — used to prove the
	// error is surfaced, not swallowed, and DeleteBucket is NOT attempted.
	failDeleteKey string

	deletedKeys []obsFakeDeleted
	abortedUps  []obsFakeUpload
	bucketDel   atomic.Bool
	versionsCnt atomic.Int32
}

type obsFakeVersion struct {
	Key       string
	VersionID string
	IsDelete  bool
}

type obsFakeUpload struct {
	Key      string
	UploadID string
}

type obsFakeDeleted struct {
	Key       string
	VersionID string
}

// ── AWS S3 XML wire types minio-go expects ────────────────────────────────

type obsListVersionsResult struct {
	XMLName             xml.Name           `xml:"ListVersionsResult"`
	Name                string             `xml:"Name"`
	MaxKeys             int                `xml:"MaxKeys"`
	IsTruncated         bool               `xml:"IsTruncated"`
	KeyMarker           string             `xml:"KeyMarker"`
	NextKeyMarker       string             `xml:"NextKeyMarker"`
	NextVersionIDMarker string             `xml:"NextVersionIdMarker"`
	Versions            []obsListVersion   `xml:"Version"`
	DeleteMarkers       []obsListDelMarker `xml:"DeleteMarker"`
}

type obsListVersion struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type obsListDelMarker struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type obsListMultipartUploadsResult struct {
	XMLName xml.Name          `xml:"ListMultipartUploadsResult"`
	Bucket  string            `xml:"Bucket"`
	Uploads []obsMultipartItem `xml:"Upload"`
}

type obsMultipartItem struct {
	Key      string `xml:"Key"`
	UploadID string `xml:"UploadId"`
}

type obsDeleteRequest struct {
	XMLName xml.Name         `xml:"Delete"`
	Objects []obsDeleteEntry `xml:"Object"`
}

type obsDeleteEntry struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId"`
}

type obsDeleteResult struct {
	XMLName xml.Name            `xml:"DeleteResult"`
	Deleted []obsDeletedEntry   `xml:"Deleted"`
	Errors  []obsDeleteErrEntry `xml:"Error"`
}

type obsDeletedEntry struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
}

type obsDeleteErrEntry struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (f *obsFakeS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		var bucket, key string
		if i := strings.IndexByte(p, '/'); i >= 0 {
			bucket, key = p[:i], p[i+1:]
		} else {
			bucket = p
		}
		if bucket != f.bucket {
			w.WriteHeader(http.StatusNotFound)
			_ = obsXMLError(w, "NoSuchBucket", "bucket does not exist")
			return
		}

		f.mu.Lock()
		defer f.mu.Unlock()

		switch {
		case r.Method == http.MethodHead && key == "":
			if f.exists {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case r.Method == http.MethodGet && key == "" && r.URL.Query().Has("versions"):
			f.versionsCnt.Add(1)
			f.serveListVersions(w, r)
		case r.Method == http.MethodGet && key == "" && r.URL.Query().Has("uploads"):
			f.serveListMultipart(w)
		case r.Method == http.MethodPost && key == "" && r.URL.Query().Has("delete"):
			f.serveMultiDelete(w, r)
		case r.Method == http.MethodDelete && key != "" && r.URL.Query().Has("uploadId"):
			f.abortedUps = append(f.abortedUps, obsFakeUpload{Key: key, UploadID: r.URL.Query().Get("uploadId")})
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && key == "":
			f.exists = false
			f.bucketDel.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ok/>`))
				return
			}
			http.Error(w, "unexpected fake-s3 call: "+r.Method+" "+r.URL.String(), http.StatusMethodNotAllowed)
		}
	})
}

func (f *obsFakeS3) serveListVersions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	keyMarker := q.Get("key-marker")
	verMarker := q.Get("version-id-marker")

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
	resp := obsListVersionsResult{
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
			resp.DeleteMarkers = append(resp.DeleteMarkers, obsListDelMarker{Key: v.Key, VersionID: v.VersionID})
		} else {
			resp.Versions = append(resp.Versions, obsListVersion{Key: v.Key, VersionID: v.VersionID})
		}
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(resp)
}

func (f *obsFakeS3) serveListMultipart(w http.ResponseWriter) {
	resp := obsListMultipartUploadsResult{Bucket: f.bucket}
	for _, u := range f.uploads {
		resp.Uploads = append(resp.Uploads, obsMultipartItem{Key: u.Key, UploadID: u.UploadID})
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(resp)
}

func (f *obsFakeS3) serveMultiDelete(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var dr obsDeleteRequest
	if err := xml.Unmarshal(body, &dr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := obsDeleteResult{}
	for _, o := range dr.Objects {
		if f.failDeleteKey != "" && o.Key == f.failDeleteKey {
			resp.Errors = append(resp.Errors, obsDeleteErrEntry{
				Key: o.Key, Code: "AccessDenied", Message: "object is locked",
			})
			continue
		}
		f.deletedKeys = append(f.deletedKeys, obsFakeDeleted{Key: o.Key, VersionID: o.VersionID})
		newVersions := f.versions[:0]
		for _, v := range f.versions {
			if v.Key == o.Key && v.VersionID == o.VersionID {
				continue
			}
			newVersions = append(newVersions, v)
		}
		f.versions = newVersions
		resp.Deleted = append(resp.Deleted, obsDeletedEntry{Key: o.Key, VersionID: o.VersionID})
	}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(resp)
}

func obsXMLError(w http.ResponseWriter, code, msg string) error {
	w.Header().Set("Content-Type", "application/xml")
	_, err := fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
	return err
}

func obsTestMinio(t *testing.T, srv *httptest.Server) *minio.Client {
	t.Helper()
	endpoint := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4("test-access", "test-secret", ""),
		Secure:       false,
		Region:       "me-east-215",
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatalf("minio.New: %v", err)
	}
	return client
}

// TestEmptyAndRemoveOBSBucket_NotFound — an already-gone bucket is idempotent
// success (nil error, no DeleteBucket attempted). This is the tolerant-of-
// not-found contract the janitor + a re-run wipe depend on.
func TestEmptyAndRemoveOBSBucket_NotFound(t *testing.T) {
	fake := &obsFakeS3{bucket: "catalyst-hw01-omani-works-deadbeef", exists: false}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := emptyAndRemoveOBSBucket(context.Background(), obsTestMinio(t, srv), fake.bucket); err != nil {
		t.Fatalf("emptyAndRemoveOBSBucket on absent bucket: %v (want nil)", err)
	}
	if fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was called against a non-existent bucket")
	}
}

// TestEmptyAndRemoveOBSBucket_Empty — an existing empty bucket is deleted.
func TestEmptyAndRemoveOBSBucket_Empty(t *testing.T) {
	fake := &obsFakeS3{bucket: "catalyst-hw02-omani-works-cafef00d", exists: true}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := emptyAndRemoveOBSBucket(context.Background(), obsTestMinio(t, srv), fake.bucket); err != nil {
		t.Fatalf("emptyAndRemoveOBSBucket on empty bucket: %v", err)
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called on empty bucket")
	}
}

// TestEmptyAndRemoveOBSBucket_VersionsBatched is the core #4872 regression:
// a populated, VERSIONED backup bucket (1500 versions across 3 keys) must be
// fully drained via batched, version-aware multi-object delete and THEN the
// bucket deleted. The old serial+non-versioned code left versions behind and
// 409'd BucketNotEmpty, leaking the bucket to the 100-bucket OBS quota.
func TestEmptyAndRemoveOBSBucket_VersionsBatched(t *testing.T) {
	fake := &obsFakeS3{bucket: "catalyst-hw229-omani-works-abcd1234", exists: true}
	for i := 0; i < 1500; i++ {
		fake.versions = append(fake.versions, obsFakeVersion{
			Key:       "harbor/blob-" + strconv.Itoa(i%3),
			VersionID: "v-" + strconv.Itoa(i),
			IsDelete:  i%7 == 0, // sprinkle delete markers (non-current versions)
		})
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := emptyAndRemoveOBSBucket(context.Background(), obsTestMinio(t, srv), fake.bucket); err != nil {
		t.Fatalf("emptyAndRemoveOBSBucket on versioned bucket: %v", err)
	}
	if got := len(fake.deletedKeys); got != 1500 {
		t.Errorf("deleted versions = %d, want 1500 (every version + delete marker must be removed)", got)
	}
	if fake.versionsCnt.Load() < 2 {
		t.Errorf("ListObjectVersions called %d time(s), want ≥2 (pagination proves the batch loop iterated)", fake.versionsCnt.Load())
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called after the versioned empty completed")
	}
}

// TestEmptyAndRemoveOBSBucket_MultipartAbortedBeforeDelete — a dangling
// multipart upload keeps a bucket non-empty; the purge must abort it before
// DeleteBucket, or RemoveBucket 409s and the bucket leaks.
func TestEmptyAndRemoveOBSBucket_MultipartAbortedBeforeDelete(t *testing.T) {
	fake := &obsFakeS3{
		bucket:  "catalyst-hw77-omani-works-99887766",
		exists:  true,
		uploads: []obsFakeUpload{{Key: "velero/backup.tar", UploadID: "up-42"}},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	if err := emptyAndRemoveOBSBucket(context.Background(), obsTestMinio(t, srv), fake.bucket); err != nil {
		t.Fatalf("emptyAndRemoveOBSBucket with in-progress multipart: %v", err)
	}
	if len(fake.abortedUps) != 1 || fake.abortedUps[0].Key != "velero/backup.tar" {
		t.Errorf("abortedUps = %+v, want one abort of velero/backup.tar before delete", fake.abortedUps)
	}
	if !fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was not called after multipart abort")
	}
}

// TestEmptyAndRemoveOBSBucket_SurfacesDeleteError proves the anti-pattern is
// gone: a per-object delete failure must be RETURNED (not swallowed), and the
// bucket must NOT be deleted (a still-non-empty bucket would only 409). The
// old `_ = RemoveObject(...)` hid this and reported phantom success.
func TestEmptyAndRemoveOBSBucket_SurfacesDeleteError(t *testing.T) {
	fake := &obsFakeS3{
		bucket:        "catalyst-hw180-omani-works-0badf00d",
		exists:        true,
		failDeleteKey: "locked/object",
		versions: []obsFakeVersion{
			{Key: "ok/object", VersionID: "v1"},
			{Key: "locked/object", VersionID: "v2"},
		},
	}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	err := emptyAndRemoveOBSBucket(context.Background(), obsTestMinio(t, srv), fake.bucket)
	if err == nil {
		t.Fatalf("emptyAndRemoveOBSBucket returned nil, want a surfaced delete error")
	}
	if !strings.Contains(err.Error(), "locked/object") {
		t.Errorf("error %q does not name the stuck object — the diagnostic was lost", err.Error())
	}
	if fake.bucketDel.Load() {
		t.Errorf("DeleteBucket was called despite a failed object delete — would 409 BucketNotEmpty and mask the leak")
	}
}
