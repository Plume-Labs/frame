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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// readyConditionReason reads the Ready condition's Reason the way a caller
// would before readyReason() existed. It is deliberately local rather than a
// call to readyReason(), so that TestSetConditionReasonRegression below stays
// compilable — and failing — against the pre-fix helpers.go it exists to
// catch a regression of. That file (commit f79f55f) has neither
// meta.SetStatusCondition nor readyReason; this file must build there
// unmodified for the failure to be evidence rather than an assertion.
func readyConditionReason(conds []metav1.Condition) string {
	for _, c := range conds {
		if c.Type == conditionTypeReady {
			return c.Reason
		}
	}
	return ""
}

// TestSetConditionReasonRegression exercises the exact path Frame calls in
// production: TalosMachineConfigReconciler.setCondition, a thin wrapper that
// forwards to this package's shared condition-writing call — the very call
// site the fix repoints. It is not a test of meta.SetStatusCondition; it goes
// through Frame's own reconciler method with a fake client, exactly as the
// real controller does via r.Status().Patch.
//
// Two calls keep Status=False (a resolve failure, then a client-build
// failure, same generation) but change Reason and Message. Against the
// pre-fix helpers.go — where the shared call was the old setCondition, which
// replaced an existing condition only when Status differed — the second
// write is silently dropped and this test fails with readyConditionReason
// still reporting "PatchResolveFailed". That is the same class of bug
// FrameNode hits going Provisioning -> Degraded -> Offline (all Ready=False):
// a different controller, the same shared helper, the same frozen reason.
//
// Verified: checked out at f79f55f (the pre-fix commit) this file compiles
// unmodified and the test fails. On this commit it passes.
func TestSetConditionReasonRegression(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme (client-go): %v", err)
	}
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme (framev1beta1): %v", err)
	}

	tmc := &framev1beta1.TalosMachineConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "regression", Namespace: "default"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&framev1beta1.TalosMachineConfig{}).
		WithObjects(tmc).
		Build()
	r := &TalosMachineConfigReconciler{Client: c}
	ctx := context.Background()

	if err := r.setCondition(ctx, tmc, metav1.ConditionFalse, "PatchResolveFailed", "first failure"); err != nil {
		t.Fatalf("first setCondition: %v", err)
	}
	if got := readyConditionReason(tmc.Status.Conditions); got != "PatchResolveFailed" {
		t.Fatalf("readyConditionReason after first write = %q, want PatchResolveFailed", got)
	}

	if err := r.setCondition(ctx, tmc, metav1.ConditionFalse, "ClientBuildFailed", "second failure, same Status"); err != nil {
		t.Fatalf("second setCondition: %v", err)
	}
	if got := readyConditionReason(tmc.Status.Conditions); got != "ClientBuildFailed" {
		t.Fatalf("readyConditionReason after a reason-only change = %q, want ClientBuildFailed "+
			"(pre-fix setCondition froze it at PatchResolveFailed)", got)
	}
}
