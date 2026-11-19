// Package chunk splits a parsed document into the chunks that are embedded and searched.
//
// It splits on headings first: the elements between two headings form a section. A section is
// then cut into windows of at most tokens tokens, each window but the first starting overlap
// tokens before the end of the one before it. A token is a run of characters without white
// space; the white space after it is kept, so a list or a table keeps its lines. Each chunk
// starts with one line that names the document's title and the path of headings above it,
// joined by " > ", and an empty line. The page of a chunk is the page of its first token.
package chunk

import (
	"regexp"
	"strings"

	"github.com/openova-io/openova/products/cortex/internal/parse"
)

// Piece is one chunk of a document, numbered from 0 in document order.
type Piece struct {
	Ordinal int
	Page    int
	Text    string
}

var token = regexp.MustCompile(`\S+\s*`)

type word struct {
	text string
	page int
}

// Split returns the chunks of doc. title is used when the parsed document has none. tokens is
// at least 1 and overlap is less than tokens; the store keeps both in their ranges.
func Split(doc parse.Document, title string, tokens, overlap int) []Piece {
	if doc.Title != "" {
		title = doc.Title
	}
	tokens = max(tokens, 1)
	if overlap < 0 || overlap >= tokens {
		overlap = 0
	}
	var out []Piece
	var path []string // the headings above, by level; "" for a level that is skipped
	var words []word
	flush := func() {
		head := []string{title}
		for i, h := range path {
			if h != "" && (i > 0 || h != title) {
				head = append(head, h)
			}
		}
		prefix := strings.Join(head, " > ") + "\n\n"
		for start := 0; start < len(words); start += tokens - overlap {
			end := min(start+tokens, len(words))
			var b strings.Builder
			b.WriteString(prefix)
			for _, w := range words[start:end] {
				b.WriteString(w.text)
			}
			out = append(out, Piece{Ordinal: len(out), Page: words[start].page, Text: strings.TrimSpace(b.String())})
			if end == len(words) {
				break
			}
		}
		words = nil
	}
	for _, el := range doc.Elements {
		if el.Type == "heading" && el.Level >= 1 && el.Level <= 6 {
			flush()
			for len(path) < el.Level {
				path = append(path, "")
			}
			path = append(path[:el.Level-1], el.Text)
			continue
		}
		found := token.FindAllString(el.Text, -1)
		for i, t := range found {
			if i == len(found)-1 {
				t = strings.TrimRight(t, " \t\r\n") + "\n\n"
			}
			words = append(words, word{text: t, page: el.Page})
		}
	}
	flush()
	return out
}
