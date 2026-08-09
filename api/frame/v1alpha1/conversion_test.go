/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/conversion"
	"sigs.k8s.io/randfill"

	v1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// A hand-written fixture is written from the same mental model as the
// conversion function it checks, so it systematically misses the field you
// added to v1beta1 and forgot to carry through ConvertFrom. Random objects do
// not share that mental model.
//
// The seed is fixed so a failure is reproducible; bump it deliberately if you
// want a different corpus, never automatically.
const fuzzSeed = 20260809

// fuzzIterations is per kind. Enough that a field missed in one branch of a
// nil/empty/populated three-way shows up, cheap enough that the whole file
// runs in well under a second.
const fuzzIterations = 200

func newFiller() *randfill.Filler {
	return randfill.New().
		RandSource(rand.NewSource(fuzzSeed)).
		NilChance(0.2).
		NumElements(0, 4).
		Funcs(
			// Conversion deliberately does not carry TypeMeta —
			// controller-runtime's conversion handler stamps apiVersion and
			// kind on the destination itself. A fuzzed TypeMeta would fail
			// every round trip for a reason that is not a bug.
			func(tm *metav1.TypeMeta, _ randfill.Continue) {
				*tm = metav1.TypeMeta{}
			},
			// metav1.Time round-trips through RFC3339 with second
			// granularity; a fuzzed nanosecond would fail equality for a
			// reason that has nothing to do with conversion.
			func(t *metav1.Time, c randfill.Continue) {
				*t = metav1.Unix(c.Int63n(1<<31), 0)
			},
			// resource.Quantity is all unexported fields, which randfill skips
			// by default — left alone, every fuzzed MaxCPU would be the zero
			// Quantity and the pointer copy would never be exercised against a
			// real value. These are parsed rather than constructed so the
			// internal representation is one a real object could hold.
			func(q *resource.Quantity, c randfill.Continue) {
				*q = resource.MustParse(fmt.Sprintf("%dm", c.Int63n(1<<20)+1))
			},
			// Conditions must be structurally valid: the projections read Type
			// and Reason, and the apiserver treats the list as a map keyed by
			// type, so duplicate types are not a shape any stored object can
			// have.
			func(c *[]metav1.Condition, cont randfill.Continue) {
				reasons := []string{"Submitted", "Running", "Suspended", "Completed", "Failed"}
				statuses := []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
				types := []string{"Ready", "Progressing", "Degraded"}
				n := cont.Intn(len(types) + 1)
				if n == 0 {
					*c = nil
					return
				}
				out := make([]metav1.Condition, 0, n)
				for i := range n {
					out = append(out, metav1.Condition{
						Type:               types[i],
						Status:             statuses[cont.Intn(len(statuses))],
						Reason:             reasons[cont.Intn(len(reasons))],
						Message:            cont.String(0),
						ObservedGeneration: cont.Int63n(100),
						LastTransitionTime: metav1.Unix(cont.Int63n(1<<31), 0),
					})
				}
				*c = out
			},
		)
}

// hubRoundTrip is the direction that must be exactly lossless: v1beta1 is the
// storage version, so anything it can hold has to survive a trip through the
// served v1alpha1 endpoint unchanged. It is lossless by construction —
// v1beta1 has no field v1alpha1 lacks, because observedGeneration and
// FrameResourceQuota's used/namespaces were added to v1alpha1 before the
// freeze specifically so this would hold — and this test is what keeps that
// true as fields are added.
//
// This one direction is enough to catch a field dropped by *either* function:
// anything ConvertFrom fails to carry down, or ConvertTo fails to carry back
// up, is missing from the result. That includes FrameUser's bijection, where
// the hub's status.passwordHash only survives if ConvertFrom parks it in
// spec.passwordHash and ConvertTo reads it back out of there.
//
// The reverse direction is deliberately not fuzzed. Everything it could lose
// is v1alpha1-only, and there are exactly three such fields — status.phase,
// FrameJob.spec.namespace and TalosSecretReference.namespace — all three of
// which are *supposed* to change. A fuzz over that direction would need all
// three excluded and would then be asserting nothing. They are pinned by name
// in the two tests below instead.
func hubRoundTrip[H conversion.Hub, S conversion.Convertible](t *testing.T, name string, newHub func() H, newSpoke func() S) {
	t.Helper()
	f := newFiller()
	for i := range fuzzIterations {
		original := newHub()
		f.Fill(original)

		spoke := newSpoke()
		if err := spoke.ConvertFrom(original); err != nil {
			t.Fatalf("%s [%d]: ConvertFrom: %v", name, i, err)
		}
		roundTripped := newHub()
		if err := spoke.ConvertTo(roundTripped); err != nil {
			t.Fatalf("%s [%d]: ConvertTo: %v", name, i, err)
		}

		if diff := cmp.Diff(original, roundTripped); diff != "" {
			t.Fatalf("%s [%d]: v1beta1 -> v1alpha1 -> v1beta1 lost data (-want +got):\n%s", name, i, diff)
		}
	}
}

func TestHubRoundTripIsLossless(t *testing.T) {
	hubRoundTrip(t, "FrameJob",
		func() *v1beta1.FrameJob { return &v1beta1.FrameJob{} },
		func() *FrameJob { return &FrameJob{} })
	hubRoundTrip(t, "FrameNode",
		func() *v1beta1.FrameNode { return &v1beta1.FrameNode{} },
		func() *FrameNode { return &FrameNode{} })
	hubRoundTrip(t, "FrameResourceQuota",
		func() *v1beta1.FrameResourceQuota { return &v1beta1.FrameResourceQuota{} },
		func() *FrameResourceQuota { return &FrameResourceQuota{} })
	hubRoundTrip(t, "SchedulingPolicy",
		func() *v1beta1.SchedulingPolicy { return &v1beta1.SchedulingPolicy{} },
		func() *SchedulingPolicy { return &SchedulingPolicy{} })
	hubRoundTrip(t, "TalosMachineConfig",
		func() *v1beta1.TalosMachineConfig { return &v1beta1.TalosMachineConfig{} },
		func() *TalosMachineConfig { return &TalosMachineConfig{} })
	hubRoundTrip(t, "TalosUpgrade",
		func() *v1beta1.TalosUpgrade { return &v1beta1.TalosUpgrade{} },
		func() *TalosUpgrade { return &TalosUpgrade{} })
	hubRoundTrip(t, "FrameUser",
		func() *v1beta1.FrameUser { return &v1beta1.FrameUser{} },
		func() *FrameUser { return &FrameUser{} })
}

// A fuzzer that never generates a populated value proves nothing while looking
// busy — the FrameService owner's nil-map trap, in general form. This asserts
// the corpus actually reaches the branches the round trip is meant to cover.
func TestFuzzCorpusReachesTheInterestingBranches(t *testing.T) {
	f := newFiller()
	var (
		populatedParams, emptyParams, nilParams int
		populatedDisks, emptyDisks, nilDisks    int
		nonEmptyHash                            int
		populatedCreds                          int
	)
	for range fuzzIterations {
		job := &v1beta1.FrameJob{}
		f.Fill(job)
		switch {
		case job.Spec.Parameters == nil:
			nilParams++
		case len(job.Spec.Parameters) == 0:
			emptyParams++
		default:
			populatedParams++
		}

		node := &v1beta1.FrameNode{}
		f.Fill(node)
		switch {
		case node.Status.DiscoveredDisks == nil:
			nilDisks++
		case len(node.Status.DiscoveredDisks) == 0:
			emptyDisks++
		default:
			populatedDisks++
		}

		user := &v1beta1.FrameUser{}
		f.Fill(user)
		if user.Status.PasswordHash != "" {
			nonEmptyHash++
		}
		if len(user.Status.Credentials) > 0 {
			populatedCreds++
		}
	}

	// nil and populated are the two branches every rebuild function has. The
	// non-nil-but-empty case is the one that separates "preserves nil-ness"
	// from "normalises to nil", so it has to appear too.
	for _, c := range []struct {
		what  string
		count int
	}{
		{"populated parameter maps", populatedParams},
		{"empty-but-present parameter maps", emptyParams},
		{"nil parameter maps", nilParams},
		{"populated discoveredDisks", populatedDisks},
		{"empty-but-present discoveredDisks", emptyDisks},
		{"nil discoveredDisks", nilDisks},
		{"non-empty status.passwordHash", nonEmptyHash},
		{"populated credentials", populatedCreds},
	} {
		if c.count == 0 {
			t.Errorf("the corpus produced no %s in %d objects — "+
				"TestHubRoundTripIsLossless is not exercising that branch",
				c.what, fuzzIterations)
		}
	}
}

// The other direction is deliberately *not* lossless, in exactly two places.
// This test pins both, so a future change that starts silently preserving —
// or silently dropping something else — fails here rather than in production.
func TestSpokeRoundTripNormalisesTheTwoRemovedNamespaceFields(t *testing.T) {
	// The namespace the CR itself lives in, as opposed to any namespace a
	// removed field once named.
	const ownNamespace = "team-a"

	t.Run("FrameJob.spec.namespace becomes the object's own", func(t *testing.T) {
		src := &FrameJob{}
		src.Namespace = ownNamespace
		src.Spec.Pipeline = "neura-training-dag"
		src.Spec.Namespace = "somewhere-else"

		hub := &v1beta1.FrameJob{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		back := &FrameJob{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.Namespace != ownNamespace {
			t.Fatalf("spec.namespace = %q, want the object's own namespace %q — "+
				"a v1alpha1 client must see the namespace the operator acts in, "+
				"not the one it asked for",
				back.Spec.Namespace, ownNamespace)
		}
		if len(back.Annotations) != 0 {
			t.Fatalf("annotations = %v, want none — nothing is stashed", back.Annotations)
		}
	})

	t.Run("TalosSecretRef.namespace is dropped, not stashed", func(t *testing.T) {
		src := &TalosMachineConfig{}
		src.Namespace = ownNamespace
		src.Spec.NodeName = "w2"
		src.Spec.TalosEndpoint = "10.0.0.2:50000"
		src.Spec.ConfigPatch = "machine: {}"
		src.Spec.TalosSecretRef = TalosSecretReference{Name: "talos-certs", Namespace: "kube-system"}

		hub := &v1beta1.TalosMachineConfig{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		if hub.Spec.TalosSecretRef.Name != "talos-certs" {
			t.Fatalf("name = %q, want talos-certs", hub.Spec.TalosSecretRef.Name)
		}
		back := &TalosMachineConfig{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.TalosSecretRef.Namespace != "" {
			t.Fatalf("namespace = %q, want empty — empty already meant "+
				"'this CR's own namespace', so the normalised value is the truth",
				back.Spec.TalosSecretRef.Namespace)
		}
		if len(back.Annotations) != 0 {
			t.Fatalf("annotations = %v, want none — nothing is stashed", back.Annotations)
		}
	})

	// TalosUpgrade carries the same reference type and the same removal. It is
	// a separate pair of functions, so it needs a separate assertion: the two
	// have already diverged once in this freeze (TalosUpgrade has no
	// object-level CEL and TalosMachineConfig does).
	t.Run("TalosUpgrade drops it too", func(t *testing.T) {
		src := &TalosUpgrade{}
		src.Namespace = ownNamespace
		src.Spec.NodeName = "w2"
		src.Spec.TalosEndpoint = "10.0.0.2:50000"
		src.Spec.Image = "ghcr.io/siderolabs/installer:v1.8.0"
		src.Spec.TalosSecretRef = TalosSecretReference{Name: "talos-certs", Namespace: "kube-system"}

		hub := &v1beta1.TalosUpgrade{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		if hub.Spec.TalosSecretRef.Name != "talos-certs" {
			t.Fatalf("name = %q, want talos-certs", hub.Spec.TalosSecretRef.Name)
		}
		back := &TalosUpgrade{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.TalosSecretRef.Namespace != "" {
			t.Fatalf("namespace = %q, want empty", back.Spec.TalosSecretRef.Namespace)
		}
	})

	t.Run("FrameUser's password hash moves between spec and status", func(t *testing.T) {
		src := &FrameUser{}
		src.Spec.Email = "a@b.test"
		src.Spec.Role = "admin"
		src.Spec.PasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"

		hub := &v1beta1.FrameUser{}
		if err := src.ConvertTo(hub); err != nil {
			t.Fatalf("ConvertTo: %v", err)
		}
		if hub.Status.PasswordHash != src.Spec.PasswordHash {
			t.Fatalf("status.passwordHash = %q, want the spec value", hub.Status.PasswordHash)
		}
		if hub.Spec.PasswordAuth != src.Spec.PasswordAuth {
			t.Fatalf("passwordAuth = %q, want %q", hub.Spec.PasswordAuth, src.Spec.PasswordAuth)
		}
		back := &FrameUser{}
		if err := back.ConvertFrom(hub); err != nil {
			t.Fatalf("ConvertFrom: %v", err)
		}
		if back.Spec.PasswordHash != src.Spec.PasswordHash {
			t.Fatalf("spec.passwordHash = %q, want it back", back.Spec.PasswordHash)
		}
	})
}

func TestLegacyPhaseIsProjectedNotStored(t *testing.T) {
	cases := []struct {
		name       string
		conditions []metav1.Condition
		want       string
	}{
		{"no conditions at all", nil, "Pending"},
		{"submitted", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Submitted"}}, "Submitted"},
		{"running", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Running"}}, "Running"},
		{"suspended", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Suspended"}}, "Suspended"},
		{"completed", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Completed"}}, "Completed"},
		{"failed", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Failed"}}, "Failed"},
		{"a reason outside the vocabulary falls back to the least-committal legal value",
			[]metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse, Reason: "WorkflowCreated"}}, "Submitted"},
		// This is the shape of both FrameJobs stored on the cluster: reconciled
		// by the pre-F3 build, so a write-once Submitted condition and no Ready
		// at all. Without this branch the projection would call them Pending —
		// "nothing has run" — of two jobs whose workflow was created eleven days
		// ago. Submitted is what their conditions actually assert. Their real
		// outcome (Completed) is recorded nowhere a projection can reach:
		// status.completionTime is set on Failed too, so it cannot be used to
		// tell them apart. Task 21's forced re-reconcile is what restores it.
		{"the legacy write-once Submitted condition, with no Ready beside it",
			[]metav1.Condition{
				{Type: "Submitted", Status: metav1.ConditionTrue, Reason: "WorkflowCreated"}}, "Submitted"},
		// A Ready condition wins over the legacy one whenever both are present,
		// which is the state every object reaches after one reconcile.
		{"Ready wins over the legacy condition", []metav1.Condition{
			{Type: "Submitted", Status: metav1.ConditionTrue, Reason: "WorkflowCreated"},
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Completed"}}, "Completed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &v1beta1.FrameJob{}
			hub.Spec.Pipeline = "neura-training-dag"
			hub.Status.Conditions = tc.conditions
			spoke := &FrameJob{}
			if err := spoke.ConvertFrom(hub); err != nil {
				t.Fatalf("ConvertFrom: %v", err)
			}
			if spoke.Status.Phase != tc.want {
				t.Fatalf("projected phase = %q, want %q", spoke.Status.Phase, tc.want)
			}

			// And the projection is one-way: converting back must not put it
			// anywhere.
			roundTripped := &v1beta1.FrameJob{}
			if err := spoke.ConvertTo(roundTripped); err != nil {
				t.Fatalf("ConvertTo: %v", err)
			}
			if diff := cmp.Diff(hub.Status.Conditions, roundTripped.Status.Conditions); diff != "" {
				t.Fatalf("conditions changed on the way back up (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFrameNodePhaseProjection(t *testing.T) {
	// Discovering and Failed are v1alpha1 enum members with no counterpart in
	// the v1beta1 reason vocabulary, and no controller ever wrote either. They
	// stay unreachable rather than being given a source they never had, so an
	// unrecognised reason and an absent condition both project to the empty
	// string — which status.phase's omitempty renders as an absent field, the
	// same shape an unreconciled FrameNode always had. Degraded was the
	// alternative for the unrecognised case and was rejected: every FrameNode
	// token asserts something about hardware, and a wrong Degraded invites
	// someone to drain a healthy node.
	cases := []struct {
		name       string
		conditions []metav1.Condition
		want       string
	}{
		{"no conditions at all", nil, ""},
		// All three stored FrameNodes are exactly this, and all three carry
		// status.phase: Discovered, so the projection reproduces the stored
		// value on every object that exists.
		{"discovered, as all three live nodes are", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Discovered"}}, "Discovered"},
		{"provisioning", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Provisioning"}}, "Provisioning"},
		{"online", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Online"}}, "Online"},
		{"degraded", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Degraded"}}, "Degraded"},
		{"offline", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Offline"}}, "Offline"},
		{"an unrecognised reason projects to absent, never to a guess",
			[]metav1.Condition{
				{Type: "Ready", Status: metav1.ConditionFalse, Reason: "TalosUnreachable"}}, ""},
		{"a non-Ready condition alone is not a phase", []metav1.Condition{
			{Type: "Progressing", Status: metav1.ConditionTrue, Reason: "Online"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := &v1beta1.FrameNode{}
			hub.Spec.IP = "192.168.2.201"
			hub.Status.Conditions = tc.conditions
			spoke := &FrameNode{}
			if err := spoke.ConvertFrom(hub); err != nil {
				t.Fatalf("ConvertFrom: %v", err)
			}
			if spoke.Status.Phase != tc.want {
				t.Fatalf("projected phase = %q, want %q", spoke.Status.Phase, tc.want)
			}
		})
	}
}

// The v1alpha1 enum is the contract the projection has to stay inside: a value
// outside it is rejected by the apiserver on write, so a projection that can
// emit one is a bug that only shows up when someone tries to persist a
// converted object.
func TestProjectedPhasesAreAllInsideTheV1alpha1Enums(t *testing.T) {
	jobEnum := map[string]bool{
		"": true, "Pending": true, "Submitted": true, "Running": true,
		"Suspended": true, "Completed": true, "Failed": true,
	}
	nodeEnum := map[string]bool{
		"": true, "Discovering": true, "Discovered": true, "Provisioning": true,
		"Online": true, "Degraded": true, "Offline": true, "Failed": true,
	}
	// Every reason any Frame controller writes, plus values no controller
	// writes, plus the shape of the two stored FrameJobs.
	reasons := []string{
		"", "Submitted", "Running", "Suspended", "Completed", "Failed",
		"Discovered", "Provisioning", "Online", "Degraded", "Offline",
		"WorkflowCreated", "Reconciled", "Applied", "TalosUnreachable",
		"UnknownType", "SizeRefused", "ModelCacheMissing",
	}
	for _, r := range reasons {
		for _, condType := range []string{"Ready", "Submitted", "Progressing"} {
			conds := []metav1.Condition{{Type: condType, Status: metav1.ConditionFalse, Reason: r}}
			if got := FrameJobPhaseFromConditions(conds); !jobEnum[got] {
				t.Errorf("FrameJobPhaseFromConditions(%s/%q) = %q, outside the v1alpha1 enum", condType, r, got)
			}
			if got := FrameNodePhaseFromConditions(conds); !nodeEnum[got] {
				t.Errorf("FrameNodePhaseFromConditions(%s/%q) = %q, outside the v1alpha1 enum", condType, r, got)
			}
		}
	}
	if got := FrameJobPhaseFromConditions(nil); !jobEnum[got] {
		t.Errorf("FrameJobPhaseFromConditions(nil) = %q, outside the v1alpha1 enum", got)
	}
	if got := FrameNodePhaseFromConditions(nil); !nodeEnum[got] {
		t.Errorf("FrameNodePhaseFromConditions(nil) = %q, outside the v1alpha1 enum", got)
	}
}

// A conversion handed the wrong hub type must say so rather than panic: the
// webhook calls these through an interface, so the assertion is the only thing
// standing between a mis-registered kind and a nil dereference in the manager.
func TestConversionRejectsTheWrongHubType(t *testing.T) {
	if err := (&FrameJob{}).ConvertTo(&v1beta1.FrameNode{}); err == nil {
		t.Fatal("ConvertTo accepted a *v1beta1.FrameNode as a FrameJob hub")
	}
	if err := (&FrameJob{}).ConvertFrom(&v1beta1.FrameNode{}); err == nil {
		t.Fatal("ConvertFrom accepted a *v1beta1.FrameNode as a FrameJob hub")
	}
}
