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
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

const frameNodeFinalizer = "frame.plume-labs.io/framenode"

// FrameNode lifecycle phases.
const (
	// nodePhaseAwaitingNode means no Kubernetes Node carries the name this
	// FrameNode classifies. FrameNode does not install machines —
	// FrameInstall does — so there is nothing to do but wait for the node to
	// join; the Node watch wakes the reconciler when it does.
	nodePhaseAwaitingNode = "AwaitingNode"
	// nodePhaseOnline means the corresponding Kubernetes Node is Ready.
	nodePhaseOnline = "Online"
	// nodePhaseDegraded means the corresponding Kubernetes Node reports
	// Ready=False.
	nodePhaseDegraded = "Degraded"
)

// nodePhaseFromStatus is the FrameNode reconciler's state-machine input.
//
// It used to read fn.Status.Phase. That field is gone (F2), and the Ready
// condition's reason has always carried the same value — the controller set
// Reason: phase alongside it — so this is a read of the same information
// from the place it now lives. It is deliberately not the conversion
// package's FrameNodePhaseFromConditions: controllers work on the hub and
// must not depend on a spoke's projection, which answers "" for any reason
// outside v1alpha1's phase enum precisely because it exists to feed a
// v1alpha1 reader, not to drive this state machine.
func nodePhaseFromStatus(fn *framev1beta1.FrameNode) string {
	return readyReason(fn.Status.Conditions)
}

// FrameNodeReconciler reconciles a FrameNode object
type FrameNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update

func (r *FrameNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var fn framev1beta1.FrameNode
	if err := r.Get(ctx, req.NamespacedName, &fn); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !fn.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &fn)
	}

	if !controllerutil.ContainsFinalizer(&fn, frameNodeFinalizer) {
		controllerutil.AddFinalizer(&fn, frameNodeFinalizer)
		return ctrl.Result{}, r.Update(ctx, &fn)
	}

	// Classification comes first, and it does not depend on provisioning.
	//
	// Role, rack, zone, service class and RDMA are facts about a machine that
	// hold however the machine was installed, and they are what the inference
	// provider, the scheduler and the placement screen read. So if the node
	// this object describes is in the cluster, its labels get projected --
	// whatever phase the FrameNode is in, and whether or not Frame ever
	// installed it.
	//
	// Before this, projection lived only at the end of the provisioning path.
	// Measured on the live cluster on 2026-09-12: three FrameNode objects 48
	// days old, none with spec.disk, all stopped at Discovered, and not one
	// node carrying frame.plume-labs.io/service-class -- which
	// internal/services/provider/inference/inference.go uses as the
	// nodeSelector of every Deployment it creates. Nothing had been scheduled
	// through it only because the estate's one FrameService was blocked a step
	// earlier on a PVC that does not exist.
	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}
	var node corev1.Node
	switch err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); {
	case err == nil:
		if reason, msg, ok := r.mayClassify(ctx, &fn, nodeName); !ok {
			return ctrl.Result{}, r.setPhase(ctx, &fn, reason, msg)
		}
		return r.reconcileOnline(ctx, &fn)
	case !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	// No node of that name is in the cluster. This used to fall through to a
	// provisioning path that dialled a Talos maintenance API; that path is
	// gone, and FrameNode only classifies.
	return r.awaitNode(ctx, &fn, nodeName)
}

// awaitNode records that the node this FrameNode classifies is not in the
// cluster, and polls as a backstop to the Node watch.
func (r *FrameNodeReconciler) awaitNode(ctx context.Context, fn *framev1beta1.FrameNode, nodeName string) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("Node not in the cluster", "nodeName", nodeName)
	r.Recorder.Event(fn, corev1.EventTypeWarning, "NodeNotFound", "Waiting for node "+nodeName+" to join the cluster")
	return ctrl.Result{RequeueAfter: 30 * time.Second},
		r.setPhase(ctx, fn, nodePhaseAwaitingNode, "No Kubernetes Node named "+nodeName+"; waiting for it to join the cluster")
}

// The labels Frame projects from a FrameNode onto its corev1.Node. These are
// API, not an implementation detail: the inference provider's NodeSelector
// and the FrameJob controller's Workflow labels both read
// nodeLabelServiceClass, so renaming one of these silently unschedules
// everything that selects on it (F12).
//
// rack is under frame.plume-labs.io, not topology.kubernetes.io: the
// well-known keys in the kubernetes.io namespace are zone and region, and
// rack is not one of them — that prefix is reserved for upstream.
//
// A caution for anyone reading nodeLabelServiceClass and generalising: the
// same key means something else on a Namespace, where
// FrameResourceQuota uses it to select which namespaces a quota projects
// into. Two meanings, one key, distinguished only by what it is attached to.
const (
	nodeLabelRack         = "frame.plume-labs.io/rack"
	nodeLabelZone         = "topology.kubernetes.io/zone"
	nodeLabelServiceClass = "frame.plume-labs.io/service-class"
	nodeLabelRole         = "frame.plume-labs.io/role"
	nodeLabelRDMA         = "frame.plume-labs.io/rdma"

	// nodePhaseUnclassified is the state of a FrameNode that names a real node
	// but is not allowed to write labels onto it. See mayClassify.
	nodePhaseUnclassified = "Unclassified"
)

// frameNodeLabels is every key reconcileDelete strips. Keeping the list in
// one place is what stops a fifth label being added to the write path and
// forgotten in the delete path.
var frameNodeLabels = []string{
	nodeLabelRack, nodeLabelZone, nodeLabelServiceClass, nodeLabelRole, nodeLabelRDMA,
}

// applyNodeLabel sets key when value is non-empty and removes it otherwise.
// Writing an empty value is legal but makes "unclassified" and "explicitly
// empty" indistinguishable to a selector, which is not a distinction Frame
// wants to have to explain.
func applyNodeLabel(labels map[string]string, key, value string) {
	if value == "" {
		delete(labels, key)
		return
	}
	labels[key] = value
}

// mayClassify decides whether this FrameNode is allowed to write labels onto
// the node it names. It exists because matching is by name and nothing proves
// a FrameNode has anything to do with the node it claims.
//
// That was already true of reconcileOnline, but reaching it used to cost a
// provisioning attempt and two reconciles. Projecting before any of that --
// which is the point of the change this guard ships with -- made a latent
// escalation immediate: a principal bound to framenode-editor-role has
// no RBAC on Node objects at all, and could otherwise create a bare FrameNode
// named after a classified node and have applyNodeLabel strip every Frame
// label off it, service-class included, on the next reconcile.
//
// Returns a condition reason, a message, and whether projection may proceed.
func (r *FrameNodeReconciler) mayClassify(ctx context.Context, fn *framev1beta1.FrameNode, nodeName string) (string, string, bool) {
	// An object that classifies nothing has nothing to say about a node, and
	// must not be able to say it by deleting what is there. This is the shape
	// the escalation takes: every field empty, so every label removed.
	if fn.Spec.Rack == "" && fn.Spec.Zone == "" && fn.Spec.Role == "" &&
		string(fn.Spec.ServiceClass) == "" && fn.Spec.RDMAInterface == "" {
		return nodePhaseUnclassified,
			"No classification to project; refusing to strip labels from node " + nodeName, false
	}

	// Two FrameNodes naming one node is a contradiction whoever wrote them
	// second. Refusing the newcomer keeps the node's labels answering to the
	// object that already owns them.
	var list framev1beta1.FrameNodeList
	if err := r.List(ctx, &list); err != nil {
		// Failing open here would defeat the guard, so fail closed and retry
		// on the next reconcile.
		return nodePhaseUnclassified, "Cannot verify node ownership: " + err.Error(), false
	}
	for i := range list.Items {
		other := &list.Items[i]
		if other.UID == fn.UID {
			continue
		}
		name := other.Spec.Hostname
		if name == "" {
			name = other.Name
		}
		if name != nodeName {
			continue
		}
		if other.CreationTimestamp.Time.Before(fn.CreationTimestamp.Time) {
			return nodePhaseUnclassified,
				"Node " + nodeName + " is already classified by FrameNode " + other.Namespace + "/" + other.Name, false
		}
	}
	return "", "", true
}

// reconcileOnline syncs Kubernetes Node readiness back into the FrameNode status.
func (r *FrameNodeReconciler) reconcileOnline(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return r.awaitNode(ctx, fn, nodeName)
		}
		return ctrl.Result{}, err
	}

	base := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	applyNodeLabel(node.Labels, nodeLabelRack, fn.Spec.Rack)
	applyNodeLabel(node.Labels, nodeLabelZone, fn.Spec.Zone)
	applyNodeLabel(node.Labels, nodeLabelServiceClass, string(fn.Spec.ServiceClass))
	applyNodeLabel(node.Labels, nodeLabelRole, fn.Spec.Role)
	rdma := ""
	if fn.Spec.RDMAInterface != "" {
		rdma = "true"
	}
	applyNodeLabel(node.Labels, nodeLabelRDMA, rdma)
	// The old topology.kubernetes.io/rack key is removed here as well as in
	// reconcileDelete, so an existing node relabels itself on its next
	// reconcile rather than carrying both keys until someone deletes the
	// FrameNode.
	delete(node.Labels, "topology.kubernetes.io/rack")
	if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node labels: %w", err)
	}

	phase := nodePhase(&node)
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	fn.Status.NodeName = node.Name
	fn.Status.Capacity = node.Status.Capacity
	fn.Status.Allocatable = node.Status.Allocatable
	fn.Status.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == nodePhaseOnline),
		Reason:             phase,
		Message:            "Synced from k8s Node",
		ObservedGeneration: fn.Generation,
	})
	if err := r.Status().Patch(ctx, fn, patch); err != nil {
		return ctrl.Result{}, err
	}

	eventType := corev1.EventTypeNormal
	if phase != nodePhaseOnline {
		eventType = corev1.EventTypeWarning
	}
	r.Recorder.Event(fn, eventType, phase, fmt.Sprintf("Node %s is %s", nodeName, phase))
	log.Info("Reconciled FrameNode", "phase", phase, "nodeName", nodeName)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FrameNodeReconciler) reconcileDelete(ctx context.Context, fn *framev1beta1.FrameNode) (ctrl.Result, error) {
	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err == nil {
		base := node.DeepCopy()
		for _, key := range frameNodeLabels {
			delete(node.Labels, key)
		}
		// The pre-freeze key, for nodes labelled before F12.
		delete(node.Labels, "topology.kubernetes.io/rack")
		if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(fn, frameNodeFinalizer)
	return ctrl.Result{}, r.Update(ctx, fn)
}

func (r *FrameNodeReconciler) setPhase(ctx context.Context, fn *framev1beta1.FrameNode, phase, msg string) error {
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.ObservedGeneration = fn.Generation
	// Status is derived from the phase rather than hardcoded to False, so
	// setPhase(ctx, fn, nodePhaseOnline, ...) stays honest if it is ever
	// called that way. Today every caller passes a non-Online phase.
	meta.SetStatusCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             conditionStatus(phase == nodePhaseOnline),
		Reason:             phase,
		Message:            msg,
		ObservedGeneration: fn.Generation,
	})
	return r.Status().Patch(ctx, fn, patch)
}

func nodePhase(node *corev1.Node) string {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			switch c.Status {
			case corev1.ConditionTrue:
				return nodePhaseOnline
			case corev1.ConditionFalse:
				return nodePhaseDegraded
			}
		}
	}
	return "Offline"
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameNode{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.nodeToFrameNode)).
		Named("framenode").
		Complete(r)
}

func (r *FrameNodeReconciler) nodeToFrameNode(ctx context.Context, obj client.Object) []reconcile.Request {
	var list framev1beta1.FrameNodeList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, fn := range list.Items {
		name := fn.Spec.Hostname
		if name == "" {
			name = fn.Name
		}
		if name == obj.GetName() {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: fn.Name, Namespace: fn.Namespace},
			})
		}
	}
	return reqs
}
