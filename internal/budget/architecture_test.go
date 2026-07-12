package budget

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageHasNoProviderTokenOrReportImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, "\"")
			if strings.Contains(p, "/internal/local") || strings.Contains(p, "/internal/reports") || strings.Contains(p, "token") {
				t.Fatalf("forbidden import %q in %s", p, name)
			}
		}
	}
}
