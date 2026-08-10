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
	"reflect"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	framev1alpha1 "github.com/rmocq/frame/api/frame/v1alpha1"
	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// These specs are the half of F14 layer 1 that api/frame/v1alpha1's fuzz
// cannot reach, and the difference is the whole point of running them here.
//
// The fuzz exercises the conversion functions against structs the test built.
// That proves the functions carry every field, and 27 mutations confirm it
// does. What it cannot prove is anything about the *apiserver*: that the shape
// ConvertTo emits is a shape the shipping v1beta1 schema accepts and stores
// without pruning a field off it, that the shape ConvertFrom emits is one the
// v1alpha1 schema accepts likewise, and that neither version's defaulting
// invents a value on the way through. Those are properties of the CRDs, not of
// the Go code, and only a real apiserver holding the real CRDs can show them.
//
// Task 11 pointed this suite at bin/crd-render — the CRDs as kustomize builds
// them — precisely so the schema under test is the schema that ships.
//
// The conversion webhook *is* serving in this suite, and not because Task 19
// arrived early: envtest turns it on by itself the moment the Go types
// implement Hub and Convertible, whatever the manifests say. See
// serveConversionWebhook in suite_test.go for the mechanism and for what Task
// 19 still owns.
//
// So the specs come in two shapes. The ones under "conversion through the
// apiserver" drive ConvertTo and ConvertFrom in-process and use the apiserver
// only to produce the input and to validate and store the output; they isolate
// the schema question. The ones under "the apiserver dispatching conversion"
// call neither function and let the apiserver do it over HTTP; they are the
// end-to-end path. Both are worth having: the first tells you *which* hop lost
// a field, the second tells you the hop happens at all.

type conversionSpoke interface {
	client.Object
	conversion.Convertible
}

type conversionHub interface {
	client.Object
	conversion.Hub
}

// specOf and statusOf reach the two fields every Frame kind has by the same
// names. Reflection rather than eight interface methods: the alternative is
// eight accessors on production types that exist only for this file.
func specOf(o any) any {
	return reflect.ValueOf(o).Elem().FieldByName("Spec").Interface()
}

func statusOf(o any) any {
	return reflect.ValueOf(o).Elem().FieldByName("Status").Interface()
}

func setStatus(o any, status any) {
	reflect.ValueOf(o).Elem().FieldByName("Status").Set(reflect.ValueOf(status))
}

// writeThroughAPIServer creates obj, writes the status it arrived carrying
// through the status subresource, and reads the result back. What comes out is
// what storage holds: defaults applied, anything the schema prunes already
// gone.
func writeThroughAPIServer(obj client.Object) {
	GinkgoHelper()
	intendedStatus := statusOf(obj)

	Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	DeferCleanup(func() { _ = k8sClient.Delete(ctx, obj) })

	setStatus(obj, intendedStatus)
	Expect(k8sClient.Status().Update(ctx, obj)).To(Succeed())
	Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
}

// reKey turns a converted object into one the apiserver will accept as a new
// object: conversion copies ObjectMeta wholesale, so the copy arrives wearing
// the original's identity.
func reKey(obj client.Object, name string) {
	obj.SetName(name)
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetGeneration(0)
	obj.SetCreationTimestamp(metav1.Time{})
	obj.SetManagedFields(nil)
}

// conversionSurvivesStorage is the assertion this file exists for:
//
//	fixture --[apiserver]--> stored --ConvertTo--> hub --[apiserver]--> stored
//	  --ConvertFrom--> back,  and back == fixture.
//
// Every arrow marked [apiserver] is a real create plus a real status write plus
// a real read, against the rendered CRDs. A field the v1beta1 schema would
// prune is lost at the second one and the comparison catches it; a field
// v1alpha1's defaulting would invent appears at the first and the comparison
// catches that too.
//
// The comparison is against `fixture` — a deep copy taken before anything
// touches the object — and that is the whole difference between a fidelity
// test and a tautology. Comparing against `alpha` after the first write, which
// is what this helper used to do, compares f(x) with f(f(x)): writeThrough-
// APIServer ends in a Get, so `alpha` has already been through
// ConvertTo -> store -> ConvertFrom by then, and anything conversion or the
// v1beta1 schema drops is dropped identically on both sides and cancels. That
// version stayed green with `dst.Spec.Rack = src.Spec.Rack` deleted from
// FrameNode.ConvertFrom, and green again with spec.rack deleted from the
// v1beta1 schema — verbatim the case the paragraph above claims it catches.
func conversionSurvivesStorage[S conversionSpoke, H conversionHub](
	alpha S, hub H, back S, hubName string, ignore ...cmp.Option,
) {
	GinkgoHelper()

	fixture, ok := alpha.DeepCopyObject().(S)
	Expect(ok).To(BeTrue(), "DeepCopyObject must return the same concrete type")

	writeThroughAPIServer(alpha)

	Expect(alpha.ConvertTo(hub)).To(Succeed())
	reKey(hub, hubName)
	// If ConvertTo emitted something the v1beta1 schema refuses, this is where
	// it surfaces — as a rejected write rather than as a silent difference.
	writeThroughAPIServer(hub)

	Expect(back.ConvertFrom(hub)).To(Succeed())

	Expect(cmp.Diff(specOf(fixture), specOf(back), ignore...)).To(BeEmpty(),
		"spec changed on a v1alpha1 -> v1beta1 -> v1alpha1 trip through the apiserver")
	Expect(cmp.Diff(statusOf(fixture), statusOf(back), ignore...)).To(BeEmpty(),
		"status changed on a v1alpha1 -> v1beta1 -> v1alpha1 trip through the apiserver")
}

var _ = Describe("v1alpha1 <-> v1beta1 conversion through the apiserver", func() {
	Context("FrameJob", func() {
		It("survives a trip through v1beta1 storage unchanged, phase included", func() {
			// A modern object: Ready carries the phase as its reason, which is
			// the invariant F3 established and the one the projection reads. On
			// this shape the round trip is exact — status.phase is reconstructed
			// to the very value that was stored, so nothing is excluded from the
			// comparison.
			alpha := &framev1alpha1.FrameJob{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-job-modern", Namespace: "default"},
				Spec: framev1alpha1.FrameJobSpec{
					Pipeline:     "neura-inference-dag",
					ServiceClass: "HIGH",
					Priority:     "high",
					Namespace:    "default",
					GPUCount:     0,
					Suspended:    false,
					Parameters:   map[string]string{"dataset": "embeddings", "shards": "4"},
				},
				Status: framev1alpha1.FrameJobStatus{
					ObservedGeneration: 1,
					Phase:              "Completed",
					ArgoWorkflowName:   "neura-embed-refresh",
					StartTime:          &metav1.Time{Time: metav1.Now().Rfc3339Copy().Time},
					CompletionTime:     &metav1.Time{Time: metav1.Now().Rfc3339Copy().Time},
					Message:            "Workflow succeeded",
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "Completed",
						Message:            "Workflow succeeded",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			conversionSurvivesStorage(alpha, &framev1beta1.FrameJob{}, &framev1alpha1.FrameJob{}, "conv-job-modern-hub")
		})

		It("degrades a pre-F3 object's phase to Submitted, and says so out loud", func() {
			// This is the exact shape of both FrameJobs stored on the cluster:
			// generation 1, a write-once Submitted/WorkflowCreated condition, no
			// Ready, and status.phase: Completed. Nothing in the object records
			// the outcome — completionTime is set on Failed too — so the
			// projection cannot recover Completed and does not pretend to. It
			// reports what the conditions assert.
			//
			// This spec exists so that degradation is a decision on the record
			// rather than a surprise found in production, and so Task 21's
			// forced re-reconcile has something to change.
			alpha := &framev1alpha1.FrameJob{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-job-legacy", Namespace: "default"},
				Spec: framev1alpha1.FrameJobSpec{
					Pipeline:     "neura-training-dag",
					ServiceClass: "LOW",
					Priority:     "medium",
					Namespace:    "default",
				},
				Status: framev1alpha1.FrameJobStatus{
					ObservedGeneration: 1,
					Phase:              "Completed",
					ArgoWorkflowName:   "nightly-consolidation",
					StartTime:          &metav1.Time{Time: metav1.Now().Rfc3339Copy().Time},
					CompletionTime:     &metav1.Time{Time: metav1.Now().Rfc3339Copy().Time},
					Conditions: []metav1.Condition{{
						Type:               "Submitted",
						Status:             metav1.ConditionTrue,
						Reason:             "WorkflowCreated",
						Message:            "ArgoWorkflow default/nightly-consolidation created",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			back := &framev1alpha1.FrameJob{}
			conversionSurvivesStorage(alpha, &framev1beta1.FrameJob{}, back, "conv-job-legacy-hub",
				cmpopts.IgnoreFields(framev1alpha1.FrameJobStatus{}, "Phase"))

			Expect(back.Status.Phase).To(Equal("Submitted"),
				"a pre-F3 object has no Ready condition; Submitted is what its conditions assert, "+
					"and Pending would claim the workflow was never created")
		})
	})

	Context("FrameNode", func() {
		It("survives a trip through v1beta1 storage unchanged, CIDR address included", func() {
			// Modelled on the three stored FrameNodes: Ready/Discovered,
			// status.phase Discovered, no spec.disk, and a network.address
			// holding a CIDR. The CIDR is the point — neither version carries an
			// isIP rule on that field, so a conversion or a schema that
			// tightened it would strand all three, and this write is what would
			// fail.
			alpha := &framev1alpha1.FrameNode{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-node", Namespace: "default"},
				Spec: framev1alpha1.FrameNodeSpec{
					IP:            "192.168.2.201",
					Role:          "controlplane",
					ServiceClass:  "HIGH",
					Hostname:      "neura-k3s-cp",
					Rack:          "rack-01",
					Zone:          "local",
					RDMAInterface: "ib0",
					Network: framev1alpha1.NetworkSpec{
						Address: "192.168.2.201/24",
						Gateway: "192.168.2.1",
						DNS:     []string{"192.168.2.1", "1.1.1.1"},
					},
				},
				Status: framev1alpha1.FrameNodeStatus{
					ObservedGeneration:     1,
					Phase:                  "Discovered",
					DiscoveredHostname:     "neura-k3s-cp",
					DiscoveredTalosVersion: "v1.8.0",
					KubeletVersion:         "v1.31.0",
					NodeName:               "neura-k3s-cp",
					Capacity: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("32Gi"),
					},
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("7500m"),
						corev1.ResourceMemory: resource.MustParse("30Gi"),
					},
					DiscoveredDisks: []framev1alpha1.DiskInfo{{Name: "/dev/nvme0n1", Size: "1Ti", Type: "nvme"}},
					DiscoveredNICs:  []framev1alpha1.NICInfo{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", Speed: "10Gbps"}},
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionFalse,
						Reason:             "Discovered",
						Message:            "waiting for user to set spec.disk",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			conversionSurvivesStorage(alpha, &framev1beta1.FrameNode{}, &framev1alpha1.FrameNode{}, "conv-node-hub")
		})
	})

	Context("FrameResourceQuota", func() {
		It("survives a trip through v1beta1 storage unchanged, including a measured zero", func() {
			// status.namespaces drops omitempty deliberately (Task 7): absent
			// means "never reconciled", 0 means "reconciled, matched nothing".
			// A conversion that lost the field would read as the former, and the
			// three stored quotas are exactly the objects that distinction is
			// for. spec.maxGPUs: 0 is on the wire on all three.
			alpha := &framev1alpha1.FrameResourceQuota{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-quota", Namespace: "default"},
				Spec: framev1alpha1.FrameResourceQuotaSpec{
					ServiceClass: "HIGH",
					MaxGPUs:      0,
					MaxCPU:       new(resource.MustParse("16")),
					MaxMemory:    new(resource.MustParse("64Gi")),
					MaxJobs:      10,
				},
				Status: framev1alpha1.FrameResourceQuotaStatus{
					ObservedGeneration: 3,
					Namespaces:         0,
					Used: corev1.ResourceList{
						"limits.cpu":                          resource.MustParse("2500m"),
						"count/framejobs.frame.plume-labs.io": resource.MustParse("2"),
					},
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "Reconciled",
						Message:            "projected into 0 namespaces",
						ObservedGeneration: 2,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			conversionSurvivesStorage(alpha, &framev1beta1.FrameResourceQuota{},
				&framev1alpha1.FrameResourceQuota{}, "conv-quota-hub")
		})
	})

	Context("SchedulingPolicy", func() {
		It("survives a trip through v1beta1 storage unchanged", func() {
			// The one stored policy, field for field.
			alpha := &framev1alpha1.SchedulingPolicy{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-policy", Namespace: "default"},
				Spec: framev1alpha1.SchedulingPolicySpec{
					Scheduler:     "volcano",
					QueueName:     "neura-high",
					PriorityClass: "neura-high",
					Preemption:    true,
					PriorityValue: new(int32(100)),
					QueueWeight:   new(int32(100)),
				},
				Status: framev1alpha1.SchedulingPolicyStatus{
					ObservedGeneration: 1,
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "Applied",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			conversionSurvivesStorage(alpha, &framev1beta1.SchedulingPolicy{},
				&framev1alpha1.SchedulingPolicy{}, "conv-policy-hub")
		})
	})

	Context("TalosMachineConfig", func() {
		It("survives a trip through v1beta1 storage, dropping only the secret ref's namespace", func() {
			alpha := &framev1alpha1.TalosMachineConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-tmc", Namespace: "default"},
				Spec: framev1alpha1.TalosMachineConfigSpec{
					NodeName:       "neura-k3s-w2",
					TalosEndpoint:  "192.168.2.203:50000",
					ConfigPatch:    "machine:\n  install:\n    disk: /dev/nvme0n1\n",
					TalosSecretRef: framev1alpha1.TalosSecretReference{Name: "talos-certs", Namespace: "kube-system"},
				},
				Status: framev1alpha1.TalosMachineConfigStatus{
					ObservedGeneration: 1,
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "Applied",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			back := &framev1alpha1.TalosMachineConfig{}
			conversionSurvivesStorage(alpha, &framev1beta1.TalosMachineConfig{}, back, "conv-tmc-hub",
				cmpopts.IgnoreFields(framev1alpha1.TalosSecretReference{}, "Namespace"))

			// The one announced normalisation, asserted rather than merely
			// ignored: empty already meant "this CR's own namespace" (F6).
			Expect(back.Spec.TalosSecretRef.Namespace).To(BeEmpty())
			Expect(back.Spec.TalosSecretRef.Name).To(Equal("talos-certs"))
			Expect(back.Annotations).To(BeEmpty(), "nothing is stashed in an annotation")
		})
	})

	Context("TalosUpgrade", func() {
		It("survives a trip through v1beta1 storage, dropping only the secret ref's namespace", func() {
			alpha := &framev1alpha1.TalosUpgrade{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-tu", Namespace: "default"},
				Spec: framev1alpha1.TalosUpgradeSpec{
					NodeName:       "neura-k3s-w2",
					TalosEndpoint:  "192.168.2.203:50000",
					Image:          "ghcr.io/siderolabs/installer:v1.8.0",
					TalosSecretRef: framev1alpha1.TalosSecretReference{Name: "talos-certs", Namespace: "kube-system"},
				},
				Status: framev1alpha1.TalosUpgradeStatus{
					ObservedGeneration: 1,
					Conditions: []metav1.Condition{{
						Type:               "Ready",
						Status:             metav1.ConditionFalse,
						Reason:             "Upgrading",
						ObservedGeneration: 1,
						LastTransitionTime: metav1.Now().Rfc3339Copy(),
					}},
				},
			}
			back := &framev1alpha1.TalosUpgrade{}
			conversionSurvivesStorage(alpha, &framev1beta1.TalosUpgrade{}, back, "conv-tu-hub",
				cmpopts.IgnoreFields(framev1alpha1.TalosSecretReference{}, "Namespace"))

			Expect(back.Spec.TalosSecretRef.Namespace).To(BeEmpty())
			Expect(back.Spec.Image).To(Equal("ghcr.io/siderolabs/installer:v1.8.0"))
		})
	})

	Context("FrameUser", func() {
		It("survives a trip through v1beta1 storage, moved password hash included", func() {
			// FrameUser is the bijection: spec.passwordHash on v1alpha1 is
			// status.passwordHash on v1beta1 (F11), so the field changes section
			// rather than disappearing, and it has to survive both ways.
			//
			// That it survives *storage* here is a consequence of conversion
			// serving. With strategy None the apiserver persists the
			// intersection of the request version's schema and the storage
			// version's, and the hash is in exactly one of them — so writing it
			// at v1beta1 while v1alpha1 stores would silently drop it. With the
			// webhook answering, the v1beta1 write is converted down to
			// spec.passwordHash before it is stored and converted back up on
			// read, and nothing is lost. This spec therefore also demonstrates
			// what Task 19 buys: the same object, without conversion, loses the
			// field with a 200 response.
			//
			// frameuser_v1beta1_schema_test.go owns the invariant itself — it
			// reads the storage version and the strategy off the running
			// apiserver and asserts whichever half must survive.
			// A literal, not alpha.Spec.PasswordHash: the object the assertion
			// below compares against must not be one the trip could have
			// emptied. This is the freeze's only bijection, and the version
			// that self-compared stayed green with
			// `dst.Spec.PasswordHash = src.Status.PasswordHash` commented out
			// of ConvertFrom — both sides were "".
			const hash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"

			alpha := &framev1alpha1.FrameUser{
				ObjectMeta: metav1.ObjectMeta{Name: "conv-user", Namespace: "default"},
				Spec: framev1alpha1.FrameUserSpec{
					Email:        "operator@frame.test",
					Role:         framev1alpha1.RoleOperator,
					PasswordAuth: framev1alpha1.PasswordEnabled,
					PasswordHash: hash,
				},
				Status: framev1alpha1.FrameUserStatus{
					ObservedGeneration: 0,
					Credentials: []framev1alpha1.WebAuthnCredential{{
						ID:        "Y3JlZC1pZA",
						PublicKey: "cHVibGljLWtleQ",
						SignCount: 42,
						AddedAt:   metav1.Now().Rfc3339Copy(),
						Label:     "YubiKey 5C",
					}},
				},
			}
			back := &framev1alpha1.FrameUser{}
			conversionSurvivesStorage(alpha, &framev1beta1.FrameUser{}, back, "conv-user-hub")

			Expect(back.Spec.PasswordHash).To(Equal(hash),
				"the moved hash must come back in spec on v1alpha1, or password login breaks with a 200")
			Expect(back.Status.Credentials).To(HaveLen(1))
			Expect(back.Status.Credentials[0].SignCount).To(BeNumerically("==", 42))
			Expect(back.Status.Credentials[0].Label).To(Equal("YubiKey 5C"))
		})
	})
})

// Everything above drives the conversion functions in-process and uses the
// apiserver to produce and to store. These two do neither: the object is
// written at one version and read back at the other, and every conversion in
// between is the apiserver calling /convert over HTTP. Nothing in the test
// touches ConvertTo or ConvertFrom.
//
// That is the difference F14 is about. A Go round trip proves the functions are
// mutually inverse. Only this proves they are reachable, dispatched on the
// right hub, wired to the right kinds, and that what comes back out of etcd at
// the *other* version is the object that went in.
var _ = Describe("the apiserver dispatching conversion", func() {
	It("serves a FrameJob written at v1alpha1 back at v1beta1, and the reverse", func() {
		alpha := &framev1alpha1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "dispatch-job", Namespace: "default"},
			Spec: framev1alpha1.FrameJobSpec{
				Pipeline:     "neura-inference-dag",
				ServiceClass: "HIGH",
				Priority:     "high",
				Namespace:    "default",
				Parameters:   map[string]string{"dataset": "embeddings"},
			},
			Status: framev1alpha1.FrameJobStatus{
				ObservedGeneration: 1,
				Phase:              "Completed",
				ArgoWorkflowName:   "neura-embed-refresh",
				Message:            "Workflow succeeded",
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionTrue,
					Reason:             "Completed",
					Message:            "Workflow succeeded",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now().Rfc3339Copy(),
				}},
			},
		}
		writeThroughAPIServer(alpha)

		// Read the same object at the hub version. The apiserver has to call
		// ConvertTo to answer this at all.
		beta := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(alpha), beta)).To(Succeed())
		Expect(beta.Spec.Pipeline).To(Equal("neura-inference-dag"))
		Expect(beta.Spec.ServiceClass).To(Equal(framev1beta1.ServiceClassHigh))
		Expect(beta.Spec.Parameters).To(HaveKeyWithValue("dataset", framev1beta1.ParameterValue("embeddings")))
		Expect(beta.Status.ArgoWorkflowName).To(Equal("neura-embed-refresh"))
		Expect(beta.Status.Conditions).To(HaveLen(1))
		Expect(beta.Status.Conditions[0].Reason).To(Equal("Completed"))

		// Coming back down needs care to mean anything. v1alpha1 is still the
		// storage version, so reading a v1alpha1-stored object at v1alpha1
		// converts nothing and hands back the phase the client itself wrote —
		// an assertion on that would pass against a projection that did not
		// exist. So drive the change through v1beta1 instead: move Ready's
		// reason to Failed on the hub, which forces the apiserver to call
		// ConvertFrom to store the result.
		beta.Status.Conditions[0].Status = metav1.ConditionFalse
		beta.Status.Conditions[0].Reason = "Failed"
		beta.Status.Conditions[0].Message = "Workflow failed"
		Expect(k8sClient.Status().Update(ctx, beta)).To(Succeed())

		roundTripped := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(alpha), roundTripped)).To(Succeed())
		Expect(roundTripped.Status.Phase).To(Equal("Failed"),
			"no client ever wrote Failed into status.phase; only the projection could have put it there")
	})

	It("reconstructs the removed FrameJob.spec.namespace inside the apiserver", func() {
		// v1beta1 has no spec.namespace at all, so a job created through the
		// hub gives the field no source but ConvertFrom. Reading it back at
		// v1alpha1 with the value filled in is the reconstruction working, and
		// it is the field the SDK still sends (Task 22).
		beta := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "dispatch-job-ns", Namespace: "default"},
			Spec:       framev1beta1.FrameJobSpec{Pipeline: "neura-training-dag"},
		}
		Expect(k8sClient.Create(ctx, beta)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, beta) })

		alpha := &framev1alpha1.FrameJob{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(beta), alpha)).To(Succeed())
		Expect(alpha.Spec.Namespace).To(Equal("default"),
			"the removed field comes back as the object's own namespace, never as something the client asked for")
		// And with no conditions written yet, the projection's own floor.
		Expect(alpha.Status.Phase).To(Equal("Pending"))
	})

	It("computes a FrameNode's phase inside the apiserver, from the Ready reason alone", func() {
		// Written at v1beta1, which has no phase field at all, and read back at
		// v1alpha1, which does. The value can only have come from the
		// projection running inside the conversion webhook.
		beta := &framev1beta1.FrameNode{
			ObjectMeta: metav1.ObjectMeta{Name: "dispatch-node", Namespace: "default"},
			Spec: framev1beta1.FrameNodeSpec{
				IP:           "192.168.2.202",
				Role:         "worker",
				ServiceClass: framev1beta1.ServiceClassMedium,
				Network: framev1beta1.NetworkSpec{
					Address: "192.168.2.202/24",
					Gateway: "192.168.2.1",
					DNS:     []string{"192.168.2.1"},
				},
			},
			Status: framev1beta1.FrameNodeStatus{
				ObservedGeneration: 1,
				Conditions: []metav1.Condition{{
					Type:               "Ready",
					Status:             metav1.ConditionFalse,
					Reason:             "Discovered",
					Message:            "waiting for user to set spec.disk",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now().Rfc3339Copy(),
				}},
			},
		}
		writeThroughAPIServer(beta)

		alpha := &framev1alpha1.FrameNode{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(beta), alpha)).To(Succeed())
		Expect(alpha.Status.Phase).To(Equal("Discovered"),
			"the legacy phase is projected by the conversion webhook, not stored")
		Expect(alpha.Spec.Network.Address).To(Equal("192.168.2.202/24"),
			"the CIDR address survives conversion in both directions")
		Expect(alpha.Spec.ServiceClass).To(Equal("MEDIUM"))
	})
})
