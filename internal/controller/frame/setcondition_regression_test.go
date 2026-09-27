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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
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

// TestSetConditionReasonRegression exercises a path Frame calls in
// production: FrameNodeReconciler.setPhase, which writes the Ready condition
// through r.Status().Patch. It is not a test of meta.SetStatusCondition; it
// goes through Frame's own reconciler method with a fake client, exactly as
// the real controller does.
//
// Two calls keep Status=False (the node is absent, then the FrameNode may not
// classify it, same generation) but change Reason and Message. Against the
// pre-fix helpers.go — whose old setCondition replaced an existing condition
// only when Status differed — the second write was silently dropped and the
// reason stayed frozen. FrameNode walks exactly such transitions
// (AwaitingNode -> Unclassified, Degraded -> Offline: all Ready=False).
//
// History: this test first ran through TalosMachineConfigReconciler, verified
// failing at f79f55f (the pre-fix commit) and passing after it. That
// reconciler left with the Talos kinds; FrameNode is the controller that
// still writes Ready=False reasons in sequence.
func TestSetConditionReasonRegression(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme (client-go): %v", err)
	}
	if err := framev1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme (framev1beta1): %v", err)
	}

	fn := &framev1beta1.FrameNode{
		ObjectMeta: metav1.ObjectMeta{Name: "regression", Namespace: "default"},
		Spec:       framev1beta1.FrameNodeSpec{IP: "192.0.2.1"},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&framev1beta1.FrameNode{}).
		WithObjects(fn).
		Build()
	r := &FrameNodeReconciler{Client: c}
	ctx := context.Background()

	if err := r.setPhase(ctx, fn, nodePhaseAwaitingNode, "first: node absent"); err != nil {
		t.Fatalf("first setPhase: %v", err)
	}
	if got := readyConditionReason(fn.Status.Conditions); got != nodePhaseAwaitingNode {
		t.Fatalf("readyConditionReason after first write = %q, want %s", got, nodePhaseAwaitingNode)
	}

	if err := r.setPhase(ctx, fn, nodePhaseUnclassified, "second: same Status, new reason"); err != nil {
		t.Fatalf("second setPhase: %v", err)
	}
	if got := readyConditionReason(fn.Status.Conditions); got != nodePhaseUnclassified {
		t.Fatalf("readyConditionReason after a reason-only change = %q, want %s "+
			"(pre-fix setCondition froze it at the first reason)", got, nodePhaseUnclassified)
	}

	// The write reached the object, not only the in-memory copy.
	var stored framev1beta1.FrameNode
	if err := c.Get(ctx, client.ObjectKeyFromObject(fn), &stored); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := readyConditionReason(stored.Status.Conditions); got != nodePhaseUnclassified {
		t.Fatalf("stored reason = %q, want %s", got, nodePhaseUnclassified)
	}
}
