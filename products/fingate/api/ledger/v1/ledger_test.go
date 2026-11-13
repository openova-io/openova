package ledgerv1_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
)

var (
	comment = regexp.MustCompile(`//[^\n]*`)
	rpcRE   = regexp.MustCompile(`rpc\s+(\w+)\s*\(\s*(\w+)\s*\)\s*returns\s*\(\s*(\w+)\s*\)\s*;`)
	msgRE   = regexp.MustCompile(`message\s+(\w+)\s*\{([^}]*)\}`)
	fieldRE = regexp.MustCompile(`(?:repeated\s+)?(?:map<[^>]+>|[\w.]+)\s+(\w+)\s*=\s*(\d+)\s*;`)
)

// TestGeneratedCodeMatchesTheProtoFile fails when ledger.proto was changed and the Go code was
// not generated again: it compares every operation, message and field number of the file with
// the descriptor compiled into ledger.pb.go.
func TestGeneratedCodeMatchesTheProtoFile(t *testing.T) {
	b, err := os.ReadFile("ledger.proto")
	if err != nil {
		t.Fatal(err)
	}
	src := comment.ReplaceAllString(string(b), "")

	var fromFile, fromCode []string
	for _, m := range rpcRE.FindAllStringSubmatch(src, -1) {
		fromFile = append(fromFile, fmt.Sprintf("rpc %s(%s) %s", m[1], m[2], m[3]))
	}
	for _, m := range msgRE.FindAllStringSubmatch(src, -1) {
		fromFile = append(fromFile, "message "+m[1])
		for _, f := range fieldRE.FindAllStringSubmatch(m[2], -1) {
			fromFile = append(fromFile, fmt.Sprintf("field %s.%s=%s", m[1], f[1], f[2]))
		}
	}

	fd := ledgerv1.File_api_ledger_v1_ledger_proto
	if got := string(fd.Package()); got != "openova.fingate.ledger.v1" {
		t.Fatalf("package %s, want openova.fingate.ledger.v1", got)
	}
	methods := fd.Services().ByName("Ledger").Methods()
	for i := 0; i < methods.Len(); i++ {
		m := methods.Get(i)
		fromCode = append(fromCode, fmt.Sprintf("rpc %s(%s) %s", m.Name(), m.Input().Name(), m.Output().Name()))
	}
	msgs := fd.Messages()
	for i := 0; i < msgs.Len(); i++ {
		m := msgs.Get(i)
		fromCode = append(fromCode, "message "+string(m.Name()))
		for j := 0; j < m.Fields().Len(); j++ {
			f := m.Fields().Get(j)
			fromCode = append(fromCode, fmt.Sprintf("field %s.%s=%d", m.Name(), f.Name(), f.Number()))
		}
	}

	slices.Sort(fromFile)
	slices.Sort(fromCode)
	if !slices.Equal(fromFile, fromCode) {
		t.Fatalf("ledger.proto and the generated code differ; generate the code again as ledger.proto says\nfile:\n%s\ncode:\n%s",
			strings.Join(fromFile, "\n"), strings.Join(fromCode, "\n"))
	}
	if methods.Len() != 14 {
		t.Fatalf("the port has %d operations, want 14", methods.Len())
	}
}
