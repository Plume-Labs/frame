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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	servicesv1alpha1 "github.com/rmocq/frame/api/services/v1alpha1"
	"github.com/rmocq/frame/internal/services/provider"
	"github.com/rmocq/frame/internal/services/provider/inference"
)

var _ = Describe("FrameService Webhook", func() {
	var (
		obj       *servicesv1alpha1.FrameService
		oldObj    *servicesv1alpha1.FrameService
		validator FrameServiceCustomValidator
	)

	BeforeEach(func() {
		obj = &servicesv1alpha1.FrameService{}
		oldObj = &servicesv1alpha1.FrameService{}
		validator = FrameServiceCustomValidator{
			Registry: provider.NewRegistry(inference.New(7680)),
		}
	})

	valid := func() servicesv1alpha1.FrameServiceSpec {
		return servicesv1alpha1.FrameServiceSpec{
			Type: "inference",
			Parameters: map[string]string{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
			ServiceClass: "HIGH",
		}
	}

	It("admits a service that fits", func() {
		obj.Spec = valid()
		Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
	})

	It("refuses a type no provider answers to, and names the ones that exist", func() {
		obj.Spec = valid()
		obj.Spec.Type = "infrence"
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("inference"))
	})

	It("refuses parameters the provider's schema rejects", func() {
		obj.Spec = valid()
		delete(obj.Spec.Parameters, "model")
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("model"))
	})

	It("refuses an instance that cannot fit, at admission rather than at runtime", func() {
		obj.Spec = valid()
		obj.Spec.Parameters["contextLength"] = "32768"
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("7680Mi"))
	})

	It("applies the same rules on update", func() {
		oldObj.Spec = valid()
		obj.Spec = valid()
		obj.Spec.Parameters["contextLength"] = "32768"
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).To(HaveOccurred())
	})

	It("refuses to change the type of an existing service", func() {
		// The provisioned workload belongs to the old provider; switching type
		// would orphan it with nothing left that knows how to clean it up.
		oldObj.Spec = valid()
		obj.Spec = valid()
		obj.Spec.Type = "database"
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("immutable"))
	})

	It("allows deletion", func() {
		obj.Spec = valid()
		Expect(validator.ValidateDelete(ctx, obj)).Error().NotTo(HaveOccurred())
	})
})
