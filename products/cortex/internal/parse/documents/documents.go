// Package documents is the parser "documents": it posts an original to the document converter
// (docling-serve, POST /v1/convert/file) and maps the answer's json_content, a DoclingDocument of
// version 1, to the parsed document of package parse, in the reading order of its body and with
// the page of each element's own provenance. Its Markdown is not read: the page-break placeholder
// marks a change of page, not a page, so it misnumbers every page after one without content.
package documents

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/cortex/internal/parse"
)

// The scripts of Config.Script, each with the OCR preset and language measured for it.
const (
	Latin  = "latin"  // preset rapidocr, language en
	Arabic = "arabic" // preset tesseract, language ara: rapidocr returns Arabic in visual order
)

// pair is an OCR preset and language, or an input format and the extension it is detected by.
type pair struct{ a, b string }

var (
	ocr     = map[string]pair{Latin: {"rapidocr", "en"}, Arabic: {"tesseract", "ara"}}
	formats = map[string]pair{"application/pdf": {"pdf", "pdf"}, "image/png": {"image", "png"},
		"image/jpeg": {"image", "jpg"}, "image/tiff": {"image", "tiff"},
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {"docx", "docx"},
		"application/vnd.openxmlformats-officedocument.presentationml.presentation": {"pptx", "pptx"},
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {"xlsx", "xlsx"}}
	maxAnswer int64 = 256 << 20 // the largest answer read, in bytes
	maxDepth        = 64        // the nesting of the document
	maxCells        = 1 << 20   // the cells of one table
	client          = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// ErrAnswer is returned when the converter did not convert the original or its answer is not a
	// document this parser reads.
	ErrAnswer = errors.New("documents: the converter gave no document this parser reads")
)

// DefaultTimeout bounds one conversion. It is below the 30-minute lease of a job, so a call that
// hangs ends before another claim can take the same job.
const DefaultTimeout = 25 * time.Minute

// Config configures a Parser. URL is the converter's base address: http or https and a host, no
// path, query or user. Script is Latin or Arabic; "" is Latin. Timeout 0 is DefaultTimeout.
type Config struct {
	URL, Script string
	Timeout     time.Duration
}

// Parser posts an original to the converter, following no redirect, and maps its answer.
type Parser struct {
	endpoint string
	ocr      pair
	timeout  time.Duration
}

// New returns a Parser, or an error for a configuration it refuses.
func New(c Config) (*Parser, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("documents: the converter address %q is not http(s)://host[:port]", c.URL)
	}
	o, ok := ocr[cmp.Or(c.Script, Latin)]
	if !ok || c.Timeout < 0 {
		return nil, fmt.Errorf("documents: the script %q or the timeout %s is refused", c.Script, c.Timeout)
	}
	return &Parser{u.Scheme + "://" + u.Host + "/v1/convert/file", o, cmp.Or(c.Timeout, DefaultTimeout)}, nil
}

// Parse converts content of a media type the parser reads. Another type returns
// parse.ErrMediaType and sends nothing.
func (p *Parser) Parse(ctx context.Context, mediaType string, content io.Reader) (parse.Document, error) {
	f, ok := formats[mediaType]
	if !ok {
		return parse.Document{}, parse.ErrMediaType
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	mw := multipart.NewWriter(pw)
	go func() { pw.CloseWithError(p.form(mw, mediaType, f, content)) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, pr)
	if err != nil {
		return parse.Document{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return parse.Document{}, fmt.Errorf("documents: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var answer struct {
		Status   string
		Document struct {
			JSON json.RawMessage `json:"json_content"`
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	switch {
	case resp.StatusCode != http.StatusOK:
		return parse.Document{}, fmt.Errorf("%w: the converter answered %d", ErrAnswer, resp.StatusCode)
	case err != nil:
		return parse.Document{}, fmt.Errorf("documents: %w", err)
	case int64(len(body)) > maxAnswer || json.Unmarshal(body, &answer) != nil:
		return parse.Document{}, fmt.Errorf("%w: %d bytes that are not its JSON", ErrAnswer, len(body))
	case answer.Status != "success" && answer.Status != "partial_success":
		return parse.Document{}, fmt.Errorf("%w: status %q", ErrAnswer, answer.Status)
	}
	return mapDocument(answer.Document.JSON)
}

// form writes the request: the options, then the original as the part files.
func (p *Parser) form(mw *multipart.Writer, mediaType string, f pair, content io.Reader) error {
	for _, kv := range []pair{{"from_formats", f.a}, {"to_formats", "json"}, {"do_ocr", "true"},
		{"force_ocr", "false"}, {"ocr_preset", p.ocr.a}, {"ocr_lang", p.ocr.b}, {"image_export_mode", "placeholder"},
		{"include_images", "false"}, {"abort_on_error", "false"}} {
		if err := mw.WriteField(kv.a, kv.b); err != nil {
			return err
		}
	}
	w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {mediaType},
		"Content-Disposition": {`form-data; name="files"; filename="original.` + f.b + `"`}})
	if err == nil {
		_, err = io.Copy(w, content)
	}
	return cmp.Or(err, mw.Close())
}

type ref struct {
	Ref string `json:"$ref"`
}

// item is a node of a DoclingDocument: a group, a text, a table (with Data) or a picture.
type item struct {
	Label, Text        string
	Level              int
	Enumerated         bool
	ContentLayer       string `json:"content_layer"`
	Children, Captions []ref
	Prov               []struct {
		Page int `json:"page_no"`
	}
	Data *struct {
		Rows  int `json:"num_rows"`
		Cols  int `json:"num_cols"`
		Cells []struct {
			Text string
			Row0 int `json:"start_row_offset_idx"`
			Row1 int `json:"end_row_offset_idx"`
			Col0 int `json:"start_col_offset_idx"`
			Col1 int `json:"end_col_offset_idx"`
		} `json:"table_cells"`
	}
}

type docling struct {
	Schema                          string `json:"schema_name"`
	Version                         string
	Body                            item
	Groups, Texts, Tables, Pictures []item
	FieldRegions                    []item `json:"field_regions"`
	FieldItems                      []item `json:"field_items"`
}

type mapper struct {
	colls map[string][]item
	owned map[string]bool // captions: written with their table or picture
	seen  map[string]bool
	doc   parse.Document
	err   error // the first fault of the answer: the walk stops at it
}

func mapDocument(raw json.RawMessage) (parse.Document, error) {
	var d docling
	if err := json.Unmarshal(raw, &d); err != nil || d.Schema != "DoclingDocument" || !strings.HasPrefix(d.Version, "1.") {
		return parse.Document{}, fmt.Errorf("%w: schema %q, version %q, %v", ErrAnswer, d.Schema, d.Version, err)
	}
	m := &mapper{colls: map[string][]item{"groups": d.Groups, "texts": d.Texts, "tables": d.Tables, "pictures": d.Pictures,
		"field_regions": d.FieldRegions, "field_items": d.FieldItems}, owned: map[string]bool{}, seen: map[string]bool{},
		doc: parse.Document{Schema: parse.Schema, Elements: []parse.Element{}}}
	for _, f := range slices.Concat(d.Tables, d.Pictures) {
		for _, r := range f.Captions {
			m.owned[r.Ref] = true
		}
	}
	if m.walk(d.Body.Children, 0); m.err != nil {
		return parse.Document{}, m.err
	}
	return m.doc, nil
}

func (m *mapper) fail(format string, args ...any) {
	m.err = cmp.Or(m.err, fmt.Errorf("%w: "+format, append([]any{ErrAnswer}, args...)...))
}

// deep records a fault when the walk nests deeper than maxDepth, and reports whether it stops.
func (m *mapper) deep(depth int) bool {
	if depth > maxDepth {
		m.fail("it nests deeper than %d", maxDepth)
	}
	return m.err != nil
}

// resolve returns what a reference names; nil for key-value and form items, which make no element.
func (m *mapper) resolve(r string) (string, *item) {
	coll, n, _ := strings.Cut(strings.TrimPrefix(r, "#/"), "/")
	i, err := strconv.Atoi(n)
	list, known := m.colls[coll]
	skip := coll == "key_value_items" || coll == "form_items"
	if !strings.HasPrefix(r, "#/") || err != nil || i < 0 || (!skip && (!known || i >= len(list))) {
		m.fail("the reference %q names nothing", r)
	}
	if skip || m.err != nil {
		return "", nil
	}
	return coll, &list[i]
}

// take resolves a reference of the walk: nil for one met before or outside the body layer.
func (m *mapper) take(r string) (string, *item) {
	if m.seen[r] {
		return "", nil
	}
	m.seen[r] = true
	if coll, it := m.resolve(r); it != nil && cmp.Or(it.ContentLayer, "body") == "body" {
		return coll, it
	}
	return "", nil
}

func (m *mapper) walk(refs []ref, depth int) {
	for _, r := range refs {
		if m.deep(depth) {
			return
		}
		switch coll, it := m.take(r.Ref); {
		case it == nil:
		case coll == "groups" && (it.Label == "list" || it.Label == "ordered_list"):
			lines, page := []string{}, 0
			m.list(it, 0, depth+1, &lines, &page)
			m.add("list", 0, page, strings.Join(lines, "\n"))
		case coll == "groups" && it.Label == "inline":
			text, page := m.flat(it, depth+1)
			m.add("paragraph", 0, page, text)
		case coll == "texts":
			m.text(r.Ref, it)
			m.walk(it.Children, depth+1)
		case coll == "tables" || coll == "pictures":
			m.floating(it, depth)
		default: // other groups, field regions and field items hold content
			m.walk(it.Children, depth+1)
		}
	}
}

func (m *mapper) text(r string, it *item) {
	text, page := strings.TrimSpace(it.Text), pageOf(it)
	switch {
	case m.owned[r] || it.Label == "page_header" || it.Label == "page_footer":
	case it.Label == "title":
		m.doc.Title = cmp.Or(m.doc.Title, text)
		m.add("heading", 1, page, text)
	case it.Label == "section_header":
		m.add("heading", min(max(it.Level, 1)+1, 6), page, text)
	case it.Label == "list_item" && text != "":
		m.add("list", 0, page, "- "+line(text))
	default:
		m.add("paragraph", 0, page, text)
	}
}

// list writes a line per item; a group in it, or a list under an item, nests two spaces deeper. A
// table or picture in it ends the list so far, so it keeps its place between the items around it.
func (m *mapper) list(g *item, indent, depth int, lines *[]string, page *int) {
	n := 0
	for _, c := range g.Children {
		if m.deep(depth) {
			return
		}
		switch coll, it := m.take(c.Ref); {
		case it == nil:
		case coll == "tables" || coll == "pictures":
			m.add("list", 0, *page, strings.Join(*lines, "\n"))
			*lines, *page = nil, 0
			m.floating(it, depth)
		case coll == "groups" && it.Label != "inline":
			m.list(it, indent+1, depth+1, lines, page)
		default:
			text, p := line(it.Text), pageOf(it)
			if coll == "groups" {
				text, p = m.flat(it, depth+1)
			}
			if text != "" {
				n++
				marker := "-"
				if g.Label == "ordered_list" || it.Enumerated {
					marker = strconv.Itoa(n) + "."
				}
				*lines = append(*lines, strings.Repeat("  ", indent)+marker+" "+text)
				*page = cmp.Or(*page, p)
			}
			if coll != "groups" {
				m.list(it, indent, depth+1, lines, page)
			}
		}
	}
}

// flat returns the text of an item and of all under it on one line, and the first page among them.
func (m *mapper) flat(it *item, depth int) (string, int) {
	text, page := it.Text, pageOf(it)
	for _, c := range it.Children {
		if m.deep(depth) {
			return "", 0
		}
		if _, ci := m.take(c.Ref); ci != nil {
			t, p := m.flat(ci, depth+1)
			text, page = text+" "+t, cmp.Or(page, p)
		}
	}
	return line(text), page
}

// floating writes a picture (captions as its text) or a table (captions, table, then its footnotes).
func (m *mapper) floating(it *item, depth int) {
	var captions []string
	for _, r := range it.Captions {
		if _, c := m.resolve(r.Ref); c != nil && strings.TrimSpace(c.Text) != "" {
			captions = append(captions, strings.TrimSpace(c.Text))
			if it.Data != nil {
				m.add("paragraph", 0, pageOf(c), strings.TrimSpace(c.Text))
			}
		}
	}
	if it.Data == nil {
		m.add("figure", 0, pageOf(it), strings.Join(captions, "\n"))
		return
	}
	m.add("table", 0, pageOf(it), m.markdown(it))
	m.walk(it.Children, depth+1)
}

// add appends an element; an element without text is not written.
func (m *mapper) add(kind string, level, page int, text string) {
	if strings.TrimSpace(text) != "" {
		m.doc.Elements = append(m.doc.Elements, parse.Element{Type: kind, Level: level, Page: page, Text: text})
	}
}

func pageOf(it *item) int {
	if len(it.Prov) > 0 {
		return max(it.Prov[0].Page, 0)
	}
	return 0
}

func line(text string) string { return strings.Join(strings.Fields(text), " ") }

// markdown writes a table as Markdown: the first row is the header; a spanning cell is repeated.
func (m *mapper) markdown(it *item) string {
	rows, cols := it.Data.Rows, it.Data.Cols
	switch {
	case rows <= 0 || cols <= 0:
		return ""
	case rows > maxCells/cols:
		m.fail("a table of %d by %d cells", rows, cols)
		return ""
	}
	grid := make([]string, rows*cols)
	for _, c := range it.Data.Cells {
		for i := max(c.Row0, 0); i < min(c.Row1, rows); i++ {
			for j := max(c.Col0, 0); j < min(c.Col1, cols); j++ {
				grid[i*cols+j] = strings.ReplaceAll(line(c.Text), "|", `\|`)
			}
		}
	}
	out := []string{}
	for i := range rows {
		out = append(out, "| "+strings.Join(grid[i*cols:(i+1)*cols], " | ")+" |")
		if i == 0 {
			out = append(out, "|"+strings.Repeat("---|", cols))
		}
	}
	return strings.Join(out, "\n")
}
