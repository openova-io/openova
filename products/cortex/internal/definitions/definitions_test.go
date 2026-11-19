package definitions

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/cortex/internal/store"
)

const doc = `models: {embed: {alias: embed-default, dimensions: 1024}, chat: {alias: agent-fast}}
sources:
  - id: hr-policies
    title: HR policies
    connector: {type: upload}
    access: {readers: ["org:members"], managers: ["group:hr-admins"]}
  - id: board-papers
    title: Board papers
    connector: {type: upload}
    access: {readers: [], managers: ["user:secretary@acme.example"]}
`

func TestParse(t *testing.T) {
	defs, err := Parse([]byte(doc))
	if err != nil || len(defs) != 2 {
		t.Fatalf("got %+v, %v", defs, err)
	}
	if d := defs[0]; d.ID != "hr-policies" || d.Title != "HR policies" || !reflect.DeepEqual(d.Readers, []string{"org:members"}) ||
		!reflect.DeepEqual(d.Managers, []string{"group:hr-admins"}) || len(d.Digest) != 64 || d.Digest == defs[1].Digest {
		t.Fatalf("got %+v", defs)
	}
	// The same entries as JSON, keys in another order: the same digests.
	same, err := Parse([]byte(`{"sources":[{"access":{"managers":["group:hr-admins"],"readers":["org:members"]},"connector":{"type":"upload"},"title":"HR policies","id":"hr-policies"}]}`))
	if err != nil || same[0].Digest != defs[0].Digest {
		t.Fatalf("a change of layout changed the digest: %+v, %v", same, err)
	}
	changed, _ := Parse([]byte(strings.Replace(doc, "{type: upload}", "{type: upload}\n    chunk: {tokens: 256}", 1)))
	if changed[0].Digest == defs[0].Digest || changed[1].Digest != defs[1].Digest {
		t.Fatal("a change of one entry did not change its digest alone")
	}
	if defs, err := Parse([]byte("sources: []")); err != nil || len(defs) != 0 {
		t.Errorf("sources: []: %v, %v", defs, err)
	}
}

func TestParseRefuses(t *testing.T) {
	entry := func(s string) string {
		return "sources:\n  - " + strings.ReplaceAll(s, "\n", "\n    ") + "\n"
	}
	ok := "id: hr\ntitle: HR\nconnector: {type: upload}\naccess: {readers: [\"org:members\"], managers: [\"group:hr\"]}"
	var many strings.Builder
	many.WriteString("sources:\n")
	for i := 0; i <= MaxSources; i++ {
		fmt.Fprintf(&many, "  - {id: s%d, title: S, connector: {type: upload}, access: {readers: [], managers: [\"group:a\"]}}\n", i)
	}
	for what, d := range map[string]string{
		"a list":                   "- a",
		"an empty object":          "{}",
		"sources null":             "sources: null",
		"sources with no value":    "sources:",
		"models only":              "models: []",
		"a misspelt sources":       strings.Replace(entry(ok), "sources:", "source:", 1),
		"not YAML":                 "sources: [",
		"sources is an object":     "sources: {a: 1}",
		"an id of another form":    entry(strings.Replace(ok, "id: hr", "id: HR", 1)),
		"an id twice":              entry(ok) + "  - " + strings.ReplaceAll(ok, "\n", "\n    ") + "\n",
		"no title":                 entry(strings.Replace(ok, "title: HR", "title: \"\"", 1)),
		"a connector of no type":   entry(strings.Replace(ok, "{type: upload}", "{type: ftp}", 1)),
		"no connector":             entry(strings.Replace(ok, "connector: {type: upload}\n", "", 1)),
		"no readers":               entry(strings.Replace(ok, "readers: [\"org:members\"], ", "", 1)),
		"no manager":               entry(strings.Replace(ok, "[\"group:hr\"]", "[]", 1)),
		"org:members as manager":   entry(strings.Replace(ok, "group:hr\"]", "org:members\"]", 1)),
		"a reader with a space":    entry(strings.Replace(ok, "org:members", "group:a b", 1)),
		"a reader of another kind": entry(strings.Replace(ok, "org:members", "role:admin", 1)),
		"an agent of no org":       entry(strings.Replace(ok, "org:members", "agent:bot", 1)),
		"more than 500 sources":    many.String(),
	} {
		if defs, err := Parse([]byte(d)); err == nil {
			t.Errorf("%s: accepted %+v", what, defs)
		}
	}
	if _, err := Parse([]byte(entry(ok))); err != nil {
		t.Fatalf("the valid entry of this test was refused: %v", err)
	}
}

// applier records each version and fails while fail is above zero.
type applier struct {
	mu   sync.Mutex
	fail int
	got  chan []store.SourceDefinition
}

func (a *applier) ApplyDefinitions(_ context.Context, defs []store.SourceDefinition) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail > 0 {
		a.fail--
		return errors.New("the database is away")
	}
	a.got <- defs
	return nil
}

func TestLoader(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName, Namespace: "acme-cortex"}, Data: map[string]string{DataKey: doc}}
	client := fake.NewClientset(cm)
	a := &applier{fail: 2, got: make(chan []store.SourceDefinition, 10)}
	var mu sync.Mutex
	var lines []string
	l := NewLoader(client, "acme-cortex", ConfigMapName, a, func(f string, v ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(f, v...))
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- l.Run(ctx) }()
	wait := func(what string) []store.SourceDefinition {
		t.Helper()
		select {
		case defs := <-a.got:
			return defs
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: nothing was applied", what)
			return nil
		}
	}
	if defs := wait("a version that failed twice"); len(defs) != 2 {
		t.Fatalf("applied %+v", defs)
	}
	<-l.Applied()
	update := func(data map[string]string) {
		cm = cm.DeepCopy()
		cm.Data = data
		if _, err := client.CoreV1().ConfigMaps("acme-cortex").Update(context.Background(), cm, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	logged := func(want string) {
		t.Helper()
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
			mu.Lock()
			text := strings.Join(lines, "\n")
			mu.Unlock()
			if strings.Contains(text, want) {
				return
			}
		}
		t.Fatalf("no log line with %q", want)
	}
	update(map[string]string{DataKey: "sources: [{id: BAD}]"})
	logged("id does not match")
	update(map[string]string{"other": doc})
	logged("it has no key definitions.yaml")
	update(map[string]string{DataKey: "sources: []"})
	if defs := wait("the empty list after two refused versions"); len(defs) != 0 {
		t.Fatalf("applied %+v", defs)
	}
	if err := client.CoreV1().ConfigMaps("acme-cortex").Delete(context.Background(), ConfigMapName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	logged("was deleted; the sources in use stay")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case defs := <-a.got:
		t.Fatalf("a refused version or the deletion applied %+v", defs)
	default:
	}
	mu.Lock()
	defer mu.Unlock()
	text := strings.Join(lines, "\n")
	for _, want := range []string{"was not applied: the database is away; it is tried again", "refused ConfigMap acme-cortex/cortex-definitions",
		"it has no key definitions.yaml", "applied ConfigMap acme-cortex/cortex-definitions"} {
		if !strings.Contains(text, want) {
			t.Errorf("the log has no line with %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "HR policies") {
		t.Errorf("the log holds the document:\n%s", text)
	}
}

// TestParseChunkAndConnector reads the connector type and the chunk settings: a missing object
// or key takes the schema's default, null and values out of range are refused.
func TestParseChunkAndConnector(t *testing.T) {
	src := func(chunk string) string {
		return "sources:\n  - {id: hr, title: HR, connector: {type: git}, access: {readers: [], managers: [\"group:a\"]}" + chunk + "}\n"
	}
	for chunk, want := range map[string]store.ChunkSettings{"": {Tokens: 512, Overlap: 64}, ", chunk: {}": {Tokens: 512, Overlap: 64},
		", chunk: {tokens: 256}": {Tokens: 256, Overlap: 64}, ", chunk: {tokens: 2048, overlap: 0}": {Tokens: 2048, Overlap: 0}} {
		defs, err := Parse([]byte(src(chunk)))
		if err != nil || defs[0].Chunk != want || defs[0].Connector != "git" {
			t.Errorf("%q: %+v, %v; want %+v and connector git", chunk, defs, err, want)
		}
	}
	for _, chunk := range []string{", chunk: null", ", chunk: {tokens: null}", ", chunk: {overlap: null}", ", chunk: []", ", chunk: {tokens: 64}",
		", chunk: {tokens: 4096}", ", chunk: {tokens: 128, overlap: 128}", ", chunk: {overlap: 300}", ", chunk: {tokens: 12.5}", ", chunk: {size: 512}"} {
		if defs, err := Parse([]byte(src(chunk))); err == nil {
			t.Errorf("%q: accepted %+v", chunk, defs)
		}
	}
}
