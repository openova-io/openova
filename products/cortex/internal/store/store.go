// Package store is the database of cortex: migration 0001, the two database roles and the
// trimmed search. Store is the port the processes use; Postgres implements it.
//
// Every read of a chunk or a document is trimmed by row-level security to the caller's
// principals. A chunk row carries its readers, the principals that may read its document,
// so the chunk policy tests the row's own column, and the search binds the same principals
// a second time in its WHERE clause. A one-percent reader then gets an exact scan of its
// rows through the GIN index on readers, not the HNSW index, whose recall is below 0.99
// under such a filter (spikes PRD-060 and PRD-061).
package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Hit is one chunk a search returned.
type Hit struct {
	ChunkID    int64
	DocumentID string
	SourceID   string
	Page       int
	Text       string
	Score      float64
}

// Chunk is one piece of a document's text with its embedding.
type Chunk struct {
	Ordinal   int
	Page      int // 0 when the format has no pages
	Text      string
	Embedding []float32
}

// SourceDefinition is the part of one sources[] entry of the definitions that the store keeps.
type SourceDefinition struct {
	ID       string
	Title    string
	Digest   string   // SHA-256 of the sources[] entry; stored as source.definition_sha256
	Readers  []string // principals that read every inheriting document of the source
	Managers []string // principals that manage the source and read all of its documents
}

// SourceStatus is one source as a caller sees it.
type SourceStatus struct {
	ID         string
	Title      string
	State      string // idle, syncing or error
	Documents  int64  // the documents of the source the caller may read
	Chunks     int64  // the chunks of those documents
	LastSyncAt *time.Time
	LastError  *string
}

// Document is the record of one document.
type Document struct {
	ID        string
	SourceID  string
	Title     string
	URI       string
	State     string // fetched, indexed or failed
	UpdatedAt time.Time
}

// Store is the port of the cortex database, the one place a second vector implementation
// would enter.
type Store interface {
	// Search runs the trimmed query. principals must not be empty.
	Search(ctx context.Context, principals []string, sources []string, vector []float32, limit int) ([]Hit, error)
	// ReadableSources returns the ids of sources in which any of the principals may read at least one document
	// (through source_acl or document_acl).
	ReadableSources(ctx context.Context, principals []string) ([]string, error)
	// Sources returns, sorted by id, the sources in which any of the principals may read at least
	// one document, each with the number of its documents and chunks that they may read.
	Sources(ctx context.Context, principals []string) ([]SourceStatus, error)
	// Documents returns, sorted by id, the documents of ids that any of the principals may read.
	Documents(ctx context.Context, principals []string, ids []string) ([]Document, error)
	// ReplaceChunks writes a document's chunks and, when readers is not nil, its access rows, in one transaction.
	ReplaceChunks(ctx context.Context, documentID string, chunks []Chunk, readers []string) error
	// ApplyDefinitions makes source and source_acl equal to the definitions; removed sources are deleted.
	ApplyDefinitions(ctx context.Context, defs []SourceDefinition) error
}

// Role is one of the two database roles a process takes.
type Role string

const (
	// RoleQuery is the role of the query process: it reads, trimmed, and queues jobs.
	RoleQuery Role = "cortex_query"
	// RoleIndex is the role of the index process: it writes everything else.
	RoleIndex Role = "cortex_index"
)

// Errors of the store.
var (
	ErrNoPrincipals = errors.New("store: no principals")
	ErrNotFound     = errors.New("store: not found")
	ErrExtension    = errors.New("store: the vector extension is missing or older than 0.8.0")
	ErrDimensions   = errors.New("store: the embedding length differs from the database")
)

// MaxLimit is the largest number of hits one search returns.
const MaxLimit = 200

// principalPattern is the form of a principal: user:<subject or e-mail>, group:<group>,
// agent:<organization>/<agent> or org:members.
var principalPattern = regexp.MustCompile(`^(user:.+|group:.+|agent:[^/]+/.+|org:members)$`)

// checkPrincipals refuses an empty list and a principal of another form.
func checkPrincipals(principals []string) error {
	if len(principals) == 0 {
		return ErrNoPrincipals
	}
	for _, p := range principals {
		if !principalPattern.MatchString(p) {
			return fmt.Errorf("store: %q is not a principal", p)
		}
	}
	return nil
}

// checkDimensions refuses a vector length outside 64 to 4096.
func checkDimensions(d int) error {
	if d < 64 || d > 4096 {
		return fmt.Errorf("store: the vector length %d is not between 64 and 4096", d)
	}
	return nil
}
