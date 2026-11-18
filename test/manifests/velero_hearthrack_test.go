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
	for doc := range strings.SplitSeq(string(raw), "\n---") {
		var s struct {
			APIVersion string `json:"apiVersion"`
			Kind       string `json:"kind"`
			Metadata   struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Schedule string `json:"schedule"`
				Template struct {
					IncludedNamespaces []string `json:"includedNamespaces"`
					IncludedResources  []string `json:"includedResources"`
					SnapshotVolumes    bool     `json:"snapshotVolumes"`
					StorageLocation    string   `json:"storageLocation"`
					TTL                string   `json:"ttl"`
				} `json:"template"`
			} `json:"spec"`
		}
		// Strict: a misspelled field would otherwise be silently ignored by this
		// test and by nobody else until the first restore.
		if err := yaml.UnmarshalStrict([]byte(doc), &s); err != nil {
			t.Fatal(err)
		}
		if s.APIVersion != "velero.io/v1" || s.Kind != "Schedule" || s.Metadata.Namespace != "velero" || s.Spec.Schedule == "" {
			t.Errorf("unexpected document %q: want a velero Schedule with a cron expression", s.Metadata.Name)
		}
		// Cluster-scoped kinds (framestorages, inferencemodels...) are only
		// backed up when every namespace is included.
		if !Has(s.Spec.Template.IncludedNamespaces, "*") {
			t.Errorf("%s must include namespace \"*\" so namespaced and cluster-scoped kinds are both saved",
				s.Metadata.Name)
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
