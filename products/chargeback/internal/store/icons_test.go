package store

import (
	"errors"
	"strings"
	"testing"
)

// ValidateIcon is the gate every icon passes before it is kept (DESIGN.md
// §22.10). Pure, so each refusal is pinned without a database.
func TestValidateIcon(t *testing.T) {
	ok := []struct{ name, ct, body string }{
		{"plain svg", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`},
		{"xml declaration and comment", IconSVG, `<?xml version="1.0" encoding="UTF-8"?><!-- a comment --><svg xmlns="http://www.w3.org/2000/svg"/>`},
		{"own #id", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><defs><path id="a" d="M0 0"/></defs><use xlink:href="#a"/></svg>`},
		{"inline style", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><style>path{fill:#000}</style><path d="M0 0"/></svg>`},
		{"opacity is not a handler", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><path opacity=".5" d="M0 0"/></svg>`},
		{"png", IconPNG, "\x89PNG\r\n\x1a\nrest"},
		{"webp", IconWebP, "RIFF\x00\x00\x00\x00WEBPVP8 "},
	}
	for _, c := range ok {
		if err := ValidateIcon(c.ct, []byte(c.body)); err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		}
	}
	bad := []struct{ name, ct, body, want string }{
		{"script", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><script>x</script></svg>`, "<script>"},
		{"SCRIPT upper-case", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><SCRIPT>x</SCRIPT></svg>`, "<script>"},
		{"onload", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg" onload="x()"/>`, "event handler"},
		{"foreignObject", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject/></svg>`, "foreignObject"},
		{"external href", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><image href="//cdn.example/x.png"/></svg>`, "links out"},
		{"external xlink:href", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="data:image/svg+xml;base64,AA"/></svg>`, "links out"},
		{"javascript in a style value", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><path style="background:url(javascript:x)"/></svg>`, "javascript:"},
		{"entity", IconSVG, `<!DOCTYPE svg [<!ENTITY a "b">]><svg xmlns="http://www.w3.org/2000/svg">&a;</svg>`, "ENTITY"},
		{"lower-case entity alone", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><!entity a "b"></svg>`, "ENTITY"},
		{"processing instruction", IconSVG, `<?xml-stylesheet href="x.css"?><svg xmlns="http://www.w3.org/2000/svg"/>`, "processing instruction"},
		{"two roots", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`, "one <svg> root"},
		{"html root", IconSVG, `<html/>`, "<svg> root"},
		{"text outside", IconSVG, `hello <svg xmlns="http://www.w3.org/2000/svg"/>`, "text outside"},
		{"truncated", IconSVG, `<svg xmlns="http://www.w3.org/2000/svg"><path`, "well-formed"},
		{"not utf-8", IconSVG, "<svg>\xff</svg>", "UTF-8"},
		{"empty", IconSVG, ``, "empty"},
		{"too large", IconSVG, `<svg>` + strings.Repeat(" ", IconMaxBytes) + `</svg>`, "64 KiB"},
		{"png that is an svg", IconPNG, `<svg/>`, "not a PNG"},
		{"webp that is a png", IconWebP, "\x89PNG\r\n\x1a\nrest", "WebP"},
		{"gif", "image/gif", "GIF89a", "image/svg+xml, image/png or image/webp"},
	}
	for _, c := range bad {
		err := ValidateIcon(c.ct, []byte(c.body))
		if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: = %v, want ErrInvalid naming %q", c.name, err, c.want)
		}
	}
}

func TestIconIDAndColour(t *testing.T) {
	if id := IconID([]byte("abc")); id != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || !ValidIconID(id) {
		t.Fatalf("IconID = %s", id)
	}
	if ValidIconID(strings.ToUpper(IconID([]byte("abc")))) {
		t.Fatal("an id is lower-case hex")
	}
	for in, want := range map[string]string{"#3b82f6": "#3B82F6", " #FFE4E6 ": "#FFE4E6", "": ""} {
		if got, err := NormalizeColour("accent", in); err != nil || got != want {
			t.Errorf("NormalizeColour(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"red", "#fff", "3B82F6", "#3B82F6FF", "#GGGGGG"} {
		if _, err := NormalizeColour("accent", in); err == nil || !strings.Contains(err.Error(), "accent must be a colour written #RRGGBB") {
			t.Errorf("NormalizeColour(%q) = %v, want refused", in, err)
		}
	}
}
