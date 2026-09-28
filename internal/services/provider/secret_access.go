package provider

import "fmt"

// ReasonSecretAccessForbidden is the Ready reason when the manager may not
// use Secrets in a namespace: it holds nothing on Secrets cluster-wide, only
// per namespace through a RoleBinding to frame-manager-secret-writer
// (docs/deployment.md, "Manager access to Secrets"). A retry can't fix it;
// an operator's binding can, and the next degraded requeue picks it up.
const ReasonSecretAccessForbidden = "SecretAccessForbidden"

// SecretAccessForbidden is a Forbidden answer on a Secret, turned into the
// grant that is missing.
type SecretAccessForbidden struct {
	Namespace string
	Action    string // read, write, delete
}

func (e *SecretAccessForbidden) Error() string {
	return fmt.Sprintf("the manager may not %s Secrets in namespace %q: bind ClusterRole frame-manager-secret-writer there (Helm: rbac.managerSecretAccess.write; kustomize: config/overlays/production)", e.Action, e.Namespace)
}
