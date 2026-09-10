package manifests

// Task 6, lot 1 (hardware/Redfish). FrameMachine gets exactly the same
// read-for-everyone / write-for-one-tier shape as every other Frame CRD in
// this file: `get`/`list`/`watch` reach the viewer tier (and so editor and
// admin, since each aggregates the tier below), and the standard admin verb
// bundle — the same `create`/`delete`/`deletecollection`/`get`/`list`/
// `patch`/`update`/`watch` shape every other Frame kind's admin tier gets —
// is admin-only. `patch` is the verb that matters for the console: it is the
// one verb that reaches spec.powerRequest, i.e. power control. See
// docs/superpowers/specs/2026-09-10-lot1-hardware-redfish-design.md,
// "Authorization".
//
// Admin and not editor, on purpose: restarting a Deployment is bounded by an
// update strategy, powering off a chassis is bounded by nothing, and the
// chassis may be carrying the very cluster that hosts the console making the
// request. See deploy/kubernetes/base/rbac.yaml's cluster-control-admin
// block for the full rationale. Editor keeps the same create/delete/patch/
// update rules in config/rbac/framemachine_editor_role.yaml that every other
// kind's editor role has — they are legitimate `kubectl` operations for
// someone who already holds them by other means — but that file carries no
// `rbac.frame.plume-labs.io/tier` label, so `frame-editor` never aggregates
// them. It is the label's absence that keeps power control out of editor's
// hands, not an absent rule.
//
// The last assertion — no tier grants anything on `secrets` — is the one
// this file exists to defend. BMC credentials are readable only by the
// operator's own service account; registering a machine is deliberately a
// `kubectl` step because of it. A later change that adds a Secret grant to
// make a registration form work has to be a decision someone takes on
// purpose, not a line that slips in under an unrelated diff. Asserted last
// so a failure here is unambiguous about which grant broke it.

import (
	"sort"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
)

func TestViewersCanReadFrameMachinesAndNothingElseTiersCanWrite(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	viewer := AggregatedRules(roles, "viewer")
	editor := AggregatedRules(roles, "editor")

	for _, verb := range []string{"get", "list", "watch"} {
		a := Access{Group: "frame.plume-labs.io", Resource: "framemachines", Verb: verb, Why: "the Hardware screen"}
		if !Grants(viewer, a) {
			t.Errorf("frame-viewer does not aggregate %s — %s", a, a.Why)
		}
	}

	// No verb beyond get/list/watch reaches viewer or editor. This is what
	// stops an operator (frame:operators, the editor tier) from reaching
	// spec.powerRequest — the design says admin only.
	writeVerbsToCheck := []string{"create", "update", "patch", "delete", "deletecollection"}
	for _, verb := range writeVerbsToCheck {
		a := Access{Group: "frame.plume-labs.io", Resource: "framemachines", Verb: verb}
		if Grants(viewer, a) {
			t.Errorf("frame-viewer aggregates %s on framemachines — a viewer must not be able to write a machine", a)
		}
		if Grants(editor, a) {
			t.Errorf("frame-editor aggregates %s on framemachines — power control is admin-only, "+
				"not editor: restarting a Deployment is bounded by an update strategy, powering off a "+
				"chassis is bounded by nothing", a)
		}
	}
}

// standardAdminVerbs is the bundle every other Frame kind's admin tier holds
// on its own kind (rbac-tier-roles.yaml / config/rbac/*_admin_role.yaml) —
// the eight RBAC verbs meaningful on a namespaced custom resource. An
// earlier version of this test asserted only `patch`, which is true but not
// the whole shape: it would have passed unchanged whether admin held the
// full bundle or a hand-trimmed one, which is exactly the gap that let the
// design doc describe the row wrong (Fix round 1, Finding B).
var standardAdminVerbs = []string{"create", "delete", "deletecollection", "get", "list", "patch", "update", "watch"}

// verbSetFor returns, sorted and de-duplicated, every verb any rule in
// `rules` grants for (group, resource). A literal `*` verb is returned as
// itself rather than expanded — so a rule widened to `verbs: ["*"]` shows up
// as `["*"]` in the comparison below and fails it by not matching the
// enumerated bundle, instead of silently satisfying every verb check the way
// checking each verb individually through Grants would.
func verbSetFor(rules []rbacv1.PolicyRule, group, resource string) []string {
	seen := map[string]bool{}
	for _, r := range rules {
		if !matches(r.APIGroups, group) || !matches(r.Resources, resource) {
			continue
		}
		for _, v := range r.Verbs {
			seen[v] = true
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func verbSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The shape assertion Finding B asked for: admin's verb set on framemachines
// is exactly the standard admin bundle, no more and no less. Written this
// way, a future narrowing (someone hand-trims a verb, breaking `kubectl`
// lifecycle management for admins) and a future widening (someone appends a
// verb, or replaces the list with a bare `*`) both fail here, where the old
// single-verb assertion would have caught neither.
func TestAdminHoldsExactlyTheStandardBundleOnFrameMachines(t *testing.T) {
	admin := AggregatedRules(ClusterRoles(t, tierRoleFiles(t)...), "admin")

	got := verbSetFor(admin, "frame.plume-labs.io", "framemachines")
	want := append([]string(nil), standardAdminVerbs...)
	sort.Strings(want)

	if !verbSetEqual(got, want) {
		t.Errorf("frame-admin's verb set on framemachines is %v, want exactly the standard admin bundle %v — "+
			"admin should hold the same shape every other Frame kind's admin tier gets, no more and no less "+
			"(patch is the verb that matters for spec.powerRequest, not the only one admin should have)", got, want)
	}
}

// The discriminating assertion. It must fail the moment any tier gains any
// verb on `secrets`, naming the tier, so a Secret grant added later — say, to
// make a registration form work — is a decision someone takes deliberately
// and not a line that slips in.
func TestFrameMachineLotGrantsNoTierAnySecretAccess(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)

	for _, tier := range []string{"viewer", "editor", "admin"} {
		rules := AggregatedRules(roles, tier)
		for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete", "deletecollection"} {
			a := Access{Group: "", Resource: "secrets", Verb: verb}
			if Grants(rules, a) {
				t.Errorf("frame-%s aggregates %s on secrets — BMC credentials are readable only by the "+
					"operator's own service account; a console tier gaining Secret access has to be a "+
					"deliberate decision, not a slip", tier, a)
			}
		}
	}
}
