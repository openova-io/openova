// Command capability-index writes the capability index,
// products/catalyst/bootstrap/api/internal/catalog/capabilities.json: every contract file
// platform/_capabilities/<name>.yaml, checked against platform/_schemas/capability.schema.json, and per
// capability the Blueprints whose blueprint.yaml provides it (capability.Build). Run it through write.sh:
//
//	sh products/catalyst/bootstrap/api/internal/capability/write.sh           write the index
//	sh products/catalyst/bootstrap/api/internal/capability/write.sh --check   write nothing; exit 1 when the committed index differs
//
// Exit codes: 0 written or in sync; 1 --check found the committed index stale or missing; 2 an input is
// wrong (one line per error, naming the file).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/capability"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("capability-index", flag.ContinueOnError)
	fl.SetOutput(stderr)
	root := fl.String("root", "", "the repository root")
	check := fl.Bool("check", false, "write nothing; exit 1 when the committed index differs")
	if err := fl.Parse(args); err != nil || *root == "" || fl.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "usage: capability-index -root <repository root> [--check]")
		return 2
	}
	tree := os.DirFS(*root)
	schema, err := fs.ReadFile(tree, capability.SchemaPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "capability-index: %s: %v\n", capability.SchemaPath, err)
		return 2
	}
	out, errs := capability.Build(*root, schema)
	if len(errs) > 0 {
		for _, e := range errs {
			_, _ = fmt.Fprintln(stderr, "capability-index: "+e)
		}
		return 2
	}
	target := filepath.Join(*root, capability.IndexPath)
	if *check {
		have, err := fs.ReadFile(tree, capability.IndexPath)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(stderr, "capability-index: %s: %v\n", capability.IndexPath, err)
			return 2
		}
		if err != nil || !bytes.Equal(have, out) {
			_, _ = fmt.Fprintf(stderr, "capability-index: %s is stale; run %s and commit the result\n", capability.IndexPath, capability.WriteCommand)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "capability-index: %s is in sync\n", capability.IndexPath)
		return 0
	}
	if err := os.WriteFile(target, out, 0o600); err != nil {
		_, _ = fmt.Fprintf(stderr, "capability-index: %s: %v\n", capability.IndexPath, err)
		return 2
	}
	_, _ = fmt.Fprintf(stdout, "capability-index: wrote %s\n", capability.IndexPath)
	return 0
}
