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

// The legacy status.phase values for FrameService. They exist only in
// v1alpha1; v1beta1 is conditions-only and these are computed on the way down
// and never stored.
const (
	legacyPhasePending  = "Pending"
	legacyPhaseReady    = "Ready"
	legacyPhaseDegraded = "Degraded"
	legacyPhaseDeleting = "Deleting"
)

// conditionTypeReady is the one condition the FrameService controller writes.
// It shares a spelling with legacyPhaseReady, which is a coincidence of
// vocabulary: one is a condition type, the other a phase value.
const conditionTypeReady = "Ready"

// FrameServicePhaseFromStatus projects v1beta1's conditions back onto
// v1alpha1's status.phase.
//
// FrameService's Ready reason is diagnostic — UnknownType, NotProvisionable,
// SizeRefused, ModelCacheMissing, and whatever a provider returns — and
// collapsing that vocabulary into a five-valued enum would throw away the
// only useful part. Worse, it would not even be legal: none of those strings
// is a member of v1alpha1's phase enum, so a reason-based projection would
// emit values the apiserver rejects on write. So unlike FrameJob and
// FrameNode, this projection reads Ready.Status and the deletion timestamp,
// never the reason.
//
// The mapping is exactly what the controller used to store: setStatus was only
// ever called with "Ready" (on ConditionTrue) or "Degraded" (on
// ConditionFalse), so those two are faithful. Pending is what an object looks
// like before its first reconcile. Deleting is derived from the deletion
// timestamp, which the controller never encoded in the field at all.
// Provisioning was in the enum and was never written by anything, so — as with
// FrameNode's Discovering and Failed — it stays unreachable rather than being
// given a source it never had.
//
// Deleting is checked before the conditions because it is the one state the
// conditions cannot express: an object mid-deletion keeps whatever Ready it
// last had, and reporting Ready for something that is going away is the
// misreading the field's Deleting member exists to prevent.
func FrameServicePhaseFromStatus(deleting bool, conditions []metav1.Condition) string {
	if deleting {
		return legacyPhaseDeleting
	}
	ready := meta.FindStatusCondition(conditions, conditionTypeReady)
	if ready == nil {
		return legacyPhasePending
	}
	if ready.Status == metav1.ConditionTrue {
		return legacyPhaseReady
	}
	return legacyPhaseDegraded
}
