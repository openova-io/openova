// Package runner is the program of the machine-runner image: it runs one task of the
// machine plane on one target, stores the task's files in the evidence bucket and writes
// the result document of package runresult.
//
// This version has node mode only, with two tasks. Task inventory writes the software
// inventory of the node as CycloneDX with Trivy, offline. Task hardening evaluates an
// XCCDF profile of the ComplianceAsCode content with oscap-chroot. Every task writes a
// completion marker as its last step; the document says completed only when the marker
// exists, and a run whose collector did not finish stores nothing.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/runresult"
)

// Env holds the places the runner reads and writes. NodeEnv gives those of the image.
type Env struct {
	HostRoot    string // the file system of the node, mounted read-only
	WorkDir     string // the memory volume; task files and markers are written here
	ContentDir  string // the ComplianceAsCode data streams, one folder per release
	BindingDir  string // the object-storage binding of the evidence bucket
	ResultFile  string // the termination-message file of the container
	Trivy       string // the trivy program
	OscapChroot string // the oscap-chroot program
}

// NodeEnv returns the places of a node-mode Job of the machine-runner image.
func NodeEnv() Env {
	return Env{
		HostRoot:    "/host",
		WorkDir:     "/run/machine-runner",
		ContentDir:  "/content/scap",
		BindingDir:  "/var/run/secrets/evidence-store",
		ResultFile:  "/dev/termination-log",
		Trivy:       "trivy",
		OscapChroot: "oscap-chroot",
	}
}

// The files a task stores, and their evidence kinds.
const (
	FileSBOM             = "sbom.cdx.json"
	FileHardeningARF     = "hardening.arf.xml"
	FileHardeningResults = "hardening.results.json"
	KindSBOM             = "sbom"
	KindHardeningARF     = "hardening-arf"
	KindHardeningResults = "hardening-results"
)

// Messages the runner writes. Each one starts with a fixed word that a reader can match.
const (
	MessageNoProfile    = "no-profile-for-release"
	messageNotSupported = "not-supported: "
	messageInvalid      = "invalid-arguments: "
	messageUploadFailed = "evidence-upload-failed: "
)

// file is one file a task wrote into the work folder and the run stores.
type file struct {
	kind string
	name string
}

// taskResult is what a task returns to Run.
type taskResult struct {
	outcome   runresult.Outcome
	message   string
	files     []file
	hardening *runresult.Hardening
}

func failed(format string, a ...any) taskResult {
	return taskResult{outcome: runresult.Failed, message: fmt.Sprintf(format, a...)}
}

func refused(message string) taskResult {
	return taskResult{outcome: runresult.Refused, message: message}
}

// Run runs one task and returns its result document. The document says completed only
// when the task's completion marker exists. Files are stored only for a task that
// succeeded and completed; a failed store turns the outcome to Failed.
func Run(ctx context.Context, cfg Config, env Env) runresult.Document {
	var t taskResult
	switch {
	case cfg.Mode != ModeNode:
		t = refused(messageNotSupported + "mode " + cfg.Mode)
	case cfg.Task == TaskInventory:
		t = inventory(ctx, cfg, env)
	case cfg.Task == TaskHardening:
		t = hardening(ctx, cfg, env)
	default:
		t = refused(messageNotSupported + "task " + cfg.Task)
	}
	doc := runresult.Document{Outcome: t.outcome, Message: t.message}
	doc.Completed = markerExists(env, cfg.Task)
	if t.outcome == runresult.Succeeded && !doc.Completed {
		t = taskResult{outcome: runresult.Incomplete, message: "completion-marker-missing"}
		doc.Outcome, doc.Message = t.outcome, t.message
	}
	if t.outcome != runresult.Succeeded {
		return doc
	}
	evidence, err := store(ctx, cfg, env, t.files)
	if err != nil {
		doc.Outcome, doc.Message = runresult.Failed, messageUploadFailed+err.Error()
		return doc
	}
	doc.Evidence = evidence
	if t.hardening != nil {
		for _, e := range evidence {
			if e.Kind == KindHardeningResults {
				t.hardening.ResultsKey = e.Key
			}
		}
		doc.Hardening = t.hardening
	}
	return doc
}

// Main reads the arguments, runs the task and writes the result document to the
// termination-message file and to stdout. It returns the exit code: 0 for the outcomes
// Succeeded and NoChange, 1 for every other outcome.
func Main(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	var doc runresult.Document
	cfg, err := ParseArgs(args)
	if err != nil {
		doc = runresult.Document{Outcome: runresult.Refused, Message: messageInvalid + err.Error()}
	} else {
		doc = Run(ctx, cfg, env)
	}
	out, err := runresult.Encode(doc)
	if err != nil {
		doc = runresult.Document{Outcome: runresult.Failed, Message: "result-not-encoded: " + err.Error()}
		out, _ = runresult.Encode(doc)
	}
	if env.ResultFile != "" {
		if err := os.WriteFile(env.ResultFile, out, 0o600); err != nil {
			_, _ = fmt.Fprintf(stderr, "machine-runner: the result file was not written: %v\n", err)
		}
	}
	_, _ = fmt.Fprintln(stdout, string(out))
	if doc.Outcome == runresult.Succeeded || doc.Outcome == runresult.NoChange {
		return 0
	}
	return 1
}

// markerPath is the completion marker of a task.
func markerPath(env Env, task string) string {
	return filepath.Join(env.WorkDir, task+".completed")
}

// writeMarker is the last step of every task.
func writeMarker(env Env, task string) error {
	return os.WriteFile(markerPath(env, task), []byte(task+"\n"), 0o600)
}

func markerExists(env Env, task string) bool {
	if task == "" {
		return false
	}
	info, err := os.Stat(markerPath(env, task))
	return err == nil && info.Mode().IsRegular()
}

// collect runs one collector program. It returns nil when the program exits with one of
// the codes in ok. A program stopped by a signal gives Incomplete; a run whose context
// ended gives Cancelled; any other exit gives Failed with the end of the program's
// error output.
func collect(ctx context.Context, extraEnv []string, ok []int, program string, args ...string) *taskResult {
	cmd := exec.CommandContext(ctx, program, args...) //nolint:gosec // the program is a fixed path of Env; every argument was checked by ParseArgs
	cmd.Env = append(os.Environ(), extraEnv...)
	var stderr tail
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err := cmd.Run()
	name := filepath.Base(program)
	if ctx.Err() != nil {
		return &taskResult{outcome: runresult.Cancelled, message: "cancelled while " + name + " ran"}
	}
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return &taskResult{outcome: runresult.Failed, message: name + " did not start: " + err.Error()}
	}
	code := exit.ExitCode()
	if code < 0 {
		return &taskResult{outcome: runresult.Incomplete, message: name + " was stopped: " + exit.String()}
	}
	if containsInt(ok, code) {
		return nil
	}
	msg := fmt.Sprintf("%s exited %d", name, code)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		msg += ": " + s
	}
	return &taskResult{outcome: runresult.Failed, message: msg}
}

func containsInt(list []int, n int) bool {
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// tail keeps the last 512 bytes written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 512 {
		t.b = t.b[len(t.b)-512:]
	}
	return len(p), nil
}

func (t *tail) String() string { return strings.ToValidUTF8(string(t.b), "") }
