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

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

func TestNodeTuningKSMDefaultsOff(t *testing.T) {
	// Page merging across tenants is a side channel. A NodeTuning that says
	// nothing about KSM must not turn it on.
	var nt framev1beta1.NodeTuning
	if nt.Spec.KSM != nil {
		t.Fatalf("KSM must be nil unless declared, got %+v", nt.Spec.KSM)
	}
}

func TestNodeTuningPhaseConstantsAreDistinct(t *testing.T) {
	seen := map[framev1beta1.NodeTuningPhase]bool{}
	for _, p := range []framev1beta1.NodeTuningPhase{
		framev1beta1.PhaseInSync, framev1beta1.PhaseDrifted,
		framev1beta1.PhaseRebootPending, framev1beta1.PhaseApplying,
		framev1beta1.PhaseFailed,
	} {
		if seen[p] {
			t.Fatalf("duplicate phase constant %q", p)
		}
		seen[p] = true
	}
}
