// Package ingest takes documents in and makes them searchable. This version takes uploads.
//
// The query process stages an upload (Intake): it stores the original in the originals bucket
// under <document id>/<SHA-256 of the content> and queues an index job that carries the
// upload's item. The query process holds the role cortex_query, which may queue a job and write
// nothing else. The index process runs the jobs (Indexer): an index job records the document,
// with its own reader list when the upload named readers, reads the original, parses it,
// chunks it, embeds the chunks through the model gateway and writes them; a delete job removes
// the original and then the document. A job whose source was removed ends and deletes the
// original it staged.
package ingest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/openova-io/openova/products/cortex/internal/chunk"
	"github.com/openova-io/openova/products/cortex/internal/jobs"
	"github.com/openova-io/openova/products/cortex/internal/parse"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// Parser turns an original into the parsed document; parse.Text is one.
type Parser interface {
	Parse(ctx context.Context, mediaType string, content io.Reader) (parse.Document, error)
}

// Embedder is the gateway's embedding route; *gateway.Embedder is one.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
}

// Item is what the query process stages for the index job of an upload: the job's item.
type Item struct {
	ExternalID string   `json:"externalId"` // upload:<document id>
	Title      string   `json:"title"`
	URI        string   `json:"uri"` // /v1/documents/<document id>/content
	MediaType  string   `json:"mediaType"`
	SHA256     string   `json:"sha256"`
	Object     string   `json:"object"`
	Readers    []string `json:"readers,omitempty"` // the document's own reader list; empty: it inherits
}

// ErrType is returned for an upload whose content is not of a type a parser reads.
var ErrType = errors.New("ingest: the type of the content is not allowed")

// MediaTypes are the types an upload may have, decided by content sniffing, mapped to the
// media type the parser is given. Only the text parser exists; the documents parser adds more.
var MediaTypes = map[string]string{"text/plain; charset=utf-8": "text/plain"}

// Intake stages uploads.
type Intake struct {
	Objects Objects
	Queue   interface {
		Enqueue(ctx context.Context, job store.Job) (string, error)
	}
}

// Upload stores the original of an upload to the source sourceID and queues its index job.
// content is read twice and must hold size bytes; readers is the document's own reader list.
func (in *Intake) Upload(ctx context.Context, sourceID, filename string, content io.ReadSeeker, size int64, readers []string) (documentID, jobID string, err error) {
	head := make([]byte, 512)
	n, err := io.ReadFull(content, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	mediaType, ok := MediaTypes[http.DetectContentType(head[:n])]
	if !ok {
		return "", "", ErrType
	}
	h := sha256.New()
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	if _, err := io.Copy(h, content); err != nil {
		return "", "", err
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	documentID = newID()
	it := Item{ExternalID: "upload:" + documentID, Title: title(filename), URI: "/v1/documents/" + documentID + "/content",
		MediaType: mediaType, SHA256: sum, Object: documentID + "/" + sum, Readers: readers}
	if err := in.Objects.Put(ctx, it.Object, content, size, sum); err != nil {
		return "", "", err
	}
	item, _ := json.Marshal(it)
	jobID, err = in.Queue.Enqueue(ctx, store.Job{Kind: "index", SourceID: sourceID, DocumentID: documentID, Item: item})
	if err != nil {
		return "", "", err
	}
	return documentID, jobID, nil
}

// title is the display name of an upload: its file name without control characters, at most
// 200 characters, or "upload".
func title(filename string) string {
	t := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, filename))
	if r := []rune(t); len(r) > 200 {
		t = string(r[:200])
	}
	if t == "" {
		return "upload"
	}
	return t
}

// newID returns a random UUID in its text form.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	x := hex.EncodeToString(b[:])
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:]
}

// IndexStore is the part of the store the index process writes; *store.Postgres is one.
type IndexStore interface {
	PutDocument(ctx context.Context, d store.NewDocument, readers []string) (store.ChunkSettings, error)
	ReplaceChunks(ctx context.Context, documentID string, chunks []store.Chunk, readers []string) error
	MarkDocument(ctx context.Context, id, state string) error
	DeleteDocument(ctx context.Context, id string) error
}

// embedBatch is the number of chunks embedded by one call of the gateway.
const embedBatch = 32

// Indexer runs index and delete jobs.
type Indexer struct {
	Store    IndexStore
	Objects  Objects
	Parser   Parser
	Embedder Embedder
}

// Handle runs one job. An error that wraps jobs.ErrPermanent is not tried again.
func (x *Indexer) Handle(ctx context.Context, j store.Job) error {
	switch j.Kind {
	case "index":
		return x.index(ctx, j)
	case "delete":
		return x.delete(ctx, j)
	}
	return fmt.Errorf("%w: the job kind %q has no handler", jobs.ErrPermanent, j.Kind)
}

func (x *Indexer) index(ctx context.Context, j store.Job) error {
	var it Item
	dec := json.NewDecoder(bytes.NewReader(j.Item))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&it); err != nil || it.Object == "" || it.SHA256 == "" || it.MediaType == "" || j.DocumentID == "" {
		return fmt.Errorf("%w: the index job has no valid item", jobs.ErrPermanent)
	}
	settings, err := x.Store.PutDocument(ctx, store.NewDocument{ID: j.DocumentID, SourceID: j.SourceID, ExternalID: it.ExternalID,
		Title: it.Title, URI: it.URI, SHA256: it.SHA256, ObjectKey: it.Object}, it.Readers)
	if errors.Is(err, store.ErrNotFound) {
		if err := x.Objects.Delete(ctx, it.Object); err != nil {
			return err
		}
		return fmt.Errorf("%w: the source %s was removed", jobs.ErrPermanent, j.SourceID)
	}
	if err != nil {
		return err
	}
	err = x.write(ctx, j.DocumentID, it, settings)
	if errors.Is(err, ErrNoObject) {
		// A delete ran while this job waited for a retry: the document goes with its original.
		return errors.Join(fmt.Errorf("%w: the original was deleted", jobs.ErrPermanent), x.Store.DeleteDocument(ctx, j.DocumentID))
	}
	if err != nil {
		return errors.Join(err, x.Store.MarkDocument(ctx, j.DocumentID, "failed"))
	}
	return x.Store.MarkDocument(ctx, j.DocumentID, "indexed")
}

// write parses, chunks and embeds the original and replaces the document's chunks.
func (x *Indexer) write(ctx context.Context, id string, it Item, settings store.ChunkSettings) error {
	rc, _, err := x.Objects.Get(ctx, it.Object)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	doc, err := x.Parser.Parse(ctx, it.MediaType, rc)
	if errors.Is(err, parse.ErrMediaType) {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	if err != nil {
		return err
	}
	pieces := chunk.Split(doc, it.Title, settings.Tokens, settings.Overlap)
	chunks := make([]store.Chunk, 0, len(pieces))
	for start := 0; start < len(pieces); start += embedBatch {
		batch := pieces[start:min(start+embedBatch, len(pieces))]
		texts := make([]string, len(batch))
		for i, p := range batch {
			texts[i] = p.Text
		}
		vectors, err := x.Embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("ingest: %d vectors for %d chunks", len(vectors), len(batch))
		}
		for i, p := range batch {
			chunks = append(chunks, store.Chunk{Ordinal: p.Ordinal, Page: p.Page, Text: p.Text, Embedding: vectors[i]})
		}
	}
	return x.Store.ReplaceChunks(ctx, id, chunks, nil)
}

// delete removes the original named by the job's item, then the document when the job names one.
// Every delete job carries its object: the route copies the document's, and the removal of a
// source writes the object of each of its documents.
func (x *Indexer) delete(ctx context.Context, j store.Job) error {
	var it struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(j.Item, &it); err != nil || it.Object == "" {
		return fmt.Errorf("%w: the delete job has no valid item", jobs.ErrPermanent)
	}
	if err := x.Objects.Delete(ctx, it.Object); err != nil || j.DocumentID == "" {
		return err
	}
	return x.Store.DeleteDocument(ctx, j.DocumentID)
}
