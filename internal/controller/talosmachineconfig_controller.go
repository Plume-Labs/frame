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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/v1alpha1"
)

const talosMachineConfigFinalizer = "frame.plume-labs.io/talosmachineconfig"

// TalosMachineConfigReconciler reconciles a TalosMachineConfig object
type TalosMachineConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *TalosMachineConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var tmc framev1alpha1.TalosMachineConfig
	if err := r.Get(ctx, req.NamespacedName, &tmc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !tmc.DeletionTimestamp.IsZero() {
		controllerutil.RemoveFinalizer(&tmc, talosMachineConfigFinalizer)
		return ctrl.Result{}, r.Update(ctx, &tmc)
	}

	if !controllerutil.ContainsFinalizer(&tmc, talosMachineConfigFinalizer) {
		controllerutil.AddFinalizer(&tmc, talosMachineConfigFinalizer)
		return ctrl.Result{}, r.Update(ctx, &tmc)
	}

	patch, err := r.resolvePatch(ctx, &tmc)
	if err != nil {
		return ctrl.Result{}, r.setErrorCondition(ctx, &tmc, fmt.Sprintf("Could not resolve patch: %v", err))
	}

	// TODO: call Talos gRPC API once github.com/siderolabs/talos/pkg/machinery is added.
	// Example:
	//   creds, err := loadTalosCredentials(ctx, r.Client, tmc.Namespace, tmc.Spec.TalosSecretRef)
	//   c, err := talos.New(ctx, talos.WithEndpoints(tmc.Spec.TalosEndpoint), talos.WithCredentials(creds))
	//   _, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{Data: []byte(patch)})
	log.Info("Resolved config patch (Talos apply pending SDK integration)",
		"node", tmc.Spec.NodeName,
		"endpoint", tmc.Spec.TalosEndpoint,
		"patchLen", len(patch),
	)

	statusPatch := client.MergeFrom(tmc.DeepCopy())
	setCondition(&tmc.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "PatchResolved",
		Message:            "Config patch resolved; apply requires Talos SDK integration",
		ObservedGeneration: tmc.Generation,
	})
	return ctrl.Result{}, r.Status().Patch(ctx, &tmc, statusPatch)
}

// resolvePatch returns the config patch string from inline or ConfigMapRef.
func (r *TalosMachineConfigReconciler) resolvePatch(ctx context.Context, tmc *framev1alpha1.TalosMachineConfig) (string, error) {
	if tmc.Spec.ConfigPatch != "" {
		return tmc.Spec.ConfigPatch, nil
	}
	if tmc.Spec.ConfigPatchRef == nil {
		return "", fmt.Errorf("neither configPatch nor configPatchRef specified")
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{
		Name:      tmc.Spec.ConfigPatchRef.Name,
		Namespace: tmc.Namespace,
	}, &cm); err != nil {
		return "", err
	}
	key := tmc.Spec.ConfigPatchRef.Key
	if key == "" {
		key = "patch.yaml"
	}
	val, ok := cm.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in ConfigMap %s", key, cm.Name)
	}
	return val, nil
}

func (r *TalosMachineConfigReconciler) setErrorCondition(ctx context.Context, tmc *framev1alpha1.TalosMachineConfig, msg string) error {
	patch := client.MergeFrom(tmc.DeepCopy())
	setCondition(&tmc.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "Error",
		Message:            msg,
		ObservedGeneration: tmc.Generation,
	})
	return r.Status().Patch(ctx, tmc, patch)
}

// SetupWithManager sets up the controller with the Manager.
func (r *TalosMachineConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.TalosMachineConfig{}).
		Named("talosmachineconfig").
		Complete(r)
}
