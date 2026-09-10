package manifests

// Task 6, lot 1 (hardware/Redfish). FrameMachine gets exactly the same
// read-for-everyone / write-for-one-tier shape as every other Frame CRD in
// this file: `get`/`list`/`watch` reach the viewer tier (and so editor and
// admin, since each aggregates the tier below), and the one console-writable
// field — spec.powerRequest, reached only through `patch` — is admin-only.
//
// Admin and not editor, on purpose: restarting a Deployment is bounded by an
// update strategy, powering off a chassis is bounded by nothing, and the
// chassis may be carrying the very cluster that hosts the console making the
// request. See deploy/kubernetes/base/rbac.yaml's cluster-control-admin
// block for the full rationale.
//
// The last assertion — no tier grants anything on `secrets` — is the one
// this file exists to defend. BMC credentials are readable only by the
// operator's own service account; registering a machine is deliberately a
// `kubectl` step because of it. A later change that adds a Secret grant to
// make a registration form work has to be a decision someone takes on
// purpose, not a line that slips in under an unrelated diff. Asserted last
// so a failure here is unambiguous about which grant broke it.

import "testing"

func TestViewersCanReadFrameMachinesAndNothingElseTiersCanWrite(t *testing.T) {
	roles := ClusterRoles(t, tierRoleFiles(t)...)
	viewer := AggregatedRules(roles, "viewer")
	editor := AggregatedRules(roles, "editor")
	admin := AggregatedRules(roles, "admin")

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

	if !Grants(admin, Access{Group: "frame.plume-labs.io", Resource: "framemachines", Verb: "patch", Why: "the power dialog"}) {
		t.Error("frame-admin does not aggregate patch on framemachines — the power dialog 403s for admins")
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
