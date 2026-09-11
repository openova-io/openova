package pdf

import "strings"

// The core PDF fonts (Helvetica and friends) are single-byte, encoded
// WinAnsi / cp1252, so every string handed to fpdf must be cp1252 bytes.
// Encoding here — rather than through fpdf's UnicodeTranslatorFromDescriptor,
// which reads a `.map` file off disk — keeps the image a single static binary
// with no font directory to mount.
//
// A document in a script cp1252 cannot carry needs a real embedded TrueType
// font, which is the seam Options.FontFile opens; until one is configured such
// a rune is transliterated, and if it cannot be, replaced. It is never emitted
// raw: a stray high byte would render as mojibake on the customer's invoice.

// cp1252High maps the runes that live in the 0x80-0x9F window of cp1252 —
// the typographic punctuation an invoice actually uses (en dash, curly
// quotes, ellipsis, euro sign).
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84,
	'…': 0x85, '†': 0x86, '‡': 0x87, 'ˆ': 0x88,
	'‰': 0x89, 'Š': 0x8A, '‹': 0x8B, 'Œ': 0x8C,
	'Ž': 0x8E, '‘': 0x91, '’': 0x92, '“': 0x93,
	'”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B,
	'œ': 0x9C, 'ž': 0x9E, 'Ÿ': 0x9F,
}

// transliterate covers the few runes worth approximating rather than losing:
// the exotic spaces a pasted address carries, the minus signs a spreadsheet
// emits, and the soft hyphen.
var transliterate = map[rune]string{
	' ': " ", ' ': " ", ' ': " ", ' ': " ",
	'−': "-", '‑': "-", '­': "",
	'⁄': "/",
}

// encode converts a UTF-8 string to cp1252 bytes carried in a Go string, the
// form fpdf writes verbatim into the content stream. Control characters other
// than newline are dropped; an unrepresentable rune becomes "?" so the gap is
// visible rather than silent.
func encode(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteByte('\n')
		case r == '\t':
			b.WriteString("    ")
		case r < 0x20 || r == 0x7F:
			// dropped: a control byte in a content stream is never wanted
		case r < 0x80:
			b.WriteByte(byte(r))
		default:
			if sub, ok := transliterate[r]; ok {
				b.WriteString(sub)
				continue
			}
			if c, ok := cp1252High[r]; ok {
				b.WriteByte(c)
				continue
			}
			if r >= 0xA0 && r <= 0xFF {
				b.WriteByte(byte(r))
				continue
			}
			b.WriteByte('?')
		}
	}
	return b.String()
}
