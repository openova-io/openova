package store

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationsAreNumberedFromOne(t *testing.T) {
	migs, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 1 || migs[0].Version != 1 || migs[0].Name != "cortex" {
		t.Fatalf("migrations = %+v, want only 0001_cortex", migs)
	}
	sql, err := render(migs[0].SQL, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sql, "{{") || !strings.Contains(sql, "embedding vector(1024) NOT NULL") {
		t.Fatal("render did not write the vector length into migration 0001")
	}
}

func TestParseMigrationsRefusesBadSets(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"gap":      {"0001_a.sql": {Data: []byte("x")}, "0003_c.sql": {Data: []byte("x")}},
		"doubled":  {"0001_a.sql": {Data: []byte("x")}, "0001_b.sql": {Data: []byte("x")}},
		"bad name": {"0001_a.up.sql": {Data: []byte("x")}},
		"empty":    {},
	}
	for name, fsys := range cases {
		if _, err := parseMigrations(fsys); err == nil {
			t.Errorf("%s: parseMigrations accepted the set", name)
		}
	}
}

func TestDimensionsAreBounded(t *testing.T) {
	for _, d := range []int{0, 63, 4097} {
		if _, err := render("vector({{dimensions}})", d); err == nil {
			t.Errorf("render accepted the vector length %d", d)
		}
	}
	for _, d := range []int{64, 4096} {
		if _, err := render("vector({{dimensions}})", d); err != nil {
			t.Errorf("render refused the vector length %d: %v", d, err)
		}
	}
}

func TestCheckPrincipals(t *testing.T) {
	if err := checkPrincipals(nil); !errors.Is(err, ErrNoPrincipals) {
		t.Errorf("an empty list gave %v, want ErrNoPrincipals", err)
	}
	for _, p := range []string{"user:alice@example.org", "group:/HR admins", "agent:acme/helpdesk", "org:members"} {
		if err := checkPrincipals([]string{p}); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	for _, p := range []string{"", "alice", "user:", "org:others", "agent:acme", "group:a\nb"} {
		if err := checkPrincipals([]string{"org:members", p}); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	for v, want := range map[string]bool{"0.8.0": true, "0.8.6": true, "0.10.1": true, "1.0": true,
		"0.7.4": false, "0.5": false, "": false, "x.y": false, "0": false} {
		if got := versionAtLeast(v, 0, 8); got != want {
			t.Errorf("versionAtLeast(%q, 0.8) = %v, want %v", v, got, want)
		}
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := vectorLiteral([]float32{1, -0.5, 0.25, 3e-8}); got != "[1,-0.5,0.25,3e-08]" {
		t.Errorf("vectorLiteral = %s", got)
	}
}

func TestSortHitsBestFirst(t *testing.T) {
	hits := []Hit{{ChunkID: 3, Score: 0.2}, {ChunkID: 2, Score: 0.9}, {ChunkID: 1, Score: 0.2}}
	sortHits(hits)
	if hits[0].ChunkID != 2 || hits[1].ChunkID != 1 || hits[2].ChunkID != 3 {
		t.Errorf("sortHits = %+v", hits)
	}
}
