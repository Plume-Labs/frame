package manifests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestVeleroBacksUpEveryHearthrackKind keeps deploy/resilience/velero-hearthrack.yaml
// complete: every CRD shipped in config/crd/bases must be listed in a Velero
// schedule, so a new kind cannot reach a cluster that has no backup of it.
func TestVeleroBacksUpEveryHearthrackKind(t *testing.T) {
	root := Root(t)

	raw, err := os.ReadFile(filepath.Join(root, "deploy/resilience/velero-hearthrack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, doc := range strings.SplitSeq(string(raw), "\n---") {
		var s struct {
			Spec struct {
				Template struct {
					IncludedResources []string `json:"includedResources"`
				} `json:"template"`
			} `json:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
			t.Fatal(err)
		}
		for _, r := range s.Spec.Template.IncludedResources {
			listed[r] = true
		}
	}

	crds, err := filepath.Glob(filepath.Join(root, "config/crd/bases/*.yaml"))
	if err != nil || len(crds) == 0 {
		t.Fatalf("no CRDs found (err=%v): the scan is reading nothing", err)
	}
	for _, f := range crds {
		// File names are <group>_<plural>.yaml.
		group, plural, ok := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".yaml"), "_")
		if !ok {
			t.Fatalf("unexpected CRD file name %s", f)
		}
		if !listed[plural+"."+group] {
			t.Errorf("%s.%s is missing from deploy/resilience/velero-hearthrack.yaml: no backup of its objects",
				plural, group)
		}
	}
}
