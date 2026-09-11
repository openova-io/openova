package pdf

import (
	"bytes"
	"compress/zlib"
	"io"
	"regexp"
	"strings"
	"testing"
)

// A PDF's TEXT LAYER is what makes a rendered invoice a document rather than
// a picture of one: the customer can select the total, a mail gateway can
// index it, and an e-invoice pipeline can read it back. Asserting on the byte
// length only would pass on a blank page, so the tests here pull the text out
// of the content streams and assert on what the reader will actually see.
//
// This is a deliberately small reader — enough for the streams THIS package
// writes (Flate-compressed content, text in Tj / TJ operators, WinAnsi
// single-byte strings) and no more. It is test-only: nothing in the service
// parses a PDF.

var (
	streamRe = regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	tjRe     = regexp.MustCompile(`\(((?:[^()\\]|\\.)*)\)\s*Tj`)
	tjArrRe  = regexp.MustCompile(`\[((?:[^\[\]\\]|\\.)*)\]\s*TJ`)
	strRe    = regexp.MustCompile(`\(((?:[^()\\]|\\.)*)\)`)
)

// extractText returns every string drawn into the document, in order.
func extractText(t *testing.T, pdfBytes []byte) string {
	t.Helper()
	var out strings.Builder
	for _, m := range streamRe.FindAllSubmatch(pdfBytes, -1) {
		body := m[1]
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if inflated, err := io.ReadAll(r); err == nil {
				body = inflated
			}
			_ = r.Close()
		}
		for _, tm := range tjRe.FindAllSubmatch(body, -1) {
			out.WriteString(unescapePDF(string(tm[1])))
			out.WriteString("\n")
		}
		for _, am := range tjArrRe.FindAllSubmatch(body, -1) {
			for _, sm := range strRe.FindAllSubmatch(am[1], -1) {
				out.WriteString(unescapePDF(string(sm[1])))
			}
			out.WriteString("\n")
		}
	}
	return out.String()
}

// unescapePDF undoes the literal-string escaping fpdf applies and maps the
// cp1252 bytes back to UTF-8 so an assertion can be written in normal text.
func unescapePDF(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n':
				b = append(b, '\n')
			case 'r':
				b = append(b, '\r')
			case 't':
				b = append(b, '\t')
			default:
				b = append(b, s[i])
			}
			continue
		}
		b = append(b, s[i])
	}
	return decodeCP1252(b)
}

// decodeCP1252 is the inverse of encode(), for assertions only.
func decodeCP1252(b []byte) string {
	reverse := map[byte]rune{}
	for r, c := range cp1252High {
		reverse[c] = r
	}
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c >= 0xA0:
			sb.WriteRune(rune(c))
		default:
			if r, ok := reverse[c]; ok {
				sb.WriteRune(r)
			} else {
				sb.WriteRune('?')
			}
		}
	}
	return sb.String()
}

// The extractor is an absence-assertion tool, so prove it can go red: a PDF
// whose text it cannot see would let every golden check below pass on a blank
// page. A guard that has never been seen fail is not a guard.
func TestExtractorSeesTextAndMissesWhatIsAbsent(t *testing.T) {
	got := extractText(t, mustRenderFixture(t, "invoice"))
	if !strings.Contains(got, "INV-2026-00042") {
		t.Fatalf("the extractor did not find the invoice number; it cannot judge any other assertion.\n%s", got)
	}
	if strings.Contains(got, "CN-2026-00003") {
		t.Fatal("the extractor reported a string that is not in this document — it is matching noise")
	}
}
