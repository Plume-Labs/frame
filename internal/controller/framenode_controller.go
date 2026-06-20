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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const frameNodeFinalizer = "frame.plume-labs.io/framenode"

// FrameNodeReconciler reconciles a FrameNode object
type FrameNodeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=framenodes/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;patch;update

func (r *FrameNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var fn framev1alpha1.FrameNode
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

	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Node not yet joined", "nodeName", nodeName)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setPhase(ctx, &fn, "Provisioning", "Waiting for node to join the cluster")
		}
		return ctrl.Result{}, err
	}

	// Apply topology and service-class labels
	base := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}
	node.Labels["topology.kubernetes.io/rack"] = fn.Spec.Rack
	node.Labels["topology.kubernetes.io/zone"] = fn.Spec.Zone
	node.Labels["frame.plume-labs.io/service-class"] = fn.Spec.ServiceClass
	node.Labels["frame.plume-labs.io/role"] = fn.Spec.Role
	if fn.Spec.RDMAInterface != "" {
		node.Labels["frame.plume-labs.io/rdma"] = "true"
	}
	if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching node labels: %w", err)
	}

	phase := nodePhase(&node)
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.Phase = phase
	fn.Status.NodeName = node.Name
	fn.Status.Capacity = node.Status.Capacity
	fn.Status.Allocatable = node.Status.Allocatable
	fn.Status.KubeletVersion = node.Status.NodeInfo.KubeletVersion
	setCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus(phase == "Online"),
		Reason:             phase,
		Message:            "Synced from k8s Node",
		ObservedGeneration: fn.Generation,
	})
	if err := r.Status().Patch(ctx, &fn, patch); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Reconciled FrameNode", "phase", phase, "nodeName", nodeName)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *FrameNodeReconciler) reconcileDelete(ctx context.Context, fn *framev1alpha1.FrameNode) (ctrl.Result, error) {
	nodeName := fn.Spec.Hostname
	if nodeName == "" {
		nodeName = fn.Name
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err == nil {
		base := node.DeepCopy()
		delete(node.Labels, "topology.kubernetes.io/rack")
		delete(node.Labels, "topology.kubernetes.io/zone")
		delete(node.Labels, "frame.plume-labs.io/service-class")
		delete(node.Labels, "frame.plume-labs.io/role")
		delete(node.Labels, "frame.plume-labs.io/rdma")
		if err := r.Patch(ctx, &node, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
	}

	controllerutil.RemoveFinalizer(fn, frameNodeFinalizer)
	return ctrl.Result{}, r.Update(ctx, fn)
}

func (r *FrameNodeReconciler) setPhase(ctx context.Context, fn *framev1alpha1.FrameNode, phase, msg string) error {
	patch := client.MergeFrom(fn.DeepCopy())
	fn.Status.Phase = phase
	setCondition(&fn.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus(false),
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
				return "Online"
			case corev1.ConditionFalse:
				return "Degraded"
			}
		}
	}
	return "Offline"
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.FrameNode{}).
		Named("framenode").
		Complete(r)
}
