package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// example is a full configuration for kind database (ADR-0033).
const example = `{"schema": "openova.io/access-session-config/v1",
 "sessionUID": "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11",
 "kind": "database",
 "subject": "jane.roe@example.org",
 "protocol": "kubernetes",
 "params": {"hostname": "k8s-ws-proxy.catalyst-system.svc.cluster.local", "port": "8443", "use-ssl": "true",
            "namespace": "acme", "pod": "access-s-01jce1q7v8", "container": "tool",
            "exec-command": "/usr/bin/psql -h 127.0.0.1 -p 6432 -U session billing"},
 "paramFiles": {"client-cert": "exec-cert", "client-key": "exec-key", "ca-cert": "exec-ca"},
 "controls": {"keystrokes": false, "clipboardCopy": false, "clipboardPaste": true},
 "terminal": true,
 "ingest": "http://access-controller.guacamole.svc.cluster.local:8081",
 "chunkBytes": 1048576,
 "chunkSeconds": 5}`

func TestParseExample(t *testing.T) {
	c, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if c.SessionUID != "5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11" || c.Kind != "database" || c.Protocol != "kubernetes" ||
		c.Params["container"] != "tool" || c.ParamFiles["ca-cert"] != "exec-ca" || !c.Controls.ClipboardPaste ||
		c.Controls.ClipboardCopy || !c.Terminal || c.ChunkBytes != 1048576 || c.ChunkSeconds != 5 || !c.Recording() {
		t.Fatalf("parsed %+v", c)
	}
}

// edit returns the example with fn applied to its top-level object.
func edit(t *testing.T, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(example), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseRefuses(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"missing field", edit(t, func(m map[string]any) { delete(m, "chunkSeconds") }), "missing field chunkSeconds"},
		{"missing control", edit(t, func(m map[string]any) { delete(m["controls"].(map[string]any), "keystrokes") }), "missing field controls.keystrokes"},
		{"unknown field", edit(t, func(m map[string]any) { m["token"] = "x" }), "unknown field token"},
		{"unknown control", edit(t, func(m map[string]any) { m["controls"].(map[string]any)["print"] = true }), "unknown field controls.print"},
		{"another case", edit(t, func(m map[string]any) { m["SessionUID"] = m["sessionUID"]; delete(m, "sessionUID") }), "unknown field SessionUID"},
		{"null", edit(t, func(m map[string]any) { m["terminal"] = nil }), "field terminal is null"},
		{"text after", []byte(example + " {}"), "text after its object"},
		{"not an object", []byte(`[1]`), "not a JSON object"},
		{"schema", edit(t, func(m map[string]any) { m["schema"] = "openova.io/access-session-config/v2" }), "schema is"},
		{"uid", edit(t, func(m map[string]any) { m["sessionUID"] = "5B0F6F0E-6F1E-4C58-9D0C-0D6B1F1F3A11" }), "not a lower-case UUID"},
		{"kind", edit(t, func(m map[string]any) { m["kind"] = "web" }), `kind "web" is not one of`},
		{"subject", edit(t, func(m map[string]any) { m["subject"] = "jane roe" }), "not an e-mail address"},
		{"protocol", edit(t, func(m map[string]any) { m["protocol"] = "http" }), `protocol "http"`},
		{"agent parameter", edit(t, func(m map[string]any) { m["params"].(map[string]any)["recording-path"] = "/tmp" }), `names "recording-path"`},
		{"agent parameter as file", edit(t, func(m map[string]any) { m["paramFiles"].(map[string]any)["disable-copy"] = "x" }), `names "disable-copy"`},
		{"recording family", edit(t, func(m map[string]any) { m["params"].(map[string]any)["recording-exclude-output"] = "true" }), `names "recording-exclude-output"`},
		{"typescript family", edit(t, func(m map[string]any) { m["params"].(map[string]any)["typescript-write-existing"] = "true" }), `names "typescript-write-existing"`},
		{"create path family", edit(t, func(m map[string]any) { m["paramFiles"].(map[string]any)["create-drive-path"] = "x" }), `names "create-drive-path"`},
		{"twice", edit(t, func(m map[string]any) { m["params"].(map[string]any)["ca-cert"] = "x" }), `"ca-cert" is in params and in paramFiles`},
		{"file path", edit(t, func(m map[string]any) { m["paramFiles"].(map[string]any)["ca-cert"] = "../token" }), `the file "../token"`},
		{"not a string", edit(t, func(m map[string]any) { m["params"].(map[string]any)["port"] = 8443 }), "does not decode"},
		{"https ingest", edit(t, func(m map[string]any) { m["ingest"] = "https://access-controller:8081" }), "is not the http base address"},
		{"ingest path", edit(t, func(m map[string]any) { m["ingest"] = "http://access-controller:8081/internal" }), "is not the http base address"},
		{"chunk too big", edit(t, func(m map[string]any) { m["chunkBytes"] = MaxChunkBytes + 1 }), "chunkBytes 4194305"},
		{"no chunk time", edit(t, func(m map[string]any) { m["chunkSeconds"] = 0 }), "chunkSeconds 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error with %q", err, tc.want)
			}
		})
	}
}

func TestRecordingOff(t *testing.T) {
	c, err := Parse(edit(t, func(m map[string]any) { m["chunkBytes"], m["chunkSeconds"] = 0, 0 }))
	if err != nil {
		t.Fatal(err)
	}
	if c.Recording() {
		t.Fatal("chunkBytes 0 records")
	}
}

func TestLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if _, err := Load(p); err == nil {
		t.Fatal("a missing file loaded")
	}
	if err := os.WriteFile(p, []byte(example), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(p); err != nil || c.Subject != "jane.roe@example.org" {
		t.Fatalf("got %+v, %v", c, err)
	}
}
