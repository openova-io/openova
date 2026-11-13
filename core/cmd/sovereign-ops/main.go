// Command sovereign-ops runs one life-cycle action of a Sovereign, as a
// Kubernetes Job or CronJob: sovereign-ops <subcommand> [flags]. Every
// subcommand is idempotent and ends with the result line and exit code of
// package internal/ops. Logs go to standard error.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
)

// subcommands is the dispatcher's table. A subcommand is one entry here and
// one package under internal/.
var subcommands = map[string]func(ctx context.Context, args []string, stderr io.Writer) ([]string, error){}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, "/dev/termination-log")
	stop()
	os.Exit(code)
}

// run dispatches args to a subcommand and returns the exit code. A missing
// or unknown subcommand fails with exit code 1.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, termLog string) int {
	var names []string
	for n := range subcommands {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(args) == 0 {
		return ops.Finish(stdout, termLog, "", nil, fmt.Errorf("usage: sovereign-ops <subcommand> [flags]; subcommands: [%s]", strings.Join(names, " ")))
	}
	cmd, ok := subcommands[args[0]]
	if !ok {
		return ops.Finish(stdout, termLog, args[0], nil, fmt.Errorf("unknown subcommand %q; subcommands: [%s]", args[0], strings.Join(names, " ")))
	}
	evidence, err := cmd(ctx, args[1:], stderr)
	return ops.Finish(stdout, termLog, args[0], evidence, err)
}
