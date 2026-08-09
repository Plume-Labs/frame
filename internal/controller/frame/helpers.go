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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// conditionTypeReady is the standard status.conditions[].type this package's
// controllers use to report overall reconcile health.
const conditionTypeReady = "Ready"

// readyReason is the Reason of the Ready condition, or "" when there is none.
//
// This is the controllers' state-machine input now that status.phase is gone
// (F2). It is also exactly what api/frame/v1alpha1's conversion projects back
// out as the legacy phase, so a reason that stops tracking reality here shows
// up as a wrong phase to every v1alpha1 client.
func readyReason(conditions []metav1.Condition) string {
	if c := meta.FindStatusCondition(conditions, conditionTypeReady); c != nil {
		return c.Reason
	}
	return ""
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
