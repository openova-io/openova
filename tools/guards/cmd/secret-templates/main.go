// Command secret-templates names the files under clusters/ whose Secret
// manifests are templates of references, which guard G1
// (scripts/check-no-secret-values-in-git.sh) does not count as Secret values.
//
// G1 counts every line `kind: Secret` of a file under clusters/ as a Secret
// value committed to Git. A manifest that Flux renders from a Secret of the
// cluster (postBuild.substituteFrom) holds no value, only the name of one:
// clusters/_template/bootstrap-secrets renders the bootstrap Secrets from the
// seed Secret flux-system/bootstrap-seed that first boot creates. Such a
// manifest is a template, and it passes only when
//
//	every entry under data and stringData is exactly one reference ${NAME},
//	NAME of capitals, digits and underscores, as YAML reads the value
//	(plain, "${NAME}" or '${NAME}'): no literal, no text around the
//	reference, no default (${NAME:=x}, ${NAME:-x}), no empty value;
//	data and stringData hold at least one entry together;
//	the manifest has no field but apiVersion, kind, metadata, type,
//	immutable, data and stringData. metadata (labels, annotations) is not
//	judged.
//
// A file is named only when it has at least one Secret, every Secret in it is
// such a template, and every line `kind: Secret` of the file (the line G1
// counts, SECRET_KIND of scripts/secret-guards.py) is the kind of one of those
// templates. Every other file stays a finding of G1, under its allow-list.
//
// The manifest is read as written, by a strict reader of the one form a
// template needs: each field of the manifest starts a line of its own at the
// left edge; metadata, data and stringData are block mappings; apiVersion,
// kind, type and immutable hold one word or one quoted string on their line;
// each entry of data and stringData is one line `key: value`, at one indent.
// A document is split at a line `---` or `...`. A form the reader does not
// know (a flow mapping, a block scalar, an anchor or alias, a tag, a comment
// after a value, a tab, a carriage return) is not a template, so YAML can
// never read a value the reader did not judge.
//
// It reads the files Git lists (tracked, and new files Git does not ignore)
// under clusters/. It prints the path of each template file, one per line, and
// to standard error why each other file with a line `kind: Secret` is not one.
//
// Usage, from tools/guards:
//
//	go run ./cmd/secret-templates   exit 0, 2 when it cannot read
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	// The line G1 counts (SECRET_KIND of scripts/secret-guards.py).
	secretKindLine = regexp.MustCompile(`^\s*kind:\s*Secret\s*$`)
	// A field of the manifest: a key at the left edge, then nothing, a
	// comment, or a space and its value.
	fieldLine = regexp.MustCompile(`^([A-Za-z]+):(?: +(.*))?$`)
	// An entry of data or stringData: indent, a key of a Secret, a space and
	// the value.
	entryLine = regexp.MustCompile(`^( +)([-._A-Za-z0-9]+):(?: +(.*))?$`)
	// The value of apiVersion, kind, type or immutable: one word or one
	// quoted string without a quote or backslash inside.
	oneWord = regexp.MustCompile(`^(?:[A-Za-z0-9./_-]+|"[^"\\]*"|'[^']*')$`)
	// The one value a template entry holds.
	reference = regexp.MustCompile(`^\$\{[A-Z0-9_]+\}$`)
	// A document separator, with a comment at most.
	separator = regexp.MustCompile(`^(?:---|\.\.\.)(?:\s+#.*)?\s*$`)
	fields    = map[string]bool{"apiVersion": true, "kind": true, "metadata": true, "type": true, "immutable": true, "data": true, "stringData": true}
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("secret-templates", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		say(errw, "secret-templates: unknown arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	if *root == "" {
		top, err := git(".", "rev-parse", "--show-toplevel")
		if err != nil {
			say(errw, "secret-templates: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(top)
	}
	listed, err := git(*root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "clusters")
	if err != nil {
		say(errw, "secret-templates: cannot list the files: %v\n", err)
		return 2
	}
	tree := os.DirFS(*root)
	read := 0
	for _, p := range strings.Split(listed, "\x00") {
		if !strings.HasPrefix(p, "clusters/") {
			continue
		}
		b, err := fs.ReadFile(tree, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			say(errw, "secret-templates: %v\n", err)
			return 2
		}
		read++
		text := string(b)
		if bytes.IndexByte(b, 0) >= 0 || !hasSecretKindLine(text) {
			continue // a binary file, or no line G1 counts
		}
		if why := judgeFile(text); why != "" {
			say(errw, "secret-templates: %s is not a template of references: %s\n", p, why)
			continue
		}
		say(out, "%s\n", p)
	}
	if read == 0 {
		say(errw, "secret-templates: no file under clusters/ was read: nothing was measured\n")
		return 2
	}
	return 0
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

func hasSecretKindLine(text string) bool {
	for _, l := range strings.Split(text, "\n") {
		if secretKindLine.MatchString(l) {
			return true
		}
	}
	return false
}

// judgeFile returns "" when the file holds at least one Secret, every Secret
// in it is a template and every line `kind: Secret` is the kind of one of
// them; otherwise the first reason it is not.
func judgeFile(text string) string {
	lines := strings.Split(text, "\n")
	kinds := map[int]bool{} // the lines `kind: Secret` of the templates
	secrets, start := 0, 0
	for i := 0; i <= len(lines); i++ {
		if i < len(lines) && !separator.MatchString(lines[i]) {
			continue
		}
		doc := lines[start:i]
		kind, at := kindOf(doc)
		if kind == "Secret" {
			secrets++
			if why := judge(doc, start); why != "" {
				return fmt.Sprintf("the Secret of line %d: %s", start+at+1, why)
			}
			kinds[start+at] = true
		}
		start = i + 1
	}
	for i, l := range lines {
		if secretKindLine.MatchString(l) && !kinds[i] {
			return fmt.Sprintf("line %d names the kind Secret outside the kind field of a template", i+1)
		}
	}
	if secrets == 0 {
		return "it holds no Secret"
	}
	return ""
}

// kindOf returns the value of the first field kind of a document, unquoted,
// and the index of its line in the document.
func kindOf(doc []string) (string, int) {
	for i, l := range doc {
		if m := fieldLine.FindStringSubmatch(l); m != nil && m[1] == "kind" {
			return unquote(strings.TrimRight(m[2], " ")), i
		}
	}
	return "", -1
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' && !strings.ContainsAny(v[1:len(v)-1], "\"\\") ||
		v[0] == '\'' && v[len(v)-1] == '\'' && !strings.Contains(v[1:len(v)-1], "'")) {
		return v[1 : len(v)-1]
	}
	return v
}

// judge returns "" when one Secret document is a template, otherwise why it
// is not. first is the index of the document's first line in the file.
func judge(doc []string, first int) string {
	seen := map[string]bool{}
	keys := map[string]bool{}
	block, indent, entries := "", 0, 0
	for i, l := range doc {
		n := first + i + 1
		if strings.ContainsAny(l, "\t\r") {
			return fmt.Sprintf("line %d holds a tab or a carriage return", n)
		}
		body := strings.TrimLeft(l, " ")
		if body == "" || body[0] == '#' {
			continue // a blank line or a comment
		}
		if body == l { // a field of the manifest
			m := fieldLine.FindStringSubmatch(l)
			if m == nil {
				return fmt.Sprintf("line %d is not a field `key: value` of the manifest", n)
			}
			key, value := m[1], strings.TrimRight(m[2], " ")
			if !fields[key] {
				return fmt.Sprintf("the field %s (line %d) can carry a value; a template has only apiVersion, kind, metadata, type, immutable, data and stringData", key, n)
			}
			if seen[key] {
				return fmt.Sprintf("the field %s appears twice (line %d)", key, n)
			}
			seen[key] = true
			block, indent = "", 0
			switch {
			case key == "metadata" || key == "data" || key == "stringData":
				if value != "" && value[0] != '#' {
					return fmt.Sprintf("%s (line %d) is not a block mapping", key, n)
				}
				block = key
			case !oneWord.MatchString(value):
				return fmt.Sprintf("the value of %s (line %d) is not one word on its line", key, n)
			}
			continue
		}
		switch block {
		case "metadata":
			continue // labels and annotations are not judged
		case "data", "stringData":
		default:
			return fmt.Sprintf("line %d continues a field that holds no block", n)
		}
		m := entryLine.FindStringSubmatch(l)
		if m == nil {
			return fmt.Sprintf("line %d of %s is not one entry `key: ${NAME}`", n, block)
		}
		if indent == 0 {
			indent = len(m[1])
		} else if len(m[1]) != indent {
			return fmt.Sprintf("line %d of %s is not at the indent of its first entry", n, block)
		}
		key, value := m[2], strings.TrimRight(m[3], " ")
		if keys[block+"."+key] {
			return fmt.Sprintf("%s.%s appears twice (line %d)", block, key, n)
		}
		keys[block+"."+key] = true
		switch {
		case value == "" || value == `""` || value == `''`:
			return fmt.Sprintf("%s.%s (line %d) is empty", block, key, n)
		case !reference.MatchString(unquote(value)):
			return fmt.Sprintf("%s.%s (line %d) is not exactly one reference ${NAME} (NAME of capitals, digits and underscores; no literal, no text around it, no default)", block, key, n)
		}
		entries++
	}
	if entries == 0 {
		return "it has no entry under data or stringData"
	}
	return ""
}
