// Package parse turns an original into the parsed document, the neutral form every parser
// writes and the chunker reads:
//
//	{"schema": "openova.io/parsed-document/v1", "title": "Leave policy", "language": "en",
//	 "elements": [{"type": "heading", "level": 1, "page": 1, "text": "Leave policy"}, ...]}
//
// An element's type is heading, paragraph, list, table or figure. level is set for a heading
// (1 to 6) only; page is absent for a format without pages; text is always set, and a table
// is Markdown. Text is the parser of plain text and Markdown, in process.
package parse

import (
	"context"
	"errors"
	"io"
	"regexp"
	"strings"
)

// Schema is the value of the field schema of a parsed document.
const Schema = "openova.io/parsed-document/v1"

// Document is a parsed document.
type Document struct {
	Schema   string    `json:"schema"`
	Title    string    `json:"title"`
	Language string    `json:"language,omitempty"`
	Elements []Element `json:"elements"`
}

// Element is one block of a parsed document.
type Element struct {
	Type  string `json:"type"`
	Level int    `json:"level,omitempty"`
	Page  int    `json:"page,omitempty"`
	Text  string `json:"text"`
}

// ErrMediaType is returned for a media type the parser does not read.
var ErrMediaType = errors.New("parse: the media type is not read by this parser")

var (
	heading  = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t#]*$`)
	listItem = regexp.MustCompile(`^[ \t]*([-*+]|[0-9]{1,9}[.)])[ \t]`)
)

// Text parses plain text and Markdown: a line that starts with one to six # is a heading, a
// block of lines that are all list items is a list, a block of lines that all start with | is a
// table, a fenced block is a paragraph kept as it is, and every other block of lines without an
// empty line is a paragraph. The title is the first heading of level 1. Bytes that are not
// UTF-8 are replaced.
type Text struct{}

// Parse reads content of the media type text/plain or text/markdown.
func (Text) Parse(_ context.Context, mediaType string, content io.Reader) (Document, error) {
	if mediaType != "text/plain" && mediaType != "text/markdown" {
		return Document{}, ErrMediaType
	}
	raw, err := io.ReadAll(content)
	if err != nil {
		return Document{}, err
	}
	text := strings.ReplaceAll(strings.ToValidUTF8(string(raw), "�"), "\r\n", "\n")
	doc := Document{Schema: Schema, Elements: []Element{}}
	var block []string
	fence := ""
	flush := func() {
		if len(block) > 0 {
			doc.Elements = append(doc.Elements, Element{Type: kind(block, fence != ""), Text: strings.Join(block, "\n")})
		}
		block = nil
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case fence != "":
			block = append(block, line)
			if strings.HasPrefix(trimmed, fence) {
				flush()
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			flush()
			fence = trimmed[:3]
			block = append(block, line)
		case trimmed == "":
			flush()
		case heading.MatchString(line):
			flush()
			m := heading.FindStringSubmatch(line)
			doc.Elements = append(doc.Elements, Element{Type: "heading", Level: len(m[1]), Text: m[2]})
			if doc.Title == "" && len(m[1]) == 1 {
				doc.Title = m[2]
			}
		default:
			block = append(block, line)
		}
	}
	flush()
	return doc, nil
}

// kind returns the type of a block of lines.
func kind(block []string, fenced bool) string {
	if fenced {
		return "paragraph"
	}
	list, table := true, true
	for _, l := range block {
		list = list && listItem.MatchString(l)
		table = table && strings.HasPrefix(strings.TrimSpace(l), "|")
	}
	switch {
	case table:
		return "table"
	case list:
		return "list"
	}
	return "paragraph"
}
