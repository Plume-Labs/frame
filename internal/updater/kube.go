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
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Workload says where a component runs: the object to patch and the container
// inside it. This table is the updater's whole write surface (spec §4.8): it can
// change the image of these containers and nothing else.
type Workload struct {
	Kind      string // "Deployment" or "DaemonSet"
	Namespace string
	Name      string
	Container string
}

// DefaultWorkloads is where Hearthrack's own components run in the standard
// install. ui and uiproxy are two containers of one Deployment.
func DefaultWorkloads() map[string]Workload {
	return map[string]Workload{
		"controller": {Kind: "Deployment", Namespace: "frame-system", Name: "frame-controller-manager", Container: "manager"},
		"provisiond": {Kind: "Deployment", Namespace: "frame-system", Name: "frame-provisiond", Container: "provisiond"},
		"authd":      {Kind: "Deployment", Namespace: "cluster-control", Name: "cluster-control-auth", Container: "authd"},
		"uiproxy":    {Kind: "Deployment", Namespace: "cluster-control", Name: "cluster-control-ui", Container: "uiproxy"},
		"ui":         {Kind: "Deployment", Namespace: "cluster-control", Name: "cluster-control-ui", Container: "ui"},
		"agent":      {Kind: "DaemonSet", Namespace: "kube-system", Name: "frame-node-tuning-agent", Container: "agent"},
	}
}

// KubeCluster implements Cluster against a Kubernetes API. It never creates or
// deletes anything: a workload that does not exist is an error, not a reason to
// create it.
type KubeCluster struct {
	Client    client.Client
	Workloads map[string]Workload
	// ReadyTimeout bounds one WaitReady; PollEvery is the pause between looks.
	ReadyTimeout time.Duration
	PollEvery    time.Duration
}

var _ Cluster = (*KubeCluster)(nil)

func (k *KubeCluster) workload(component string) (Workload, error) {
	w, ok := k.Workloads[component]
	if !ok {
		return Workload{}, fmt.Errorf("component %q has no workload in this install", component)
	}
	return w, nil
}

// SetImage changes the image of one container, by a patch that names only that
// container, so nothing else in the pod template is rewritten.
func (k *KubeCluster) SetImage(ctx context.Context, component, image string) error {
	w, err := k.workload(component)
	if err != nil {
		return err
	}
	if image == "" {
		return fmt.Errorf("refusing to set an empty image on %s", component)
	}
	key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}
	switch w.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := k.Client.Get(ctx, key, &d); err != nil {
			return err
		}
		base := d.DeepCopy()
		if !setContainerImage(d.Spec.Template.Spec.Containers, w.Container, image) {
			return fmt.Errorf("deployment %s has no container %q", key, w.Container)
		}
		return k.Client.Patch(ctx, &d, client.StrategicMergeFrom(base))
	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := k.Client.Get(ctx, key, &ds); err != nil {
			return err
		}
		base := ds.DeepCopy()
		if !setContainerImage(ds.Spec.Template.Spec.Containers, w.Container, image) {
			return fmt.Errorf("daemonset %s has no container %q", key, w.Container)
		}
		return k.Client.Patch(ctx, &ds, client.StrategicMergeFrom(base))
	}
	return fmt.Errorf("unsupported workload kind %q", w.Kind)
}

// WaitReady returns once the rollout of the workload has finished and every
// replica is ready, or an error when the timeout passes or ctx ends.
func (k *KubeCluster) WaitReady(ctx context.Context, component string) error {
	w, err := k.workload(component)
	if err != nil {
		return err
	}
	timeout, every := k.ReadyTimeout, k.PollEvery
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if every <= 0 {
		every = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		ok, why, err := k.rolledOut(ctx, w)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s/%s not ready after %s: %s", w.Namespace, w.Name, timeout, why)
		case <-t.C:
		}
	}
}

func (k *KubeCluster) rolledOut(ctx context.Context, w Workload) (bool, string, error) {
	key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}
	switch w.Kind {
	case "Deployment":
		var d appsv1.Deployment
		if err := k.Client.Get(ctx, key, &d); err != nil {
			return false, "", err
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		s := d.Status
		switch {
		case s.ObservedGeneration < d.Generation:
			return false, "the controller has not seen the new spec yet", nil
		case s.UpdatedReplicas < want:
			return false, fmt.Sprintf("%d of %d replicas updated", s.UpdatedReplicas, want), nil
		case s.Replicas > s.UpdatedReplicas:
			return false, "old replicas still terminating", nil
		case s.ReadyReplicas < want:
			return false, fmt.Sprintf("%d of %d replicas ready", s.ReadyReplicas, want), nil
		}
		return true, "", nil
	case "DaemonSet":
		var ds appsv1.DaemonSet
		if err := k.Client.Get(ctx, key, &ds); err != nil {
			return false, "", err
		}
		s := ds.Status
		switch {
		case s.ObservedGeneration < ds.Generation:
			return false, "the controller has not seen the new spec yet", nil
		case s.UpdatedNumberScheduled < s.DesiredNumberScheduled:
			return false, fmt.Sprintf("%d of %d nodes updated", s.UpdatedNumberScheduled, s.DesiredNumberScheduled), nil
		case s.NumberReady < s.DesiredNumberScheduled:
			return false, fmt.Sprintf("%d of %d nodes ready", s.NumberReady, s.DesiredNumberScheduled), nil
		}
		return true, "", nil
	}
	return false, "", fmt.Errorf("unsupported workload kind %q", w.Kind)
}

func setContainerImage(cs []corev1.Container, name, image string) bool {
	for i := range cs {
		if cs[i].Name == name {
			cs[i].Image = image
			return true
		}
	}
	return false
}

// Running reports the image each known component runs now. A workload this
// install does not have is left out (an optional component), not an error.
func (k *KubeCluster) Running(ctx context.Context) ([]Running, error) {
	var out []Running
	for _, name := range sortedComponentNames(k.Workloads) {
		w := k.Workloads[name]
		key := types.NamespacedName{Namespace: w.Namespace, Name: w.Name}
		var containers []corev1.Container
		switch w.Kind {
		case "Deployment":
			var d appsv1.Deployment
			if err := k.Client.Get(ctx, key, &d); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			containers = d.Spec.Template.Spec.Containers
		case "DaemonSet":
			var ds appsv1.DaemonSet
			if err := k.Client.Get(ctx, key, &ds); err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			containers = ds.Spec.Template.Spec.Containers
		default:
			return nil, fmt.Errorf("unsupported workload kind %q", w.Kind)
		}
		for _, c := range containers {
			if c.Name == w.Container {
				out = append(out, Running{Component: name, Image: c.Image})
			}
		}
	}
	return out, nil
}

// Unready counts the known workloads whose rollout is not complete.
func (k *KubeCluster) Unready(ctx context.Context) (int, error) {
	n := 0
	for _, name := range sortedComponentNames(k.Workloads) {
		ok, _, err := k.rolledOut(ctx, k.Workloads[name])
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return 0, err
		}
		if !ok {
			n++
		}
	}
	return n, nil
}

func sortedComponentNames(m map[string]Workload) []string {
	imgs := make(map[string]string, len(m))
	for n := range m {
		imgs[n] = ""
	}
	return sortedComponents(imgs)
}
