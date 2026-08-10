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

package main

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/rmocq/frame/api/services/v1beta1"
)

// TestSchemeServesConversion pins the one property of this binary's scheme
// that the CRDs depend on and nothing else checks.
//
// controller-runtime registers the /convert handler from
// registerConversionWebhook, which only does so when
// conversion.IsConvertible reports the type has more than one GVK *in the
// scheme it was handed*. Every controller and webhook in this operator is
// typed on the hub, so nothing else in the binary needs the v1alpha1 packages
// — and a sweep that retypes the operator onto v1beta1 can drop them from
// init() without breaking a single compile or envtest. The result is a
// manager that serves admission but 404s every /convert call, while the
// shipped CRDs say strategy: Webhook. That is invisible to envtest, which
// builds its own scheme in each suite file with both versions registered, and
// it makes every read and write at v1alpha1 fail against a real apiserver.
//
// This is a contract between the scheme and the CRD manifests, not a snapshot
// of the scheme's contents: it says nothing about how many kinds exist, only
// that each one the manifests point at /convert for can actually be
// converted.
func TestSchemeServesConversion(t *testing.T) {
	hubs := map[string]runtime.Object{
		"FrameJob":              &framev1beta1.FrameJob{},
		"FrameNode":             &framev1beta1.FrameNode{},
		"FrameResourceQuota":    &framev1beta1.FrameResourceQuota{},
		"SchedulingPolicy":      &framev1beta1.SchedulingPolicy{},
		"TalosMachineConfig":    &framev1beta1.TalosMachineConfig{},
		"TalosUpgrade":          &framev1beta1.TalosUpgrade{},
		"FrameUser":             &framev1beta1.FrameUser{},
		"services/FrameService": &servicesv1beta1.FrameService{},
	}

	for kind, obj := range hubs {
		t.Run(kind, func(t *testing.T) {
			ok, err := conversion.IsConvertible(scheme, obj)
			if err != nil {
				t.Fatalf("IsConvertible(%s): %v", kind, err)
			}
			if !ok {
				t.Fatalf("%s is not convertible in the manager's scheme, so controller-runtime "+
					"will not register /convert — check that both api versions are added in init()", kind)
			}
		})
	}
}
