// Package ops holds what every subcommand of sovereign-ops shares: the
// result line and the exit codes. A run ends with one JSON line on standard
// output, also written to the termination log:
//
//	{"command":"seed-repo","result":"success","evidence":["..."],"error":""}
//
// Exit code 0 is success, 1 a failure, 75 a transient failure that the
// caller may retry.
package ops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// The exit codes of every subcommand.
const (
	ExitSuccess   = 0
	ExitFailed    = 1
	ExitTransient = 75
)

// ErrTransient marks a failure that the caller may retry.
var ErrTransient = errors.New("transient")

// Transient wraps err so that errors.Is(err, ErrTransient) holds. nil stays nil.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrTransient, err)
}

// Line is the result line of one run.
type Line struct {
	Command  string   `json:"command"`
	Result   string   `json:"result"`
	Evidence []string `json:"evidence"`
	Error    string   `json:"error"`
}

// Finish writes the result line of a run that ended with evidence and err
// to out and to the file termLog, and returns the exit code. A failed
// write to termLog is ignored: outside a Pod the file may not exist.
func Finish(out io.Writer, termLog, command string, evidence []string, err error) int {
	l, code := Line{Command: command, Result: "success", Evidence: append([]string{}, evidence...)}, ExitSuccess
	if err != nil {
		l.Result, l.Error, code = "failed", err.Error(), ExitFailed
		if errors.Is(err, ErrTransient) {
			code = ExitTransient
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if eerr := enc.Encode(l); eerr != nil {
		return ExitFailed
	}
	if _, werr := out.Write(b.Bytes()); werr != nil {
		return ExitFailed
	}
	if termLog != "" {
		_ = os.WriteFile(termLog, b.Bytes(), 0o600)
	}
	return code
}
