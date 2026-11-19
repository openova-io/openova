package search

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/cortex/internal/gateway"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

// fakeStore answers from fixed lists and records what Search was asked.
type fakeStore struct {
	store.Store
	readable   []string
	hits       []store.Hit
	docs       []store.Document
	principals []string
	sources    []string
	limit      int
}

func (f *fakeStore) ReadableSources(_ context.Context, p []string) ([]string, error) {
	return f.readable, nil
}
func (f *fakeStore) Search(_ context.Context, p, s []string, v []float32, limit int) ([]store.Hit, error) {
	f.principals, f.sources, f.limit = p, s, limit
	return f.hits, nil
}
func (f *fakeStore) Documents(_ context.Context, p, ids []string) ([]store.Document, error) {
	return f.docs, nil
}

type fakeEmbedder struct {
	calls int
	err   error
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	return [][]float32{{1, 0}}, e.err
}

type fakeReranker struct {
	ranked []gateway.Ranked
	topN   int
	err    error
}

func (r *fakeReranker) Rerank(_ context.Context, q string, docs []string, topN int) ([]gateway.Ranked, error) {
	r.topN = topN
	return r.ranked, r.err
}

func fixture() (*Service, *fakeStore, *fakeEmbedder) {
	st := &fakeStore{
		readable: []string{"board", "hr"},
		hits: []store.Hit{{ChunkID: 1, DocumentID: "d1", SourceID: "hr", Page: 2, Text: "one", Score: 0.9},
			{ChunkID: 2, DocumentID: "d2", SourceID: "hr", Text: "two", Score: 0.8},
			{ChunkID: 3, DocumentID: "gone", SourceID: "hr", Text: "three", Score: 0.7}},
		docs: []store.Document{{ID: "d1", Title: "Leave", URI: "u1"}, {ID: "d2", Title: "Pay", URI: "u2"}},
	}
	em := &fakeEmbedder{}
	return &Service{Store: st, Embedder: em}, st, em
}

func ptr[T any](v T) *T { return &v }

func TestSearchValidatesTheRequest(t *testing.T) {
	s, _, em := fixture()
	for _, r := range []Request{{Query: ""}, {Query: strings.Repeat("é", MaxQuery+1)}, {Query: "\xff"},
		{Query: "q", TopK: ptr(0)}, {Query: "q", TopK: ptr(MaxTopK + 1)}} {
		if _, err := s.Search(context.Background(), []string{"org:members"}, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v gave %v", r, err)
		}
	}
	if _, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: strings.Repeat("é", MaxQuery)}); err != nil || em.calls != 1 {
		t.Errorf("a query of %d characters gave %v", MaxQuery, err)
	}
}

func TestSearchChecksTheSourcesAskedFor(t *testing.T) {
	s, st, em := fixture()
	p := []string{"user:ann", "org:members"}
	for _, named := range [][]string{{"finance"}, {"hr", "finance"}} {
		if _, err := s.Search(context.Background(), p, Request{Query: "q", Sources: named}); !errors.Is(err, ErrForbidden) {
			t.Errorf("%v gave %v", named, err)
		}
	}
	if em.calls != 0 {
		t.Fatal("a forbidden source reached the gateway")
	}
	if _, err := s.Search(context.Background(), p, Request{Query: "q", Sources: []string{"hr", "hr"}}); err != nil ||
		!reflect.DeepEqual(st.sources, []string{"hr"}) || !reflect.DeepEqual(st.principals, p) || st.limit != DefaultTopK {
		t.Fatalf("the store got %v %v %d (%v)", st.principals, st.sources, st.limit, err)
	}
	if _, err := s.Search(context.Background(), p, Request{Query: "q"}); err != nil || !reflect.DeepEqual(st.sources, []string{"board", "hr"}) {
		t.Fatalf("without sources the store got %v (%v)", st.sources, err)
	}
	st.readable = nil
	if got, err := s.Search(context.Background(), p, Request{Query: "q"}); err != nil || got == nil || len(got) != 0 || em.calls != 2 {
		t.Fatalf("a caller who reads nothing got %v, %v, %d gateway calls", got, err, em.calls)
	}
}

func TestSearchResults(t *testing.T) {
	s, _, _ := fixture()
	got, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: "q", TopK: ptr(3)})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v; want the two hits whose document is readable", got, err)
	}
	if got[0].Title != "Leave" || got[0].URI != "u1" || *got[0].Chunk.Page != 2 || got[0].Chunk.Score != 0.9 || got[1].Chunk.Page != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestSearchReranks(t *testing.T) {
	s, st, _ := fixture()
	rr := &fakeReranker{ranked: []gateway.Ranked{{Index: 1, Score: 0.7}, {Index: 0, Score: 0.2}}}
	s.Reranker = rr
	got, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: "q", TopK: ptr(2)})
	if err != nil || len(got) != 2 || got[0].DocumentID != "d2" || got[0].Chunk.Score != 0.7 || st.limit != 2*Candidates || rr.topN != 2 {
		t.Fatalf("got %+v, %v; the store was asked for %d", got, err, st.limit)
	}
	rr.topN = 0
	if _, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: "q", Rerank: ptr(false)}); err != nil || rr.topN != 0 || st.limit != DefaultTopK {
		t.Fatalf("rerank false still reranked (%v)", err)
	}
	rr.err = errors.New("down")
	if _, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: "q"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a failed rerank gave %v", err)
	}
}

func TestSearchGatewayUnavailable(t *testing.T) {
	s, _, em := fixture()
	em.err = errors.New("down")
	if _, err := s.Search(context.Background(), []string{"org:members"}, Request{Query: "q"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}
