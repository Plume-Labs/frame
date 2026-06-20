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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const talosUpgradeFinalizer = "frame.plume-labs.io/talosupgrade"

// TalosUpgradeReconciler reconciles a TalosUpgrade object
type TalosUpgradeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

	// Validate secret exists before attempting upgrade
	if err := r.validateSecret(ctx, &tu); err != nil {
		return ctrl.Result{}, r.setUpgradeCondition(ctx, &tu, metav1.ConditionFalse, "SecretMissing",
			fmt.Sprintf("Talos secret not found: %v", err))
	}

	// TODO: call Talos gRPC API once github.com/siderolabs/talos/pkg/machinery is added.
	// Example:
	//   creds, err := loadTalosCredentials(ctx, r.Client, tu.Namespace, tu.Spec.TalosSecretRef)
	//   c, err := talos.New(ctx, talos.WithEndpoints(tu.Spec.TalosEndpoint), talos.WithCredentials(creds))
	//   _, err = c.Upgrade(ctx, &machineapi.UpgradeRequest{
	//       Image:        tu.Spec.Image,
	//       PreserveData: tu.Spec.PreserveData,
	//   })
	log.Info("Upgrade queued (Talos apply pending SDK integration)",
		"node", tu.Spec.NodeName,
		"endpoint", tu.Spec.TalosEndpoint,
		"image", tu.Spec.Image,
	)

	return ctrl.Result{}, r.setUpgradeCondition(ctx, &tu, metav1.ConditionTrue, "UpgradeQueued",
		"Secret validated; upgrade requires Talos SDK integration")
}

func (r *TalosUpgradeReconciler) validateSecret(ctx context.Context, tu *framev1alpha1.TalosUpgrade) error {
	ref := tu.Spec.TalosSecretRef
	ns := ref.Namespace
	if ns == "" {
		ns = tu.Namespace
	}
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(corev1GVK("v1", "Secret"))
	return r.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ns}, obj)
}

func (r *TalosUpgradeReconciler) setUpgradeCondition(ctx context.Context, tu *framev1alpha1.TalosUpgrade, status metav1.ConditionStatus, reason, msg string) error {
	patch := client.MergeFrom(tu.DeepCopy())
	setCondition(&tu.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: tu.Generation,
	})
	return r.Status().Patch(ctx, tu, patch)
}

// SetupWithManager sets up the controller with the Manager.
func (r *TalosUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.TalosUpgrade{}).
		Named("talosupgrade").
		Complete(r)
}
