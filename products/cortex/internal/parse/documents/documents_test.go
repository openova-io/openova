package documents

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/ingest"
	"github.com/openova-io/openova/products/cortex/internal/parse"
)

var _ ingest.Parser = (*Parser)(nil)

// converted is a DoclingDocument in the shape docling-core 2.104.0 writes: page 1 has a header in
// the furniture layer, a title and nested lists; page 2 a section, a captioned table with a footnote
// and an inline group; page 3 nothing; page 4 two pictures, a deep heading and a footnote.
const converted = `{"schema_name": "DoclingDocument", "version": "1.10.0", "name": "original",
 "body": {"self_ref": "#/body", "children": [{"$ref": "#/texts/0"}, {"$ref": "#/texts/1"}, {"$ref": "#/texts/2"},
  {"$ref": "#/groups/0"}, {"$ref": "#/texts/7"}, {"$ref": "#/texts/8"}, {"$ref": "#/tables/0"}, {"$ref": "#/groups/2"},
  {"$ref": "#/pictures/0"}, {"$ref": "#/pictures/1"}, {"$ref": "#/texts/12"}, {"$ref": "#/texts/13"},
  {"$ref": "#/texts/14"}, {"$ref": "#/key_value_items/0"}, {"$ref": "#/groups/3"}, {"$ref": "#/texts/15"}]},
 "groups": [{"label": "list", "children": [{"$ref": "#/texts/3"}, {"$ref": "#/texts/4"}]},
  {"label": "list", "children": [{"$ref": "#/texts/5"}, {"$ref": "#/texts/6"}]},
  {"label": "inline", "children": [{"$ref": "#/texts/9"}, {"$ref": "#/texts/10"}]}, {"label": "section", "children": [{"$ref": "#/groups/3"}]}],
 "texts": [{"label": "page_header", "content_layer": "furniture", "prov": [{"page_no": 1}], "text": "Internal"},
  {"label": "title", "prov": [{"page_no": 1}], "text": "Leave policy"}, {"label": "text", "prov": [{"page_no": 1}], "text": "Employees accrue leave monthly."},
  {"label": "list_item", "prov": [{"page_no": 1}], "text": "A: 30 days", "enumerated": false, "marker": "-"},
  {"label": "list_item", "prov": [{"page_no": 1}], "text": "B: 25\ndays", "children": [{"$ref": "#/groups/1"}]},
  {"label": "list_item", "prov": [{"page_no": 1}], "text": "first", "enumerated": true, "marker": "1."},
  {"label": "list_item", "prov": [{"page_no": 1}], "text": "second", "enumerated": true, "marker": "2."},
  {"label": "section_header", "level": 1, "prov": [{"page_no": 2}], "text": "Grades"}, {"label": "caption", "prov": [{"page_no": 2}], "text": "Table 1: Days per grade"},
  {"label": "text", "prov": [{"page_no": 2}], "text": "Pay is"}, {"label": "text", "prov": [{"page_no": 2}], "text": " monthly. "},
  {"label": "caption", "prov": [{"page_no": 4}], "text": "Figure 1: Organisation chart"},
  {"label": "section_header", "level": 9, "prov": [{"page_no": 4}], "text": "Annex"},
  {"label": "text", "content_layer": "furniture", "prov": [{"page_no": 4}], "text": "Page 4"},
  {"label": "text", "prov": [{"page_no": 4}], "text": "  "}, {"label": "footnote", "prov": [{"page_no": 4}], "text": "1 Unless agreed."},
  {"label": "footnote", "prov": [{"page_no": 2}], "text": "Days are working days."}],
 "tables": [{"label": "table", "prov": [{"page_no": 2}], "captions": [{"$ref": "#/texts/8"}],
  "children": [{"$ref": "#/texts/8"}, {"$ref": "#/texts/16"}], "data": {"num_rows": 3, "num_cols": 2, "table_cells": [
   {"text": "Grade", "start_row_offset_idx": 0, "end_row_offset_idx": 1, "start_col_offset_idx": 0, "end_col_offset_idx": 1},
   {"text": "Days", "start_row_offset_idx": 0, "end_row_offset_idx": 1, "start_col_offset_idx": 1, "end_col_offset_idx": 2},
   {"text": "A|B", "start_row_offset_idx": 1, "end_row_offset_idx": 2, "start_col_offset_idx": 0, "end_col_offset_idx": 1},
   {"text": "30", "start_row_offset_idx": 1, "end_row_offset_idx": 2, "start_col_offset_idx": 1, "end_col_offset_idx": 2},
   {"text": "Total", "start_row_offset_idx": 2, "end_row_offset_idx": 3, "start_col_offset_idx": 0, "end_col_offset_idx": 2}]}}],
 "pictures": [{"label": "picture", "prov": [{"page_no": 4}], "captions": [{"$ref": "#/texts/11"}], "children": [{"$ref": "#/texts/11"}]},
  {"label": "picture", "prov": [{"page_no": 4}]}],
 "key_value_items": [{"label": "key_value_region"}]}`

// converter is a fake converter: it records the request and answers with status and body.
type converter struct {
	url, file string // file: the name, media type and content of the part files
	calls     int
	fields    map[string][]string
}

func newConverter(t *testing.T, status int, body string) *converter {
	c := &converter{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/convert/file" || r.ParseMultipartForm(1<<20) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		c.fields = r.MultipartForm.Value
		if fh := r.MultipartForm.File["files"]; len(fh) == 1 {
			f, _ := fh[0].Open()
			b, _ := io.ReadAll(f)
			c.file = fh[0].Filename + " " + fh[0].Header.Get("Content-Type") + " " + string(b)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c.url = srv.URL
	return c
}

func answer(status, doc string) string {
	return `{"document": {"filename": "original.pdf", "md_content": null, "json_content": ` + doc + `}, "status": "` + status + `", "errors": []}`
}

func convert(t *testing.T, c Config, mediaType string) (parse.Document, error) {
	p, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return p.Parse(context.Background(), mediaType, strings.NewReader("body"))
}

func TestParseMapsTheConvertedDocument(t *testing.T) {
	doc, err := convert(t, Config{URL: newConverter(t, http.StatusOK, answer("success", converted)).url}, "application/pdf")
	want := parse.Document{Schema: parse.Schema, Title: "Leave policy", Elements: []parse.Element{
		{Type: "heading", Level: 1, Page: 1, Text: "Leave policy"},
		{Type: "paragraph", Page: 1, Text: "Employees accrue leave monthly."},
		{Type: "list", Page: 1, Text: "- A: 30 days\n- B: 25 days\n  1. first\n  2. second"},
		{Type: "heading", Level: 2, Page: 2, Text: "Grades"},
		{Type: "paragraph", Page: 2, Text: "Table 1: Days per grade"},
		{Type: "table", Page: 2, Text: "| Grade | Days |\n|---|---|\n| A\\|B | 30 |\n| Total | Total |"},
		{Type: "paragraph", Page: 2, Text: "Days are working days."},
		{Type: "paragraph", Page: 2, Text: "Pay is monthly."},
		{Type: "figure", Page: 4, Text: "Figure 1: Organisation chart"},
		{Type: "heading", Level: 6, Page: 4, Text: "Annex"},
		{Type: "paragraph", Page: 4, Text: "1 Unless agreed."},
	}}
	if err != nil || !reflect.DeepEqual(doc, want) {
		t.Fatalf("got  %+v, %v\nwant %+v", doc, err, want)
	}
}

func TestParseSendsTheOriginalWithTheMeasuredSettingsAndNothingElse(t *testing.T) {
	docx := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	for _, tc := range []struct{ script, mediaType, preset, lang, format, file string }{
		{"", "application/pdf", "rapidocr", "en", "pdf", "original.pdf application/pdf body"},
		{Latin, "image/jpeg", "rapidocr", "en", "image", "original.jpg image/jpeg body"},
		{Arabic, docx, "tesseract", "ara", "docx", "original.docx " + docx + " body"},
	} {
		c := newConverter(t, http.StatusOK, answer("partial_success", converted))
		if _, err := convert(t, Config{URL: c.url + "/", Script: tc.script}, tc.mediaType); err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{"from_formats": {tc.format}, "to_formats": {"json"}, "do_ocr": {"true"},
			"force_ocr": {"false"}, "ocr_preset": {tc.preset}, "ocr_lang": {tc.lang}, "image_export_mode": {"placeholder"},
			"include_images": {"false"}, "abort_on_error": {"false"}}
		if !reflect.DeepEqual(c.fields, want) || c.file != tc.file {
			t.Fatalf("script %q, %s: fields %v, file %q", tc.script, tc.mediaType, c.fields, c.file)
		}
	}
	c := newConverter(t, http.StatusOK, answer("success", converted))
	for _, mt := range []string{"text/plain", "text/markdown", "application/zip", "application/pdf; charset=binary"} {
		if _, err := convert(t, Config{URL: c.url}, mt); !errors.Is(err, parse.ErrMediaType) || c.calls != 0 {
			t.Fatalf("%s: %v after %d calls, want parse.ErrMediaType and no call", mt, err, c.calls)
		}
	}
}

func TestParseRefusesABadAnswer(t *testing.T) {
	doc := func(from, to string) string { return answer("success", strings.Replace(converted, from, to, 1)) }
	last := `{"$ref": "#/texts/15"}]}`
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"a server error", "{}", http.StatusInternalServerError, ErrAnswer},
		{"not JSON", "<html>", http.StatusOK, ErrAnswer},
		{"status failure", answer("failure", "null"), http.StatusOK, ErrAnswer},
		{"no document", answer("success", "null"), http.StatusOK, ErrAnswer},
		{"another schema", doc(`"DoclingDocument"`, `"Other"`), http.StatusOK, ErrAnswer},
		{"version 2", doc(`"1.10.0"`, `"2.0.0"`), http.StatusOK, ErrAnswer},
		{"a reference to nothing", doc(last, `{"$ref": "#/texts/17"}]}`), http.StatusOK, ErrAnswer},
		{"no collection", doc(last, `{"$ref": "#/textz/0"}]}`), http.StatusOK, ErrAnswer}, {"the body", doc(last, `{"$ref": "#/body"}]}`), http.StatusOK, ErrAnswer},
		{"a caption that names nothing", doc(`"#/texts/11"}]`, `"#/texts/99"}]`), http.StatusOK, ErrAnswer},
		{"a table too large", doc(`"num_rows": 3`, `"num_rows": 1048576`), http.StatusOK, ErrAnswer},
	} {
		_, err := convert(t, Config{URL: newConverter(t, tc.status, tc.body).url}, "application/pdf")
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	defer func(n int64, d int) { maxAnswer, maxDepth = n, d }(maxAnswer, maxDepth)
	for _, l := range []struct {
		answer, depth int
		want          string
	}{{64, 64, "bytes"}, {256 << 20, 2, "nests deeper"}} {
		maxAnswer, maxDepth = int64(l.answer), l.depth
		_, err := convert(t, Config{URL: newConverter(t, http.StatusOK, answer("success", converted)).url}, "application/pdf")
		if !errors.Is(err, ErrAnswer) || !strings.Contains(err.Error(), l.want) {
			t.Fatalf("limits %d and %d: %v", l.answer, l.depth, err)
		}
	}
}

func TestParseEndsAtItsTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	if _, err := convert(t, Config{URL: srv.URL, Timeout: 50 * time.Millisecond}, "application/pdf"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v, want the deadline", err)
	}
}

func TestNewRefusesABadConfig(t *testing.T) {
	for _, c := range []Config{{URL: "ftp://converter"}, {URL: "http://"}, {URL: "http://u:p@converter"},
		{URL: "http://converter/v1"}, {URL: "http://converter?x=1"}, {URL: "http://converter#x"}, {URL: "converter:5001"},
		{URL: "http://converter", Script: "cyrillic"}, {URL: "http://converter", Timeout: -time.Second}} {
		if _, err := New(c); err == nil {
			t.Fatalf("%+v was accepted", c)
		}
	}
	if p, err := New(Config{URL: "https://converter:5001"}); err != nil || p.timeout != DefaultTimeout {
		t.Fatalf("defaults: %+v, %v", p, err)
	}
}

// mapped converts a DoclingDocument of version 1.10.0 with the given body, groups, texts and tables.
func mapped(t *testing.T, body, groups, texts, tables string) parse.Document {
	doc := `{"schema_name": "DoclingDocument", "version": "1.10.0", "body": {"children": [` + body + `]}, "groups": [` +
		groups + `], "texts": [` + texts + `], "tables": [` + tables + `]}`
	got, err := convert(t, Config{URL: newConverter(t, http.StatusOK, answer("success", doc)).url}, "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func expect(t *testing.T, got parse.Document, elements ...parse.Element) {
	t.Helper()
	if w := (parse.Document{Schema: parse.Schema, Elements: elements}); !reflect.DeepEqual(got, w) {
		t.Fatalf("got  %+v\nwant %+v", got, w)
	}
}

func TestParseKeepsAListAroundATableInReadingOrder(t *testing.T) {
	got := mapped(t, `{"$ref": "#/groups/0"}`,
		`{"label": "list", "children": [{"$ref": "#/texts/0"}, {"$ref": "#/tables/0"}, {"$ref": "#/texts/1"}]}`,
		`{"label": "list_item", "prov": [{"page_no": 1}], "text": "item 1"}, {"label": "list_item", "prov": [{"page_no": 2}], "text": "item 2"}`,
		`{"label": "table", "prov": [{"page_no": 1}], "data": {"num_rows": 1, "num_cols": 1, "table_cells": [
		 {"text": "cell", "start_row_offset_idx": 0, "end_row_offset_idx": 1, "start_col_offset_idx": 0, "end_col_offset_idx": 1}]}}`)
	expect(t, got, parse.Element{Type: "list", Page: 1, Text: "- item 1"},
		parse.Element{Type: "table", Page: 1, Text: "| cell |\n|---|"},
		parse.Element{Type: "list", Page: 2, Text: "- item 2"})
}

func TestParseWalksIntoChapterAndSectionGroups(t *testing.T) {
	got := mapped(t, `{"$ref": "#/groups/0"}, {"$ref": "#/groups/1"}`,
		`{"label": "chapter", "children": [{"$ref": "#/texts/0"}]}, {"label": "section", "children": [{"$ref": "#/groups/2"}]},
		 {"label": "section", "children": [{"$ref": "#/texts/1"}]}`,
		`{"label": "text", "prov": [{"page_no": 1}], "text": "On the slide"}, {"label": "text", "prov": [{"page_no": 2}], "text": "In the sheet"}`, ``)
	expect(t, got, parse.Element{Type: "paragraph", Page: 1, Text: "On the slide"},
		parse.Element{Type: "paragraph", Page: 2, Text: "In the sheet"})
}

func TestParseDropsPageHeadersAndFootersOfTheBody(t *testing.T) {
	got := mapped(t, `{"$ref": "#/texts/0"}, {"$ref": "#/texts/1"}, {"$ref": "#/texts/2"}`, ``,
		`{"label": "page_header", "prov": [{"page_no": 1}], "text": "Internal"},
		 {"label": "text", "prov": [{"page_no": 1}], "text": "Leave accrues monthly."},
		 {"label": "page_footer", "content_layer": "body", "prov": [{"page_no": 1}], "text": "Page 1"}`, ``)
	expect(t, got, parse.Element{Type: "paragraph", Page: 1, Text: "Leave accrues monthly."})
}

func TestParseRefusesARedirect(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		moved := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/moved" {
				moved++
				_, _ = io.WriteString(w, answer("success", converted))
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			http.Redirect(w, r, "/moved", status)
		}))
		t.Cleanup(srv.Close)
		if _, err := convert(t, Config{URL: srv.URL}, "application/pdf"); !errors.Is(err, ErrAnswer) || moved != 0 {
			t.Fatalf("%d: %v after %d calls of the new address, want ErrAnswer and none", status, err, moved)
		}
	}
}
