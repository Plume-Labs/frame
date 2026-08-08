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

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	// TODO (user): Add any additional imports if needed
)

var _ = Describe("FrameNode Webhook", func() {
	var (
		obj       *framev1alpha1.FrameNode
		oldObj    *framev1alpha1.FrameNode
		validator FrameNodeCustomValidator
		defaulter FrameNodeCustomDefaulter
	)

	BeforeEach(func() {
		obj = &framev1alpha1.FrameNode{}
		oldObj = &framev1alpha1.FrameNode{}
		validator = FrameNodeCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = FrameNodeCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating FrameNode under Defaulting Webhook", func() {
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

	Context("When creating or updating FrameNode under Validating Webhook", func() {
		provisioned := func() framev1alpha1.FrameNodeSpec {
			return framev1alpha1.FrameNodeSpec{
				IP:   "192.168.10.10",
				Disk: "/dev/nvme0n1",
				Network: framev1alpha1.NetworkSpec{
					Address: "192.168.10.10/24",
					Gateway: "192.168.10.1",
					DNS:     []string{"1.1.1.1"},
				},
			}
		}

		It("admits a discovery-phase node carrying nothing but an IP", func() {
			// This is the wizard's first call: create the CR so the controller
			// can reach the maintenance API and report the disks back. Rejecting
			// it makes every later step unreachable.
			obj.Spec = framev1alpha1.FrameNodeSpec{IP: "192.168.10.10"}
			Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
		})

		It("rejects a node whose IP is not an IP, in either phase", func() {
			obj.Spec = framev1alpha1.FrameNodeSpec{IP: "worker-01"}
			Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
		})

		It("still checks the shape of network detail given during discovery", func() {
			obj.Spec = framev1alpha1.FrameNodeSpec{
				IP:      "192.168.10.10",
				Network: framev1alpha1.NetworkSpec{DNS: []string{"not-an-ip"}},
			}
			Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
		})

		It("admits a fully provisioned node", func() {
			obj.Spec = provisioned()
			Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
		})

		DescribeTable("requires the network once a disk is chosen",
			func(mutate func(*framev1alpha1.FrameNodeSpec)) {
				spec := provisioned()
				mutate(&spec)
				obj.Spec = spec
				Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
			},
			Entry("no address", func(s *framev1alpha1.FrameNodeSpec) { s.Network.Address = "" }),
			Entry("no gateway", func(s *framev1alpha1.FrameNodeSpec) { s.Network.Gateway = "" }),
			Entry("no DNS", func(s *framev1alpha1.FrameNodeSpec) { s.Network.DNS = nil }),
		)

		It("applies the same rule on update, so patching in a disk pulls the network in with it", func() {
			oldObj.Spec = framev1alpha1.FrameNodeSpec{IP: "192.168.10.10"}
			obj.Spec = framev1alpha1.FrameNodeSpec{IP: "192.168.10.10", Disk: "/dev/nvme0n1"}
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).Error().To(HaveOccurred())

			obj.Spec = provisioned()
			Expect(validator.ValidateUpdate(ctx, oldObj, obj)).To(BeNil())
		})
	})

})
