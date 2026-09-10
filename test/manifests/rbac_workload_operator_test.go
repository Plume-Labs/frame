package manifests

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// enforcedNamespaces reads deploy/kubernetes/pod-security/namespaces.yaml —
// the single source of truth for where `baseline` is enforced, and therefore
// for where restart and scale may be granted at all.
//
// Parsed rather than duplicated: a second hand-maintained list is how the two
// drift, and the drift is silent in the direction that matters (a RoleBinding
// in a namespace that is not enforced hands back the escalation, and nothing
// else in the repository would notice).
func enforcedNamespaces(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(Root(t), "deploy", "kubernetes", "pod-security", "namespaces.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^  name: (\S+)$`).FindAllStringSubmatch(string(raw), -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatalf("parsed no namespaces out of %s — the fixture is not reading what it claims", path)
	}
	sort.Strings(out)
	return out
}

// workloadRoles is every ClusterRole that may write a pod template, with the
// groups its RoleBindings must reach. Two of them, because restart and scale
// are an operator action while the manifest editor is admin-only, and a
// RoleBinding carries one roleRef and one subject list.
var workloadRoles = []struct {
	name    string
	groups  []string
	notFor  []string
	purpose string
}{
	{
		name:    "cluster-control-workload-operator",
		groups:  []string{"Group:frame:operators", "Group:frame:admins"},
		notFor:  []string{"Group:frame:viewers"},
		purpose: "restart and scale",
	},
	{
		name:    "cluster-control-workload-admin",
		groups:  []string{"Group:frame:admins"},
		notFor:  []string{"Group:frame:viewers", "Group:frame:operators"},
		purpose: "the YAML editor",
	},
}

// The whole shape of the exception, in one test: one RoleBinding per enforced
// namespace, for each role, and none anywhere else.
//
// The second half is the load-bearing one. A RoleBinding in an unenforced
// namespace — rook-ceph, kube-system, monitoring — is a grant to write a pod
// template where nothing refuses `privileged: true` with a `hostPath: /`
// volume, which is node root. That is precisely the state the 2026-08-10
// removal closed, and it would be invisible: the RBAC would look tidy, the tier
// tests would stay green, and the console would simply work in one more
// namespace.
func TestWorkloadRolesAreBoundOnlyWhereBaselineIsEnforced(t *testing.T) {
	root := Root(t)
	file := filepath.Join(root, "deploy", "kubernetes", "base", "rbac-workload-operator.yaml")
	bindings := RoleBindings(t, file)
	want := enforcedNamespaces(t)

	for _, role := range workloadRoles {
		t.Run(role.name, func(t *testing.T) {
			seen := map[string]bool{}
			for key, rb := range bindings {
				if rb.RoleRef.Name != role.name {
					continue
				}
				seen[rb.Namespace] = true
				subs := SubjectNames(rb.Subjects)
				for _, g := range role.groups {
					if !Has(subs, g) {
						t.Errorf("RoleBinding %s does not reach %s (subjects: %s) — %s 403s for them",
							key, g, Join(subs), role.purpose)
					}
				}
				for _, g := range role.notFor {
					if Has(subs, g) {
						t.Errorf("RoleBinding %s names %s — %s is not theirs", key, g, role.purpose)
					}
				}
				if Has(subs, "ServiceAccount:cluster-control-ui") {
					t.Errorf("RoleBinding %s subjects the ServiceAccount, which is not the identity "+
						"an impersonated request carries", key)
				}
			}

			for _, ns := range want {
				if !seen[ns] {
					t.Errorf("no %s RoleBinding in %q — baseline is enforced there, so %s should work and will 403",
						role.name, ns, role.purpose)
				}
				delete(seen, ns)
			}
			for ns := range seen {
				t.Errorf("%s is bound in %q, which is not in "+
					"deploy/kubernetes/pod-security/namespaces.yaml — nothing refuses a privileged pod "+
					"there, so this grant is node root", role.name, ns)
			}
		})
	}
}

// Both ClusterRoles must stay unaggregated and must stay small. A tier label on
// either undoes the namespacing in one line; an extra rule widens a namespaced
// grant into something else.
func TestWorkloadRolesAreUnaggregatedAndNarrow(t *testing.T) {
	file := filepath.Join(Root(t), "deploy", "kubernetes", "base", "rbac-workload-operator.yaml")
	roles := ClusterRoles(t, file)

	for _, role := range workloadRoles {
		cr, ok := roles[role.name]
		if !ok {
			t.Fatalf("%s is not defined in rbac-workload-operator.yaml", role.name)
		}
		if tier, has := cr.Labels[TierLabel]; has {
			t.Fatalf("%s carries %s=%s — the aggregation would grant it cluster-wide, "+
				"which is exactly what binding it per namespace exists to prevent", role.name, TierLabel, tier)
		}
	}

	operator := roles["cluster-control-workload-operator"].Rules
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "patch"},
		{Group: "apps", Resource: "statefulsets", Verb: "patch"},
		{Group: "apps", Resource: "deployments/scale", Verb: "patch"},
		{Group: "apps", Resource: "statefulsets/scale", Verb: "patch"},
	} {
		if !Grants(operator, a) {
			t.Errorf("cluster-control-workload-operator does not grant %s — restart or scale 403s everywhere", a)
		}
	}
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "delete"},
		{Group: "apps", Resource: "deployments", Verb: "update"},
		{Group: "apps", Resource: "daemonsets", Verb: "patch"},
		{Group: "", Resource: "pods", Verb: "delete"},
		{Group: "", Resource: "secrets", Verb: "get"},
	} {
		if Grants(operator, a) {
			t.Errorf("cluster-control-workload-operator grants %s — it is restart and scale, nothing else; "+
				"`update` in particular is the editor's verb and belongs to admins", a)
		}
	}

	admin := roles["cluster-control-workload-admin"].Rules
	for _, a := range []Access{
		{Group: "", Resource: "pods", Verb: "update"},
		{Group: "apps", Resource: "deployments", Verb: "update"},
		{Group: "apps", Resource: "statefulsets", Verb: "update"},
		{Group: "apps", Resource: "daemonsets", Verb: "update"},
		{Group: "batch", Resource: "jobs", Verb: "update"},
	} {
		if !Grants(admin, a) {
			t.Errorf("cluster-control-workload-admin does not grant %s — the YAML tab cannot save anywhere", a)
		}
	}
	// The kind bound the spec draws, asserted on the namespaced role too:
	// bounding by namespace does not license widening by kind.
	for _, a := range []Access{
		{Group: "", Resource: "secrets", Verb: "update"},
		{Group: "", Resource: "configmaps", Verb: "update"},
		{Group: "", Resource: "serviceaccounts", Verb: "update"},
		{Group: "apps", Resource: "deployments", Verb: "delete"},
		{Group: "", Resource: "pods/exec", Verb: "create"},
	} {
		if Grants(admin, a) {
			t.Errorf("cluster-control-workload-admin grants %s — the editor is five kinds, "+
				"update and patch, and nothing else", a)
		}
	}
}
