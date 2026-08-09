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
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	// TODO (user): Add any additional imports if needed
)

var _ = Describe("FrameJob Webhook", func() {
	var (
		obj       *framev1alpha1.FrameJob
		oldObj    *framev1alpha1.FrameJob
		validator FrameJobCustomValidator
		defaulter FrameJobCustomDefaulter
	)

	BeforeEach(func() {
		obj = &framev1alpha1.FrameJob{}
		oldObj = &framev1alpha1.FrameJob{}
		validator = FrameJobCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = FrameJobCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating FrameJob under Defaulting Webhook", func() {
		// TODO (user): Add logic for defaulting webhooks
		// Example:
		// It("Should apply defaults when a required field is empty", func() {
		//     By("simulating a scenario where defaults should be applied")
		//     obj.SomeFieldWithDefault = ""
		//     By("calling the Default method to apply defaults")
		//     defaulter.Default(ctx, obj)
		//     By("checking that the default values are set")
		//     Expect(obj.SomeFieldWithDefault).To(Equal("default_value"))
		// })
	})

	Context("When creating or updating FrameJob under Validating Webhook", func() {
		// TODO (user): Add logic for validating webhooks
		// Example:
		// It("Should deny creation if a required field is missing", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = ""
		//     Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
		// })
		//
		// It("Should admit creation if all required fields are present", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = "valid_value"
		//     Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
		// })
		//
		// It("Should validate updates correctly", func() {
		//     By("simulating a valid update scenario")
		//     oldObj.SomeRequiredField = "updated_value"
		//     obj.SomeRequiredField = "updated_value"
		//     Expect(validator.ValidateUpdate(ctx, oldObj, obj)).To(BeNil())
		// })
	})

})

func TestValidateFrameJobAdmitsGPUsAtLowServiceClass(t *testing.T) {
	// F8: the constraint was deleted deliberately. This test is the guard
	// against it being "restored" by someone reading the old doc comment in a
	// git blame.
	job := &framev1alpha1.FrameJob{
		Spec: framev1alpha1.FrameJobSpec{
			Pipeline:     "neura-training-dag",
			ServiceClass: "LOW",
			GPUCount:     2,
		},
	}
	warnings, err := validateFrameJob(job)
	if err != nil {
		t.Fatalf("expected a GPU job at LOW to be admitted, got error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for a known pipeline, got %v", warnings)
	}
}

func TestValidateFrameJobWarnsOnUnknownPipeline(t *testing.T) {
	job := &framev1alpha1.FrameJob{
		Spec: framev1alpha1.FrameJobSpec{Pipeline: "training", ServiceClass: "LOW", GPUCount: 2},
	}
	warnings, err := validateFrameJob(job)
	if err != nil {
		t.Fatalf("an unknown pipeline must warn, not reject: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}
	if !strings.Contains(warnings[0], `pipeline "training" not in known list`) {
		t.Fatalf("warning did not name the pipeline: %q", warnings[0])
	}
}
