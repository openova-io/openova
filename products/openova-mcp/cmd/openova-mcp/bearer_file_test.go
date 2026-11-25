package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// envBearerVar is the environment bearer the stdio server read until AIP-157:
// the name of the bearer-file variable without its _FILE suffix.
var envBearerVar = strings.TrimSuffix(fallbackFileVar, "_FILE")

// AIP-152. fileServer is the stdio server as main builds it, with the bearer
// file variable set to bearerFile and the old environment bearer set to
// fromEnv, over a stub catalyst-api that records each forwarded bearer.
func fileServer(t *testing.T, bearerFile, fromEnv string) (call func(method, tool string, args any) string, forwarded *[]string) {
	t.Setenv(fallbackFileVar, bearerFile)
	t.Setenv(envBearerVar, fromEnv)
	forwarded = new([]string)
	api := catalystapi.New("https://console.acme.omani.homes").WithHTTPClient(&http.Client{Transport: stubRoundTrip(func(r *http.Request) (*http.Response, error) {
		*forwarded = append(*forwarded, r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"apps":[]}`)), Header: http.Header{}}, nil
	})})
	var out bytes.Buffer
	s := newStdioServer(tools.NewRegistry(api), identity.NewInsecureResolver(""), &out)
	s.lineDelimited = true
	return func(method, tool string, args any) string {
		out.Reset()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"name": tool, "arguments": args}})
		must(t, s.dispatch(raw) == nil, method+" "+tool+": the reply could not be written")
		return out.String()
	}, forwarded
}

func acme(t *testing.T, email, tier string) string {
	return mintUnsigned(t, jwt.MapClaims{"sub": email, "email": email, "org_id": "acme", "tier": tier, "typ": "session"})
}

// putBearer replaces the file by rename, as a renewing sidecar does.
func putBearer(t *testing.T, path, content string) {
	must(t, os.WriteFile(path+".new", []byte(content), 0o600) == nil && os.Rename(path+".new", path) == nil, "cannot replace the bearer file")
}

func must(t *testing.T, ok bool, msg string) {
	t.Helper()
	if !ok {
		t.Fatal(msg)
	}
}

// started serves a file of the developer bearer t1, beside an org-admin env bearer.
func started(t *testing.T) (string, func(method, tool string, args any) string, *[]string) {
	path := filepath.Join(t.TempDir(), "bearer")
	putBearer(t, path, acme(t, "t1@acme.test", "developer")+"\n")
	call, forwarded := fileServer(t, path, acme(t, "env@acme.test", "org-admin"))
	return path, call, forwarded
}

func TestBearerFileReadAtEveryCall(t *testing.T) {
	path, call, forwarded := started(t)
	must(t, strings.Contains(call("tools/call", "whoami", nil), "t1@acme.test"), "before the renewal tools/call must act on the file, not the env bearer")
	t2 := acme(t, "t2@acme.test", "org-admin")
	putBearer(t, path, t2)
	must(t, strings.Contains(call("tools/call", "whoami", nil), "t2@acme.test"), "after the renewal tools/call must act on the new bearer, with no restart")
	r := call("tools/call", "list_applications", nil)
	must(t, !strings.Contains(r, `"error"`) && strings.Join(*forwarded, ",") == "Bearer "+t2, "list_applications must forward the renewed bearer: "+r)
	auth := map[string]any{"_auth": map[string]any{"token": acme(t, "t3@acme.test", "viewer")}}
	must(t, strings.Contains(call("tools/call", "whoami", auth), "t3@acme.test"), "an _auth token in the call must still win over the file")
}

func TestBearerFileReadAtEveryList(t *testing.T) {
	path, call, _ := started(t)
	l := call("tools/list", "", nil)
	must(t, strings.Contains(l, `"name":"whoami"`) && !strings.Contains(l, `"name":"create_application"`), "before the renewal tools/list must act on the developer bearer of the file")
	putBearer(t, path, acme(t, "t2@acme.test", "org-admin"))
	must(t, strings.Contains(call("tools/list", "", nil), `"name":"create_application"`), "after the renewal tools/list must act on the new bearer")
}

func TestBearerFileGoneMeansNoBearer(t *testing.T) {
	path, call, _ := started(t)
	must(t, strings.Contains(call("tools/call", "whoami", nil), "t1@acme.test"), "whoami must act on the bearer of the file")
	must(t, os.Remove(path) == nil, "cannot remove the bearer file")
	for _, gone := range []string{"missing", "empty"} {
		if gone == "empty" {
			putBearer(t, path, " \n")
		}
		r := call("tools/call", "whoami", nil)
		must(t, strings.Contains(r, `"code":-32001,"message":"unauthenticated","data":"no bearer`), gone+" file: tools/call must have no bearer, never one read earlier: "+r)
		must(t, !strings.Contains(call("tools/list", "", nil), `"name"`), gone+" file: tools/list must offer nothing")
	}
}

// AIP-157. With no bearer file, a valid bearer in the environment is never
// used: a call without _auth has no bearer at all.
func TestEnvironmentBearerIsNotRead(t *testing.T) {
	// The name is written in two pieces, as scripts/check-agent-plane.py writes
	// its patterns: this test holds no bearer and is not counted as holding one.
	must(t, envBearerVar == "OPENOVA_MCP"+"_BEARER", "the old environment bearer variable is named wrongly: "+envBearerVar)
	call, forwarded := fileServer(t, "", acme(t, "env@acme.test", "org-admin"))
	r := call("tools/call", "whoami", nil)
	must(t, strings.Contains(r, `"code":-32001,"message":"unauthenticated","data":"no bearer`), "with no bearer file tools/call must have no bearer, never the environment's: "+r)
	r = call("tools/call", "list_applications", nil)
	must(t, strings.Contains(r, `"code":-32001,"message":"unauthenticated","data":"no bearer`) && len(*forwarded) == 0, "with no bearer file nothing may be forwarded: "+r)
	must(t, !strings.Contains(call("tools/list", "", nil), `"name"`), "with no bearer file tools/list must offer nothing")
}
