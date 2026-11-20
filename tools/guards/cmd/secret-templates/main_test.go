package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A template of references with labels, annotations and immutable: it passes.
const template = `# A comment above the manifest.
apiVersion: v1
kind: Secret
metadata:
  name: harbor-robot-token
  namespace: flux-system
  labels:
    catalyst.openova.io/sovereign: ${SOVEREIGN_FQDN}
  annotations:
    # an annotation that holds a value is not judged here
    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "catalyst-system"
type: Opaque
immutable: false
data:
  token: "${HARBOR_ROBOT_TOKEN}"
  plain: ${PLAIN_1}
  single: '${SINGLE_2}'
stringData:
  POWERDNS_API_KEY: ${POWERDNS_API_KEY}
`

// secret builds a Secret manifest with the given body after kind and metadata.
func secret(body string) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: x\n  namespace: flux-system\ntype: Opaque\n" + body
}

func TestJudgeFile(t *testing.T) {
	cases := []struct {
		name string
		text string
		pass bool
	}{
		{"a template with labels, annotations and immutable", template, true},
		{"a template with only stringData", secret("stringData:\n  a: ${A}\n"), true},
		{"a template in a file with a ConfigMap", "apiVersion: v1\nkind: ConfigMap\ndata:\n  a: literal\n---\n" + secret("data:\n  a: ${A}\n"), true},
		{"two templates", secret("data:\n  a: ${A}\n") + "---\n" + secret("data:\n  b: ${B}\n"), true},
		{"a template after a separator with a comment", "--- # first\n" + secret("data:\n  a: ${A}\n"), true},

		// The cases the rule must fail.
		{"a literal value", secret("data:\n  token: c2VjcmV0\n"), false},
		{"a quoted literal value", secret("stringData:\n  token: \"secret\"\n"), false},
		{"text before a reference", secret("data:\n  token: x${A}\n"), false},
		{"text after a reference", secret("data:\n  token: ${A}x\n"), false},
		{"two references", secret("data:\n  token: ${A}${B}\n"), false},
		{"a default with :=", secret("data:\n  token: ${A:=x}\n"), false},
		{"a default with :-", secret("data:\n  token: ${A:-x}\n"), false},
		{"an empty value", secret("data:\n  token:\n"), false},
		{"an empty quoted value", secret("stringData:\n  token: \"\"\n"), false},
		{"an empty reference", secret("data:\n  token: ${}\n"), false},
		{"no entry under data and stringData", secret(""), false},
		{"an empty data block", secret("data:\n"), false},
		{"data as a flow mapping", secret("data: {token: \"${A}\"}\n"), false},
		{"a field outside the list", secret("data:\n  a: ${A}\nbinaryData:\n  b: c2VjcmV0\n"), false},
		{"a field outside the list that holds a reference", secret("data:\n  a: ${A}\nextra: ${B}\n"), false},
		{"a name in small letters", secret("data:\n  token: ${a}\n"), false},
		{"a variable without braces", secret("data:\n  token: $A\n"), false},
		{"a comment after the value", secret("data:\n  token: ${A} # the token\n"), false},
		{"a block scalar", secret("data:\n  token: |\n    c2VjcmV0\n"), false},
		{"a nested value under an entry", secret("data:\n  token:\n    x: ${A}\n"), false},
		{"an entry at another indent", secret("data:\n  a: ${A}\n    b: ${B}\n"), false},
		{"an alias", secret("data:\n  token: *value\n"), false},
		{"a merge key", secret("data:\n  <<: *values\n"), false},
		{"a tag", secret("data:\n  token: !!binary ${A}\n"), false},
		{"a tab", secret("data:\n  token:\t${A}\n"), false},
		{"a carriage return", secret("data:\n  token: ${A}\r\n"), false},
		{"data twice", secret("data:\n  a: ${A}\ndata:\n  b: literal\n"), false},
		{"one key twice", secret("data:\n  a: ${A}\n  a: literal\n"), false},
		{"type over two lines", "apiVersion: v1\nkind: Secret\ntype: Opaque\n  more\ndata:\n  a: ${A}\n", false},
		{"a quoted field key", secret("\"data\":\n  a: literal\n"), false},
		{"a template beside a Secret with a literal", secret("data:\n  a: ${A}\n") + "---\n" + secret("data:\n  b: literal\n"), false},
		{"a line kind: Secret inside a ConfigMap", "apiVersion: v1\nkind: ConfigMap\ndata:\n  x: |\n    kind: Secret\n---\n" + secret("data:\n  a: ${A}\n"), false},
		{"a quoted kind with a literal", "apiVersion: v1\nkind: \"Secret\"\ndata:\n  a: literal\n---\n" + secret("data:\n  a: ${A}\n"), false},
		{"no Secret at all", "apiVersion: v1\nkind: ConfigMap\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why := judgeFile(c.text)
			if (why == "") != c.pass {
				t.Errorf("judgeFile passes=%v (%q), want passes=%v", why == "", why, c.pass)
			}
		})
	}
}

// The reason names the line and what is wrong with it.
func TestReason(t *testing.T) {
	why := judgeFile(secret("data:\n  a: ${A}\n  token: literal\n"))
	if !strings.Contains(why, "data.token (line 9) is not exactly one reference") {
		t.Errorf("reason %q", why)
	}
}

func TestRunOnATree(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"clusters/a/template.yaml": template,
		"clusters/a/literal.yaml":  secret("data:\n  token: c2VjcmV0\n"),
		"clusters/a/config.yaml":   "apiVersion: v1\nkind: ConfigMap\n",
		"platform/x/secret.yaml":   template, // outside clusters/: not G1's
	}
	for p, c := range files {
		write(t, root, p, c)
	}
	gitInit(t, root)
	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errw.String())
	}
	if got := out.String(); got != "clusters/a/template.yaml\n" {
		t.Errorf("listed %q, want only clusters/a/template.yaml", got)
	}
	if !strings.Contains(errw.String(), "clusters/a/literal.yaml is not a template of references: the Secret of line 2: data.token (line 8)") {
		t.Errorf("stderr %q does not say why the literal is not a template", errw.String())
	}
}

// A tree without a file under clusters/ measures nothing: exit 2, never an
// empty list that would read as a pass.
func TestNothingReadIsNotAPass(t *testing.T) {
	root := t.TempDir()
	write(t, root, "platform/x/secret.yaml", template)
	gitInit(t, root)
	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 2 || out.Len() != 0 {
		t.Errorf("exit %d, out %q, want exit 2 and no output", code, out.String())
	}
}

func TestUnknownArgument(t *testing.T) {
	var out, errw bytes.Buffer
	if code := run([]string{"clusters"}, &out, &errw); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
}

func write(t *testing.T, root, p, content string) {
	t.Helper()
	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}
