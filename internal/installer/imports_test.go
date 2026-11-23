package installer

import (
	"os/exec"
	"strings"
	"testing"
)

// The guided installer runs on a machine that has no cluster: nothing from
// Kubernetes, and nothing outside the standard library, this module and the
// modules internal/provision already depends on.
func TestInstallerImportsNothingFromKubernetes(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "../../cmd/installer").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for p := range strings.FieldsSeq(string(out)) {
		if strings.HasPrefix(p, "k8s.io/") || strings.HasPrefix(p, "sigs.k8s.io/") ||
			strings.HasPrefix(p, "github.com/Plume-Labs/frame/api/") ||
			strings.HasPrefix(p, "github.com/Plume-Labs/frame/internal/controller") {
			t.Errorf("cmd/installer depends on %s", p)
		}
	}
}
