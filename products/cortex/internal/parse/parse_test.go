package parse

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const markdown = "# Leave policy\r\n\nEmployees accrue leave\nmonthly.\n\n## Grades\n- A: 30 days\n- B: 25 days\n\n| Grade | Days |\n|---|---|\n| A | 30 |\n\n```\n# not a heading\n\n```\nlast \xff line ###\n"

func TestTextParsesMarkdown(t *testing.T) {
	doc, err := Text{}.Parse(context.Background(), "text/plain", strings.NewReader(markdown))
	if err != nil {
		t.Fatal(err)
	}
	want := Document{Schema: Schema, Title: "Leave policy", Elements: []Element{
		{Type: "heading", Level: 1, Text: "Leave policy"},
		{Type: "paragraph", Text: "Employees accrue leave\nmonthly."},
		{Type: "heading", Level: 2, Text: "Grades"},
		{Type: "list", Text: "- A: 30 days\n- B: 25 days"},
		{Type: "table", Text: "| Grade | Days |\n|---|---|\n| A | 30 |"},
		{Type: "paragraph", Text: "```\n# not a heading\n\n```"},
		{Type: "paragraph", Text: "last � line ###"},
	}}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got  %+v\nwant %+v", doc, want)
	}
}

func TestTextRefusesOtherTypes(t *testing.T) {
	if _, err := (Text{}).Parse(context.Background(), "application/pdf", strings.NewReader("%PDF")); !errors.Is(err, ErrMediaType) {
		t.Fatalf("a PDF: %v, want ErrMediaType", err)
	}
	doc, err := Text{}.Parse(context.Background(), "text/markdown", strings.NewReader("plain words, no heading"))
	if err != nil || doc.Title != "" || len(doc.Elements) != 1 || doc.Elements[0].Type != "paragraph" {
		t.Fatalf("got %+v, %v", doc, err)
	}
}
