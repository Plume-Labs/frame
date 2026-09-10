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
// `deployments/scale`/`statefulsets/scale` patch used to be here — cluster-wide
// on cluster-control-operator, aggregated into frame-editor. Lot 2 (2026-09-09)
// moved it out: cluster-wide it is the same pod-template escalation the
// 2026-08-10 removal closed for the full patch, through the `/scale`
// subresource instead. It is now a namespaced grant in
// rbac-workload-operator.yaml, bound only into the namespaces enforcing Pod
// Security `baseline` — see TestWorkloadRolesAreUnaggregatedAndNarrow (grants
// it on the namespaced role) and TestWorkloadWritesAreNotAggregatedIntoAnyTier
// (asserts it is absent from every aggregated tier, this one included).
var consoleWrites = []Access{
	{Group: "", Resource: "nodes", Verb: "patch", Why: "cordon/uncordon, frame-sdk.ts:1036"},
	{Group: "", Resource: "pods/eviction", Verb: "create", Why: "drain, frame-sdk.ts:1082"},
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

// lot 2. Each access is asserted at the tier that should have it *and* at the
// tier below, because only the pair discriminates: asserting "an admin can"
// alone passes against a rule mislabelled `tier: viewer`, which grants it to
// everyone, and that is exactly the defect (C4) this file was written after.
var lot2OperatorWrites = []Access{
	{Group: "", Resource: "pods/log", Verb: "get", Why: "the Logs tab, WorkloadClient.logs()"},
	{Group: "", Resource: "pods", Verb: "delete", Why: "Delete pod, WorkloadClient.deletePod()"},
}

func TestOperatorsCanOperateWorkloadsAndViewersCannot(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	editor := AggregatedRules(roles, "editor")
	viewer := AggregatedRules(roles, "viewer")

	for _, a := range lot2OperatorWrites {
		if !Grants(editor, a) {
			t.Errorf("frame-editor does not aggregate %s — %s returns 403 for operators and admins", a, a.Why)
		}
		if Grants(viewer, a) {
			t.Errorf("frame-viewer aggregates %s — a viewer can %s, and a log is where a secret gets printed", a, a.Why)
		}
	}
}

// Exec is admin-only per the decision that opened this lot's design, and it is
// the one workload grant that stays cluster-wide — a shell creates no pod, so
// bounding it to the enforced namespaces would close nothing. It inherits the
// target pod's privilege instead: exec into one of the deliberately privileged
// pods in an exempt namespace is root on the node, which is why this is
// admin-only and recorded rather than why it is safe.
//
// Asserted at both tiers: "an admin can" alone passes against a rule
// mislabelled `tier: viewer`, which grants a shell to everyone.
func TestOnlyAdminsExec(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	admin := AggregatedRules(roles, "admin")
	editor := AggregatedRules(roles, "editor")

	a := Access{Group: "", Resource: "pods/exec", Verb: "create", Why: "the Terminal tab"}
	if !Grants(admin, a) {
		t.Errorf("frame-admin does not aggregate %s — %s is refused for everyone", a, a.Why)
	}
	if Grants(editor, a) {
		t.Errorf("frame-editor aggregates %s — %s is admin-only by design", a, a.Why)
	}
}

// The tree is blank for a viewer without these, and blank is how the Accounts
// screen failed: a 200, an empty list, and nothing to notice.
func TestViewersCanReadTheWholeWorkloadTree(t *testing.T) {
	viewer := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "viewer")
	for _, a := range []Access{
		{Group: "apps", Resource: "daemonsets", Verb: "list", Why: "the DaemonSet rows of the tree"},
		{Group: "apps", Resource: "replicasets", Verb: "list", Why: "attaching a Deployment's pods to it"},
		{Group: "batch", Resource: "jobs", Verb: "list", Why: "the Job rows of the tree"},
		{Group: "apps", Resource: "deployments", Verb: "watch", Why: "useLiveResource streams the tree"},
		{Group: "", Resource: "pods", Verb: "watch", Why: "useLiveResource streams the tree"},
	} {
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}
}

// Every grant that can write a pod template must NOT reach any aggregated
// tier. All of them are bound namespace by namespace, into exactly the
// namespaces where `baseline` Pod Security refuses the privileged payload; a
// tier label on either workload ClusterRole would make the aggregation pick it
// up and grant it everywhere, including rook-ceph and kube-system, which is the
// escalation the 2026-08-10 removal closed.
//
// The editor's `update`/`patch` is in this list for the same reason restart is:
// cluster-wide, it is the identical escalation through a different door — an
// admin writes `privileged: true` and a `hostPath: /` into a pod template in an
// exempt namespace and holds node root.
//
// Asserted at all three tiers, because "not at editor" alone passes against a
// role mislabelled `tier: admin`.
func TestWorkloadWritesAreNotAggregatedIntoAnyTier(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	for _, tier := range []string{"viewer", "editor", "admin"} {
		rules := AggregatedRules(roles, tier)
		for _, a := range []Access{
			{Group: "apps", Resource: "deployments", Verb: "patch", Why: "Restart"},
			{Group: "apps", Resource: "statefulsets", Verb: "patch", Why: "Restart"},
			{Group: "apps", Resource: "deployments/scale", Verb: "patch", Why: "Scale"},
			{Group: "apps", Resource: "statefulsets/scale", Verb: "patch", Why: "Scale"},
			{Group: "", Resource: "pods", Verb: "update", Why: "the YAML tab"},
			{Group: "", Resource: "pods", Verb: "patch", Why: "the YAML tab"},
			{Group: "apps", Resource: "deployments", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "statefulsets", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "daemonsets", Verb: "update", Why: "the YAML tab"},
			{Group: "apps", Resource: "daemonsets", Verb: "patch", Why: "the YAML tab"},
			{Group: "batch", Resource: "jobs", Verb: "update", Why: "the YAML tab"},
			{Group: "batch", Resource: "jobs", Verb: "patch", Why: "the YAML tab"},
		} {
			if Grants(rules, a) {
				t.Errorf("frame-%s aggregates %s cluster-wide — %s must be bound per namespace, "+
					"or a pod template can be rewritten in rook-ceph or kube-system and that is node root", tier, a, a.Why)
			}
		}
	}
}

// The other half of the same rule: the tree must still be readable everywhere,
// including in the namespaces where nothing may be written. Without this, a
// zealous reading of the test above could be "satisfied" by removing the reads
// as well, and the console would go blank on every infrastructure namespace.
func TestReadsStayClusterWideEvenWhereWritesDoNot(t *testing.T) {
	viewer := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "viewer")
	for _, a := range []Access{
		{Group: "apps", Resource: "deployments", Verb: "get", Why: "showing a rook-ceph deployment's YAML"},
		{Group: "apps", Resource: "daemonsets", Verb: "get", Why: "showing a kube-system daemonset's YAML"},
		{Group: "batch", Resource: "jobs", Verb: "get", Why: "showing a Job's YAML"},
		{Group: "", Resource: "pods", Verb: "get", Why: "showing a pod's YAML"},
	} {
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}
}

// The bound the spec draws around the YAML editor. "Edit any resource" would
// mean cluster-wide update for admins, which is a far larger grant than this
// screen needs and could not be tied to a call site the way
// deploy/kubernetes/base/rbac.yaml requires.
func TestTheManifestEditorIsNotAClusterWideGrant(t *testing.T) {
	admin := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "admin")
	for _, a := range []Access{
		{Group: "", Resource: "secrets", Verb: "update"},
		{Group: "", Resource: "configmaps", Verb: "update"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Verb: "update"},
		{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions", Verb: "update"},
	} {
		if Grants(admin, a) {
			t.Errorf("frame-admin aggregates %s — the editor is bounded to the five kinds the tree shows", a)
		}
	}
}
