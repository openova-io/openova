package suite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Check FND-G05, No NodePort: no Service of a region holds a node port. The
// check runs scripts/check-live-nodeports.sh of the checkout at RepoRoot once
// for each region, with that region's kubeconfig. It does not copy the
// script: the script is the rule.
//
// The script exits 0 also when it could not read the Services of the cluster
// (no kubectl, or a cluster that cannot be reached); it then prints a warning
// and no verdict. So a region passes only when the script exits 0 AND prints
// its verdict line nodePortVerdict. Any other exit code fails the region.
const nodePortID = "FND-G05"

// nodePortScript is the path of the script, relative to the checkout.
const nodePortScript = "scripts/check-live-nodeports.sh"

// nodePortVerdict starts the line that the script prints when it has read
// every Service of the cluster and found no node port in a namespace of the
// platform.
const nodePortVerdict = "OK: zero live node ports in owned namespaces across "

type nodePortCheck struct{}

func init() { RegisterCheck(nodePortCheck{}) }

func (nodePortCheck) ID() string { return nodePortID }

func (nodePortCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	if len(cc.Regions) == 0 {
		return errors.New("the run has no region")
	}
	for i, region := range cc.Regions {
		rec.Step(nodePortID+"."+strconv.Itoa(i+1), "no Service holds a node port in region "+region.ID, func(ev Evidence) error {
			code, out, err := runRepoScript(ctx, cc.RepoRoot, ev, nodePortScript, "--kubeconfig", region.Kubeconfig)
			if err != nil {
				return err
			}
			if code != 0 {
				return fmt.Errorf("%s exited %d for region %s: %s", nodePortScript, code, region.ID, lastLine(out))
			}
			if !strings.Contains(out, nodePortVerdict) {
				return fmt.Errorf("%s exited 0 for region %s without its verdict line %q: it did not read the Services: %s",
					nodePortScript, region.ID, strings.TrimSpace(nodePortVerdict), lastLine(out))
			}
			return nil
		})
	}
	return nil
}

// scriptOutputLines is how many of the last lines of a script's output the
// evidence keeps.
const scriptOutputLines = 40

// runRepoScript runs `bash <root>/<script> <args>` in the working directory of
// the suite, so that a relative kubeconfig path means what it means to the
// suite. It returns the exit code and the output (standard output and
// standard error together), and puts the script, the exit code and the last
// lines of the output in ev. The error is not nil only when bash could not
// be started.
func runRepoScript(ctx context.Context, root string, ev Evidence, script string, args ...string) (int, string, error) {
	path := filepath.Join(root, filepath.FromSlash(script))
	cmd := exec.CommandContext(ctx, "bash")
	// cmd.Args[0] is bash. The script and its arguments follow it.
	cmd.Args = append(append(cmd.Args, path), args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		return 0, "", fmt.Errorf("%s could not be started: %w", script, err)
	}
	text := out.String()
	ev["script"], ev["exit_code"], ev["output"] = script, code, lastLines(text, scriptOutputLines)
	return code, text, nil
}

// lastLines returns the last n lines of s that are not empty.
func lastLines(s string, n int) []string {
	lines := []string{}
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimRight(line, " \r\t"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// lastLine returns the last line of s that is not empty, or "(no output)".
func lastLine(s string) string {
	if lines := lastLines(s, 1); len(lines) == 1 {
		return strings.TrimSpace(lines[0])
	}
	return "(no output)"
}
