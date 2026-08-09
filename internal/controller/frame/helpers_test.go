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

package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestReadyReasonReturnsTheReadyConditionsReason documents readyReason()'s
// own contract: it reads whatever meta.SetStatusCondition last wrote,
// including a reason-only update that leaves Status unchanged. This is new
// code exercising new code — readyReason doesn't exist before this task, so
// it cannot be the regression proof. That proof is
// TestSetConditionReasonRegression in setcondition_regression_test.go, which
// goes through the pre-existing call path (TalosMachineConfigReconciler.setCondition)
// instead and is verified to fail against the pre-fix helpers.go.
func TestReadyReasonReturnsTheReadyConditionsReason(t *testing.T) {
	var conds []metav1.Condition

	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Provisioning",
		Message:            "Waiting to apply config",
		ObservedGeneration: 1,
	})
	if got := readyReason(conds); got != "Provisioning" {
		t.Fatalf("readyReason = %q, want Provisioning", got)
	}

	meta.SetStatusCondition(&conds, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Offline",
		Message:            "Node reports NotReady",
		ObservedGeneration: 2,
	})
	if got := readyReason(conds); got != "Offline" {
		t.Fatalf("readyReason = %q after a reason-only change, want Offline", got)
	}
	if len(conds) != 1 {
		t.Fatalf("expected one Ready condition, got %d", len(conds))
	}
	if conds[0].ObservedGeneration != 2 {
		t.Fatalf("observedGeneration = %d, want 2", conds[0].ObservedGeneration)
	}
	if conds[0].Message != "Node reports NotReady" {
		t.Fatalf("message = %q, want the second message", conds[0].Message)
	}
}

func TestReadyReasonIsEmptyWithoutAReadyCondition(t *testing.T) {
	conds := []metav1.Condition{{
		Type:   "Submitted",
		Status: metav1.ConditionTrue,
		Reason: "WorkflowCreated",
	}}
	if got := readyReason(conds); got != "" {
		t.Fatalf("readyReason = %q, want empty", got)
	}
}
