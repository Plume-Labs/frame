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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func kubeFixture(t *testing.T) (*KubeCluster, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	two := int32(2)
	ui := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cluster-control", Name: "cluster-control-ui", Generation: 1},
		Spec: appsv1.DeploymentSpec{Replicas: &two, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "ui", Image: "ui@old"}, {Name: "uiproxy", Image: "proxy@old"},
		}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2},
	}
	agent := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "frame-node-tuning-agent", Generation: 1},
		Spec:       appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "agent@old"}}}}},
		Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberReady: 3},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(ui, agent).WithStatusSubresource(ui, agent).Build()
	return &KubeCluster{Client: c, Workloads: DefaultWorkloads(), ReadyTimeout: 200 * time.Millisecond, PollEvery: 5 * time.Millisecond}, c
}

func TestSetImageChangesOnlyTheNamedContainer(t *testing.T) {
	k, c := kubeFixture(t)
	if err := k.SetImage(context.Background(), "ui", "ui@new"); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "cluster-control", Name: "cluster-control-ui"}, &d); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, ct := range d.Spec.Template.Spec.Containers {
		got[ct.Name] = ct.Image
	}
	if got["ui"] != "ui@new" || got["uiproxy"] != "proxy@old" {
		t.Fatalf("containers: %v", got)
	}
}

func TestSetImageNeverCreatesOrGuesses(t *testing.T) {
	k, c := kubeFixture(t)
	ctx := context.Background()
	if err := k.SetImage(ctx, "shell", "x@y"); err == nil {
		t.Error("an unknown component was accepted")
	}
	if err := k.SetImage(ctx, "ui", ""); err == nil {
		t.Error("an empty image was accepted")
	}
	// A workload the install does not have is an error, never a create.
	if err := k.SetImage(ctx, "authd", "a@b"); err == nil {
		t.Error("a missing workload was accepted")
	}
	var d appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: "cluster-control", Name: "cluster-control-auth"}, &d); err == nil {
		t.Error("the updater created a Deployment")
	}
	// A container that does not exist in the workload is an error too.
	k.Workloads["ui"] = Workload{Kind: "Deployment", Namespace: "cluster-control", Name: "cluster-control-ui", Container: "nope"}
	if err := k.SetImage(ctx, "ui", "ui@new"); err == nil || !strings.Contains(err.Error(), "no container") {
		t.Errorf("got %v", err)
	}
}

func TestWaitReadyWaitsForTheRolloutNotJustTheReplicas(t *testing.T) {
	k, c := kubeFixture(t)
	ctx := context.Background()
	key := types.NamespacedName{Namespace: "cluster-control", Name: "cluster-control-ui"}

	if err := k.WaitReady(ctx, "ui"); err != nil {
		t.Fatalf("a settled deployment should be ready: %v", err)
	}

	// Generation moved, the controller has not caught up: not ready, even though
	// the old replicas are all up (the trap a naive readiness check falls into).
	var d appsv1.Deployment
	if err := c.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	d.Generation = 2
	if err := c.Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := k.WaitReady(ctx, "ui"); err == nil || !strings.Contains(err.Error(), "not seen the new spec") {
		t.Fatalf("got %v", err)
	}

	// Caught up, but one replica is still old.
	if err := c.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 2}
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := k.WaitReady(ctx, "ui"); err == nil || !strings.Contains(err.Error(), "old replicas") {
		t.Fatalf("got %v", err)
	}

	// Finished.
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: d.Generation, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 2}
	if err := c.Status().Update(ctx, &d); err != nil {
		t.Fatal(err)
	}
	if err := k.WaitReady(ctx, "ui"); err != nil {
		t.Fatalf("got %v", err)
	}
}

func TestDaemonSetReadiness(t *testing.T) {
	k, c := kubeFixture(t)
	ctx := context.Background()
	if err := k.WaitReady(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	var ds appsv1.DaemonSet
	key := types.NamespacedName{Namespace: "kube-system", Name: "frame-node-tuning-agent"}
	if err := c.Get(ctx, key, &ds); err != nil {
		t.Fatal(err)
	}
	ds.Status.NumberReady = 2
	if err := c.Status().Update(ctx, &ds); err != nil {
		t.Fatal(err)
	}
	if err := k.WaitReady(ctx, "agent"); err == nil || !strings.Contains(err.Error(), "2 of 3 nodes ready") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitReadyStopsWithTheContext(t *testing.T) {
	k, c := kubeFixture(t)
	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: "cluster-control", Name: "cluster-control-ui"}
	_ = c.Get(context.Background(), key, &d)
	d.Status.ReadyReplicas = 0
	_ = c.Status().Update(context.Background(), &d)
	k.ReadyTimeout = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := k.WaitReady(ctx, "ui"); err == nil {
		t.Fatal("expected a timeout")
	}
}
