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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// This file covers stage 2: the controller dispatching on spec.type for a
// container-substrate FrameJob. framejob_controller_test.go's existing
// pipeline-path Describe blocks are untouched by this change — see in
// particular "creates the backing ArgoWorkflow with the job's spec on second
// reconcile" and "deletes the backing Workflow when the FrameJob is deleted
// through the reconciler", which exercise exactly the code path stage 2 was
// not supposed to alter, and which would fail if it had (buildWorkflow was
// not modified, and Reconcile's pipeline body still runs unchanged whenever
// spec.container is nil).

var _ = Describe("FrameJob Controller — container substrate", func() {
	const ns = "default"
	ctx := context.Background()

	r := func() *FrameJobReconciler {
		return &FrameJobReconciler{Client: k8sClient, Scheme: k8sClient.Scheme(), Recorder: record.NewFakeRecorder(100)}
	}

	deleteJob := func(name string) {
		key := types.NamespacedName{Name: name, Namespace: ns}
		fresh := &framev1beta1.FrameJob{}
		if err := k8sClient.Get(ctx, key, fresh); err == nil {
			fresh.Finalizers = nil
			_ = k8sClient.Update(ctx, fresh)
			_ = k8sClient.Delete(ctx, fresh)
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, &framev1beta1.FrameJob{}))
		}, "5s").Should(BeTrue())
	}

	It("creates a batch/v1 Job on the default scheduler for type=realtime", func() {
		name := "realtime-job"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeRealtime,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())

		_, err := r().Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}) // finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}) // creates the Job
		Expect(err).NotTo(HaveOccurred())

		k8sJob := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, k8sJob)).To(Succeed(),
			"a batch/v1 Job must actually exist, not merely have avoided an error")
		Expect(k8sJob.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/example/worker:latest"))
		Expect(k8sJob.Labels["frame.plume-labs.io/workload-type"]).To(Equal("realtime"))

		// Volcano-only fields must not appear on a realtime job. The
		// apiserver's own PodSpec defaulting fills an empty schedulerName
		// with "default-scheduler" (SetDefaults_PodSpec) by the time this
		// Get reads it back, so the meaningful assertion is "not volcano",
		// not "empty".
		Expect(k8sJob.Spec.Template.Spec.SchedulerName).NotTo(Equal("volcano"), "realtime uses the default scheduler")

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(fetched.Status.ContainerJobName).To(Equal(name))
		Expect(fetched.Status.ContainerJobKind).To(Equal("Job.batch"))
		Expect(fetched.Status.ArgoWorkflowName).To(BeEmpty(), "ArgoWorkflowName must not be repurposed for a container job")
		Expect(readyReason(fetched.Status.Conditions)).To(Equal(jobPhaseSubmitted))
	})

	It("creates a batch/v1 Job for type=background", func() {
		name := "background-job"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeBackground,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		k8sJob := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, k8sJob)).To(Succeed())
		Expect(k8sJob.Labels["frame.plume-labs.io/workload-type"]).To(Equal("background"))
	})

	It("wires envFrom into the container and propagates the FrameJob's labels onto the batch/v1 Job and its pod template (GAP 1 + GAP 2)", func() {
		name := "realtime-envfrom-labels"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				// A caller-set label, and one deliberately colliding with a
				// key Frame itself always sets — the collision is the point:
				// Frame's own value must win, never the caller's.
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "neura",
					"frame.plume-labs.io/job":      "caller-supplied-and-must-be-overwritten",
				},
			},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{
					Image: "ghcr.io/example/worker:latest",
					EnvFrom: []corev1.EnvFromSource{
						{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "objectstore-creds"}}},
					},
				},
				Type: framev1beta1.WorkloadTypeRealtime,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req) // finalizer
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req) // creates the Job
		Expect(err).NotTo(HaveOccurred())

		k8sJob := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, k8sJob)).To(Succeed())

		container := k8sJob.Spec.Template.Spec.Containers[0]
		Expect(container.EnvFrom).To(HaveLen(1))
		Expect(container.EnvFrom[0].SecretRef.Name).To(Equal("objectstore-creds"),
			"GAP 1: envFrom must reach the container the Job runs, or the credentials it names are silently dropped")

		Expect(k8sJob.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "neura"),
			"GAP 2: the FrameJob's own labels must land on the created Job so a label-selecting watcher can find it")
		Expect(k8sJob.Labels).To(HaveKeyWithValue("frame.plume-labs.io/job", name),
			"Frame's own label must win the collision, not the caller-supplied value")

		Expect(k8sJob.Spec.Template.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "neura"),
			"the pod template must carry the same labels as the Job object")
		Expect(k8sJob.Spec.Template.Labels).To(HaveKeyWithValue("frame.plume-labs.io/job", name))
	})

	It("creates a Volcano Job with schedulerName=volcano for type=batch, and degrades with a condition when no SchedulingPolicy matches", func() {
		name := "batch-job-no-policy"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeBatch,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		vj := &unstructured.Unstructured{}
		vj.SetGroupVersionKind(volcanoJobGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vj)).To(Succeed(),
			"a Volcano Job must actually exist for type=batch")
		schedulerName, _, _ := unstructured.NestedString(vj.Object, "spec", "schedulerName")
		Expect(schedulerName).To(Equal("volcano"))
		_, queueSet, _ := unstructured.NestedString(vj.Object, "spec", "queue")
		Expect(queueSet).To(BeFalse(), "no SchedulingPolicy named frame-batch exists, so spec.queue must be absent (default queue)")

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(fetched.Status.ContainerJobKind).To(Equal("Job.batch.volcano.sh"))
		cond := meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeSchedulingPolicy)
		Expect(cond).NotTo(BeNil(), "a missing SchedulingPolicy must be reported in a condition, never silently")
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("PolicyNotFound"))
	})

	It("resolves the queue from the frame-batch SchedulingPolicy when one exists", func() {
		policy := &framev1beta1.SchedulingPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "frame-batch", Namespace: ns},
			Spec: framev1beta1.SchedulingPolicySpec{
				Scheduler: "volcano",
				QueueName: "hpc-queue",
			},
		}
		Expect(k8sClient.Create(ctx, policy)).To(Succeed())
		defer func() {
			fresh := &framev1beta1.SchedulingPolicy{}
			key := types.NamespacedName{Name: "frame-batch", Namespace: ns}
			if err := k8sClient.Get(ctx, key, fresh); err == nil {
				fresh.Finalizers = nil
				_ = k8sClient.Update(ctx, fresh)
				_ = k8sClient.Delete(ctx, fresh)
			}
		}()

		name := "batch-job-with-policy"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeBatch,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		vj := &unstructured.Unstructured{}
		vj.SetGroupVersionKind(volcanoJobGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vj)).To(Succeed())
		queue, _, _ := unstructured.NestedString(vj.Object, "spec", "queue")
		Expect(queue).To(Equal("hpc-queue"))

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		cond := meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeSchedulingPolicy)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("Applied"))
	})

	It("wires envFrom into the container and propagates the FrameJob's labels onto the Volcano Job and its pod template (GAP 1 + GAP 2)", func() {
		name := "batch-envfrom-labels"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "neura",
					"frame.plume-labs.io/job":      "caller-supplied-and-must-be-overwritten",
				},
			},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{
					Image: "ghcr.io/example/worker:latest",
					EnvFrom: []corev1.EnvFromSource{
						{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "job-config"}}},
					},
				},
				Type: framev1beta1.WorkloadTypeBatch,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		vj := &unstructured.Unstructured{}
		vj.SetGroupVersionKind(volcanoJobGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vj)).To(Succeed())

		tasks, found, err := unstructured.NestedSlice(vj.Object, "spec", "tasks")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(tasks).To(HaveLen(1))
		task, ok := tasks[0].(map[string]any)
		Expect(ok).To(BeTrue())

		containers, found, err := unstructured.NestedSlice(task, "template", "spec", "containers")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(containers).To(HaveLen(1))
		container, ok := containers[0].(map[string]any)
		Expect(ok).To(BeTrue())

		envFrom, found, err := unstructured.NestedSlice(container, "envFrom")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "GAP 1: envFrom must reach the Volcano Job's container template")
		Expect(envFrom).To(HaveLen(1))

		Expect(vj.GetLabels()).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "neura"),
			"GAP 2: the FrameJob's own labels must land on the created Volcano Job")
		Expect(vj.GetLabels()).To(HaveKeyWithValue("frame.plume-labs.io/job", name),
			"Frame's own label must win the collision, not the caller-supplied value")

		templateLabels, found, err := unstructured.NestedStringMap(task, "template", "metadata", "labels")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(templateLabels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "neura"))
		Expect(templateLabels).To(HaveKeyWithValue("frame.plume-labs.io/job", name))
	})

	It("holds creation and reports Suspended when spec.suspended is true before any object exists", func() {
		name := "held-job"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeRealtime,
				Suspended: true,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &batchv1.Job{}))).To(BeTrue(),
			"suspended=true before creation must hold the Job, not create it")

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(readyReason(fetched.Status.Conditions)).To(Equal(jobPhaseSuspended))
	})

	It("syncs spec.suspend live onto an already-created batch/v1 Job (realtime/background)", func() {
		name := "live-suspend-job"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeRealtime,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req) // creates the Job
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		fetched.Spec.Suspended = true
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		k8sJob := &batchv1.Job{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, k8sJob)).To(Succeed())
		Expect(k8sJob.Spec.Suspend).NotTo(BeNil())
		Expect(*k8sJob.Spec.Suspend).To(BeTrue(), "a running batch/v1 Job supports live spec.suspend, unlike Volcano")

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(readyReason(fetched.Status.Conditions)).To(Equal(jobPhaseSuspended))
	})

	It("reports SuspendApplied=False rather than lying Suspended when a running Volcano Job is asked to pause", func() {
		name := "batch-cant-suspend"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeBatch,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req) // creates the Volcano Job
		Expect(err).NotTo(HaveOccurred())

		// Drive the backing Volcano Job to Running so the phase read below is
		// not just the empty-phase default.
		vj := &unstructured.Unstructured{}
		vj.SetGroupVersionKind(volcanoJobGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vj)).To(Succeed())
		Expect(unstructured.SetNestedField(vj.Object, "Running", "status", "state", "phase")).To(Succeed())
		Expect(k8sClient.Update(ctx, vj)).To(Succeed())

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		fetched.Spec.Suspended = true
		Expect(k8sClient.Update(ctx, fetched)).To(Succeed())

		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(readyReason(fetched.Status.Conditions)).To(Equal(jobPhaseRunning),
			"Ready must keep reporting the real phase, not silently claim Suspended")
		cond := meta.FindStatusCondition(fetched.Status.Conditions, conditionTypeSuspendApplied)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal("VolcanoLiveSuspendUnsupported"))
	})

	It("deletes the backing batch/v1 Job when the FrameJob is deleted", func() {
		name := "delete-realtime"
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeRealtime,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &batchv1.Job{})).To(Succeed(),
			"the Job must exist before we can prove it gets deleted")

		Expect(k8sClient.Delete(ctx, job)).To(Succeed())
		_, err = r().Reconcile(ctx, req) // runs reconcileContainerDelete
		Expect(err).NotTo(HaveOccurred())

		// batch/v1 Job carries the batch.kubernetes.io/job-tracking
		// finalizer, added by the API server's own Job registry strategy at
		// create time (unconditionally, not behind a feature gate on modern
		// Kubernetes). The job-controller normally removes it once it has
		// GC'd the Job's pods, but envtest runs only the apiserver — no
		// controller-manager — so that finalizer is never lifted and the
		// object stays present with a DeletionTimestamp forever. What this
		// test can actually prove, and does, is that reconcileContainerDelete
		// issued the real delete request against the right object, not that
		// Kubernetes finished tearing it down — that half needs a
		// controller this suite does not run.
		jobKey := types.NamespacedName{Name: name, Namespace: ns}
		k8sJobAfter := &batchv1.Job{}
		err = k8sClient.Get(ctx, jobKey, k8sJobAfter)
		if err == nil {
			Expect(k8sJobAfter.DeletionTimestamp).NotTo(BeNil(),
				"reconcileContainerDelete must delete the backing Job, not just drop the finalizer")
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &framev1beta1.FrameJob{}))
		}, "5s").Should(BeTrue())
	})

	It("deletes the backing Volcano Job when the FrameJob is deleted", func() {
		name := "delete-batch"
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeBatch,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
		_, err = r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		vj := &unstructured.Unstructured{}
		vj.SetGroupVersionKind(volcanoJobGVK)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vj)).To(Succeed(),
			"the Volcano Job must exist before we can prove it gets deleted")

		Expect(k8sClient.Delete(ctx, job)).To(Succeed())
		_, err = r().Reconcile(ctx, req) // runs reconcileContainerDelete
		Expect(err).NotTo(HaveOccurred())

		vjAfter := &unstructured.Unstructured{}
		vjAfter.SetGroupVersionKind(volcanoJobGVK)
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, vjAfter))).To(BeTrue())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &framev1beta1.FrameJob{}))
		}, "5s").Should(BeTrue())
	})

	It("adds the finalizer on a container-substrate FrameJob exactly like the pipeline path", func() {
		name := "finalizer-container"
		defer deleteJob(name)
		job := &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "ghcr.io/example/worker:latest"},
				Type:      framev1beta1.WorkloadTypeRealtime,
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: ns}}
		_, err := r().Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())

		fetched := &framev1beta1.FrameJob{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, fetched)).To(Succeed())
		Expect(controllerutil.ContainsFinalizer(fetched, frameJobFinalizer)).To(BeTrue())
	})
})

var _ = Describe("batchJobPhase", func() {
	makeJob := func(conditions []any, active int64) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if conditions != nil {
			_ = unstructured.SetNestedSlice(obj.Object, conditions, "status", "conditions")
		}
		_ = unstructured.SetNestedField(obj.Object, active, "status", "active")
		return obj
	}
	trueCond := func(t string) map[string]any {
		return map[string]any{"type": t, "status": "True"}
	}

	It("maps a True Complete condition to Completed", func() {
		Expect(batchJobPhase(makeJob([]any{trueCond("Complete")}, 0), false)).To(Equal(jobPhaseCompleted))
	})

	It("maps a True Failed condition to Failed", func() {
		Expect(batchJobPhase(makeJob([]any{trueCond("Failed")}, 0), false)).To(Equal(jobPhaseFailed))
	})

	It("ignores a False condition", func() {
		cond := map[string]any{"type": "Complete", "status": "False"}
		Expect(batchJobPhase(makeJob([]any{cond}, 1), false)).To(Equal(jobPhaseRunning))
	})

	It("maps active>0 with no terminal condition to Running", func() {
		Expect(batchJobPhase(makeJob(nil, 3), false)).To(Equal(jobPhaseRunning))
	})

	It("maps no active pods and no terminal condition to Submitted", func() {
		Expect(batchJobPhase(makeJob(nil, 0), false)).To(Equal(jobPhaseSubmitted))
	})

	It("reports Suspended over Running when suspended=true and not terminal", func() {
		Expect(batchJobPhase(makeJob(nil, 3), true)).To(Equal(jobPhaseSuspended))
	})

	It("does not let suspended override a terminal condition", func() {
		Expect(batchJobPhase(makeJob([]any{trueCond("Complete")}, 0), true)).To(Equal(jobPhaseCompleted))
	})
})

var _ = Describe("volcanoJobPhase", func() {
	makePhase := func(phase string) *unstructured.Unstructured {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if phase != "" {
			_ = unstructured.SetNestedField(obj.Object, phase, "status", "state", "phase")
		}
		return obj
	}

	It("maps Completed to Completed", func() {
		Expect(volcanoJobPhase(makePhase("Completed"))).To(Equal(jobPhaseCompleted))
	})

	DescribeTable("maps failure-adjacent phases to Failed",
		func(phase string) { Expect(volcanoJobPhase(makePhase(phase))).To(Equal(jobPhaseFailed)) },
		Entry("Failed", "Failed"),
		Entry("Terminating", "Terminating"),
		Entry("Terminated", "Terminated"),
		Entry("Aborting", "Aborting"),
		Entry("Aborted", "Aborted"),
	)

	DescribeTable("maps in-progress phases to Running",
		func(phase string) { Expect(volcanoJobPhase(makePhase(phase))).To(Equal(jobPhaseRunning)) },
		Entry("Running", "Running"),
		Entry("Restarting", "Restarting"),
		Entry("Completing", "Completing"),
	)

	It("maps Pending and empty/unknown to Submitted", func() {
		Expect(volcanoJobPhase(makePhase("Pending"))).To(Equal(jobPhaseSubmitted))
		Expect(volcanoJobPhase(makePhase(""))).To(Equal(jobPhaseSubmitted))
		Expect(volcanoJobPhase(makePhase("SomethingNew"))).To(Equal(jobPhaseSubmitted))
	})
})

var _ = Describe("containerObjectGVK and schedulingPolicyNameForType", func() {
	It("maps realtime and background to batch/v1 Job", func() {
		Expect(containerObjectGVK(framev1beta1.WorkloadTypeRealtime)).To(Equal(batchJobGVK))
		Expect(containerObjectGVK(framev1beta1.WorkloadTypeBackground)).To(Equal(batchJobGVK))
	})

	It("maps batch to the Volcano Job GVK", func() {
		Expect(containerObjectGVK(framev1beta1.WorkloadTypeBatch)).To(Equal(volcanoJobGVK))
	})

	It("names the SchedulingPolicy by convention frame-<type>", func() {
		Expect(schedulingPolicyNameForType(framev1beta1.WorkloadTypeBatch)).To(Equal("frame-batch"))
		Expect(schedulingPolicyNameForType(framev1beta1.WorkloadTypeRealtime)).To(Equal("frame-realtime"))
	})
})

var _ = Describe("buildBatchJob", func() {
	makeJob := func(priority string, suspended bool) *framev1beta1.FrameJob {
		return &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Name: "cjob", Namespace: "ctrl-ns"},
			Spec: framev1beta1.FrameJobSpec{
				Container: &framev1beta1.ContainerSpec{Image: "img:tag"},
				Type:      framev1beta1.WorkloadTypeRealtime,
				Priority:  priority,
				Suspended: suspended,
			},
		}
	}

	It("does not force a PriorityClass beyond what spec.priority maps to (orthogonality, design 3.1)", func() {
		job := makeJob("high", false)
		podSpec := map[string]any{}
		wf := buildBatchJob(job, podSpec, map[string]any{})
		pc, _, _ := unstructured.NestedString(wf.Object, "spec", "template", "spec", "priorityClassName")
		Expect(pc).To(Equal("frame-high"), "priority alone decides the PriorityClass, same as every other substrate")
	})

	It("sets backoffLimit=0", func() {
		wf := buildBatchJob(makeJob("", false), map[string]any{}, map[string]any{})
		backoff, _, _ := unstructured.NestedInt64(wf.Object, "spec", "backoffLimit")
		Expect(backoff).To(Equal(int64(0)))
	})

	It("sets spec.suspend from Suspended", func() {
		wf := buildBatchJob(makeJob("", true), map[string]any{}, map[string]any{})
		suspend, _, _ := unstructured.NestedBool(wf.Object, "spec", "suspend")
		Expect(suspend).To(BeTrue())
	})
})
