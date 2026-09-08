package manifests

import (
	"path/filepath"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

// tierRoleFiles is every file that defines a ClusterRole carrying a tier
// label, i.e. everything the three aggregated `frame-*` ClusterRoles pick up.
func tierRoleFiles(t *testing.T) []string {
	t.Helper()
	root := Root(t)
	paths, err := filepath.Glob(filepath.Join(root, "config", "rbac", "*_role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return append(paths, filepath.Join(root, "deploy", "kubernetes", "base", "rbac.yaml"))
}

// consoleWrites is every write the console makes that is not a Frame CRD, each
// tied to the call site that makes it. These are what C3 of the whole-branch
// review found nothing granted: `cluster-control-operator` holds all of them
// and was left both unbound and unlabelled, so the aggregation picked up the
// viewer role and the 27 per-kind CRD roles and nothing else. Cordon — the
// lot's headline example — 403'd for admins.
var consoleWrites = []Access{
	{Group: "", Resource: "nodes", Verb: "patch", Why: "cordon/uncordon, frame-sdk.ts:1036"},
	{Group: "", Resource: "pods/eviction", Verb: "create", Why: "drain, frame-sdk.ts:1082"},
	{Group: "apps", Resource: "deployments/scale", Verb: "patch", Why: "Scale button, frame-sdk.ts:2332"},
	{Group: "apps", Resource: "statefulsets/scale", Verb: "patch", Why: "Scale button, frame-sdk.ts:2332"},
	{Group: "scheduling.volcano.sh", Resource: "queues", Verb: "patch", Why: "share-weight tuning, frame-sdk.ts:1406"},
	{Group: "bus.volcano.sh", Resource: "commands", Verb: "create", Why: "queue open/close, frame-sdk.ts:1433"},
	{Group: "velero.io", Resource: "backups", Verb: "create", Why: "on-demand backup, Resilience screen"},
}

// The discriminating pair. An operator must be able to do each of these, and
// a viewer must not — asserting only the first would pass against a
// `cluster-control-operator` mislabelled `tier: viewer`, and asserting only
// the second would pass against the branch as reviewed, where the role
// carried no label at all.
func TestOperatorsCanWriteWhatViewersCannot(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)

	editor := AggregatedRules(roles, "editor")
	viewer := AggregatedRules(roles, "viewer")

	for _, a := range consoleWrites {
		if !Grants(editor, a) {
			t.Errorf("frame-editor does not aggregate %s — %s returns 403 for operators and admins", a, a.Why)
		}
		if Grants(viewer, a) {
			t.Errorf("frame-viewer aggregates %s — a viewer can %s", a, a.Why)
		}
	}
}

// The reads every screen needs must reach the lowest tier, or the console is
// blank for viewers. Cheap, and it is the other half of the label being right.
func TestViewersCanReadWhatEveryScreenNeeds(t *testing.T) {
	viewer := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "viewer")
	for _, a := range []Access{
		{Group: "", Resource: "nodes", Verb: "watch", Why: "useLiveResource streams nodes"},
		{Group: "", Resource: "pods", Verb: "list", Why: "every integrationPods() call"},
		{Group: "", Resource: "events", Verb: "watch", Why: "ClusterEventsView"},
		{Group: "apps", Resource: "deployments", Verb: "list", Why: "ApplicationClient.list()"},
		{Group: "metrics.k8s.io", Resource: "nodes", Verb: "list", Why: "node metrics"},
		{Group: "frame.plume-labs.io", Resource: "frametasks", Verb: "watch", Why: "the Tasks screen"},
	} {
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}
}

// C4, first half. The tier labels were applied to all 27 per-kind roles,
// FrameUser's included, so `frameuser-editor-role` landed in the editor tier:
// an operator could PATCH their own FrameUser to `role: admin` and hold
// `frame:admins` within one token lifetime. And `frameuser-viewer-role`
// landed in the viewer tier, which docs/deployment.md — edited by this same
// branch — says is equivalent to handing over every password hash.
func TestNoTierBelowAdminTouchesFrameUsers(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	editor := AggregatedRules(roles, "editor")

	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		a := Access{Group: "frame.plume-labs.io", Resource: "frameusers", Verb: verb}
		if Grants(editor, a) {
			t.Errorf("frame-editor (and so frame-viewer's superset) aggregates %s: "+
				"`get` is every password hash, and any write verb is a one-request promotion to admin", a)
		}
	}

	// And the admin tier must still be able to manage them, or nobody can.
	admin := AggregatedRules(roles, "admin")
	for _, verb := range []string{"get", "list", "create", "patch", "delete"} {
		a := Access{Group: "frame.plume-labs.io", Resource: "frameusers", Verb: verb}
		if !Grants(admin, a) {
			t.Errorf("frame-admin does not aggregate %s — no one can manage accounts", a)
		}
	}
}

// C4, third half. `cluster-control-operator`'s own header says it grants
// "write on safe Frame CRDs (not Talos, not /status)". Labelling
// talosmachineconfig/talosupgrade's editor roles `tier: editor` reversed that
// silently.
func TestOperatorsGetNoTalosWrites(t *testing.T) {
	editor := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "editor")
	for _, res := range []string{"talosmachineconfigs", "talosupgrades"} {
		for _, verb := range []string{"create", "update", "patch", "delete"} {
			a := Access{Group: "frame.plume-labs.io", Resource: res, Verb: verb}
			if Grants(editor, a) {
				t.Errorf("frame-editor aggregates %s — cluster-control-operator excludes Talos writes on purpose "+
					"(deploy/kubernetes/base/rbac.yaml)", a)
			}
		}
		// Reads stay: the Talos screens are read-only for everyone below admin.
		if !Grants(editor, Access{Group: "frame.plume-labs.io", Resource: res, Verb: "list"}) {
			t.Errorf("frame-editor cannot list %s — the Talos screens go blank for operators", res)
		}
	}
}

// C3, second half. Two namespaced Role/RoleBindings still subjected the
// ServiceAccount, which stopped being the identity making the request the
// moment the proxy started impersonating. Every integration panel and the
// Settings config load 403'd for every user.
func TestNamespacedRolesAreBoundToTheFrameGroups(t *testing.T) {
	root := Root(t)
	files := []string{
		filepath.Join(root, "deploy", "kubernetes", "containment", "rbac-integration-proxy.yaml"),
		filepath.Join(root, "deploy", "kubernetes", "base", "rbac.yaml"),
	}
	bindings := RoleBindings(t, files...)
	roles := Roles(t, files...)
	if len(bindings) == 0 {
		t.Fatal("no RoleBindings parsed — the fixture is not reading what it claims")
	}

	const sa = "ServiceAccount:cluster-control-ui"
	readOnlyChecked := 0
	for key, rb := range bindings {
		subs := SubjectNames(rb.Subjects)
		if Has(subs, sa) {
			t.Errorf("RoleBinding %s still subjects %s: impersonated requests do not carry the "+
				"ServiceAccount's identity, so this grants the caller nothing", key, sa)
		}
		if !Has(subs, "Group:frame:operators") || !Has(subs, "Group:frame:admins") {
			t.Errorf("RoleBinding %s reaches neither operators nor admins (subjects: %s)", key, Join(subs))
		}
		// A read-only Role has to reach the lowest tier too, or the screens
		// it feeds are blank for viewers.
		role, ok := roles[rb.Namespace+"/"+rb.RoleRef.Name]
		if !ok || writesAnything(role.Rules, writeVerbs) {
			continue
		}
		readOnlyChecked++
		if !Has(subs, "Group:frame:viewers") {
			t.Errorf("RoleBinding %s carries a read-only Role but does not name frame:viewers "+
				"(subjects: %s) — every screen it feeds is blank for viewers", key, Join(subs))
		}
	}
	if readOnlyChecked == 0 {
		t.Fatal("found no read-only namespaced Role — the fixture is not testing what it claims")
	}
}

var writeVerbs = map[string]bool{"create": true, "update": true, "patch": true, "delete": true, "deletecollection": true}

// Writes through those namespaced Roles must stop at the editor tier, the
// same way cluster-scoped writes do. Silencing an alert is an action, not a
// read.
func TestViewersCannotSilenceAlertsOrSaveSettings(t *testing.T) {
	root := Root(t)
	files := []string{
		filepath.Join(root, "deploy", "kubernetes", "containment", "rbac-integration-proxy.yaml"),
		filepath.Join(root, "deploy", "kubernetes", "base", "rbac.yaml"),
	}
	roles := Roles(t, files...)
	bindings := RoleBindings(t, files...)

	// For every namespaced Role granting a write verb, the binding that
	// carries it must not name frame:viewers.
	checked := 0
	for key, rb := range bindings {
		role, ok := roles[rb.Namespace+"/"+rb.RoleRef.Name]
		if !ok || rb.RoleRef.Kind != "Role" {
			continue
		}
		if !writesAnything(role.Rules, writeVerbs) {
			continue
		}
		checked++
		if Has(SubjectNames(rb.Subjects), "Group:frame:viewers") {
			t.Errorf("RoleBinding %s carries write verbs to frame:viewers — a viewer must not "+
				"silence an alert or save the Settings ConfigMap", key)
		}
	}
	if checked == 0 {
		t.Fatal("found no namespaced Role granting a write verb — the fixture is not testing what it claims")
	}
}

func writesAnything(rules []rbacv1.PolicyRule, writeVerbs map[string]bool) bool {
	for _, r := range rules {
		for _, v := range r.Verbs {
			if writeVerbs[v] || v == "*" {
				return true
			}
		}
	}
	return false
}
