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

package v1

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// SetupPVCWebhookWithManager registers the content-type webhook.
func SetupPVCWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &corev1.PersistentVolumeClaim{}).
		WithValidator(&PVCCustomValidator{Client: mgr.GetClient()}).
		Complete()
}

// The two non-default settings here are the whole migration story.
//
// failurePolicy=ignore: this policy governs which volumes may exist, not
// whether volumes may exist. A manager that is down, rolling, or
// unreachable must not stop the cluster from provisioning storage — a
// policy that cannot be evaluated must not stop the cluster.
//
// The objectSelector lives in the chart and kustomize manifests rather than
// in this marker (controller-gen has no marker for it): it restricts the
// webhook to PVCs carrying frame.plume-labs.io/usage. No PVC in the cluster
// carries it today, so the day this lands nothing happens — no refusal, no
// migration, and no window in which a policy arrived before the labels.
//
// +kubebuilder:webhook:path=/validate--v1-persistentvolumeclaim,mutating=false,failurePolicy=ignore,sideEffects=None,groups="",resources=persistentvolumeclaims,verbs=create;update,versions=v1,name=vpersistentvolumeclaim.kb.io,admissionReviewVersions=v1

type PVCCustomValidator struct {
	Client client.Client
}

func (v *PVCCustomValidator) ValidateCreate(ctx context.Context, obj *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, v.validate(ctx, obj)
}

func (v *PVCCustomValidator) ValidateUpdate(ctx context.Context, _, newObj *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, v.validate(ctx, newObj)
}

func (v *PVCCustomValidator) ValidateDelete(_ context.Context, _ *corev1.PersistentVolumeClaim) (admission.Warnings, error) {
	return nil, nil
}

// validate refuses a claim whose declared usage is not among the content
// types of the FrameStorage entry that owns its class.
//
// Three ways out, all of them "accept": no usage label (the claim opted
// out, and the objectSelector means it never got here anyway), no storage
// class named, and no FrameStorage entry describing that class. The last
// one matters most: local-path is the cluster's default class, and a class
// Frame does not describe is not Frame's to police.
func (v *PVCCustomValidator) validate(ctx context.Context, pvc *corev1.PersistentVolumeClaim) error {
	usage := pvc.Labels[framev1beta1.UsageLabel]
	if usage == "" {
		return nil
	}
	if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName == "" {
		return nil
	}

	var entries framev1beta1.FrameStorageList
	if err := v.Client.List(ctx, &entries); err != nil {
		return fmt.Errorf("listing FrameStorage entries: %w", err)
	}

	for i := range entries.Items {
		e := &entries.Items[i]
		if e.Spec.StorageClassName != *pvc.Spec.StorageClassName {
			continue
		}
		for _, allowed := range e.Spec.Content {
			if allowed == usage {
				return nil
			}
		}
		return fmt.Errorf(
			"storage entry %q (class %q) accepts content types [%s]; this claim declares %q",
			e.Name, e.Spec.StorageClassName, strings.Join(e.Spec.Content, ", "), usage,
		)
	}
	return nil
}
