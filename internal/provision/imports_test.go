package provision

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The package has two consumers: a controller, which runs inside a cluster,
// and a command, which runs when no cluster exists. If Kubernetes gets in
// here, the command stops being buildable and the cold start stops being
// answerable -- and nothing else in the tree would turn red the day it
// happens.
func TestProvisionImportsNothingFromKubernetes(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(p, "k8s.io/") || strings.HasPrefix(p, "sigs.k8s.io/") ||
					strings.HasPrefix(p, "github.com/rmocq/frame/api/") ||
					strings.HasPrefix(p, "github.com/rmocq/frame/internal/controller") {
					t.Errorf("%s imports %s", name, p)
				}
			}
		}
	}
}
