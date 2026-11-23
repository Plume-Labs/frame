package provision

import (
	"fmt"
	"slices"
)

// rolePath is where the installed system records Spec.Role.
const rolePath = "/etc/frame-node-role"

// The roles a node can be installed for. A fixed list, not free text: the
// value is written to a file on the target and read by whatever joins the node.
const (
	RoleControlPlane     = "control-plane"
	RoleControlPlaneCeph = "control-plane+ceph"
	RoleWorker           = "worker"
	RoleWorkerCeph       = "worker+ceph"
)

// Roles lists every accepted Spec.Role besides the empty one.
func Roles() []string {
	return []string{RoleControlPlane, RoleControlPlaneCeph, RoleWorker, RoleWorkerCeph}
}

func validateRole(r string) error {
	if r == "" || slices.Contains(Roles(), r) {
		return nil
	}
	return fmt.Errorf("spec: role %q must be empty or one of %v", r, Roles())
}
