// Command tofu-providers holds the OpenTofu provider packages that the provider
// pod of provider-opentofu installs, and serves them the way an OCI provider
// mirror does (SOV-342).
//
// The provider pod runs `tofu init` for every Workspace with the CLI
// configuration crossplane-system/opentofu-cli-config: one oci_mirror for
// registry.opentofu.org/*/* at ghcr.io/openova-io/tofu-providers and no other
// installation method, so a provider comes from that mirror or not at all, never
// from the public registry. The packages the mirror holds are listed, each with
// the digest of its OCI index, in the ConfigMap crossplane-system/opentofu-providers
// (clusters/_template/infrastructure/providers/base/opentofu-providers.yaml). That
// file is written from the list of each provider pack,
// infra/providers/<pack>/tofu-providers.txt, the versions its lock file pins: a
// pack's providers are named only in the pack's own folder.
//
// Subcommands (from tools/guards):
//
//	go run ./cmd/tofu-providers check [-root R]
//	    exit 1, with one line per finding, when
//	    - a line of a list is not <registry.opentofu.org/namespace/type> <major.minor.patch>
//	      <sha256:64 hex digits>, or lists a source a second time (rule L1);
//	    - a required_providers entry of the CloudAdoption composition's inline module
//	      has no listed version that meets its constraint, or a module of a pack has
//	      none in the pack's list (rule L2; the operators = != > >= < <= ~> are read);
//	    - a lock file of a pack pins a version its pack's list does not hold (rule L3);
//	    - a source of a pack's spec.tofuProviders is not in its pack's list (rule L4);
//	    - two packs list one source differently, or the ConfigMap is not what
//	      write-list.sh writes from the packs' lists (rule L5).
//	    Exit 0 when none holds, 2 when the tree cannot be read.
//	go run ./cmd/tofu-providers list [-root R] [-check]
//	    writes the ConfigMap from the packs' lists (write-list.sh runs this); with
//	    -check it writes nothing and exits 1 when the file differs.
//	go run ./cmd/tofu-providers pack -out DIR -version V ZIP...
//	    writes an OCI image layout of one provider version from its zips
//	    (terraform-provider-<type>_<V>_<os>_<arch>.zip) and prints the digest of its
//	    index: artifact type application/vnd.opentofu.provider, one manifest per zip of
//	    artifact type application/vnd.opentofu.provider-target with exactly one layer
//	    archive/zip, no annotation and no time stamp, tagged V. The digest follows from
//	    the zips alone; it is the digest a list line holds.
//	go run ./cmd/tofu-providers stub -out DIR -source S -version V
//	    writes a stub package of S at V for linux/amd64, for offline tests.
//	go run ./cmd/tofu-providers serve -ca-out F -addr-out F REPOSITORY=LAYOUT...
//	    answers the read side of the OCI distribution API for each layout over TLS on
//	    127.0.0.1 at a free port, until it is stopped; it writes its CA to -ca-out and
//	    then its address to -addr-out.
//
// A new provider version: put the line into the pack's tofu-providers.txt with the
// digest pack prints for the zip registry.opentofu.org serves for linux/amd64, run
// bash tools/guards/cmd/tofu-providers/write-list.sh, and push: the workflow
// mirror-tofu-providers publishes the package before the change merges. The tests
// in main_test.go show that each rule can fail.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root with git: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func rootFlag(fs *flag.FlagSet) *string {
	return fs.String("root", "", "repository root (default: the root Git reports)")
}

func resolveRoot(root *string, stderr io.Writer) bool {
	if *root != "" {
		return true
	}
	r, err := repositoryRoot()
	if err != nil {
		say(stderr, "tofu-providers: %v\n", err)
		return false
	}
	*root = r
	return true
}

const usage = "usage: tofu-providers check|list|pack|stub|serve [flags] (see the comment of main.go)\n"

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("tofu-providers "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "check":
		root := rootFlag(fs)
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !resolveRoot(root, stderr) {
			return 2
		}
		res, err := check(*root)
		if err != nil {
			say(stderr, "tofu-providers: cannot read the tree: %v\n", err)
			return 2
		}
		for _, f := range res.findings {
			say(stdout, "FAIL %s\n", f)
		}
		if len(res.findings) > 0 {
			say(stdout, "tofu-providers: %d finding(s)\n", len(res.findings))
			return 1
		}
		say(stdout, "tofu-providers: PASS — %d package(s) listed; %d required provider(s) and %d lock pin(s) are met by the lists\n", res.listed, res.requirements, res.pins)
		return 0
	case "list":
		root := rootFlag(fs)
		only := fs.Bool("check", false, "write nothing; exit 1 when the file differs")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || !resolveRoot(root, stderr) {
			return 2
		}
		changed, problems, err := writeList(*root, *only)
		if err != nil {
			say(stderr, "tofu-providers: %v\n", err)
			return 2
		}
		for _, p := range problems {
			say(stdout, "FAIL %s\n", p)
		}
		switch {
		case len(problems) > 0:
			return 1
		case changed && *only:
			say(stdout, "FAIL %s is not what the packs' lists give; run: bash %s\n", listPath, writerScript)
			return 1
		case changed:
			say(stdout, "tofu-providers: wrote %s\n", listPath)
		default:
			say(stdout, "tofu-providers: %s is current\n", listPath)
		}
		return 0
	case "pack":
		out := fs.String("out", "", "the layout folder to write (empty or absent)")
		ver := fs.String("version", "", "the provider version")
		if fs.Parse(args[1:]) != nil || *out == "" || *ver == "" || fs.NArg() == 0 {
			say(stderr, usage)
			return 2
		}
		d, err := packLayout(*out, *ver, fs.Args())
		if err != nil {
			say(stderr, "tofu-providers: %v\n", err)
			return 1
		}
		say(stdout, "%s\n", d)
		return 0
	case "stub":
		out := fs.String("out", "", "the folder for the zip")
		src := fs.String("source", "", "registry.opentofu.org/<namespace>/<type>")
		ver := fs.String("version", "", "the provider version")
		if fs.Parse(args[1:]) != nil || *out == "" || *src == "" || *ver == "" || fs.NArg() != 0 {
			say(stderr, usage)
			return 2
		}
		p, err := writeStub(*out, *src, *ver)
		if err != nil {
			say(stderr, "tofu-providers: %v\n", err)
			return 1
		}
		say(stdout, "%s\n", p)
		return 0
	case "serve":
		caOut := fs.String("ca-out", "", "file for the CA certificate (PEM)")
		addrOut := fs.String("addr-out", "", "file for the address host:port, written once the server listens")
		if fs.Parse(args[1:]) != nil || *caOut == "" || *addrOut == "" || fs.NArg() == 0 {
			say(stderr, usage)
			return 2
		}
		repos := map[string]string{}
		for _, a := range fs.Args() {
			name, dir, ok := strings.Cut(a, "=")
			if !ok || name == "" || dir == "" {
				say(stderr, "tofu-providers: %q is not REPOSITORY=LAYOUT\n", a)
				return 2
			}
			repos[name] = dir
		}
		ln, srv, err := startServe(repos, *caOut)
		if err != nil {
			say(stderr, "tofu-providers: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*addrOut, []byte(ln.Addr().String()+"\n"), 0o600); err != nil {
			say(stderr, "tofu-providers: %v\n", err)
			return 1
		}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			say(stderr, "tofu-providers: %v\n", err)
			return 1
		}
		return 0
	}
	say(stderr, usage)
	return 2
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
