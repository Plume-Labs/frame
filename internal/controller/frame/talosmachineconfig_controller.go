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

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
)

const talosMachineConfigFinalizer = "frame.plume-labs.io/talosmachineconfig"

// TalosMachineConfigReconciler reconciles a TalosMachineConfig object
type TalosMachineConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=frame.plume-labs.io,resources=talosmachineconfigs/finalizers,verbs=update
// get only, for the same reason as talosupgrade_controller.go: the only Secret
// access is buildTalosClient's single by-name Get of spec.talosSecretRef.
// Served uncached; see cmd/main.go.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
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
		r.Recorder.Event(&tmc, corev1.EventTypeWarning, "PatchResolveFailed", err.Error())
		return ctrl.Result{}, r.setCondition(ctx, &tmc, metav1.ConditionFalse, "PatchResolveFailed",
			fmt.Sprintf("could not resolve config patch: %v", err))
	}

	c, err := buildTalosClient(ctx, r.Client, tmc.Namespace, tmc.Spec.TalosEndpoint, tmc.Spec.TalosSecretRef)
	if err != nil {
		r.Recorder.Event(&tmc, corev1.EventTypeWarning, "ClientBuildFailed", err.Error())
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setCondition(ctx, &tmc,
			metav1.ConditionFalse, "ClientBuildFailed", err.Error())
	}
	defer c.Close() //nolint:errcheck

	if _, err = c.ApplyConfiguration(ctx, &machineapi.ApplyConfigurationRequest{
		Data: []byte(patch),
		Mode: machineapi.ApplyConfigurationRequest_AUTO,
	}); err != nil {
		r.Recorder.Event(&tmc, corev1.EventTypeWarning, "ApplyFailed", fmt.Sprintf("ApplyConfiguration: %v", err))
		talosConfigFailed.Inc()
		return ctrl.Result{RequeueAfter: 30 * time.Second}, r.setCondition(ctx, &tmc,
			metav1.ConditionFalse, "ApplyFailed", fmt.Sprintf("ApplyConfiguration: %v", err))
	}

	msg := fmt.Sprintf("Config patch applied to %s via %s", tmc.Spec.NodeName, tmc.Spec.TalosEndpoint)
	r.Recorder.Event(&tmc, corev1.EventTypeNormal, "Applied", msg)
	talosConfigApplied.Inc()
	log.Info("Applied Talos config patch", "node", tmc.Spec.NodeName,
		"endpoint", tmc.Spec.TalosEndpoint, "patchLen", len(patch))

	return ctrl.Result{}, r.setCondition(ctx, &tmc, metav1.ConditionTrue, "Applied", msg)
}

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

func (r *TalosMachineConfigReconciler) setCondition(ctx context.Context, tmc *framev1alpha1.TalosMachineConfig, status metav1.ConditionStatus, reason, msg string) error {
	p := client.MergeFrom(tmc.DeepCopy())
	meta.SetStatusCondition(&tmc.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: tmc.Generation,
	})
	return r.Status().Patch(ctx, tmc, p)
}

// SetupWithManager sets up the controller with the Manager.
func (r *TalosMachineConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1alpha1.TalosMachineConfig{}).
		Named("talosmachineconfig").
		Complete(r)
}
