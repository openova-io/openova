package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes a fixture repository: the catalogue with the given kit and the charts named.
func tree(t *testing.T, kit string, charts ...string) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(catalogPath, `{"blueprints": [
	  {"id": "bp-a", "title": "A \"one\"", "class": "infrastructure", "lifecycle": "experimental"},
	  {"id": "bp-b", "title": "B", "class": "platform-component", "lifecycle": "supported"},
	  {"id": "bp-nochart", "title": "N", "class": "infrastructure", "lifecycle": "experimental"}],
	 "bootstrapKit": [`+kit+`]}`)
	for _, c := range charts {
		put("platform/"+strings.TrimPrefix(c, "bp-")+"/chart/Chart.yaml", "apiVersion: v2\nname: "+c+"\nversion: 1.0.0\n")
	}
	put(filepath.Dir(outPath)+"/.keep", "")
	return root
}

const goodKit = `{"id": "bp-a", "file": "01-a.yaml", "ring": "foundation"},
  {"id": "bp-b", "file": "02-b.yaml", "ring": "observability"},
  {"id": "bp-nochart", "file": "03-n.yaml", "ring": "observability"},
  {"id": "bp-gone", "file": "04-g.yaml", "ring": "ai"}`

func runIn(root string, args ...string) (int, string) {
	var out, errOut bytes.Buffer
	code := run(args, root, &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestWritesEightRingsInOrderWithOnlyChartedBlueprints(t *testing.T) {
	root := tree(t, goodKit, "bp-a", "bp-b")
	if code, out := runIn(root); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	b, err := os.ReadFile(filepath.Join(root, outPath))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	last := -1
	for _, r := range rings {
		i := strings.Index(text, "name: \""+r.name+"\",")
		if i < 0 || i < last {
			t.Fatalf("ring %s missing or out of order", r.name)
		}
		last = i
	}
	for _, want := range []string{
		`{ blueprint: "bp-a", title: "A \"one\"", class: "infrastructure", lifecycle: "experimental" },`,
		`{ blueprint: "bp-b", title: "B", class: "platform-component", lifecycle: "supported" },`,
		"name: \"foundation\",\n    title: \"Foundation\",\n    required: true,",
		"name: \"provider\",\n    title: \"Provider\",\n    required: true,",
		"name: \"observability\",\n    title: \"Observability\",\n    required: false,",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	for _, never := range []string{"bp-nochart", "bp-gone"} {
		if strings.Contains(text, never) {
			t.Errorf("output lists %s, a kit slot without a chart or a Blueprint", never)
		}
	}
	if !strings.HasPrefix(text, "// AUTO-GENERATED") {
		t.Error("output does not start with its generated header")
	}
}

func TestCheckFailsOnAStaleListAndPassesOnAFreshOne(t *testing.T) {
	root := tree(t, goodKit, "bp-a", "bp-b")
	if code, _ := runIn(root, "--check"); code != 1 {
		t.Fatalf("--check before any write: exit %d, want 1", code)
	}
	if code, out := runIn(root); code != 0 {
		t.Fatalf("write: exit %d: %s", code, out)
	}
	if code, out := runIn(root, "--check"); code != 0 {
		t.Fatalf("--check after write: exit %d: %s", code, out)
	}
	f, err := os.OpenFile(filepath.Join(root, outPath), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("// hand edit\n")
	f.Close()
	if code, out := runIn(root, "--check"); code != 1 || !strings.Contains(out, "is stale") {
		t.Fatalf("--check after a hand edit: exit %d, %q; want 1 and stale", code, out)
	}
}

func TestInputErrorsExit2(t *testing.T) {
	cases := map[string]string{
		"a slot without a ring":           `{"id": "bp-a", "file": "01-a.yaml", "ring": null}`,
		"a slot with a ring not of eight": `{"id": "bp-a", "file": "01-a.yaml", "ring": "extras"}`,
		"an empty kit":                    ``,
	}
	for name, kit := range cases {
		t.Run(name, func(t *testing.T) {
			root := tree(t, kit, "bp-a")
			if code, out := runIn(root); code != 2 {
				t.Fatalf("exit %d, want 2: %s", code, out)
			}
			if _, err := os.Stat(filepath.Join(root, outPath)); err == nil {
				t.Fatal("an input error wrote the list")
			}
		})
	}
	root := t.TempDir()
	if code, _ := runIn(root); code != 2 {
		t.Fatalf("no catalogue: exit %d, want 2", code)
	}
}

// The committed list is the one the committed kit and catalogue give.
func TestCommittedListIsInSync(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, catalogPath)); err != nil {
		t.Skip("not inside the repository")
	}
	if code, out := runIn(root, "--check"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}
