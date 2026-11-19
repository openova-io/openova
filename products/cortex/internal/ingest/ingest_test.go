package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/cortex/internal/jobs"
	"github.com/openova-io/openova/products/cortex/internal/parse"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// memObjects is a bucket in memory.
type memObjects map[string]string

func (m memObjects) Put(_ context.Context, key string, c io.Reader, size int64, sum string) error {
	b, _ := io.ReadAll(c)
	if int64(len(b)) != size {
		return errors.New("short content")
	}
	m[key] = string(b)
	return nil
}
func (m memObjects) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	b, ok := m[key]
	if !ok {
		return nil, 0, ErrNoObject
	}
	return io.NopCloser(strings.NewReader(b)), int64(len(b)), nil
}
func (m memObjects) Delete(_ context.Context, key string) error { delete(m, key); return nil }

type queue struct{ jobs []store.Job }

func (q *queue) Enqueue(_ context.Context, j store.Job) (string, error) {
	q.jobs = append(q.jobs, j)
	return "job-1", nil
}

func TestIntakeStagesAnUpload(t *testing.T) {
	objects, q := memObjects{}, &queue{}
	in := &Intake{Objects: objects, Queue: q}
	text := "# Leave\n\nEmployees accrue leave monthly.\n"
	doc, job, err := in.Upload(context.Background(), "hr", "leave\x07.md", strings.NewReader(text), int64(len(text)), []string{"user:alice"})
	if err != nil || job != "job-1" || len(doc) != 36 {
		t.Fatalf("Upload = %q, %q, %v", doc, job, err)
	}
	sum := sha256.Sum256([]byte(text))
	key := doc + "/" + hex.EncodeToString(sum[:])
	if objects[key] != text || len(objects) != 1 {
		t.Fatalf("objects = %v, want the text under %s", objects, key)
	}
	var it Item
	if len(q.jobs) != 1 || q.jobs[0].Kind != "index" || q.jobs[0].SourceID != "hr" || q.jobs[0].DocumentID != doc || json.Unmarshal(q.jobs[0].Item, &it) != nil {
		t.Fatalf("jobs = %+v", q.jobs)
	}
	want := Item{ExternalID: "upload:" + doc, Title: "leave.md", URI: "/v1/documents/" + doc + "/content", MediaType: "text/plain",
		SHA256: hex.EncodeToString(sum[:]), Object: key, Readers: []string{"user:alice"}}
	if !reflect.DeepEqual(it, want) {
		t.Fatalf("item = %+v, want %+v", it, want)
	}
	for _, binary := range []string{"\x89PNG\r\n\x1a\n\x00\x00", "%PDF-1.7\n", "<html><body>x</body></html>", "\x00\x01\x02"} {
		if _, _, err := in.Upload(context.Background(), "hr", "x", strings.NewReader(binary), int64(len(binary)), nil); !errors.Is(err, ErrType) {
			t.Errorf("%q: %v, want ErrType", binary, err)
		}
	}
	if len(objects) != 1 || len(q.jobs) != 1 {
		t.Fatal("a refused upload stored or queued something")
	}
}

// indexStore records what the indexer writes.
type indexStore struct {
	put     []string
	readers []string
	chunks  []store.Chunk
	marks   []string
	deleted []string
	gone    bool
}

func (s *indexStore) PutDocument(_ context.Context, d store.NewDocument, readers []string) (store.ChunkSettings, error) {
	if s.gone {
		return store.ChunkSettings{}, store.ErrNotFound
	}
	s.put, s.readers = append(s.put, d.ID+" "+d.SourceID+" "+d.Title+" "+d.ObjectKey), readers
	return store.ChunkSettings{Tokens: 128, Overlap: 0}, nil
}
func (s *indexStore) ReplaceChunks(_ context.Context, _ string, c []store.Chunk, readers []string) error {
	if readers != nil {
		return errors.New("the indexer passed readers to ReplaceChunks")
	}
	s.chunks = c
	return nil
}
func (s *indexStore) MarkDocument(_ context.Context, id, state string) error {
	s.marks = append(s.marks, state)
	return nil
}
func (s *indexStore) DeleteDocument(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

type embedder struct {
	calls int
	fail  bool
}

func (e *embedder) Dimensions() int { return 2 }
func (e *embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.fail {
		return nil, errors.New("gateway: unavailable")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(i), 1}
	}
	return out, nil
}

const docID = "00000000-0000-4000-8000-0000000000d1"

func indexJob(item Item) store.Job {
	b, _ := json.Marshal(item)
	return store.Job{ID: "j1", Kind: "index", SourceID: "hr", DocumentID: docID, Item: b}
}

func TestIndexerIndexesAnUpload(t *testing.T) {
	var text bytes.Buffer
	text.WriteString("# Leave\n\n")
	for i := 0; i < 40*128; i++ {
		text.WriteString("word ")
	}
	objects := memObjects{docID + "/s": text.String()}
	st, emb := &indexStore{}, &embedder{}
	x := &Indexer{Store: st, Objects: objects, Parser: parse.Text{}, Embedder: emb}
	item := Item{ExternalID: "upload:" + docID, Title: "leave.md", MediaType: "text/plain", SHA256: "s", Object: docID + "/s", Readers: []string{"user:alice"}}
	if err := x.Handle(context.Background(), indexJob(item)); err != nil {
		t.Fatal(err)
	}
	if len(st.chunks) != 40 || emb.calls != 2 || st.chunks[39].Ordinal != 39 || st.chunks[39].Embedding[0] != 7 ||
		!strings.HasPrefix(st.chunks[0].Text, "Leave\n\nword word") || !reflect.DeepEqual(st.readers, []string{"user:alice"}) ||
		!reflect.DeepEqual(st.marks, []string{"indexed"}) || st.put[0] != docID+" hr leave.md "+docID+"/s" {
		t.Fatalf("chunks %d, embed calls %d, readers %v, marks %v, put %v", len(st.chunks), emb.calls, st.readers, st.marks, st.put)
	}

	emb.fail = true
	if err := x.Handle(context.Background(), indexJob(item)); err == nil || errors.Is(err, jobs.ErrPermanent) || st.marks[1] != "failed" {
		t.Fatalf("a gateway failure: %v, marks %v; want a failure that is retried and the document failed", err, st.marks)
	}
	bad := item
	bad.MediaType = "application/pdf"
	if err := x.Handle(context.Background(), indexJob(bad)); !errors.Is(err, jobs.ErrPermanent) {
		t.Fatalf("a type no parser reads: %v, want a permanent failure", err)
	}
	for what, j := range map[string]store.Job{"no item": {Kind: "index", DocumentID: docID}, "a field more": {Kind: "index", DocumentID: docID,
		Item: []byte(`{"object":"o","sha256":"s","mediaType":"text/plain","path":"/etc"}`)}, "another kind": {Kind: "sync"}} {
		if err := x.Handle(context.Background(), j); !errors.Is(err, jobs.ErrPermanent) {
			t.Errorf("%s: %v, want a permanent failure", what, err)
		}
	}
	st.gone = true
	if err := x.Handle(context.Background(), indexJob(item)); !errors.Is(err, jobs.ErrPermanent) || len(objects) != 0 {
		t.Fatalf("a removed source: %v, objects %v; want a permanent failure and the original deleted", err, objects)
	}
}

func TestIndexerDeletes(t *testing.T) {
	objects := memObjects{"a": "1", "b": "2"}
	st := &indexStore{}
	x := &Indexer{Store: st, Objects: objects}
	ctx := context.Background()
	if err := x.Handle(ctx, store.Job{Kind: "delete", DocumentID: docID, Item: []byte(`{"object":"a"}`)}); err != nil || len(objects) != 1 || st.deleted[0] != docID {
		t.Fatalf("delete: %v, objects %v, deleted %v", err, objects, st.deleted)
	}
	if err := x.Handle(ctx, store.Job{Kind: "delete", Item: []byte(`{"object":"b"}`)}); err != nil || len(objects) != 0 || len(st.deleted) != 1 {
		t.Fatalf("delete of an original alone: %v, objects %v, deleted %v", err, objects, st.deleted)
	}
	if err := x.Handle(ctx, store.Job{Kind: "delete", DocumentID: docID}); !errors.Is(err, jobs.ErrPermanent) {
		t.Fatalf("a delete job without its object: %v", err)
	}
	// An index job that runs after the delete of its document ends, and removes the row it wrote.
	x.Parser = parse.Text{}
	if err := x.Handle(ctx, indexJob(Item{Title: "t", MediaType: "text/plain", SHA256: "s", Object: "a"})); !errors.Is(err, jobs.ErrPermanent) || len(st.deleted) != 2 {
		t.Fatalf("an index job without its original: %v, deleted %v", err, st.deleted)
	}
}
