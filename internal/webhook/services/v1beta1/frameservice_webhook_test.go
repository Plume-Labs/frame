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

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
	servicesv1beta1 "github.com/Plume-Labs/frame/api/services/v1beta1"
	"github.com/Plume-Labs/frame/internal/services/provider"
	"github.com/Plume-Labs/frame/internal/services/provider/cache"
	"github.com/Plume-Labs/frame/internal/services/provider/inference"
)

// Through the real apiserver: the CRD's own schema, and the delete guard behind
// it, are what stands between a console edit and a bad catalog.
var _ = Describe("InferenceModel", func() {
	valid := func(name string) *servicesv1beta1.InferenceModel {
		return &servicesv1beta1.InferenceModel{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       servicesv1beta1.InferenceModelSpec{WeightsMiB: 1000, Layers: 1, KVHeads: 1, HeadDim: 1},
		}
	}

	It("refuses what llama.cpp would not take", func() {
		bad := valid("bad-kv")
		bad.Spec.KVCacheType = "q2_k"
		Expect(k8sClient.Create(ctx, bad)).To(MatchError(ContainSubstring("kvCacheType")))

		lonely := valid("lonely-message")
		lonely.Spec.ReasoningBudgetMessage = "stop"
		Expect(k8sClient.Create(ctx, lonely)).To(MatchError(ContainSubstring("needs reasoningBudget")))
	})

	It("deletes a model nothing serves", func() {
		m := valid("idle")
		Expect(k8sClient.Create(ctx, m)).To(Succeed())
		Expect(k8sClient.Delete(ctx, m)).To(Succeed())
	})

	It("refuses an instance of a model that is not in the catalog", func() {
		Expect(k8sClient.Create(ctx, &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "uses-nothing", Namespace: "default"},
			Spec: servicesv1beta1.FrameServiceSpec{
				Type: "inference", ServiceClass: "HIGH",
				Parameters: map[string]framev1beta1.ParameterValue{"model": "not-there"},
			},
		})).To(MatchError(ContainSubstring("known models are")))
	})

	It("keeps a model an instance serves", func() {
		svc := &servicesv1beta1.FrameService{
			ObjectMeta: metav1.ObjectMeta{Name: "uses-served", Namespace: "default"},
			Spec: servicesv1beta1.FrameServiceSpec{
				Type:         "inference",
				ServiceClass: "HIGH",
				Parameters: map[string]framev1beta1.ParameterValue{
					"model": "llama-3.1-8b-instruct",
				},
			},
		}
		Expect(k8sClient.Create(ctx, svc)).To(Succeed())

		held := valid("llama-3.1-8b-instruct")
		Eventually(func() error { return k8sClient.Delete(ctx, held) }).
			Should(MatchError(ContainSubstring("default/uses-served")))
		Expect(k8sClient.Delete(ctx, svc)).To(Succeed())
	})
})

var _ = Describe("FrameService Webhook", func() {
	var (
		obj       *servicesv1beta1.FrameService
		oldObj    *servicesv1beta1.FrameService
		validator FrameServiceCustomValidator
	)

	BeforeEach(func() {
		obj = &servicesv1beta1.FrameService{}
		oldObj = &servicesv1beta1.FrameService{}
		validator = FrameServiceCustomValidator{
			// nil client, nil apiReader: the validator only calls Size and
			// ParameterSchema, never Reconcile or Bind, so nothing here
			// dereferences either.
			Registry: provider.NewRegistry(inference.New(7680, nil, nil), cache.New(nil, nil)),
		}
	})

	valid := func() servicesv1beta1.FrameServiceSpec {
		return servicesv1beta1.FrameServiceSpec{
			Type: "inference",
			Parameters: map[string]framev1beta1.ParameterValue{
				"model":         "llama-3.1-8b-instruct",
				"contextLength": "8192",
			},
			ServiceClass: "HIGH",
		}
	}

	It("refuses a binding Secret that collides with the cache provider's auth Secret", func() {
		obj.Name = "jobs"
		obj.Spec = servicesv1beta1.FrameServiceSpec{
			Type:       "cache",
			Parameters: map[string]framev1beta1.ParameterValue{"profile": "cache", "maxMemory": "64Mi"},
			Binding:    servicesv1beta1.BindingSpec{SecretName: "jobs-valkey-auth"},
		}
		_, err := validator.ValidateCreate(ctx, obj)
		Expect(err).To(MatchError(ContainSubstring("collides")))
	})

	It("does not size again what nobody is resizing", func() {
		// 70B cannot fit: a model edited under a running instance looks like this.
		oldObj.Spec = valid()
		oldObj.Spec.Parameters["model"] = "llama-3.1-70b-instruct"
		obj.Spec = oldObj.Spec
		obj.Labels = map[string]string{"a": "b"}
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).NotTo(HaveOccurred())

		moved := obj.DeepCopy()
		moved.Spec.Parameters = map[string]framev1beta1.ParameterValue{"model": "llama-3.1-70b-instruct", "contextLength": "1024"}
		_, err = validator.ValidateUpdate(ctx, oldObj, moved)
		Expect(err).To(MatchError(ContainSubstring("weights alone")))
	})

	It("lets the finalizer go whatever the catalog says", func() {
		oldObj.Spec = valid()
		obj.Spec = valid()
		obj.Spec.Parameters["model"] = "llama-3.1-70b-instruct"
		now := metav1.Now()
		obj.DeletionTimestamp = &now
		_, err := validator.ValidateUpdate(ctx, oldObj, obj)
		Expect(err).NotTo(HaveOccurred())
	})

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
