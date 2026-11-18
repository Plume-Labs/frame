package manifests

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

// A PodDisruptionBudget on a single replica blocks every node drain forever, and
// one replica is exactly the setup the production overlay exists to leave: the
// overlay must keep the two settings consistent.
func TestProductionValuesAreHighlyAvailable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(Root(t), "charts/frame/values-production.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ReplicaCount        int `json:"replicaCount"`
		PodDisruptionBudget struct {
			Enabled      bool `json:"enabled"`
			MinAvailable int  `json:"minAvailable"`
		} `json:"podDisruptionBudget"`
	}
	if err := yaml.UnmarshalStrict(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.ReplicaCount < 2 {
		t.Errorf("replicaCount = %d: one replica means every restart is an admission outage", v.ReplicaCount)
	}
	if !v.PodDisruptionBudget.Enabled {
		t.Error("the PodDisruptionBudget must be enabled with more than one replica")
	}
	if v.PodDisruptionBudget.MinAvailable >= v.ReplicaCount {
		t.Errorf("minAvailable %d >= replicaCount %d would block every drain",
			v.PodDisruptionBudget.MinAvailable, v.ReplicaCount)
	}
}
