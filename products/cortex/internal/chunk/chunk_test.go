package chunk

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/cortex/internal/parse"
)

func words(from, to int) string {
	var w []string
	for i := from; i <= to; i++ {
		w = append(w, fmt.Sprintf("w%d", i))
	}
	return strings.Join(w, " ")
}

func TestSplitOnHeadingsThenWindows(t *testing.T) {
	doc := parse.Document{Title: "Leave policy", Elements: []parse.Element{
		{Type: "heading", Level: 1, Text: "Leave policy"},
		{Type: "paragraph", Page: 1, Text: words(1, 4)},
		{Type: "heading", Level: 2, Text: "Grades"},
		{Type: "heading", Level: 3, Text: "Senior"},
		{Type: "paragraph", Page: 2, Text: words(1, 3)},
		{Type: "list", Page: 3, Text: "- a\n- b"},
		{Type: "heading", Level: 2, Text: "Carry-over"},
		{Type: "paragraph", Page: 4, Text: "x"},
	}}
	got := Split(doc, "unused", 4, 1)
	want := []Piece{
		{0, 1, "Leave policy\n\nw1 w2 w3 w4"},
		{1, 2, "Leave policy > Grades > Senior\n\nw1 w2 w3\n\n-"},
		{2, 3, "Leave policy > Grades > Senior\n\n- a\n- b"},
		{3, 4, "Leave policy > Carry-over\n\nx"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestSplitWindowsOverlap(t *testing.T) {
	doc := parse.Document{Elements: []parse.Element{{Type: "paragraph", Text: words(1, 10)}}}
	got := Split(doc, "Notes", 4, 2)
	var texts []string
	for _, p := range got {
		texts = append(texts, p.Text)
	}
	want := []string{"Notes\n\nw1 w2 w3 w4", "Notes\n\nw3 w4 w5 w6", "Notes\n\nw5 w6 w7 w8", "Notes\n\nw7 w8 w9 w10"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("got %q, want %q", texts, want)
	}
	if n := len(Split(parse.Document{Elements: []parse.Element{{Type: "heading", Level: 1, Text: "Only a heading"}}}, "T", 4, 2)); n != 0 {
		t.Errorf("a document of headings only gave %d chunks", n)
	}
	if n := len(Split(doc, "Notes", 4, 9)); n != 3 {
		t.Errorf("an overlap not less than the window gave %d chunks, want 3 without overlap", n)
	}
}
