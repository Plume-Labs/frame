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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Like the Task 4/5 suite next door, this one calls Reconcile directly: no
// live manager runs here, so nothing re-triggers a reconcile on its own.
//
// Every polled matcher below reconciles on each poll. That matters more here
// than anywhere else in this package: the assertions this suite leans on
// hardest are Consistently(...).Should(BeFalse()) — "this node must NOT be
// cordoned" — and a matcher that merely read the Node without reconciling
// would report exactly that for an implementation that cordons every node in
// sight, simply because nothing ever ran. The reconcile is what gives those
// assertions any discriminating power at all.
var _ = Describe("NodeTuning rollout", func() {
	ctx := context.Background()

	worker := map[string]string{"role": "worker"}

	var testNodes []string
	var testNodeTunings []string
	var testPods []*corev1.Pod

	// The production timeouts are minutes (see defaultDrainTimeout /
	// defaultRestartTimeout); no test can wait those out, so each spec sets
	// what it needs. The default here is long enough that a spec which is not
	// about timing out never trips one by accident.
	var drainTimeout, restartTimeout time.Duration

	BeforeEach(func() {
		drainTimeout = 30 * time.Second
		restartTimeout = 30 * time.Second
	})

	rec := func() *NodeTuningReconciler {
		return &NodeTuningReconciler{
			Client:         k8sClient,
			APIReader:      k8sClient,
			Scheme:         k8sClient.Scheme(),
			Recorder:       record.NewFakeRecorder(100),
			DrainTimeout:   drainTimeout,
			RestartTimeout: restartTimeout,
		}
	}

	reconcileNow := func(ntName string) {
		_, _ = rec().Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: ntName}})
	}

	getNode := func(name string) *corev1.Node {
		n := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, n)).To(Succeed())
		return n
	}

	// setNodeReady writes the NodeReady condition. envtest nodes have no
	// conditions at all when created, which reads as NotReady — and a
	// cluster where every node is NotReady can never start a rollout, so
	// every node here starts Ready unless a spec says otherwise.
	setNodeReady := func(name string, ready bool) {
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		Eventually(func() error {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, n); err != nil {
				return err
			}
			n.Status.Conditions = []corev1.NodeCondition{{
				Type:               corev1.NodeReady,
				Status:             status,
				LastHeartbeatTime:  metav1.Now(),
				LastTransitionTime: metav1.Now(),
			}}
			return k8sClient.Status().Update(ctx, n)
		}, "5s", "50ms").Should(Succeed())
	}

	createReadyNode := func(name string, lbls map[string]string) {
		n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls}}
		Expect(k8sClient.Create(ctx, n)).To(Succeed())
		testNodes = append(testNodes, name)
		setNodeReady(name, true)
	}

	annotateNode := func(nodeName string, kv map[string]string) {
		Eventually(func() error {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
				return err
			}
			if n.Annotations == nil {
				n.Annotations = map[string]string{}
			}
			for k, v := range kv {
				n.Annotations[k] = v
			}
			return k8sClient.Update(ctx, n)
		}, "5s", "50ms").Should(Succeed())
	}

	// agentReports plays the node agent's half of the restart protocol: it
	// publishes which unit this node runs and that unit's current
	// ActiveEnterTimestamp. Without both, the controller has no allowlisted
	// unit to name and no baseline to compare a restart against.
	agentReports := func(nodeName, unit string, activeEnter time.Time) {
		annotateNode(nodeName, map[string]string{
			framev1beta1.TuningUnitAnnotation:            unit,
			framev1beta1.TuningUnitActiveEnterAnnotation: activeEnter.Format(time.RFC3339Nano),
		})
	}

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

	// setObservedNeedingRestart reports an observed KSM state that disagrees
	// with createNodeTuning's spec.ksm.enabled=true. That mismatch is
	// restart-gated (the KSM drop-in only takes effect on the unit's next
	// start), which is what puts a node in front of the approval gate and
	// therefore in front of this rollout at all.
	setObservedNeedingRestart := func(ntName, nodeName string) {
		Eventually(func() error {
			nt := &framev1beta1.NodeTuning{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: ntName}, nt); err != nil {
				return err
			}
			found := false
			for i := range nt.Status.Nodes {
				if nt.Status.Nodes[i].Name == nodeName {
					nt.Status.Nodes[i].Observed = framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false}}
					found = true
				}
			}
			if !found {
				nt.Status.Nodes = append(nt.Status.Nodes, framev1beta1.NodeTuningNodeStatus{
					Name:     nodeName,
					Observed: framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false}},
				})
			}
			return k8sClient.Status().Update(ctx, nt)
		}, "5s", "50ms").Should(Succeed())
	}

	approve := func(nt *framev1beta1.NodeTuning, nodeName string) {
		annotateNode(nodeName, map[string]string{
			framev1beta1.ApprovalAnnotation: strconv.FormatInt(nt.Generation, 10),
		})
	}

	nodeStatus := func(ntName, nodeName string) *framev1beta1.NodeTuningNodeStatus {
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

	statusMessage := func(ntName, nodeName string) string {
		if ns := nodeStatus(ntName, nodeName); ns != nil {
			return ns.Message
		}
		return ""
	}

	// phaseAfterReconcile, cordonedAfterReconcile and restartRequestedAfterReconcile
	// all reconcile once per poll — see this suite's opening comment for why
	// that is load-bearing rather than convenience.
	phaseAfterReconcile := func(ntName, nodeName string) func() framev1beta1.NodeTuningPhase {
		return func() framev1beta1.NodeTuningPhase {
			reconcileNow(ntName)
			if ns := nodeStatus(ntName, nodeName); ns != nil {
				return ns.Phase
			}
			return ""
		}
	}

	cordonedAfterReconcile := func(ntName, nodeName string) func() bool {
		return func() bool {
			reconcileNow(ntName)
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
				return false
			}
			return n.Spec.Unschedulable
		}
	}

	restartRequestedAfterReconcile := func(ntName, nodeName string) func() bool {
		return func() bool {
			reconcileNow(ntName)
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, n); err != nil {
				return false
			}
			_, ok := n.Annotations[framev1beta1.TuningRestartRequestedAnnotation]
			return ok
		}
	}

	createPodOnNode := func(name, nodeName string, owner *metav1.OwnerReference) {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: corev1.PodSpec{
				NodeName:      nodeName,
				Containers:    []corev1.Container{{Name: "c", Image: "busybox"}},
				RestartPolicy: corev1.RestartPolicyNever,
			},
		}
		if owner != nil {
			p.OwnerReferences = []metav1.OwnerReference{*owner}
		}
		Expect(k8sClient.Create(ctx, p)).To(Succeed())
		testPods = append(testPods, p)
	}

	AfterEach(func() {
		for _, p := range testPods {
			_ = k8sClient.Delete(ctx, p, client.GracePeriodSeconds(0))
		}
		testPods = nil

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

	// Losing one node is a rolling operation; losing two on a three-node
	// cluster is an outage. An implementation that starts anyway would cordon
	// node-1 here on the first reconcile.
	It("refuses to start on a node while another is not Ready", func() {
		createReadyNode("node-1", worker)
		createReadyNode("node-2", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))

		setNodeReady("node-2", false)
		approve(nt, "node-1")

		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(statusMessage("a", "node-1")).To(ContainSubstring("node-2"))
	})

	// The positive control for the refusal above: with every node Ready the
	// same approval must actually start the rollout. Without this, an
	// implementation that never rolls anything out passes the refusal test.
	It("starts the rollout once every node is Ready", func() {
		createReadyNode("node-1", worker)
		createReadyNode("node-2", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")

		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		Expect(nodeStatus("a", "node-1").Phase).To(Equal(framev1beta1.PhaseApplying))
	})

	// One node at a time: while node-1's rollout is in flight, node-2 must not
	// be touched even though it is approved and needs the same change.
	It("rolls one node at a time", func() {
		createReadyNode("node-1", worker)
		createReadyNode("node-2", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		setObservedNeedingRestart("a", "node-2")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		approve(nt, "node-2")
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		Consistently(cordonedAfterReconcile("a", "node-2"), "3s", "100ms").Should(BeFalse())
		Expect(statusMessage("a", "node-2")).To(ContainSubstring("node-1"))
	})

	// The verification that matters: k3s-agent comes back in seconds, while a
	// node only reports NotReady after roughly forty seconds of missed lease,
	// so a healthy restart usually never flaps the node. node-1 stays Ready
	// for this whole spec — an implementation that waits for a NotReady flap
	// as its evidence never sees one and never uncordons, so it fails here.
	It("verifies the restart by the unit's ActiveEnterTimestamp, not by node readiness", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		By("the agent reporting the unit came back, with the node never having gone NotReady")
		restartedAt := time.Now()
		agentReports("node-1", "k3s-agent", restartedAt)

		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
		ns := nodeStatus("a", "node-1")
		Expect(ns.RestartedAt).NotTo(BeNil())
		Expect(ns.RestartedAt.Time.Unix()).To(Equal(restartedAt.Unix()))
		Expect(getNode("node-1").Annotations).NotTo(HaveKey(framev1beta1.TuningRestartRequestedAnnotation))
	})

	// Node readiness is not the evidence a restart happened, but it is still
	// required before handing workloads back to the node.
	It("does not uncordon until the node reports Ready", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		setNodeReady("node-1", false)
		agentReports("node-1", "k3s-agent", time.Now())

		Consistently(cordonedAfterReconcile("a", "node-1"), "2s", "100ms").Should(BeTrue())

		setNodeReady("node-1", true)
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
	})

	// Restarting k3s on the server node takes the apiserver down for seconds,
	// so the controller's own reads are expected to fail mid-wait. Here the
	// probe returns garbage instead: an implementation that treats the first
	// unreadable probe as fatal marks the node Failed and abandons it
	// cordoned, which is indistinguishable from a restart that never fired.
	It("tolerates an unreadable probe mid-wait and still completes", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		By("the unit's timestamp being unreadable for a while, as it is while the apiserver is down")
		annotateNode("node-1", map[string]string{framev1beta1.TuningUnitActiveEnterAnnotation: "not-a-timestamp"})
		Consistently(phaseAfterReconcile("a", "node-1"), "2s", "100ms").Should(Equal(framev1beta1.PhaseApplying))

		agentReports("node-1", "k3s-agent", time.Now())
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
		Expect(nodeStatus("a", "node-1").RestartedAt).NotTo(BeNil())
	})

	// A rollout that keeps going through failures turns one broken node into
	// an outage. A partially tuned cluster is recoverable; this is not.
	It("halts the campaign and leaves the node cordoned on failure", func() {
		restartTimeout = 500 * time.Millisecond

		createReadyNode("node-1", worker)
		createReadyNode("node-2", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		setObservedNeedingRestart("a", "node-2")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))

		// node-1's unit never comes back: its ActiveEnterTimestamp is never
		// advanced, so the wait runs out.
		approve(nt, "node-1")
		Eventually(phaseAfterReconcile("a", "node-1"), "10s", "100ms").Should(Equal(framev1beta1.PhaseFailed))
		Expect(cordonedAfterReconcile("a", "node-1")()).To(BeTrue())

		approve(nt, "node-2")
		Consistently(cordonedAfterReconcile("a", "node-2"), "3s", "100ms").Should(BeFalse())
		Expect(statusMessage("a", "node-2")).To(ContainSubstring("node-1"))
	})

	// A failed node stays failed. Re-reconciling must not quietly restart the
	// wait (which would leave the campaign halted forever with a Applying
	// phase that claims progress is being made).
	It("keeps a failed node failed", func() {
		restartTimeout = 500 * time.Millisecond

		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(phaseAfterReconcile("a", "node-1"), "10s", "100ms").Should(Equal(framev1beta1.PhaseFailed))

		// Even the unit coming back late must not resurrect it: a human has to
		// look at a halted campaign.
		agentReports("node-1", "k3s-agent", time.Now())
		Consistently(phaseAfterReconcile("a", "node-1"), "2s", "100ms").Should(Equal(framev1beta1.PhaseFailed))
		Expect(getNode("node-1").Spec.Unschedulable).To(BeTrue())
	})

	// The drain is not decoration: asking for the restart while a workload is
	// still on the node is the disruption cordoning was supposed to avoid.
	It("does not ask for the restart while a pod is still on the node", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		createPodOnNode("stubborn", "node-1", nil)

		approve(nt, "node-1")
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		Consistently(restartRequestedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
	})

	// DaemonSet pods are never drained — they are recreated on the same node
	// by design, so waiting for them to leave waits forever. The node agent
	// itself is one of them, and it is what has to service the restart
	// request.
	It("does not wait for DaemonSet pods to leave", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		createPodOnNode("frame-agent", "node-1", &metav1.OwnerReference{
			APIVersion: "apps/v1", Kind: "DaemonSet", Name: "frame-agent", UID: "1234",
		})

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
	})

	// The unit name reaching the node comes from an annotation, which is not
	// a trustworthy channel. The compile-time allowlist (agent.IsRestartable)
	// is the boundary, and an unlisted unit is a refusal, never a cordon.
	It("refuses to start when the reported unit is not on the restart allowlist", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "../../etc/k3s", time.Now().Add(-time.Hour))

		approve(nt, "node-1")

		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(nodeStatus("a", "node-1").Phase).To(Equal(framev1beta1.PhaseFailed))
		Expect(statusMessage("a", "node-1")).To(ContainSubstring("allowlist"))
	})

	// Without a baseline there is nothing to compare a restart against, so
	// "did it come back" is unanswerable. Starting anyway would cordon and
	// drain a node for a restart that could never be verified.
	It("refuses to start when the agent has not reported the unit's timestamp", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		annotateNode("node-1", map[string]string{framev1beta1.TuningUnitAnnotation: "k3s-agent"})

		approve(nt, "node-1")

		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(nodeStatus("a", "node-1").Phase).To(Equal(framev1beta1.PhaseRebootPending))
	})

	// One restart per approved generation. Right after a verified restart the
	// agent has not re-observed yet, so the spec still disagrees with
	// observed — an implementation that re-enters the rollout on that
	// disagreement restarts the node in a loop, forever, on one approval.
	It("does not restart the same node twice for one approved generation", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		agentReports("node-1", "k3s-agent", time.Now())
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())

		// Observed still reports the old value, exactly as it does until the
		// agent's next tick.
		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(getNode("node-1").Annotations).NotTo(HaveKey(framev1beta1.TuningRestartRequestedAnnotation))
	})
})
