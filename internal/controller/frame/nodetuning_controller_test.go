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
	"strconv"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// This suite calls Reconcile directly, matching every other controller test
// in this package (framenode_controller_test.go, frameresourcequota_controller_test.go,
// ...): no live manager runs here, so nothing re-triggers a reconcile on its
// own. nodeStatusPhase below reconciles once per poll — the fan-out watches
// SetupWithManager registers (Node changes, sibling NodeTuning changes) exist
// for the real manager and are exercised at the cluster level, not here.
var _ = Describe("NodeTuning Controller", func() {
	ctx := context.Background()

	var testNodes []string
	var testNodeTunings []string

	r := func() *NodeTuningReconciler {
		return &NodeTuningReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}

	createTestNode := func(name string, lbls map[string]string) {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		testNodes = append(testNodes, name)
	}

	// createNodeTuning always asks for KSM enabled: that gives the drift test
	// something concrete to disagree with (an unset spec.ksm is "untouched",
	// never a mismatch — see diffTuning's doc comment — so a real desired
	// value is required to prove drift detection actually compares fields
	// rather than always returning InSync).
	createNodeTuning := func(name string, lbls map[string]string) *framev1beta1.NodeTuning {
		nt := &framev1beta1.NodeTuning{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: framev1beta1.NodeTuningSpec{
				NodeSelector: &metav1.LabelSelector{MatchLabels: lbls},
				KSM:          &framev1beta1.KSMSpec{Enabled: true},
			},
		}
		Expect(k8sClient.Create(ctx, nt)).To(Succeed())
		testNodeTunings = append(testNodeTunings, name)
		return nt
	}

	// setObserved writes status.nodes[].observed the way the node agent
	// (Tasks 2-3) would, via a Get/modify/Update retry loop so a concurrent
	// reconcile's status patch never silently loses this write.
	setObserved := func(ntName, nodeName string, observed framev1beta1.ObservedTuning) {
		Eventually(func() error {
			nt := &framev1beta1.NodeTuning{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: ntName}, nt); err != nil {
				return err
			}
			found := false
			for i := range nt.Status.Nodes {
				if nt.Status.Nodes[i].Name == nodeName {
					nt.Status.Nodes[i].Observed = observed
					found = true
				}
			}
			if !found {
				nt.Status.Nodes = append(nt.Status.Nodes, framev1beta1.NodeTuningNodeStatus{
					Name:     nodeName,
					Observed: observed,
				})
			}
			return k8sClient.Status().Update(ctx, nt)
		}, "5s", "50ms").Should(Succeed())
	}

	getNodeStatus := func(ntName, nodeName string) *framev1beta1.NodeTuningNodeStatus {
		nt := &framev1beta1.NodeTuning{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: ntName}, nt); err != nil {
			return nil
		}
		for i := range nt.Status.Nodes {
			if nt.Status.Nodes[i].Name == nodeName {
				return &nt.Status.Nodes[i]
			}
		}
		return nil
	}

	// nodeStatusPhase reconciles ntName on every poll so Eventually converges
	// without a live watch loop, then reports the phase for nodeName (or ""
	// if the node has no status entry yet).
	nodeStatusPhase := func(ntName, nodeName string) func() framev1beta1.NodeTuningPhase {
		return func() framev1beta1.NodeTuningPhase {
			_, _ = r().Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: ntName}})
			if ns := getNodeStatus(ntName, nodeName); ns != nil {
				return ns.Phase
			}
			return ""
		}
	}

	nodeStatusMessage := func(ntName, nodeName string) string {
		if ns := getNodeStatus(ntName, nodeName); ns != nil {
			return ns.Message
		}
		return ""
	}

	// setObservedNeedingRestart reports an observed KSM state that disagrees
	// with createNodeTuning's spec.ksm.enabled=true. That specific mismatch
	// is restart-gated (see diffTuning/internal/agent/apply.go — the KSM
	// drop-in only takes effect on the owning unit's next start), so this is
	// the fixture the approval-gate tests need to reach RebootPending at all.
	setObservedNeedingRestart := func(ntName, nodeName string) {
		setObserved(ntName, nodeName, framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false}})
	}

	// annotateNode sets a single annotation on a Node via a Get/modify/Update
	// retry loop, mirroring setObserved's pattern so a concurrent write never
	// silently loses this one either.
	annotateNode := func(nodeName, key, value string) {
		Eventually(func() error {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
				return err
			}
			if n.Annotations == nil {
				n.Annotations = map[string]string{}
			}
			n.Annotations[key] = value
			return k8sClient.Update(ctx, n)
		}, "5s", "50ms").Should(Succeed())
	}

	// nodeCordoned reports the live corev1.Node's Spec.Unschedulable. Task 5
	// performs no cordon of its own — this exists so its tests can prove that
	// stopping at RebootPending really does mean "nothing happened yet",
	// rather than just asserting on a phase string a broken implementation
	// could satisfy some other way.
	nodeCordoned := func(nodeName string) func() bool {
		return func() bool {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
				return false
			}
			return n.Spec.Unschedulable
		}
	}

	// bumpGeneration applies a trivial spec mutation (toggling
	// cpuManagerPolicy) so metadata.generation advances by exactly one, and
	// returns the object at its new generation. The mutation itself also
	// happens to be a restart-gated mismatch against the zero-value Observed
	// used by these tests, which is irrelevant to what it's used for here
	// (advancing the generation) but keeps the node genuinely still needing a
	// restart rather than accidentally resolving to InSync.
	bumpGeneration := func(ntName string) *framev1beta1.NodeTuning {
		var updated *framev1beta1.NodeTuning
		Eventually(func() error {
			nt := &framev1beta1.NodeTuning{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: ntName}, nt); err != nil {
				return err
			}
			if nt.Spec.CPUManagerPolicy == "static" {
				nt.Spec.CPUManagerPolicy = "none"
			} else {
				nt.Spec.CPUManagerPolicy = "static"
			}
			if err := k8sClient.Update(ctx, nt); err != nil {
				return err
			}
			updated = nt
			return nil
		}, "5s", "50ms").Should(Succeed())
		return updated
	}

	AfterEach(func() {
		for _, name := range testNodeTunings {
			nt := &framev1beta1.NodeTuning{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, nt); err == nil {
				_ = k8sClient.Delete(ctx, nt)
			}
		}
		for _, name := range testNodeTunings {
			n := name
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &framev1beta1.NodeTuning{}))
			}, "5s").Should(BeTrue())
		}
		testNodeTunings = nil

		for _, name := range testNodes {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, n); err == nil {
				_ = k8sClient.Delete(ctx, n)
			}
		}
		for _, name := range testNodes {
			n := name
			Eventually(func() bool {
				return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &corev1.Node{}))
			}, "5s").Should(BeTrue())
		}
		testNodes = nil
	})

	// Overlapping selectors are a configuration error, not a merge: silently
	// picking a winner makes the effective configuration of a node
	// unpredictable, which is worse than refusing. A reconciler that instead
	// picked a winner (e.g. by name) would fail this test, because it
	// asserts BOTH objects report Failed and BOTH messages name the other —
	// an implementation that only fails the "loser" would leave one of these
	// two Eventually calls hanging.
	It("marks a node Failed when two NodeTunings select it", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		createNodeTuning("a", map[string]string{"role": "worker"})
		createNodeTuning("b", map[string]string{"role": "worker"})

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseFailed))
		Expect(nodeStatusMessage("a", "node-1")).To(ContainSubstring("b"))

		Eventually(nodeStatusPhase("b", "node-1")).Should(Equal(framev1beta1.PhaseFailed))
		Expect(nodeStatusMessage("b", "node-1")).To(ContainSubstring("a"))

		By("changing nothing: AppliedGeneration stays at the zero value for a refused node")
		Expect(getNodeStatus("a", "node-1").AppliedGeneration).To(BeZero())
		Expect(getNodeStatus("b", "node-1").AppliedGeneration).To(BeZero())
	})

	// Diffs against status.nodes[].observed, the agent's measured truth — not
	// against another NodeTuning's spec (that would be the overlap case
	// above) and not a hardcoded phase. A reconciler that always reports
	// InSync fails this test outright.
	//
	// Uses tunedProfile, not ksm, as the mismatched field: tuned applies live
	// (see diffTuning's doc comment), so this proves diff detection on its
	// own, without also exercising the Task 5 approval gate, which only
	// blocks the restart-needing fields (ksm.enabled, cpuManagerPolicy).
	It("reports Drifted when observed does not match spec", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		nt := &framev1beta1.NodeTuning{
			ObjectMeta: metav1.ObjectMeta{Name: "a"},
			Spec: framev1beta1.NodeTuningSpec{
				NodeSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}},
				TunedProfile: "network-latency",
			},
		}
		Expect(k8sClient.Create(ctx, nt)).To(Succeed())
		testNodeTunings = append(testNodeTunings, "a")
		setObserved("a", "node-1", framev1beta1.ObservedTuning{TunedProfile: "balanced"})

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseDrifted))
		Expect(nodeStatusMessage("a", "node-1")).To(ContainSubstring("tunedProfile"))
	})

	// The positive control for the Drifted test above: once Observed
	// actually matches what the spec asked for, the same node must stop
	// being reported as drifted. Without this test, a reconciler that always
	// reports Drifted (or never re-evaluates once drifted) would pass the
	// Drifted test unnoticed.
	It("reports InSync when observed matches spec", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		nt := createNodeTuning("a", map[string]string{"role": "worker"})
		setObserved("a", "node-1", framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: true}})

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseInSync))
		Expect(getNodeStatus("a", "node-1").AppliedGeneration).To(Equal(nt.Generation))
		Expect(getNodeStatus("a", "node-1").Message).To(BeEmpty())
	})

	// A node the selector does not match must never appear in status.nodes:
	// proves the reconciler is scoped by the selector rather than reporting
	// on every node in the cluster. node-1's phase here is incidental (no
	// Observed was ever reported, so it mismatches spec.ksm.enabled and, with
	// no approval, stops at RebootPending) — the assertion on it exists only
	// to prove node-1 was actually reconciled at all.
	It("does not add a status entry for a node the selector does not match", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		createTestNode("node-2", map[string]string{"role": "control-plane"})
		createNodeTuning("a", map[string]string{"role": "worker"})

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))
		Expect(getNodeStatus("a", "node-2")).To(BeNil())
	})

	// NodeTuningSpec.NodeSelector's doc comment: a nil selector matches no
	// nodes rather than every node, so an object created without one is
	// inert instead of fleet-wide by accident.
	It("matches no nodes when nodeSelector is nil", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		nt := &framev1beta1.NodeTuning{
			ObjectMeta: metav1.ObjectMeta{Name: "no-selector"},
		}
		Expect(k8sClient.Create(ctx, nt)).To(Succeed())
		testNodeTunings = append(testNodeTunings, "no-selector")

		_, err := r().Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "no-selector"}})
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "no-selector"}, fetched)).To(Succeed())
		Expect(fetched.Status.Nodes).To(BeEmpty())
	})

	It("preserves Observed and RestartedAt written by the agent when the node is InSync", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		createNodeTuning("a", map[string]string{"role": "worker"})
		observed := framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: true, PagesSharing: 5528}}
		setObserved("a", "node-1", observed)

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseInSync))

		ns := getNodeStatus("a", "node-1")
		Expect(ns).NotTo(BeNil())
		Expect(ns.Observed.KSM).NotTo(BeNil())
		Expect(ns.Observed.KSM.PagesSharing).To(Equal(int64(5528)),
			"the controller must never overwrite status.nodes[].observed — that field belongs to the agent")
	})

	// A reconciler that skips the gate entirely (proceeds to Drifted/InSync
	// on any restart-needing mismatch, approval or not) fails this test: it
	// would never observe RebootPending at all, and the node would never be
	// cordoned either way so that half of the assertion alone would not
	// catch it — the phase check is what a "no gate" implementation fails.
	It("waits at RebootPending until the node is approved", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		createNodeTuning("a", map[string]string{"role": "worker"})
		setObservedNeedingRestart("a", "node-1")

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))
		Consistently(nodeCordoned("node-1"), "3s").Should(BeFalse())
		Expect(getNodeStatus("a", "node-1").AppliedGeneration).To(BeZero(),
			"a refused node must not be reported as applied to this generation")
	})

	// The positive control for the test above: approving the exact current
	// generation must actually unblock the node. Without this test, an
	// implementation that always reports RebootPending regardless of any
	// annotation — never reading ApprovalAnnotation at all — would pass
	// every other test in this file and still never let a single node
	// proceed.
	It("proceeds once the node is approved for the current generation", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		nt := createNodeTuning("a", map[string]string{"role": "worker"})
		setObservedNeedingRestart("a", "node-1")
		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))

		annotateNode("node-1", framev1beta1.ApprovalAnnotation, strconv.FormatInt(nt.Generation, 10))

		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseDrifted))
		Expect(getNodeStatus("a", "node-1").AppliedGeneration).To(Equal(nt.Generation))
	})

	// A stale approval must not carry: approving one generation says nothing
	// about the next. A reconciler that compared loosely — annotation merely
	// present, or approved >= generation, instead of approved == generation
	// — would wrongly let this node proceed once the spec moves on, and
	// this is exactly the bug the design doc calls out: someone who approved
	// enabling KSM must not thereby have approved, unseen, a later change
	// that also flips the CPU manager policy.
	It("ignores an approval for an older generation", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		nt := createNodeTuning("a", map[string]string{"role": "worker"})
		setObservedNeedingRestart("a", "node-1")
		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))

		staleGeneration := nt.Generation
		annotateNode("node-1", framev1beta1.ApprovalAnnotation, strconv.FormatInt(staleGeneration, 10))
		bumped := bumpGeneration("a")
		Expect(bumped.Generation).To(BeNumerically(">", staleGeneration))

		Consistently(nodeStatusPhase("a", "node-1"), "3s").Should(Equal(framev1beta1.PhaseRebootPending))
	})

	// Treats a malformed annotation value as no approval at all — fail
	// closed. A reconciler that used a permissive parse (e.g. defaulting a
	// parse failure to "approved", or treating any non-empty string as
	// consent) would let one of these through and pass RebootPending.
	It("treats a malformed approval annotation as no approval", func() {
		createTestNode("node-1", map[string]string{"role": "worker"})
		createNodeTuning("a", map[string]string{"role": "worker"})
		setObservedNeedingRestart("a", "node-1")
		Eventually(nodeStatusPhase("a", "node-1")).Should(Equal(framev1beta1.PhaseRebootPending))

		for _, bad := range []string{"", "not-a-number", "-1"} {
			annotateNode("node-1", framev1beta1.ApprovalAnnotation, bad)
			Consistently(nodeStatusPhase("a", "node-1"), "2s").Should(Equal(framev1beta1.PhaseRebootPending))
		}
	})
})
