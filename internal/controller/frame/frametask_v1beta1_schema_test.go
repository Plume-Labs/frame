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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// This spec is about the v1beta1 *schema*: the required fields and the verb
// enum are enforced by the apiserver from the CRD, so nothing but a real
// apiserver can show them working. It runs against the CRDs as kustomize
// renders them (renderedCRDPath), the same artefact the cluster installs.
//
// FrameTask has no controller and no v1alpha1 — Task 4's recorder is the only
// writer — so there is nothing here but the schema.
//
// The three cases are expressed as a table, matching the plan's original
// table-driven form, rather than as Go subtests: a plain *testing.T case
// sharing this package's k8sClient would run as its own top-level Test
// function, and go test orders top-level tests by source file name — this
// file sorts before suite_test.go, so the case would run before BeforeSuite
// populates k8sClient and panic on a nil client. Wrapping the same cases in a
// DescribeTable makes Ginkgo register them into the one suite BeforeSuite
// guards, which is how every sibling schema test in this package avoids the
// same trap.
var _ = Describe("FrameTask v1beta1 schema", func() {
	// A task records an action that already happened, so the required
	// fields are the ones without which the record means nothing.
	DescribeTable("validates the fields without which the record means nothing",
		func(spec framev1beta1.FrameTaskSpec, wantErr bool) {
			obj := &framev1beta1.FrameTask{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "task-", Namespace: "default"},
				Spec:       spec,
			}
			err := k8sClient.Create(ctx, obj)
			if err == nil {
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })
			}
			if wantErr {
				Expect(err).To(HaveOccurred(), "apiserver accepted a spec it should reject")
			} else {
				Expect(err).NotTo(HaveOccurred(), "apiserver rejected a valid spec: %v", err)
			}
		},
		Entry("minimal", framev1beta1.FrameTaskSpec{
			User: "alice@example.com", Verb: "patch",
			Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, false),
		Entry("no user", framev1beta1.FrameTaskSpec{
			Verb: "patch", Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, true),
		Entry("unknown verb", framev1beta1.FrameTaskSpec{
			User: "alice@example.com", Verb: "get",
			Target: framev1beta1.ObjectRef{Resource: "nodes", Name: "w2"},
		}, true),
	)
})
