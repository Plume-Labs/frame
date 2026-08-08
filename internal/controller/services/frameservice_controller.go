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

package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
)

const frameServiceFinalizer = "services.plume-labs.io/finalizer"

// degradedRequeue is how long a degraded instance waits before the controller
// looks again. Long enough not to hammer a missing operator, short enough that
// installing one is noticed without a restart.
const degradedRequeue = 2 * time.Minute

type FrameServiceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Registry *provider.Registry
}

// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=services.plume-labs.io,resources=frameservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// The inference provider inspects pods to explain a stuck rollout (a pod
// wedged in CreateContainerConfigError) with a named reason instead of a
// silent, permanent RolloutInProgress degrade.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// The inference provider checks for a PersistentVolumeClaim holding cached
// model weights before it will provision anything. That check is a single
// named existence check, made through the manager's uncached APIReader
// rather than its cached client specifically so this stays a single `get`:
// routing it through the cache would make controller-runtime open a
// cluster-scoped List+Watch informer over every PersistentVolumeClaim in the
// cluster to serve one named lookup.
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get

func (r *FrameServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var svc servicesv1alpha1.FrameService
	if err := r.Get(ctx, req.NamespacedName, &svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !svc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &svc)
	}

	// The finalizer lands before the provider runs. A crash in between would
	// otherwise leave a provisioned instance with nothing tracking it.
	if !controllerutil.ContainsFinalizer(&svc, frameServiceFinalizer) {
		controllerutil.AddFinalizer(&svc, frameServiceFinalizer)
		return ctrl.Result{}, r.Update(ctx, &svc)
	}

	p, err := r.Registry.Get(svc.Spec.Type)
	if err != nil {
		// A type the webhook would have refused can still reach here: the object
		// may predate the provider being removed. Report it rather than retrying
		// something no amount of retrying fixes.
		r.Recorder.Event(&svc, corev1.EventTypeWarning, "UnknownType", err.Error())
		frameServiceUnknownType.Inc()
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"UnknownType", err.Error(), nil, nil)
	}

	prov, ok := p.(provider.Provisioner)
	if !ok {
		msg := fmt.Sprintf("provider %q can validate but cannot provision", svc.Spec.Type)
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"NotProvisionable", msg, nil, nil)
	}

	sizing, err := prov.Size(svc.Spec.Parameters)
	if err != nil {
		return ctrl.Result{}, r.setStatus(ctx, &svc, "Degraded", metav1.ConditionFalse,
			"SizeRefused", err.Error(), nil, nil)
	}

	result, err := prov.Reconcile(ctx, &svc)
	if err != nil {
		r.Recorder.Event(&svc, corev1.EventTypeWarning, "ProvisionFailed", err.Error())
		frameServiceProvisionFailed.Inc()
		return ctrl.Result{}, fmt.Errorf("provisioning %s: %w", svc.Spec.Type, err)
	}

	if !result.Ready {
		// Degrading is not an error. Returning one would back off and bury the
		// reason in controller-runtime's retry logging instead of status.
		return ctrl.Result{RequeueAfter: degradedRequeue}, r.setStatus(ctx, &svc,
			"Degraded", metav1.ConditionFalse, result.Reason, result.Message,
			&sizing, result.Provisioned)
	}

	binding, err := prov.Bind(ctx, &svc)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("binding %s: %w", svc.Name, err)
	}

	secretRefName, err := r.reconcileBinding(ctx, &svc, binding)
	if err != nil {
		// A bindingDegradation is not a transient error: retrying without
		// operator intervention (an unrelated Secret in the way, a missing
		// namespace) would never succeed on its own, so it goes to status
		// exactly like an unready provider does, instead of into
		// controller-runtime's retry logging.
		var degradation *bindingDegradation
		if errors.As(err, &degradation) {
			return ctrl.Result{RequeueAfter: degradedRequeue}, r.setStatus(ctx, &svc,
				"Degraded", metav1.ConditionFalse, degradation.Reason, degradation.Message,
				&sizing, result.Provisioned)
		}
		return ctrl.Result{}, fmt.Errorf("reconciling binding for %s: %w", svc.Name, err)
	}

	// The endpoint carries no credential and was already publishable on its
	// own; secretRef now names the Secret reconcileBinding just wrote. Fields
	// are set individually, not by replacing svc.Status.Binding wholesale:
	// reconcileBinding already recorded status.binding.projected on this same
	// svc, and a fresh struct literal here would silently drop it.
	svc.Status.Binding.Endpoint = binding.Endpoint
	svc.Status.Binding.SecretRef = &corev1.LocalObjectReference{Name: secretRefName}
	frameServiceReady.Inc()
	log.Info("Reconciled FrameService", "type", svc.Spec.Type, "endpoint", binding.Endpoint)
	return ctrl.Result{}, r.setStatus(ctx, &svc, "Ready", metav1.ConditionTrue,
		result.Reason, result.Message, &sizing, result.Provisioned)
}

// setStatus re-fetches the FrameService and writes the outcome of this
// reconcile pass onto it. Re-fetching immediately before the write — rather
// than reusing the copy Reconcile fetched at the top — means the update
// carries the resourceVersion actually current on the server, so a status
// write never conflicts with, or clobbers, anything that landed on the object
// in between. Binding is carried over from svc because Reconcile may have set
// it earlier in this same pass, and that would otherwise be lost by starting
// from a fresh copy.
func (r *FrameServiceReconciler) setStatus(
	ctx context.Context,
	svc *servicesv1alpha1.FrameService,
	phase string,
	status metav1.ConditionStatus,
	reason, message string,
	sizing *provider.Sizing,
	provisioned []servicesv1alpha1.ProvisionedRef,
) error {
	var fresh servicesv1alpha1.FrameService
	if err := r.Get(ctx, client.ObjectKeyFromObject(svc), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}

	fresh.Status.Phase = phase
	fresh.Status.Binding = svc.Status.Binding
	fresh.Status.Provisioned = provisioned
	fresh.Status.ObservedGeneration = fresh.Generation
	if sizing != nil {
		fresh.Status.Sizing = servicesv1alpha1.Sizing{
			GPU:       sizing.GPU,
			GPUMemory: sizing.GPUMemory,
			CPU:       sizing.CPU,
			Memory:    sizing.Memory,
		}
	}
	meta.SetStatusCondition(&fresh.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: fresh.Generation,
	})

	return r.Status().Update(ctx, &fresh)
}

// reconcileDelete tears down a FrameService. The split deletionPolicy
// controls is never "does anything get deleted" — the objects that expose
// the instance are always removed, either by Kubernetes garbage collection
// acting on the owner reference every Provisioner.Reconcile is required to
// set on them, or, for the one exposing object that cannot carry that owner
// reference, explicitly right here. What deletionPolicy actually decides is
// the instance's data:
//
//   - Retain (the default) does nothing in this function beyond releasing the
//     finalizer. That is correct, not incomplete: the exposing objects are
//     already gone or going via owner-reference GC, and the data objects — a
//     PVC, a delegating operator's CR — deliberately carry no owner reference,
//     so GC leaves them alone and Retain needs no extra code to keep them.
//   - Delete additionally removes every object in status.Provisioned, which is
//     the only handle this controller has on data objects: Provisioner has no
//     teardown method, so the provider's last reconcile report is the sole
//     record of what exists to delete.
//
// A projected credentials Secret is exposure, not data, so it is removed
// unconditionally above, under neither branch of deletionPolicy: owner
// references do not cross namespaces, so deleteAllProjections — driven by
// status.binding.projected, the controller's own record of what it wrote,
// never by asking the cluster who owns what — is the only thing that will
// ever remove it. Leaving that to deletionPolicy: Retain would silently leave
// credentials behind in every namespace the FrameService had projected into.
//
// Either way the finalizer clears last, so a crash mid-teardown leaves the
// object undeleted — and retried — rather than orphaning a data object with
// nothing left tracking it.
func (r *FrameServiceReconciler) reconcileDelete(ctx context.Context, svc *servicesv1alpha1.FrameService) (ctrl.Result, error) {
	if err := r.deleteAllProjections(ctx, svc); err != nil {
		return ctrl.Result{}, fmt.Errorf("deleting projected Secrets for %s/%s: %w", svc.Namespace, svc.Name, err)
	}

	if svc.Spec.DeletionPolicy == "Delete" {
		for _, ref := range svc.Status.Provisioned {
			obj := &unstructured.Unstructured{}
			obj.SetAPIVersion(ref.APIVersion)
			obj.SetKind(ref.Kind)
			obj.SetName(ref.Name)
			obj.SetNamespace(ref.Namespace)
			if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("deleting %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
			}
		}
	}

	controllerutil.RemoveFinalizer(svc, frameServiceFinalizer)
	return ctrl.Result{}, r.Update(ctx, svc)
}

// SetupWithManager sets up the controller with the Manager.
func (r *FrameServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&servicesv1alpha1.FrameService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Named("services-frameservice").
		Complete(r)
}
