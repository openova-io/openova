// Package search is the knowledge search of cortex, the body of POST /v1/search: the sources
// asked for are checked against those the caller reads, the query is embedded through the
// gateway, the store runs the trimmed query with the caller's principals, the hits are
// reranked through the gateway when asked and an alias is set, and each hit gets the title and
// the address of its document, read trimmed as well.
//
// The query text goes to the gateway and nowhere else: no error of this package carries it.
package search

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// Limits of a request.
const (
	MaxQuery    = 2000 // characters
	DefaultTopK = 8
	MaxTopK     = 50
	// Candidates is how many hits per result the store returns when the hits are reranked.
	Candidates = 4
)

// Errors of a search. An invalid request wraps ErrInvalid.
var (
	ErrInvalid     = errors.New("invalid-request")
	ErrForbidden   = errors.New("source-forbidden")
	ErrUnavailable = errors.New("gateway-unavailable")
)

// Request is the body of POST /v1/search. Nil fields take their defaults.
type Request struct {
	Query   string   `json:"query"`
	Sources []string `json:"sources"`
	TopK    *int     `json:"topK"`
	Rerank  *bool    `json:"rerank"`
}

// Result is one hit.
type Result struct {
	DocumentID string `json:"documentId"`
	SourceID   string `json:"sourceId"`
	Title      string `json:"title"`
	URI        string `json:"uri"`
	Chunk      Chunk  `json:"chunk"`
}

// Chunk is the passage of a hit. Page is null for a format without pages. Score is the
// reranker's relevance when the hits were reranked, else the cosine similarity.
type Chunk struct {
	ID    int64   `json:"id"`
	Text  string  `json:"text"`
	Page  *int    `json:"page"`
	Score float64 `json:"score"`
}

// Embedder embeds the query.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Reranker orders passages by relevance, best first.
type Reranker interface {
	Rerank(ctx context.Context, query string, documents []string, topN int) ([]gateway.Ranked, error)
}

// Service runs searches. Reranker is nil when the parameters set no rerank alias.
type Service struct {
	Store    store.Store
	Embedder Embedder
	Reranker Reranker
}

// Search runs r for the caller's principals.
func (s *Service) Search(ctx context.Context, principals []string, r Request) ([]Result, error) {
	topK := DefaultTopK
	if r.TopK != nil {
		topK = *r.TopK
	}
	switch n := utf8.RuneCountInString(r.Query); {
	case !utf8.ValidString(r.Query) || n < 1 || n > MaxQuery:
		return nil, fmt.Errorf("%w: query must be 1 to %d characters", ErrInvalid, MaxQuery)
	case topK < 1 || topK > MaxTopK:
		return nil, fmt.Errorf("%w: topK must be 1 to %d", ErrInvalid, MaxTopK)
	}
	readable, err := s.Store.ReadableSources(ctx, principals)
	if err != nil {
		return nil, err
	}
	sources := readable
	if len(r.Sources) > 0 {
		sources = nil
		for _, id := range r.Sources {
			if !slices.Contains(readable, id) {
				return nil, ErrForbidden // also for a source that does not exist: its existence is not told
			}
			if !slices.Contains(sources, id) {
				sources = append(sources, id)
			}
		}
	}
	if len(sources) == 0 {
		return []Result{}, nil
	}
	vectors, err := s.Embedder.Embed(ctx, []string{r.Query})
	if err != nil || len(vectors) != 1 {
		return nil, fmt.Errorf("%w: embedding: %v", ErrUnavailable, err)
	}
	rerank := s.Reranker != nil && (r.Rerank == nil || *r.Rerank)
	limit := topK
	if rerank {
		limit = min(topK*Candidates, store.MaxLimit)
	}
	hits, err := s.Store.Search(ctx, principals, sources, vectors[0], limit)
	if err != nil {
		return nil, err
	}
	if rerank && len(hits) > 0 {
		texts := make([]string, len(hits))
		for i, h := range hits {
			texts[i] = h.Text
		}
		ranked, err := s.Reranker.Rerank(ctx, r.Query, texts, topK)
		if err != nil {
			return nil, fmt.Errorf("%w: rerank: %v", ErrUnavailable, err)
		}
		reordered := make([]store.Hit, 0, len(ranked))
		for _, x := range ranked {
			h := hits[x.Index]
			h.Score = x.Score
			reordered = append(reordered, h)
		}
		hits = reordered
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	return s.results(ctx, principals, hits)
}

// results adds the title and address of each hit's document. A hit whose document the caller
// may no longer read, or that was deleted meanwhile, is left out.
func (s *Service) results(ctx context.Context, principals []string, hits []store.Hit) ([]Result, error) {
	var ids []string
	for _, h := range hits {
		if !slices.Contains(ids, h.DocumentID) {
			ids = append(ids, h.DocumentID)
		}
	}
	docs, err := s.Store.Documents(ctx, principals, ids)
	if err != nil {
		return nil, err
	}
	byID := map[string]store.Document{}
	for _, d := range docs {
		byID[d.ID] = d
	}
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		d, ok := byID[h.DocumentID]
		if !ok {
			continue
		}
		c := Chunk{ID: h.ChunkID, Text: h.Text, Score: h.Score}
		if h.Page > 0 {
			c.Page = &h.Page
		}
		out = append(out, Result{DocumentID: h.DocumentID, SourceID: h.SourceID, Title: d.Title, URI: d.URI, Chunk: c})
	}
	return out, nil
}
