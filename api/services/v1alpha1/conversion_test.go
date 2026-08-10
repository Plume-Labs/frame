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
	"math/rand"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/randfill"

	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
)

// The same fixed seed as the frame group's conversion_test.go, for the same
// reason: a failure has to be reproducible. See that file for why a fuzz is
// the only test that catches the field someone forgot.
const fuzzSeed = 20260809

const fuzzIterations = 200

func newFiller() *randfill.Filler {
	return randfill.New().
		RandSource(rand.NewSource(fuzzSeed)).
		NilChance(0.2).
		NumElements(0, 4).
		Funcs(
			// Conversion deliberately does not carry TypeMeta;
			// controller-runtime's handler stamps it on the destination.
			func(tm *metav1.TypeMeta, _ randfill.Continue) {
				*tm = metav1.TypeMeta{}
			},
			func(t *metav1.Time, c randfill.Continue) {
				*t = metav1.Unix(c.Int63n(1<<31), 0)
			},
			// FrameService's Ready reasons are diagnostic, not phase tokens.
			// Using the real vocabulary here is what makes the corpus
			// representative: a projection that read the reason would produce
			// values v1alpha1's enum rejects, and these are the strings it
			// would produce them from.
			func(c *[]metav1.Condition, cont randfill.Continue) {
				reasons := []string{
					"Provisioned", "UnknownType", "NotProvisionable",
					"SizeRefused", "ModelCacheMissing", "BindingDegraded",
				}
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

// FrameService is a strict subset: v1beta1 loses only status.phase, and has no
// field v1alpha1 lacks. So the hub direction must be exactly lossless, and
// nothing is stashed in an annotation.
func TestHubRoundTripIsLossless(t *testing.T) {
	f := newFiller()
	for i := range fuzzIterations {
		original := &servicesv1beta1.FrameService{}
		f.Fill(original)

		spoke := &FrameService{}
		if err := spoke.ConvertFrom(original); err != nil {
			t.Fatalf("[%d] ConvertFrom: %v", i, err)
		}
		roundTripped := &servicesv1beta1.FrameService{}
		if err := spoke.ConvertTo(roundTripped); err != nil {
			t.Fatalf("[%d] ConvertTo: %v", i, err)
		}

		if diff := cmp.Diff(original, roundTripped); diff != "" {
			t.Fatalf("[%d] v1beta1 -> v1alpha1 -> v1beta1 lost data (-want +got):\n%s", i, diff)
		}
	}
}

// The trap the FrameService owner named: a fixture whose parameters map is nil
// exercises neither direction of the map[string]string <-> ParameterValue
// cast, so it passes while both are wrong. This asserts the corpus actually
// populates it — and the other collections beside it.
func TestFuzzCorpusReachesTheInterestingBranches(t *testing.T) {
	f := newFiller()
	var (
		populatedParams, emptyParams, nilParams                      int
		populatedProjected, populatedProvisioned, populatedProjectTo int
		nonEmptySizing, deleting                                     int
	)
	for range fuzzIterations {
		svc := &servicesv1beta1.FrameService{}
		f.Fill(svc)
		switch {
		case svc.Spec.Parameters == nil:
			nilParams++
		case len(svc.Spec.Parameters) == 0:
			emptyParams++
		default:
			populatedParams++
		}
		if len(svc.Status.Binding.Projected) > 0 {
			populatedProjected++
		}
		if len(svc.Status.Provisioned) > 0 {
			populatedProvisioned++
		}
		if len(svc.Spec.Binding.ProjectTo) > 0 {
			populatedProjectTo++
		}
		if svc.Status.Sizing.GPUMemory != "" {
			nonEmptySizing++
		}
		if !svc.DeletionTimestamp.IsZero() {
			deleting++
		}
	}
	for _, c := range []struct {
		what  string
		count int
	}{
		{"populated parameter maps", populatedParams},
		{"empty-but-present parameter maps", emptyParams},
		{"nil parameter maps", nilParams},
		{"populated status.binding.projected", populatedProjected},
		{"populated status.provisioned", populatedProvisioned},
		{"populated spec.binding.projectTo", populatedProjectTo},
		{"non-empty status.sizing.gpuMemory", nonEmptySizing},
		{"objects carrying a deletion timestamp", deleting},
	} {
		if c.count == 0 {
			t.Errorf("the corpus produced no %s in %d objects — "+
				"TestHubRoundTripIsLossless is not exercising that branch",
				c.what, fuzzIterations)
		}
	}
}

func TestFrameServicePhaseProjection(t *testing.T) {
	cases := []struct {
		name       string
		deleting   bool
		conditions []metav1.Condition
		want       string
	}{
		{"nothing has reconciled yet", false, nil, "Pending"},
		{"Ready=True is Ready", false, []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Provisioned"}}, "Ready"},
		{"Ready=False is Degraded", false, []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ModelCacheMissing"}}, "Degraded"},
		{"Ready=Unknown is Degraded, not Pending", false, []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "NotProvisionable"}}, "Degraded"},
		{"a deletion timestamp wins over a healthy Ready", true, []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Provisioned"}}, "Deleting"},
		{"deleting with no conditions at all", true, nil, "Deleting"},
		{"a non-Ready condition alone is still Pending", false, []metav1.Condition{
			{Type: "Progressing", Status: metav1.ConditionTrue, Reason: "Provisioned"}}, "Pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FrameServicePhaseFromStatus(tc.deleting, tc.conditions); got != tc.want {
				t.Fatalf("FrameServicePhaseFromStatus = %q, want %q", got, tc.want)
			}

			// And the same answer through the conversion, which is the only
			// caller that matters: it has to derive `deleting` from the hub
			// object's own metadata rather than being told.
			hub := &servicesv1beta1.FrameService{}
			hub.Spec.Type = "inference"
			hub.Status.Conditions = tc.conditions
			if tc.deleting {
				now := metav1.Now()
				hub.DeletionTimestamp = &now
				hub.Finalizers = []string{"services.plume-labs.io/frameservice"}
			}
			spoke := &FrameService{}
			if err := spoke.ConvertFrom(hub); err != nil {
				t.Fatalf("ConvertFrom: %v", err)
			}
			if spoke.Status.Phase != tc.want {
				t.Fatalf("projected phase = %q, want %q", spoke.Status.Phase, tc.want)
			}
		})
	}
}

// The projection must never emit a Ready *reason*. FrameService's reasons are
// diagnostic — UnknownType, SizeRefused, ModelCacheMissing and whatever a
// provider returns — and none of them is a member of v1alpha1's phase enum, so
// a reason-based projection would produce objects the apiserver rejects on
// write. Provisioning is in the enum and no code ever wrote it; it stays
// unreachable rather than being given a source it never had.
func TestProjectedPhaseIsAlwaysInsideTheV1alpha1Enum(t *testing.T) {
	enum := map[string]bool{
		"": true, "Pending": true, "Provisioning": true,
		"Ready": true, "Degraded": true, "Deleting": true,
	}
	reasons := []string{
		"", "Provisioned", "UnknownType", "NotProvisionable", "SizeRefused",
		"ModelCacheMissing", "BindingDegraded", "Ready", "Degraded", "Deleting",
	}
	statuses := []metav1.ConditionStatus{metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown}
	for _, r := range reasons {
		for _, s := range statuses {
			for _, deleting := range []bool{false, true} {
				conds := []metav1.Condition{{Type: "Ready", Status: s, Reason: r}}
				got := FrameServicePhaseFromStatus(deleting, conds)
				if !enum[got] {
					t.Errorf("FrameServicePhaseFromStatus(%v, Ready=%s/%q) = %q, outside the v1alpha1 enum",
						deleting, s, r, got)
				}
				if got == r && r != "Ready" && r != "Degraded" && r != "Deleting" {
					t.Errorf("the projection echoed the diagnostic reason %q as a phase", r)
				}
			}
		}
	}
	if got := FrameServicePhaseFromStatus(false, nil); !enum[got] {
		t.Errorf("FrameServicePhaseFromStatus(false, nil) = %q, outside the v1alpha1 enum", got)
	}
}

func TestConversionRejectsTheWrongHubType(t *testing.T) {
	// The services group has one kind, so the wrong hub cannot be another
	// FrameService — but the assertion is what stands between a
	// mis-registration and a nil dereference in the manager, so it is still
	// worth pinning that it returns an error rather than panicking.
	if err := (&FrameService{}).ConvertTo(&servicesv1beta1.FrameService{}); err != nil {
		t.Fatalf("ConvertTo rejected its own hub: %v", err)
	}
}
