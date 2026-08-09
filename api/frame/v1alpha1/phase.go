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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The legacy status.phase values. They exist only in v1alpha1: v1beta1 is
// conditions-only, and these are computed on the way down and never stored.
//
// A one-way projection is the whole trick that made removing phase possible.
// Conversion cannot reconstruct a stored phase from conditions in general —
// which is why the FrameJob controller first had to make its Ready condition
// track the lifecycle instead of being written once (F3). With that done,
// Ready.Reason *is* the phase for FrameJob and FrameNode, and the projection
// is a rename rather than an inference.
const (
	legacyPhasePending      = "Pending"
	legacyPhaseSubmitted    = "Submitted"
	legacyPhaseRunning      = "Running"
	legacyPhaseSuspended    = "Suspended"
	legacyPhaseCompleted    = "Completed"
	legacyPhaseFailed       = "Failed"
	legacyPhaseDiscovered   = "Discovered"
	legacyPhaseProvisioning = "Provisioning"
	legacyPhaseOnline       = "Online"
	legacyPhaseDegraded     = "Degraded"
	legacyPhaseOffline      = "Offline"
)

const (
	// conditionTypeReady is the one condition every Frame controller writes
	// and the only one either projection reads.
	conditionTypeReady = "Ready"

	// conditionTypeSubmitted is the write-once condition the pre-F3 FrameJob
	// controller wrote instead of Ready. It shares a spelling with
	// legacyPhaseSubmitted because both name the same lifecycle step, not
	// because one is derived from the other.
	conditionTypeSubmitted = "Submitted"
)

// FrameJobPhaseFromConditions projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// The FrameJob controller writes exactly one condition, Ready, whose reason is
// one of the five phase tokens (the invariant F3 established). Reading it back
// out is therefore a rename, not an inference, for every object this
// controller has touched since.
//
// The no-Ready case is not hypothetical and is not "the controller has not run
// yet". Both FrameJobs stored on the cluster sit at generation 1 with no Ready
// condition at all: they were reconciled by the pre-F3 build, which wrote a
// write-once Submitted/WorkflowCreated condition and nothing else, and nothing
// has re-reconciled them since. So the fallback is layered:
//
//   - a legacy Submitted condition, with no Ready beside it, means the workflow
//     was created and this object's conditions record nothing after that. The
//     honest projection is Submitted — what the conditions actually assert.
//   - no conditions at all means nothing has reconciled, which is Pending.
//     v1alpha1's enum always allowed Pending and the controller never wrote it
//     (R6); the projection now does, correctly.
//
// What the fallback deliberately does *not* do is read status.completionTime,
// which both stored objects carry. The controller sets it on Completed and on
// Failed alike, so it cannot tell the two apart, and guessing Completed for a
// job that failed is precisely the "an hour-old failure reads as healthy" bug
// F3 existed to kill. The outcome of those two jobs is genuinely not recorded
// anywhere in the object; a projection cannot invent it. Task 21's forced
// re-reconcile is what gives them a Ready condition and restores Completed.
func FrameJobPhaseFromConditions(conditions []metav1.Condition) string {
	ready := meta.FindStatusCondition(conditions, conditionTypeReady)
	if ready == nil {
		if meta.FindStatusCondition(conditions, conditionTypeSubmitted) != nil {
			return legacyPhaseSubmitted
		}
		return legacyPhasePending
	}
	switch ready.Reason {
	case legacyPhaseSubmitted, legacyPhaseRunning, legacyPhaseSuspended,
		legacyPhaseCompleted, legacyPhaseFailed:
		return ready.Reason
	default:
		// A reason outside the phase vocabulary is a controller bug, not a
		// client's problem. Report the least-committal legal value rather
		// than emitting a phase the v1alpha1 enum would reject on write.
		return legacyPhaseSubmitted
	}
}

// FrameNodePhaseFromConditions projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// The FrameNode controller's Ready reason has always been the phase string —
// framenode_controller.go set Reason: phase alongside Status.Phase = phase —
// so the five shared tokens are a straight read. Verified against the cluster:
// all three stored FrameNodes carry Ready/Discovered and status.phase:
// Discovered, so the projection reproduces the stored value exactly on every
// object that exists.
//
// Two of v1alpha1's seven enum members, Discovering and Failed, have no
// counterpart in the v1beta1 reason vocabulary
// (Discovered|Provisioning|Online|Degraded|Offline), and nothing in either
// type constrains the reason string, so what they map to was a decision rather
// than a lookup. They stay unreachable. No controller ever wrote either (R6),
// so routing anything onto them would manufacture a value the field has never
// held — inventing history rather than projecting it.
//
// An unrecognised reason, and an absent Ready condition, both project to the
// empty string: status.phase is omitempty, so an empty projection is an
// *absent* field on the wire and never reaches the enum. Absence is what an
// unreconciled FrameNode looked like in v1alpha1, so it is a shape clients
// already handle.
//
// The alternative for an unrecognised reason was Degraded, and it is worse.
// Unlike FrameJob's Submitted fallback — where the least-committal token is
// genuinely non-committal — every FrameNode token asserts something about
// hardware, and a wrong Degraded invites someone to drain a healthy node.
// Nothing is hidden by declining to guess: the Ready condition itself is
// served unchanged on both versions.
func FrameNodePhaseFromConditions(conditions []metav1.Condition) string {
	ready := meta.FindStatusCondition(conditions, conditionTypeReady)
	if ready == nil {
		return ""
	}
	switch ready.Reason {
	case legacyPhaseDiscovered, legacyPhaseProvisioning, legacyPhaseOnline,
		legacyPhaseDegraded, legacyPhaseOffline:
		return ready.Reason
	default:
		return ""
	}
}
