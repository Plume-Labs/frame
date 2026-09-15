package manifests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The receiver is only reachable if three files agree on one port: the
// manager's flag, the container port the Service targets, and the
// NetworkPolicy that lets Alertmanager in. And a policy that selects the
// manager pod isolates it for every port — the conversion webhook (9443),
// metrics (8443) and probes (8081) must stay explicitly open or every CRD
// read in the cluster fails.
func TestAlertReceiverPortAgreesAcrossManifests(t *testing.T) {
	root := Root(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	manager := read("config/manager/manager.yaml")
	service := read("config/manager/alert_receiver_service.yaml")
	policy := read("deploy/kubernetes/containment/networkpolicy-frame-alert-receiver.yaml")

	for name, want := range map[string]string{
		"manager flag":         "--alert-receiver-bind-address=:8445",
		"manager port":         "containerPort: 8445",
		"service target":       "targetPort: alert-receiver",
		"policy receiver port": "port: 8445",
		"policy webhook port":  "port: 9443",
		"policy metrics port":  "port: 8443",
		"policy probe port":    "port: 8081",
	} {
		haystack := map[string]string{
			"manager flag": manager, "manager port": manager, "service target": service,
			"policy receiver port": policy, "policy webhook port": policy,
			"policy metrics port": policy, "policy probe port": policy,
		}[name]
		if !strings.Contains(haystack, want) {
			t.Errorf("%s: %q missing", name, want)
		}
	}
}
