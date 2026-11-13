// Command cloud-installation checks an installation document of contract C2
// against the pack profiles and prints the ConfigMap that carries it on a
// cluster (see the constants Installation* of package cloudprofile).
//
//	cloud-installation -providers <providers directory> <installation document>
//
// The providers directory is infra/providers of this repository. Keep the
// installation document outside this repository. The command reads files
// only; it writes nothing and talks to no cluster.
//
// Exit codes: 0 the document is valid and the ConfigMap is on standard
// output; 1 the document is refused, the reason is on standard error;
// 2 usage error, or a file cannot be read.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cloud-installation", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providers := fs.String("providers", "", "the providers directory (infra/providers of this repository)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *providers == "" || fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: cloud-installation -providers <providers directory> <installation document>")
		return 2
	}
	data, err := os.ReadFile(filepath.Clean(fs.Arg(0)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "cloud-installation: %v\n", err)
		return 2
	}
	out, err := cloudprofile.InstallationConfigMap(*providers, data)
	if err != nil {
		var fe *cloudprofile.FieldError
		if errors.As(err, &fe) {
			_, _ = fmt.Fprintf(stderr, "cloud-installation: refused: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "cloud-installation: %v\n", err)
		return 2
	}
	if _, err := stdout.Write(out); err != nil {
		_, _ = fmt.Fprintf(stderr, "cloud-installation: %v\n", err)
		return 2
	}
	return 0
}
