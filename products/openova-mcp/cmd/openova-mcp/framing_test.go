package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// TestReadFrame_NDJSON pins the #4111 fix: a newline-delimited JSON message
// (the MCP stdio spec — what claude-code + the official SDKs send) is parsed
// as one frame and reported as line-delimited so the reply mirrors it.
func TestReadFrame_NDJSON(t *testing.T) {
	const msg = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	r := bufio.NewReader(strings.NewReader(msg + "\n"))
	body, lineMode, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame NDJSON: %v", err)
	}
	if !lineMode {
		t.Error("NDJSON message must report lineMode=true")
	}
	if string(body) != msg {
		t.Errorf("body = %q, want %q", body, msg)
	}
}

// TestReadFrame_NDJSON_NoTrailingNewline — a final NDJSON line at EOF without
// a trailing '\n' is still a complete message.
func TestReadFrame_NDJSON_NoTrailingNewline(t *testing.T) {
	const msg = `{"jsonrpc":"2.0","id":2,"method":"ping"}`
	r := bufio.NewReader(strings.NewReader(msg))
	body, lineMode, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame NDJSON no-newline: %v", err)
	}
	if !lineMode || string(body) != msg {
		t.Errorf("got lineMode=%v body=%q, want true / %q", lineMode, body, msg)
	}
}

// TestReadFrame_ContentLength pins backwards-compat: an LSP Content-Length
// frame still parses and reports lineMode=false.
func TestReadFrame_ContentLength(t *testing.T) {
	const msg = `{"jsonrpc":"2.0","id":3,"method":"ping"}`
	framed := "Content-Length: " + itoa(len(msg)) + "\r\n\r\n" + msg
	r := bufio.NewReader(strings.NewReader(framed))
	body, lineMode, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame Content-Length: %v", err)
	}
	if lineMode {
		t.Error("Content-Length message must report lineMode=false")
	}
	if string(body) != msg {
		t.Errorf("body = %q, want %q", body, msg)
	}
}

// TestReadFrame_SkipsBlankSeparators — stray blank lines between NDJSON
// frames are skipped, not treated as empty messages.
func TestReadFrame_SkipsBlankSeparators(t *testing.T) {
	const msg = `{"jsonrpc":"2.0","id":4,"method":"ping"}`
	r := bufio.NewReader(strings.NewReader("\n\r\n" + msg + "\n"))
	body, lineMode, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame blank-prefixed: %v", err)
	}
	if !lineMode || string(body) != msg {
		t.Errorf("got lineMode=%v body=%q", lineMode, body)
	}
}

// TestWriteFrame_MirrorsInboundFraming — writeFrame emits NDJSON when the
// peer spoke NDJSON, Content-Length otherwise.
func TestWriteFrame_MirrorsInboundFraming(t *testing.T) {
	// NDJSON reply.
	var ndjson bytes.Buffer
	s := &server{out: &ndjson, lineDelimited: true}
	if err := s.writeResult(jsonNum(1), map[string]any{"pong": true}); err != nil {
		t.Fatalf("writeResult NDJSON: %v", err)
	}
	out := ndjson.String()
	if strings.Contains(out, "Content-Length") {
		t.Errorf("NDJSON reply must not carry Content-Length header: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("NDJSON reply must end with newline: %q", out)
	}

	// Content-Length reply.
	var lsp bytes.Buffer
	s2 := &server{out: &lsp, lineDelimited: false}
	if err := s2.writeResult(jsonNum(2), map[string]any{"pong": true}); err != nil {
		t.Fatalf("writeResult Content-Length: %v", err)
	}
	if !strings.HasPrefix(lsp.String(), "Content-Length: ") {
		t.Errorf("LSP reply must start with Content-Length header: %q", lsp.String())
	}
}

// itoa is a tiny local helper to avoid importing strconv just for tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// jsonNum returns a JSON-RPC id as raw JSON.
func jsonNum(n int) []byte { return []byte(itoa(n)) }
