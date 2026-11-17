// Package agentguard keeps text from outside the platform from steering an agent, and keeps
// secrets and foreign links out of what an agent reads and what a tool answers.
//
//   - Inert makes untrusted text inert: it breaks every "[[" with a zero-width space, and it
//     puts "> " before every line that starts with a role label.
//   - Redact replaces secrets with [REDACTED].
//   - Links replaces every link whose host is not on the allow-list with [link removed].
//   - Clean applies Links, Redact and Inert, in this order, to one string. CleanValue applies
//     Clean to every string of a decoded JSON value.
//   - Fit and FitList keep an answer within MaxAnswer characters (fit.go).
//   - Budget counts the turns of an agent per key and window, and fails closed (budget.go).
//
// Text that holds no attack passes unchanged. testdata/corpus.json proves both sides: each attack
// case becomes its expected text; each benign case, such as a question that names an action,
// comes back as it went in.
package agentguard

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The texts the guard writes.
const (
	ZeroWidthSpace = "\u200b"         // Inert puts it between two opening brackets
	Redacted       = "[REDACTED]"     // Redact puts it in place of a secret
	LinkRemoved    = "[link removed]" // Links puts it in place of a link whose host is not allowed
)

// roleLabel matches the start of a line that names a role: blanks or invisible characters
// (U+00A0, U+200B, U+2060, U+FEFF), then "system", "assistant" or "user" with optional blanks
// and a colon, or "tool result" as a word. Case is ignored.
var roleLabel = regexp.MustCompile(`(?i)^[ \t\x{00a0}\x{200b}\x{2060}\x{feff}]*(?:(?:system|assistant|user)[ \t]*:|tool[ \t]+result\b)`)

// lineEnd reports whether r ends a line: line feed, carriage return, next line (U+0085),
// line separator (U+2028) or paragraph separator (U+2029).
func lineEnd(r rune) bool {
	switch r {
	case '\n', '\r', '\u0085', '\u2028', '\u2029':
		return true
	}
	return false
}

// Inert breaks every "[[" with a zero-width space, so "[[[" becomes "[", ZWSP, "[", ZWSP, "[",
// and puts "> " before every line that starts with a role label. Every other byte is kept.
func Inert(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 8)
	start, prev := true, rune(0)
	for i := 0; i < len(text); {
		r, n := utf8.DecodeRuneInString(text[i:])
		if start && roleLabel.MatchString(text[i:]) {
			b.WriteString("> ")
		}
		if r == '[' && prev == '[' {
			b.WriteString(ZeroWidthSpace)
		}
		b.WriteString(text[i : i+n])
		prev = r
		start = lineEnd(r)
		i += n
	}
	return b.String()
}

var (
	pemBlock  = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`)
	authValue = regexp.MustCompile(`(?i)\b(bearer|basic)([ \t]+)([A-Za-z0-9._~+/=-]{16,})`)
	jwtValue  = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	userinfo  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s:/@]+):[^\s/@]+@`)
	keyValue  = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?)["']?[ \t]*[:=][ \t]*)("[^"\n]*"|'[^'\n]*'|[^\s"',;&]+)`)
	flagValue = regexp.MustCompile(`(?i)((?:^|\s)--?(?:password|passwd|secret|token|api-key)[ \t]+)([^\s-]\S*)`)
)

// Redact replaces secrets with [REDACTED], in this order:
//  1. a PEM private key block, from its BEGIN line to its END line or to the end of the text;
//  2. the value after "Bearer" or "Basic" when it has at least 16 characters, not all letters;
//  3. a JSON web token (three dot-separated parts, the first starting with "eyJ");
//  4. the password of a URL ("scheme://user:password@host");
//  5. the value after a key that ends in password, passwd, secret, token, api key, access key,
//     private key or credential(s), and then ":" or "="; quotes around the value are kept;
//  6. the value after a command-line flag --password, --passwd, --secret, --token or --api-key.
func Redact(text string) string {
	text = pemBlock.ReplaceAllString(text, Redacted)
	text = replace(authValue, text, func(s []string) string {
		if strings.Trim(s[3], "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz") == "" {
			return s[0] // letters only: a word, not a credential
		}
		return s[1] + s[2] + Redacted
	})
	text = jwtValue.ReplaceAllString(text, Redacted)
	text = userinfo.ReplaceAllString(text, "${1}:"+Redacted+"@")
	text = replace(keyValue, text, func(s []string) string {
		v := s[2]
		if q := v[0]; (q == '"' || q == '\'') && len(v) >= 2 {
			return s[1] + string(q) + Redacted + string(q)
		}
		return s[1] + Redacted
	})
	return flagValue.ReplaceAllString(text, "${1}"+Redacted)
}

var (
	linkURL      = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://(?:[^\s<>"'()\[\]{}]|\[REDACTED\])+`)
	linkWWW      = regexp.MustCompile(`(?i)\bwww\.[^\s<>"'()\[\]{}]+`)
	linkRelative = regexp.MustCompile(`(^|[\s(<"'=])(//[^\s<>"'()\[\]{}/][^\s<>"'()\[\]{}]*)`)
	linkScript   = regexp.MustCompile(`(?i)\b(?:(?:javascript|vbscript):|data:[a-z]+/[a-z0-9.+-]+[;,])[^\s<>"'()\[\]{}]*`)
)

// Links replaces every link whose host is not allowed with [link removed]. A link is a
// "scheme://..." address, a "www." address, a "//host" address after a blank, a bracket, a
// quote or "=", and any "javascript:", "vbscript:" or "data:<type>/<subtype>" address. A link
// is kept only when its scheme is http or https and its host, without port and ignoring case,
// equals an entry of hosts; an entry "*.example.org" allows every host below example.org, not
// example.org itself. With no hosts every link is removed.
func Links(text string, hosts []string) string {
	text = linkURL.ReplaceAllStringFunc(text, func(m string) string { return keep(m, m, hosts) })
	text = linkWWW.ReplaceAllStringFunc(text, func(m string) string { return keep(m, "http://"+m, hosts) })
	text = replace(linkRelative, text, func(s []string) string { return s[1] + keep(s[2], "https:"+s[2], hosts) })
	return linkScript.ReplaceAllString(text, LinkRemoved)
}

// keep returns link when the address raw has an allowed scheme and host, else LinkRemoved.
func keep(link, raw string, hosts []string) string {
	u, err := url.Parse(strings.ReplaceAll(raw, Redacted, "x")) // a link that Redact has already cleaned
	if err != nil {
		return LinkRemoved
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return LinkRemoved
	}
	h := strings.ToLower(u.Hostname())
	if h == "" {
		return LinkRemoved
	}
	for _, a := range hosts {
		a = strings.ToLower(a)
		if suffix, ok := strings.CutPrefix(a, "*"); ok {
			if strings.HasPrefix(suffix, ".") && len(h) > len(suffix) && strings.HasSuffix(h, suffix) {
				return link
			}
		} else if h == a {
			return link
		}
	}
	return LinkRemoved
}

// replace puts f(submatches) in place of every match of re in text.
func replace(re *regexp.Regexp, text string, f func(s []string) string) string {
	var b strings.Builder
	last := 0
	for _, ix := range re.FindAllStringSubmatchIndex(text, -1) {
		s := make([]string, len(ix)/2)
		for g := range s {
			if ix[2*g] >= 0 {
				s[g] = text[ix[2*g]:ix[2*g+1]]
			}
		}
		b.WriteString(text[last:ix[0]])
		b.WriteString(f(s))
		last = ix[1]
	}
	b.WriteString(text[last:])
	return b.String()
}

// Clean applies Links, Redact and Inert, in this order, to text from outside the platform.
func Clean(text string, hosts []string) string {
	return Inert(Redact(Links(text, hosts)))
}

// CleanValue returns a copy of v, a value decoded from JSON, in which every string, at any
// depth of maps and slices, has passed Clean. Keys and other values are copied unchanged.
func CleanValue(v any, hosts []string) any {
	switch t := v.(type) {
	case string:
		return Clean(t, hosts)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = CleanValue(e, hosts)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = CleanValue(e, hosts)
		}
		return out
	}
	return v
}
