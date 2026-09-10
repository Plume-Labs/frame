package manifests

import "testing"

// workloadRoleNames is the pair of ClusterRoles that may write a pod
// template: restart/scale (cluster-control-workload-operator) and the YAML
// editor (cluster-control-workload-admin). Both are namespaced by design —
// see rbac-workload-operator.yaml's own header — because Pod Security is only
// enforced on the seven application namespaces in
// deploy/kubernetes/pod-security/namespaces.yaml, and a cluster-wide grant of
// either role would reach every namespace Pod Security does not (and cannot)
// cover: rook-ceph, kube-system, monitoring, and the rest of that file's own
// exclusion list.
var workloadRoleNames = []string{
	"cluster-control-workload-operator",
	"cluster-control-workload-admin",
}

// TestWorkloadRolesAreNeverBoundClusterWide scans every YAML manifest shipped
// under deploy/ for a ClusterRoleBinding to either workload role.
//
// This was the whole-branch review's deferred finding, upgraded after
// Critical 1 (TestGrantIsPairedWithItsControl, pod_security_wiring_test.go)
// showed the delivery path already failed once to pair this grant with its
// control: rbac-workload-operator.yaml shipped for a whole review cycle with
// nothing rendering the Pod Security enforcement it depends on, and nothing
// in the repository would have caught it. A ClusterRoleBinding to either
// workload role is the same class of failure by a different route — it
// reaches namespaces the enforcement in pod-security/namespaces.yaml was
// never meant to, and does not, cover — and nothing else in this package
// would have caught that either.
func TestWorkloadRolesAreNeverBoundClusterWide(t *testing.T) {
	root := Root(t)
	files := AllYAMLFiles(t, root+"/deploy")

	forbidden := map[string]bool{}
	for _, name := range workloadRoleNames {
		forbidden[name] = true
	}

	// A sanity check on the scan itself: deploy/ ships plenty of
	// ClusterRoleBindings (Velero, Cilium, the tier-aggregated roles in
	// rbac-tier-bindings.yaml, ...). Finding none would mean this is reading
	// nothing, not that the cluster is clean.
	found := 0
	for _, path := range files {
		for name, crb := range ClusterRoleBindings(t, path) {
			found++
			if forbidden[crb.RoleRef.Name] {
				t.Errorf("%s: ClusterRoleBinding %q binds %s cluster-wide — this grants pod-template "+
					"writes in every namespace, including the ones Pod Security cannot enforce, which is "+
					"the escalation the namespaced RoleBinding design in rbac-workload-operator.yaml "+
					"exists to prevent", path, name, crb.RoleRef.Name)
			}
		}
	}
	if found == 0 {
		t.Fatalf("parsed no ClusterRoleBinding out of %d YAML files under %s/deploy — the scan is not "+
			"reading what it claims", len(files), root)
	}
}
