package provision

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The package has two consumers: a controller, which runs inside a cluster,
// and a command, which runs when no cluster exists. If Kubernetes gets in
// here, the command stops being buildable and the cold start stops being
// answerable -- and nothing else in the tree would turn red the day it
// happens.
//
// parser.ParseDir is not used here even though it does the job in one call:
// it is deprecated, and its all-files-in-one-map return let an earlier
// version of this test pass silently when pointed at an empty or wrong
// directory -- nothing asserted that any file was actually found, so
// "nothing objectionable found" and "nothing looked at" were
// indistinguishable. Listing the directory and parsing each file keeps that
// visible: install.go is asserted present in the files actually parsed,
// below.
func TestProvisionImportsNothingFromKubernetes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	var parsed []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, name)
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "k8s.io/") || strings.HasPrefix(p, "sigs.k8s.io/") ||
				strings.HasPrefix(p, "github.com/rmocq/frame/api/") ||
				strings.HasPrefix(p, "github.com/rmocq/frame/internal/controller") {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}

	// Without this, pointing the scan at an empty or nonexistent directory --
	// or a future refactor that changes the working directory tests run
	// from -- makes the test pass by finding nothing to object to, which is
	// indistinguishable from finding nothing objectionable.
	if !contains(parsed, "install.go") {
		t.Fatalf("install.go was never parsed (parsed: %v); this test proves nothing if it never sees the file it exists to police", parsed)
	}
}
