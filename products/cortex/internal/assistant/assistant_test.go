package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/cortex/internal/search"
	"github.com/openova-io/openova/products/cortex/internal/store"
)

const doc = `models: {embed: {alias: embed-default, dimensions: 64}, chat: {alias: agent-fast}}
policy: {allowExternalModels: true, maxUploadMb: 5}
sources: []
assistants:
  - id: helpdesk
    title: Staff helpdesk
    instructions: Answer from the cited passages only.
    sources: [{id: hr}, {id: wiki, tier: fallback}]
    retrieval: {topK: 3, rerank: false}
    users: ["org:members"]
  - {id: board, title: Board, instructions: "", sources: [{id: papers}], users: []}
`

func TestParse(t *testing.T) {
	cfg, err := Parse([]byte(doc))
	want := Config{ChatAlias: "agent-fast", AllowExternalModels: true, Assistants: []Assistant{
		{ID: "helpdesk", Title: "Staff helpdesk", Instructions: "Answer from the cited passages only.",
			Sources: []Source{{"hr", "primary"}, {"wiki", "fallback"}}, TopK: 3, Users: []string{"org:members"}},
		{ID: "board", Title: "Board", Sources: []Source{{"papers", "primary"}}, TopK: 8, Rerank: true, Users: []string{}}}}
	if err != nil || !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	for what, change := range map[string][2]string{
		"no chat alias":     {"chat: {alias: agent-fast}", "chat: {}"},
		"an id twice":       {"id: board", "id: helpdesk"},
		"an id of capitals": {"id: board", "id: Board"},
		"no title":          {"title: Board", "title: ''"},
		"no source":         {"sources: [{id: papers}]", "sources: []"},
		"a tier unknown":    {"tier: fallback", "tier: last"},
		"topK 51":           {"topK: 3", "topK: 51"},
		"a user unknown":    {`users: ["org:members"]`, `users: ["everyone"]`},
		"not a list":        {"  - {id: board", "  {id: board"},
	} {
		if _, err := Parse([]byte(strings.Replace(doc, change[0], change[1], 1))); err == nil {
			t.Errorf("%s: the document was accepted", what)
		}
	}
}

// TestFileKeepsTheLastValidVersion reads a new version at once and keeps the last valid one when
// the file is refused or gone; with no valid version it fails.
func TestFileKeepsTheLastValidVersion(t *testing.T) {
	f := &File{Path: filepath.Join(t.TempDir(), "definitions.yaml")}
	if _, err := f.Config(); err == nil {
		t.Fatal("a missing file was accepted")
	}
	for i, text := range []string{doc, "models: {chat: {alias: agent-default}}\n", "assistants: 1\n", ""} {
		if text == "" {
			_ = os.Remove(f.Path)
		} else if err := os.WriteFile(f.Path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := f.Config()
		if want := []string{"agent-fast", "agent-default", "agent-default", "agent-default"}[i]; err != nil || cfg.ChatAlias != want {
			t.Fatalf("version %d: %q, %v; want %q", i, cfg.ChatAlias, err, want)
		}
	}
}

// tierStore holds hits[source] passages of each source; the caller reads every source but "closed".
type tierStore struct {
	store.Store
	hits     map[string]int
	searched []string
}

func (s *tierStore) ReadableSources(context.Context, []string) ([]string, error) {
	return []string{"p1", "p2", "s1", "f1"}, nil
}
func (s *tierStore) Search(_ context.Context, _, sources []string, _ []float32, limit int) ([]store.Hit, error) {
	s.searched = append(s.searched, fmt.Sprint(sources, limit))
	var out []store.Hit
	for _, id := range sources {
		for i := 0; i < s.hits[id] && len(out) < limit; i++ {
			out = append(out, store.Hit{ChunkID: int64(len(out)), DocumentID: id, SourceID: id, Text: id})
		}
	}
	return out, nil
}
func (s *tierStore) Documents(_ context.Context, _, ids []string) ([]store.Document, error) {
	var out []store.Document
	for _, id := range ids {
		out = append(out, store.Document{ID: id, SourceID: id, Title: id})
	}
	return out, nil
}

type oneVector struct{}

func (oneVector) Embed(context.Context, []string) ([][]float32, error) { return [][]float32{{1}}, nil }

// TestTiers: the secondary tier fills up to topK only when the primary one gave fewer, the
// fallback tier is searched only when both gave none, and a source the caller cannot read is
// never searched.
func TestTiers(t *testing.T) {
	a := Assistant{TopK: 3, Sources: []Source{{"p1", "primary"}, {"closed", "primary"}, {"p2", "primary"}, {"s1", "secondary"}, {"f1", "fallback"}}}
	for _, c := range []struct {
		hits     map[string]int
		searched []string
		found    int
	}{
		{map[string]int{"p1": 2, "p2": 2, "s1": 5, "f1": 5}, []string{"[p1 p2] 3"}, 3},
		{map[string]int{"p1": 1, "s1": 5, "f1": 5}, []string{"[p1 p2] 3", "[s1] 2"}, 3},
		{map[string]int{"s1": 1, "f1": 5}, []string{"[p1 p2] 3", "[s1] 3"}, 1},
		{map[string]int{"f1": 5}, []string{"[p1 p2] 3", "[s1] 3", "[f1] 3"}, 3},
	} {
		st := &tierStore{hits: c.hits}
		s := &Service{Search: &search.Service{Store: st, Embedder: oneVector{}}}
		got, err := s.retrieve(context.Background(), []string{"org:members"}, a, "q")
		if err != nil || len(got) != c.found || !reflect.DeepEqual(st.searched, c.searched) {
			t.Errorf("hits %v: searched %q, found %d (%v); want %q and %d", c.hits, st.searched, len(got), err, c.searched, c.found)
		}
	}
}
