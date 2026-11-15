// Package runresult holds the result document of a machine-runner Job: one JSON object
// that the runner writes as the container's termination message, at most MaxBytes long.
//
// The members schema, outcome, message and completed are always present. The other
// members are present only for the task that produced them.
package runresult

import (
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Schema is the value of the member schema.
const Schema = "openova.io/machine-run-result/v1"

// MaxBytes is the largest encoded document: the size of a termination message.
const MaxBytes = 4096

// Outcome is the closed list of results of one run on one target.
type Outcome string

// The outcome values.
const (
	Succeeded        Outcome = "Succeeded"
	NoChange         Outcome = "NoChange"
	Failed           Outcome = "Failed"
	Incomplete       Outcome = "Incomplete"
	Unreachable      Outcome = "Unreachable"
	AuthFailed       Outcome = "AuthFailed"
	IdentityMismatch Outcome = "IdentityMismatch"
	NoAddress        Outcome = "NoAddress"
	Refused          Outcome = "Refused"
	WindowClosed     Outcome = "WindowClosed"
	Timeout          Outcome = "Timeout"
	Cancelled        Outcome = "Cancelled"
	Killed           Outcome = "Killed"
)

// Outcomes returns every outcome value, in the order of the contract.
func Outcomes() []Outcome {
	return []Outcome{Succeeded, NoChange, Failed, Incomplete, Unreachable, AuthFailed,
		IdentityMismatch, NoAddress, Refused, WindowClosed, Timeout, Cancelled, Killed}
}

// Valid reports whether o is one of the outcome values.
func (o Outcome) Valid() bool {
	for _, v := range Outcomes() {
		if o == v {
			return true
		}
	}
	return false
}

// MessageNoResult is the message of a run whose Job ended without a parsable document.
const MessageNoResult = "no-result"

// Document is the result document.
type Document struct {
	Schema      string       `json:"schema"`
	Outcome     Outcome      `json:"outcome"`
	Message     string       `json:"message"`
	HostKeys    []HostKey    `json:"hostKeys,omitempty"`
	Fingerprint *Fingerprint `json:"fingerprint,omitempty"`
	OS          *OS          `json:"os,omitempty"`
	Evidence    []Evidence   `json:"evidence,omitempty"`
	Hardening   *Hardening   `json:"hardening,omitempty"`
	Patch       *Patch       `json:"patch,omitempty"`
	Test        *Test        `json:"test,omitempty"`
	Completed   bool         `json:"completed"`
}

// HostKey is one SSH host key the target offered.
type HostKey struct {
	Type   string `json:"type"`
	SHA256 string `json:"sha256"`
}

// Fingerprint is the identity the target reported about itself.
type Fingerprint struct {
	FirmwareUUID string `json:"firmwareUUID"`
	InstanceUUID string `json:"instanceUUID"`
	Serial       string `json:"serial"`
	MachineID    string `json:"machineID"`
}

// OS is the operating system the target reported.
type OS struct {
	Family  string `json:"family"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Kernel  string `json:"kernel"`
}

// Evidence is one file the run stored in the evidence bucket. Key is the object key in
// that bucket and SHA256 the hex digest of the stored content.
type Evidence struct {
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

// Hardening is the summary of a hardening evaluation. ResultsKey is the object key of
// the file hardening.results.json.
type Hardening struct {
	ProfileID      string `json:"profileId"`
	ContentRelease string `json:"contentRelease"`
	Pass           int    `json:"pass"`
	Fail           int    `json:"fail"`
	NotApplicable  int    `json:"notApplicable"`
	Error          int    `json:"error"`
	ResultsKey     string `json:"resultsKey"`
}

// Patch is the summary of a patch run.
type Patch struct {
	PackagesChanged    int  `json:"packagesChanged"`
	RebootRequired     bool `json:"rebootRequired"`
	Rebooted           bool `json:"rebooted"`
	PendingAfter       int  `json:"pendingAfter"`
	HealthChecksPassed bool `json:"healthChecksPassed"`
}

// Test is the summary of a test run.
type Test struct {
	Findings   int    `json:"findings"`
	ResultsKey string `json:"resultsKey"`
}

// ErrTooLarge means the document is longer than MaxBytes even with an empty message.
var ErrTooLarge = errors.New("result document is longer than 4096 bytes without its message")

// ellipsis ends a shortened message.
const ellipsis = "..."

// Encode returns d as compact JSON of at most MaxBytes bytes, with the member schema
// set to Schema. A longer document is shortened in its message only: the message keeps
// its longest leading part, cut between two characters, that lets the document fit,
// followed by "...". It returns ErrTooLarge when the document does not fit with an
// empty message, and an error for an outcome that is not in the closed list.
func Encode(d Document) ([]byte, error) {
	if !d.Outcome.Valid() {
		return nil, fmt.Errorf("outcome %q is not one of the outcome values", d.Outcome)
	}
	d.Schema = Schema
	out, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(out) <= MaxBytes {
		return out, nil
	}
	message := d.Message
	runes := utf8.RuneCountInString(message)
	fits := func(n int) ([]byte, bool) {
		d.Message = prefix(message, n) + ellipsis
		if n == 0 {
			d.Message = ""
		}
		b, err := json.Marshal(d)
		return b, err == nil && len(b) <= MaxBytes
	}
	best, ok := fits(0)
	if !ok {
		return nil, ErrTooLarge
	}
	low, high := 0, runes // the longest n with fits(n) lies in [low, high)
	for low+1 < high {
		mid := (low + high) / 2
		if b, ok := fits(mid); ok {
			low, best = mid, b
		} else {
			high = mid
		}
	}
	return best, nil
}

// prefix returns the first n characters of s.
func prefix(s string, n int) string {
	i := 0
	for n > 0 && i < len(s) {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n--
	}
	return s[:i]
}

// Parse reads a termination message. It fails when the text is not one JSON object,
// when schema is not Schema or when outcome is not in the closed list.
func Parse(text []byte) (Document, error) {
	var d Document
	if err := json.Unmarshal(text, &d); err != nil {
		return Document{}, err
	}
	if d.Schema != Schema {
		return Document{}, fmt.Errorf("schema %q is not %q", d.Schema, Schema)
	}
	if !d.Outcome.Valid() {
		return Document{}, fmt.Errorf("outcome %q is not one of the outcome values", d.Outcome)
	}
	return d, nil
}

// OrNoResult returns the parsed document, or the document of a Job that ended without a
// parsable one: outcome Failed with message no-result.
func OrNoResult(text []byte) Document {
	d, err := Parse(text)
	if err != nil {
		return Document{Schema: Schema, Outcome: Failed, Message: MessageNoResult}
	}
	return d
}
