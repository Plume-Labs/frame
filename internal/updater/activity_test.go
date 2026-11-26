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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

func TestClusterActivityCountsWhatTheConsoleCounts(t *testing.T) {
	s := runtime.NewScheme()
	_ = framev1beta1.AddToScheme(s)
	job := func(name, reason string) *framev1beta1.FrameJob {
		return &framev1beta1.FrameJob{
			ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: name},
			Status:     framev1beta1.FrameJobStatus{Conditions: []metav1.Condition{{Type: "Ready", Reason: reason, Status: "Unknown"}}},
		}
	}
	inst := func(name, phase string) *framev1beta1.FrameInstall {
		return &framev1beta1.FrameInstall{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: name}, Status: framev1beta1.FrameInstallStatus{Phase: phase}}
	}
	repl := func(name, phase string) *framev1beta1.FrameOSDReplacement {
		return &framev1beta1.FrameOSDReplacement{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: name}, Status: framev1beta1.FrameOSDReplacementStatus{Phase: phase}}
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(
		job("j1", "Running"), job("j2", "Completed"), job("j3", "Submitted"),
		inst("i1", "Installing"), inst("i2", "Ready"), inst("i3", "Failed"),
		repl("r1", "Draining"), repl("r2", "Succeeded"), repl("r3", "Cancelled"),
	).Build()
	a, err := ClusterActivity(c)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.RunningJobs != 1 || a.InstallsInProgress != 1 || a.OSDReplacementsActive != 1 {
		t.Fatalf("%+v", a)
	}
}
