// Package fit makes a tool answer safe to put in front of an agent (AIP-151).
// Every answer of the tool server is fitted and made inert before an agent
// reads it:
//
//   - Redact replaces secrets: a compact JWT, a Bearer value of 16 or more
//     characters, a private-key block.
//   - Inert makes free text inert: "[[" is broken with a zero-width space, and
//     a line that starts with a role label (System:, Assistant:, User:,
//     Tool result) is prefixed with "> ".
//   - Fit keeps a text within a number of characters: its first and its newest
//     lines, and how many were dropped.
//   - Answer does all of it to a tool result: every string leaf is redacted
//     and made inert, and above MaxAnswer characters each list keeps its first
//     and its newest entries. The answer stays valid JSON and numbers keep
//     their exact digits.
package fit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxAnswer is the most characters a tool answer may have.
const MaxAnswer = 24000

// zeroWidthSpace breaks "[[" so that no "[[" reaches an agent.
const zeroWidthSpace = "\u200b"

// roleLabels are the line starts an agent could read as a turn of its
// conversation. They are matched in any letter case.
var roleLabels = []string{"system:", "assistant:", "user:", "tool result"}

var (
	privateKeyBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY[A-Z ]*-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY[A-Z ]*-----|\z)`)
	compactJWT      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	bearerValue     = regexp.MustCompile(`(?i)(\bbearer[ \t]+)[A-Za-z0-9._~+/-]{16,}=*`)
)

// Redact replaces a private-key block (also one cut before its END line) with
// "[redacted private key]", a compact JWT with "[redacted jwt]" and a Bearer
// value of 16 or more characters with "[redacted]".
func Redact(text string) string {
	text = privateKeyBlock.ReplaceAllLiteralString(text, "[redacted private key]")
	text = compactJWT.ReplaceAllLiteralString(text, "[redacted jwt]")
	return bearerValue.ReplaceAllString(text, "${1}[redacted]")
}

// Inert breaks every "[[" with a zero-width space and prefixes with "> " each
// line that starts, after spaces or tabs, with a role label. Inert(Inert(s))
// equals Inert(s).
func Inert(text string) string {
	var b strings.Builder
	b.Grow(len(text) + len(text)/16)
	lineStart, prev := true, rune(0)
	for i, c := range text {
		if lineStart {
			if hasRoleLabel(strings.TrimLeft(text[i:], " \t")) {
				b.WriteString("> ")
			}
			lineStart = false
		}
		if c == '[' && prev == '[' {
			b.WriteString(zeroWidthSpace)
		}
		b.WriteRune(c)
		prev = c
		switch c {
		case '\n', '\r', '\u0085', ' ', ' ':
			lineStart = true
		}
	}
	return b.String()
}

func hasRoleLabel(s string) bool {
	for _, l := range roleLabels {
		if len(s) >= len(l) && strings.EqualFold(s[:len(l)], l) {
			return true
		}
	}
	return false
}

// Fit returns text unchanged when it has at most max characters. A longer
// text keeps its first and its newest lines with the line
// "[... N of M lines dropped ...]" between them. When not even its first and
// its newest line fit, it keeps its first and its last characters with
// " [... N of M characters dropped ...] " between them. The result never has
// more than max characters; a max below 1 gives "".
func Fit(text string, max int) string {
	if max < 1 {
		return ""
	}
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	if s, ok := fitLines(text, max); ok {
		return s
	}
	return fitChars(text, max)
}

func fitLines(text string, max int) (string, bool) {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	m := len(lines)
	if m < 3 {
		return "", false
	}
	budget := max - utf8.RuneCountInString(lineMarker(m, m))
	head, tail, used := 0, 0, 0
	for head+tail < m-1 {
		next := lines[head]
		if tail < head {
			next = lines[m-1-tail]
		}
		n := utf8.RuneCountInString(next)
		if used+n > budget {
			break
		}
		used += n
		if tail < head {
			tail++
		} else {
			head++
		}
	}
	if head == 0 || tail == 0 {
		return "", false
	}
	return strings.Join(lines[:head], "") + lineMarker(m-head-tail, m) + strings.Join(lines[m-tail:], ""), true
}

func lineMarker(n, m int) string { return fmt.Sprintf("[... %d of %d lines dropped ...]\n", n, m) }

func fitChars(text string, max int) string {
	r := []rune(text)
	m := len(r)
	budget := max - utf8.RuneCountInString(charMarker(m, m))
	if budget < 2 {
		return string(r[:max])
	}
	head, tail := (budget+1)/2, budget/2
	return string(r[:head]) + charMarker(m-head-tail, m) + string(r[m-tail:])
}

func charMarker(n, m int) string { return fmt.Sprintf(" [... %d of %d characters dropped ...] ", n, m) }

// Answer returns v as the JSON text of a tool answer. Every string leaf is
// redacted (Redact) and then made inert (Inert). When the text has more than
// MaxAnswer characters, each list keeps its first and its newest entries,
// as many as fit, with the entry "[... N of M entries dropped ...]" between
// them; if that is not enough, the longest strings are fitted (Fit) as well,
// and an answer that still does not fit becomes one JSON string that holds
// the fitted text. The text is always valid JSON, numbers keep their exact
// digits, and it never has more than MaxAnswer characters.
func Answer(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return "", err
	}
	tree = rebuild(tree, 0, func(s string) string { return Inert(Redact(s)) })
	text, err := encode(tree)
	if err != nil || utf8.RuneCountInString(text) <= MaxAnswer {
		return text, err
	}
	longestList, longestString := measure(tree)
	if longestList > 2 {
		if s, ok := largest(2, longestList-1, func(k int) any { return rebuild(tree, k, nil) }); ok {
			return s, nil
		}
	}
	if s, ok := largest(1, longestString-1, func(n int) any {
		return rebuild(tree, 2, func(s string) string { return Fit(s, n) })
	}); ok {
		return s, nil
	}
	s, _ := largest(0, MaxAnswer, func(n int) any { return Fit(text, n) })
	return s, nil
}

// rebuild copies a decoded JSON value. f, when not nil, maps every string
// leaf; keep, when above 0, cuts every list longer than keep to its first
// (keep+1)/2 and its newest keep/2 entries with the dropped-entries marker
// between them. The marker is not passed to f.
func rebuild(v any, keep int, f func(string) string) any {
	switch t := v.(type) {
	case string:
		if f != nil {
			return f(t)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = rebuild(e, keep, f)
		}
		return out
	case []any:
		if keep < 1 || len(t) <= keep {
			out := make([]any, len(t))
			for i, e := range t {
				out[i] = rebuild(e, keep, f)
			}
			return out
		}
		head, tail := (keep+1)/2, keep/2
		out := make([]any, 0, keep+1)
		for _, e := range t[:head] {
			out = append(out, rebuild(e, keep, f))
		}
		out = append(out, fmt.Sprintf("[... %d of %d entries dropped ...]", len(t)-head-tail, len(t)))
		for _, e := range t[len(t)-tail:] {
			out = append(out, rebuild(e, keep, f))
		}
		return out
	default:
		return v
	}
}

// measure returns the length of the longest list and the characters of the
// longest string in a decoded JSON value.
func measure(v any) (list, str int) {
	switch t := v.(type) {
	case string:
		return 0, utf8.RuneCountInString(t)
	case map[string]any:
		for _, e := range t {
			l, s := measure(e)
			list, str = maxInt(list, l), maxInt(str, s)
		}
	case []any:
		list = len(t)
		for _, e := range t {
			l, s := measure(e)
			list, str = maxInt(list, l), maxInt(str, s)
		}
	}
	return list, str
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// largest returns the encoding of build(n) for the largest n in [lo, hi]
// whose encoding has at most MaxAnswer characters.
func largest(lo, hi int, build func(int) any) (string, bool) {
	best, ok := "", false
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if s, err := encode(build(mid)); err == nil && utf8.RuneCountInString(s) <= MaxAnswer {
			best, ok, lo = s, true, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best, ok
}

func encode(v any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
