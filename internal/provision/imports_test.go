package provision

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The package has two consumers: a controller, which runs inside a cluster,
// and a command, which runs when no cluster exists. If Kubernetes gets in
// here, the command stops being buildable and the cold start stops being
// answerable.
//
// This test only parses this package's own files' import statements, which
// is direct imports only: a future dependency that does not itself start
// with "k8s.io/" or "sigs.k8s.io/" but pulls either in transitively (some
// unrelated helper library that happens to vendor client-go, say) would
// satisfy every check here and still break the command the day something
// tries to build it. TestProvisionTransitivelyImportsNothingFromKubernetes
// below is what actually closes that gap, via `go list -deps`; this test is
// kept beside it because it is deterministic and needs no toolchain
// invocation, catching the common case (a direct import) fast.
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

// TestProvisionTransitivelyImportsNothingFromKubernetes closes the gap
// TestProvisionImportsNothingFromKubernetes's own comment names above: `go
// list -deps` walks this package's actual, resolved dependency graph --
// every package anything here imports, directly or through another
// dependency -- so a k8s.io/sigs.k8s.io import arriving by way of a third
// package this one pulls in cannot hide from it the way it could from a
// parse of this directory's own source files.
//
// This shells out to the `go` toolchain rather than using go/packages
// (golang.org/x/tools), which would be a new module dependency; `go list`
// is already required to build anything in this repository.
func TestProvisionTransitivelyImportsNothingFromKubernetes(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list -deps .: %v\n%s", err, stderr.String())
	}

	var forbidden []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "k8s.io/") || strings.HasPrefix(line, "sigs.k8s.io/") ||
			strings.HasPrefix(line, "github.com/rmocq/frame/api/") ||
			strings.HasPrefix(line, "github.com/rmocq/frame/internal/controller") {
			forbidden = append(forbidden, line)
		}
	}
	if len(forbidden) > 0 {
		t.Errorf("internal/provision transitively depends on: %v", forbidden)
	}
}
