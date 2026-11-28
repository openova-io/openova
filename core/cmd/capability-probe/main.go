// Command capability-probe checks one binding of a capability contract the way a consumer uses it
// (docs/adr/0020, S7 and S11; contract files platform/_capabilities/<name>.yaml).
//
//	capability-probe --probe <name> --binding-dir <dir>   read the contract keys as files in <dir>, check
//	                                                      every required key is present and well formed,
//	                                                      then exercise the service
//	capability-probe --probe <name> --binding-dir <dir> --keys-only   only check the keys (readiness)
//	capability-probe --idle                               wait until the pod is stopped (the idle consumer
//	                                                      of bp-capability-probe)
//
// Exit 0 with one line "PROBE pass <probe> <what was done>". Exit 1 with one line
// "PROBE fail <probe> <reason>" otherwise. The probe never prints a credential (the access key id
// and the secret access key); its lines may name the other keys' values (type, endpoint, bucket,
// path-style, provider).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// probeFunc checks the binding files in dir and, unless keysOnly, exercises the service through
// them; it returns what it did.
type probeFunc func(ctx context.Context, dir string, keysOnly bool) (string, error)

// probes is the set of probe names a contract file may name in spec.contracts[].probe.
var probes = map[string]probeFunc{
	"object-storage-v1": objectStorageV1,
}

// probeTimeout bounds one probe run; the function test of a chart has 300 seconds in all.
const probeTimeout = 2 * time.Minute

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, out io.Writer) int {
	fs := flag.NewFlagSet("capability-probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("probe", "", "name of the probe, as in spec.contracts[].probe of the contract file")
	dir := fs.String("binding-dir", "", "directory that holds one file per contract key")
	idle := fs.Bool("idle", false, "wait until the process is stopped")
	keysOnly := fs.Bool("keys-only", false, "check the keys only; do not exercise the service (a readiness check)")
	if err := fs.Parse(args); err != nil {
		return fail(out, "-", "usage: "+err.Error())
	}
	if fs.NArg() > 0 {
		return fail(out, "-", "usage: unexpected argument "+fs.Arg(0))
	}
	if *idle {
		if *name != "" || *dir != "" || *keysOnly {
			return fail(out, "-", "usage: --idle takes no other flag")
		}
		<-ctx.Done()
		return 0
	}
	if *name == "" {
		return fail(out, "-", "usage: --probe is required")
	}
	probe, ok := probes[*name]
	if !ok {
		return fail(out, *name, "unknown probe")
	}
	if *dir == "" {
		return fail(out, *name, "usage: --binding-dir is required")
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	done, err := probe(pctx, *dir, *keysOnly)
	if err != nil {
		return fail(out, *name, err.Error())
	}
	_, _ = fmt.Fprintf(out, "PROBE pass %s %s\n", *name, done)
	return 0
}

func fail(out io.Writer, name, reason string) int {
	_, _ = fmt.Fprintf(out, "PROBE fail %s %s\n", name, oneLine(reason))
	return 1
}

// oneLine keeps the result to the one line the function test reads.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
