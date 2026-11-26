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

package updater

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// SetupWithManager runs the reconciler for every FrameUpdate. One worker, on
// purpose: the "one update at a time" check in Reconcile reads the other
// requests before writing its own Applying, which is only safe when no second
// reconcile can be between the read and the write. Leader election keeps the
// same promise across replicas.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&framev1beta1.FrameUpdate{}).
		Named("frameupdate-updater").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(reconcile.Func(func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
			after, err := r.Reconcile(ctx, req.Name)
			return reconcile.Result{RequeueAfter: after}, err
		}))
}
