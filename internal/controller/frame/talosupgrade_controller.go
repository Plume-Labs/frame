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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

const talosUpgradeFinalizer = "frame.plume-labs.io/talosupgrade"

// TalosUpgradeReconciler reconciles a TalosUpgrade object
type TalosUpgradeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosupgrades,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosupgrades/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosupgrades/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *TalosUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var tu framev1alpha1.TalosUpgrade
	if err := r.Get(ctx, req.NamespacedName, &tu); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tu.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(&tu, talosUpgradeFinalizer)
		return ctrl.Result{}, r.Update(ctx, &tu)
	}

	if !controllerutil.ContainsFinalizer(&tu, talosUpgradeFinalizer) {
		controllerutil.AddFinalizer(&tu, talosUpgradeFinalizer)
		return ctrl.Result{}, r.Update(ctx, &tu)
	}

	// Upgrade is not idempotent-safe to call on every reconcile: triggering it
	// again while the node is rebooting would cause a second upgrade. Guard by
	// ObservedGeneration — only call Upgrade when spec.generation changes.
	for _, cond := range tu.Status.Conditions {
		if cond.Type == conditionTypeReady && cond.ObservedGeneration == tu.Generation &&
			(cond.Reason == "UpgradeRequested" || cond.Reason == "AlreadyAtVersion") {
			return ctrl.Result{}, nil
		}
	}

	c, err := buildTalosClient(ctx, r.Client, tu.Namespace, tu.Spec.TalosEndpoint, tu.Spec.TalosSecretRef)
	if err != nil {
		r.Recorder.Event(&tu, corev1.EventTypeWarning, "ClientBuildFailed", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setCondition(ctx, &tu,
			metav1.ConditionFalse, "ClientBuildFailed", err.Error())
	}
	defer c.Close() //nolint:errcheck // upgrade already ran; a failed close of the Talos client isn't actionable

	// stage=false: upgrade without staging; force=false: honour version guards.
	//
	// c.Upgrade is deprecated in favour of LifecycleClient, but the swap is not
	// mechanical: LifecycleClient is the raw generated gRPC stub
	// (machineapi.LifecycleServiceClient), so migrating means hand-building the
	// UpgradeRequest (image, reboot mode, stage, force) and reimplementing the
	// response/error filtering that client.Client.Upgrade currently does via
	// FilterMessages. isAlreadyAtVersion below depends on the exact error
	// message shape that filtering produces; a naive migration could silently
	// change that shape and break the idempotency guard above (which exists
	// specifically to avoid re-triggering an upgrade while a node reboots).
	// Deferring until that path is migrated deliberately and re-verified.
	_, err = c.Upgrade(ctx, tu.Spec.Image, false, false) //nolint:staticcheck
	if err != nil {
		// Talos returns a "no upgrade required" error when already at the target version.
		if isAlreadyAtVersion(err) {
			msg := fmt.Sprintf("Node %s already at %s", tu.Spec.NodeName, tu.Spec.Image)
			r.Recorder.Event(&tu, corev1.EventTypeNormal, "AlreadyAtVersion", msg)
			talosUpgradeAlreadyAtVersion.Inc()
			log.Info("Node already at target version", "node", tu.Spec.NodeName, "image", tu.Spec.Image)
			return ctrl.Result{}, r.setCondition(ctx, &tu, metav1.ConditionTrue, "AlreadyAtVersion", msg)
		}
		r.Recorder.Event(&tu, corev1.EventTypeWarning, "UpgradeFailed", fmt.Sprintf("Upgrade: %v", err))
		talosUpgradeFailed.Inc()
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setCondition(ctx, &tu,
			metav1.ConditionFalse, "UpgradeFailed", fmt.Sprintf("Upgrade: %v", err))
	}

	msg := fmt.Sprintf("Upgrade to %s requested on %s", tu.Spec.Image, tu.Spec.NodeName)
	r.Recorder.Event(&tu, corev1.EventTypeNormal, "UpgradeRequested", msg)
	talosUpgradeRequested.Inc()
	log.Info("Talos upgrade requested", "node", tu.Spec.NodeName,
		"endpoint", tu.Spec.TalosEndpoint, "image", tu.Spec.Image)

	return ctrl.Result{}, r.setCondition(ctx, &tu, metav1.ConditionTrue, "UpgradeRequested", msg)
}

// isAlreadyAtVersion returns true when the Talos API indicates the node is
// already running the requested image and no upgrade is needed.
func isAlreadyAtVersion(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already at target version") || strings.Contains(msg, "no upgrade required")
}

func (r *TalosUpgradeReconciler) setCondition(ctx context.Context, tu *framev1alpha1.TalosUpgrade, status metav1.ConditionStatus, reason, msg string) error {
	p := client.MergeFrom(tu.DeepCopy())
	setCondition(&tu.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: tu.Generation,
	})
	return r.Status().Patch(ctx, tu, p)
}

// SetupWithManager sets up the controller with the Manager.
func (r *TalosUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.TalosUpgrade{}).
		Named("talosupgrade").
		Complete(r)
}
