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
	"fmt"
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
	"github.com/rmocq/frame/internal/agent"
)

// patchFailingClient fails every Patch of one named Node and delegates
// everything else. Without it no spec in this package can reach the paths
// that only run when a write to the API server fails — and "the apiserver is
// briefly unavailable" is not an edge case here, it is what restarting k3s on
// the server node does every single time.
type patchFailingClient struct {
	client.Client
	failNode string
}

func (c patchFailingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if n, ok := obj.(*corev1.Node); ok && n.Name == c.failNode {
		return fmt.Errorf("simulated apiserver failure patching node %s", n.Name)
	}
	return c.Client.Patch(ctx, obj, patch, opts...)
}

// staleNodeListClient answers Node lists from a snapshot taken earlier,
// standing in for the manager's cache lagging behind a write another
// reconcile just made. Everything else, including the APIReader the
// reconciler is given separately, stays live.
type staleNodeListClient struct {
	client.Client
	snapshot *corev1.NodeList
}

func (c staleNodeListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if nl, ok := list.(*corev1.NodeList); ok {
		c.snapshot.DeepCopyInto(nl)
		return nil
	}
	return c.Client.List(ctx, list, opts...)
}

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

	// recWith builds the reconciler over a given client, so a spec can swap in
	// one that fails writes or serves a stale cache. APIReader always stays
	// the live client: that split is the whole point of the field.
	recWith := func(c client.Client) *NodeTuningReconciler {
		return &NodeTuningReconciler{
			Client:         c,
			APIReader:      k8sClient,
			Scheme:         k8sClient.Scheme(),
			Recorder:       record.NewFakeRecorder(100),
			DrainTimeout:   drainTimeout,
			RestartTimeout: restartTimeout,
		}
	}

	rec := func() *NodeTuningReconciler { return recWith(k8sClient) }

	reconcileWith := func(c client.Client, ntName string) {
		_, _ = recWith(c).Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: ntName}})
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
			// Reconciling on each poll: a NodeTuning that cordoned a node
			// carries the rollout finalizer, and nothing else in this suite
			// would ever run the reconcile that releases the node and lets
			// the deletion through.
			Eventually(func() bool {
				reconcileNow(n)
				return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: n}, &framev1beta1.NodeTuning{}))
			}, "5s", "50ms").Should(BeTrue())
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

	// Every write in the rollout is built by mutating a node and then patching
	// it, so a returned node that shares its Annotations map with the
	// cluster-wide list edits that list whether or not the patch ever lands —
	// an uncommitted write becomes indistinguishable from a committed one to
	// anything else reading the list in the same pass. The campaign lock no
	// longer depends on that sharing (it is an explicit value now), which is
	// exactly why this needs asserting directly: no behavioural spec can
	// reach it any more.
	It("selects and adopts nodes that share no state with the list they came from", func() {
		all := []corev1.Node{{ObjectMeta: metav1.ObjectMeta{
			Name:   "node-1",
			Labels: worker,
			Annotations: map[string]string{
				framev1beta1.TuningRolloutStartedAnnotation: "2026-08-17T00:00:00Z",
				framev1beta1.TuningRolloutOwnerAnnotation:   "a",
			},
		}}}

		selected, err := selectNodes(all, &metav1.LabelSelector{MatchLabels: worker})
		Expect(err).NotTo(HaveOccurred())
		Expect(selected).To(HaveLen(1))
		delete(selected[0].Annotations, framev1beta1.TuningRolloutStartedAnnotation)
		Expect(all[0].Annotations).To(HaveKey(framev1beta1.TuningRolloutStartedAnnotation),
			"mutating a selected node must not edit the cluster-wide view before the patch lands")

		adopted := adoptOwnedNodes(all, nil, "a")
		Expect(adopted).To(HaveLen(1))
		delete(adopted[0].Annotations, framev1beta1.TuningRolloutStartedAnnotation)
		Expect(all[0].Annotations).To(HaveKey(framev1beta1.TuningRolloutStartedAnnotation))
	})

	// The halt-release procedure the failure message and the annotation docs
	// both name has to be the one that works. Clearing the started annotation
	// leaves the previous attempt's restart request behind, and a rollout
	// that reads that stale request is measuring its deadline from a
	// timestamp already past it — the node re-fails instantly, retakes the
	// cluster-wide lock, and the agent, which acts on request values it has
	// not seen before, never acts on it either.
	It("releases a halt cleanly when the started annotation is cleared", func() {
		restartTimeout = 500 * time.Millisecond

		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		staleRequest := getNode("node-1").Annotations[framev1beta1.TuningRestartRequestedAnnotation]
		Expect(staleRequest).NotTo(BeEmpty())
		Eventually(phaseAfterReconcile("a", "node-1"), "10s", "100ms").Should(Equal(framev1beta1.PhaseFailed))

		By("a human clearing exactly the one annotation the message names")
		restartTimeout = 30 * time.Second
		Eventually(func() error {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "node-1"}, n); err != nil {
				return err
			}
			delete(n.Annotations, framev1beta1.TuningRolloutStartedAnnotation)
			return k8sClient.Update(ctx, n)
		}, "5s", "50ms").Should(Succeed())

		// The retry must re-issue the request, not inherit the dead one.
		Eventually(func() string {
			reconcileNow("a")
			return getNode("node-1").Annotations[framev1beta1.TuningRestartRequestedAnnotation]
		}, "5s", "100ms").ShouldNot(Or(BeEmpty(), Equal(staleRequest)))
		Expect(nodeStatus("a", "node-1").Phase).To(Equal(framev1beta1.PhaseApplying))

		agentReports("node-1", "k3s-agent", time.Now())
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
	})

	// The lock must survive a write that did not land. This is the failure
	// mode rule 2 says to expect — the apiserver is down for seconds while
	// k3s restarts — and a lock released on an unconfirmed patch turns it
	// into two cordoned nodes at once.
	It("does not release the lock when the uncordon patch fails", func() {
		createReadyNode("node-1", worker)
		createReadyNode("node-2", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		setObservedNeedingRestart("a", "node-2")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		approve(nt, "node-2")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		agentReports("node-1", "k3s-agent", time.Now())

		By("the uncordon failing on every attempt, so node-1 is never actually finished")
		failing := patchFailingClient{Client: k8sClient, failNode: "node-1"}
		for i := 0; i < 5; i++ {
			reconcileWith(failing, "a")
		}

		Expect(getNode("node-1").Spec.Unschedulable).To(BeTrue(), "the patch failed, so node-1 is still cordoned on the server")
		Expect(getNode("node-2").Spec.Unschedulable).To(BeFalse(),
			"node-1 is cordoned and unfinished; starting node-2 is the two-nodes-down outage")
		Expect(statusMessage("a", "node-2")).To(ContainSubstring("node-1"))
	})

	// The lock is a check-then-act, and the manager's cache is a snapshot
	// from before the write being checked for. Here "a" has already cordoned
	// node-1 when "b" reconciles off a cache that predates it: reading the
	// lock from that cache starts node-2 with node-1 still down.
	It("reads the campaign lock from the API server, not a stale cache", func() {
		createReadyNode("node-1", map[string]string{"role": "w1"})
		createReadyNode("node-2", map[string]string{"role": "w2"})
		a := createNodeTuning("a", map[string]string{"role": "w1"})
		b := createNodeTuning("b", map[string]string{"role": "w2"})
		setObservedNeedingRestart("a", "node-1")
		setObservedNeedingRestart("b", "node-2")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))
		agentReports("node-2", "k3s-agent", time.Now().Add(-time.Hour))
		approve(a, "node-1")
		approve(b, "node-2")

		stale := &corev1.NodeList{}
		Expect(k8sClient.List(ctx, stale)).To(Succeed())

		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		By("reconciling b off the pre-cordon snapshot")
		lagging := staleNodeListClient{Client: k8sClient, snapshot: stale}
		for i := 0; i < 3; i++ {
			reconcileWith(lagging, "b")
		}

		Expect(getNode("node-2").Spec.Unschedulable).To(BeFalse(),
			"node-1 is cordoned; b must see that even when its own cache does not")
		Expect(statusMessage("b", "node-2")).To(ContainSubstring("node-1"))
	})

	// The baseline has to be read when the restart is asked for, not when the
	// node was cordoned: a drain can take minutes, and a restart of the unit
	// during it would otherwise sit between an old baseline and the first
	// post-request probe, "verifying" a restart nobody asked for and handing
	// the node its workloads back at the moment the agent finally acts.
	It("takes the verification baseline when the restart is requested, not at cordon time", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		By("the unit restarting for an unrelated reason while the node drains")
		agentReports("node-1", "k3s-agent", time.Now())

		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
		Consistently(cordonedAfterReconcile("a", "node-1"), "2s", "100ms").Should(BeTrue(),
			"the requested restart has not happened yet; the earlier one is not evidence of it")

		agentReports("node-1", "k3s-agent", time.Now().Add(time.Second))
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
	})

	// Deleting the object mid-rollout must not strand the node. Nothing else
	// in the cluster knows that node is cordoned, or that it holds the
	// cluster-wide lock every other rollout waits on.
	It("releases a cordoned node when the NodeTuning is deleted mid-rollout", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		fetched := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "a"}, fetched)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fetched)).To(Succeed())

		Eventually(func() bool {
			reconcileNow("a")
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "a"}, &framev1beta1.NodeTuning{}))
		}, "5s", "100ms").Should(BeTrue())

		n := getNode("node-1")
		Expect(n.Spec.Unschedulable).To(BeFalse(), "a deleted NodeTuning must not leave a node cordoned forever")
		Expect(n.Annotations).NotTo(HaveKey(framev1beta1.TuningRolloutStartedAnnotation))
		Expect(n.Annotations).NotTo(HaveKey(framev1beta1.TuningRestartRequestedAnnotation))
	})

	// A node relabelled out of the selector mid-rollout is still cordoned,
	// still holding the lock, and still owed an uncordon. Dropping it from
	// the loop because it no longer matches abandons it there.
	It("finishes a rollout on a node that stops matching the selector", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		By("relabelling the node out of the selector while it is cordoned")
		Eventually(func() error {
			n := &corev1.Node{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "node-1"}, n); err != nil {
				return err
			}
			n.Labels["role"] = "somewhere-else"
			return k8sClient.Update(ctx, n)
		}, "5s", "50ms").Should(Succeed())

		agentReports("node-1", "k3s-agent", time.Now())
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
	})

	// An overlapping NodeTuning appearing mid-rollout is a problem for the
	// next rollout, not a reason to abandon a drained, cordoned node. Ordering
	// the overlap check ahead of the in-flight one pins the node at Failed
	// with the lock held, and it stays there even after the overlap is fixed.
	It("finishes a rollout even when a second NodeTuning starts selecting the node", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		createNodeTuning("b", worker)
		agentReports("node-1", "k3s-agent", time.Now())

		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
	})

	// "Already restarted for this generation" must mean a restart actually
	// happened for it. AppliedGeneration is also advanced by a node simply
	// being InSync, so reading the loop guard off that field refuses to ever
	// restart a node that reached this generation without one — the state
	// this spec sets up by hand is exactly what the InSync branch leaves
	// behind on a node restarted at some older generation.
	It("restarts a node whose only recorded restart belongs to an older generation", func() {
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		agentReports("node-1", "k3s-agent", time.Now().Add(-time.Hour))

		old := metav1.NewTime(time.Now().Add(-72 * time.Hour))
		Eventually(func() error {
			fetched := &framev1beta1.NodeTuning{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "a"}, fetched); err != nil {
				return err
			}
			fetched.Status.Nodes = []framev1beta1.NodeTuningNodeStatus{{
				Name:                "node-1",
				Phase:               framev1beta1.PhaseInSync,
				AppliedGeneration:   nt.Generation,
				RestartedAt:         &old,
				RestartedGeneration: nt.Generation - 1,
				Observed:            framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false}},
			}}
			return k8sClient.Status().Update(ctx, fetched)
		}, "5s", "50ms").Should(Succeed())

		approve(nt, "node-1")
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())
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

	// The same loop guard, but with the agent's tick landing where it really
	// does: the agent lists NodeTunings, spends seconds on the node in
	// nsenter, and only then writes status.nodes[].observed — from the
	// snapshot it listed. If the controller records the verified restart
	// during those seconds, the agent's write is built from an object that
	// predates it.
	//
	// That is not a lost field, it is a lost array: a CRD status patch is a
	// JSON merge patch, which replaces `nodes` wholesale rather than merging
	// it, so one stale snapshot reverts RestartedAt and RestartedGeneration
	// for every node at once. restartedForGeneration then reads false and the
	// same node is cordoned, drained and restarted a second time on a single
	// approval — the exact failure the spec above believes it covers, which it
	// passed only because nothing in the suite modelled a concurrent agent
	// write.
	//
	// This drives the production agent path (agent.PatchObserved), not a
	// hand-written imitation of it, so an agent that goes back to patching
	// from its own stale copy fails here.
	It("does not restart a node twice when an agent tick lands during the restart record", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		// The agent's tick begins here: it lists the object and goes off to
		// the node. Nothing it later writes can know about anything below.
		inFlight := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "a"}, inFlight)).To(Succeed())
		Expect(nodeStatus("a", "node-1").RestartedAt).To(BeNil(),
			"the snapshot must genuinely predate the restart record, or this spec proves nothing")

		// Meanwhile the unit comes back and the controller verifies it,
		// writing RestartedAt/RestartedGeneration and uncordoning.
		agentReports("node-1", "k3s-agent", time.Now())
		Eventually(cordonedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeFalse())
		Expect(nodeStatus("a", "node-1").RestartedAt).NotTo(BeNil())

		// And now the tick that started before all that finally writes. It
		// reports the same pre-restart KSM state the agent has always seen,
		// because the drop-in only takes effect on the next start and the
		// systemd cache is refreshed before Observe, not after.
		Expect(agent.PatchObserved(ctx, k8sClient, inFlight, "node-1",
			framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false, PagesSharing: 5528}})).To(Succeed())

		after := nodeStatus("a", "node-1")
		Expect(after.RestartedAt).NotTo(BeNil(),
			"the agent must not revert the controller's restart record — that record is the loop guard")
		Expect(after.RestartedGeneration).To(Equal(nt.Generation))
		Expect(after.Observed.KSM.PagesSharing).To(Equal(int64(5528)),
			"and the agent's own write must still land, or this is a deadlock rather than a fix")

		// The consequence, which is what actually costs a cluster: no second
		// cordon, no second drain, no second restart on one approval.
		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(getNode("node-1").Annotations).NotTo(HaveKey(framev1beta1.TuningRestartRequestedAnnotation))
	})

	// The third road to the same second restart, and the one an optimistic lock
	// opened rather than closed.
	//
	// The verify pass is not atomic. continueRollout uncordons the node and
	// deletes its rollout annotations — owner and baseline included — on the
	// server, and only then sets RestartedAt/RestartedGeneration, which live
	// nowhere but in that pass's memory until the status patch at the end of
	// Reconcile. If a concurrent writer makes that patch conflict and the
	// controller answers the conflict by giving up, the annotation clear has
	// already landed: rolloutOwnedBy is false, continueRollout is never
	// re-entered, RestartedAt is never recomputed, restartedForGeneration reads
	// false, and the approval annotation is still on the node. The next pass
	// falls straight through to startRollout — a second cordon, drain and
	// restart of the same node on one approval.
	//
	// The concurrent writer is not hypothetical: it is the node agent, ticking
	// every 30 seconds on this very node, and it is why the conflict path
	// exists at all.
	//
	// The racing client is wired as both the reconciler's Client and its
	// APIReader, so an agent write lands after the top-level read AND after
	// patchStatus's own read — covering the wide window and the narrow one in
	// one pass. A version that requeues on conflict fails on RestartedAt and
	// again on the second cordon; a version that retries without re-reading
	// conflicts until it exhausts its backoff and fails the same way.
	It("records the restart even when the status patch conflicts on the verify pass", func() {
		baseline := time.Now().Add(-time.Hour)
		createReadyNode("node-1", worker)
		nt := createNodeTuning("a", worker)
		setObservedNeedingRestart("a", "node-1")
		agentReports("node-1", "k3s-agent", baseline)

		approve(nt, "node-1")
		Eventually(restartRequestedAfterReconcile("a", "node-1"), "5s", "100ms").Should(BeTrue())

		// The unit comes back. The next reconcile is the verify pass.
		agentReports("node-1", "k3s-agent", time.Now())

		listed := &framev1beta1.NodeTuning{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "a"}, listed)).To(Succeed())
		racing := &writeOnReadClient{Client: k8sClient, remaining: 2, write: func() {
			Expect(agent.PatchObserved(ctx, k8sClient, listed, "node-1",
				framev1beta1.ObservedTuning{KSM: &framev1beta1.ObservedKSM{MemoryKSM: false, PagesSharing: 7777}})).To(Succeed())
		}}

		verifier := recWith(racing)
		verifier.APIReader = racing
		_, err := verifier.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "a"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(racing.fired).To(BeNumerically(">=", 1),
			"the racing write must actually have happened, or this spec proves nothing")

		// The verify pass did commit its disruptive half — which is exactly why
		// its status half may not be dropped.
		Expect(getNode("node-1").Annotations).NotTo(HaveKey(framev1beta1.TuningRolloutOwnerAnnotation))
		Expect(getNode("node-1").Spec.Unschedulable).To(BeFalse())

		after := nodeStatus("a", "node-1")
		Expect(after).NotTo(BeNil())
		Expect(after.RestartedAt).NotTo(BeNil(),
			"the restart record cannot be recomputed once the owner annotation is cleared, so a conflict must not discard it")
		Expect(after.RestartedGeneration).To(Equal(nt.Generation))
		Expect(after.Observed.KSM.PagesSharing).To(Equal(int64(7777)),
			"and the agent's concurrent write must survive too, or the retry has simply reversed who loses")

		// The harm, asserted directly.
		Consistently(cordonedAfterReconcile("a", "node-1"), "3s", "100ms").Should(BeFalse())
		Expect(getNode("node-1").Annotations).NotTo(HaveKey(framev1beta1.TuningRestartRequestedAnnotation))
	})
})
