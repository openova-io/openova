// machine-runner runs one task of the machine plane on one target, as the program of a
// runner Job in the image built from products/machine-runner/Containerfile.
//
// It writes one result document (schema openova.io/machine-run-result/v1) to the
// termination-message file /dev/termination-log and to stdout, and exits 0 for the
// outcomes Succeeded and NoChange and 1 for every other outcome. A SIGTERM ends the
// running collector and gives outcome Cancelled. The arguments and the tasks are those
// of package internal/machines/runner.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := runner.Main(ctx, os.Args[1:], runner.NodeEnv(), os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
