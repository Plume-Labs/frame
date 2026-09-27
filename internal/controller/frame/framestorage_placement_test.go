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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

func placementEntry(t string, prev *framev1beta1.CephPlacement) *framev1beta1.FrameStorage {
	return &framev1beta1.FrameStorage{
		Spec:   framev1beta1.FrameStorageSpec{Type: t},
		Status: framev1beta1.FrameStorageStatus{CephPlacement: prev},
	}
}

// After a manager restart the reader has no last good read in memory; a
// failed read must not blank the weights the status already holds.
func TestReconcilePlacementKeepsKnownListsOnAFailedRead(t *testing.T) {
	at := metav1.Now()
	prev := &framev1beta1.CephPlacement{
		State: "Collected", CollectedAt: &at,
		OSDs: []framev1beta1.CephOSDWeight{{ID: 3, CrushWeight: "1.0900"}},
	}
	r := &FrameStorageReconciler{CephPlacement: func(context.Context) *framev1beta1.CephPlacement {
		return &framev1beta1.CephPlacement{State: "Error", Message: "dashboard login: HTTP 401"}
	}}
	fs := placementEntry("ceph-rbd", prev)
	r.reconcilePlacement(context.Background(), fs)
	got := fs.Status.CephPlacement
	if got.State != "Error" || len(got.OSDs) != 1 || got.CollectedAt != &at || got.Message == "" {
		t.Fatalf("placement = %+v", got)
	}
}

func TestReconcilePlacementOnlyForCephEntries(t *testing.T) {
	called := false
	r := &FrameStorageReconciler{CephPlacement: func(context.Context) *framev1beta1.CephPlacement {
		called = true
		return &framev1beta1.CephPlacement{State: "Collected"}
	}}
	fs := placementEntry("local-path", nil)
	r.reconcilePlacement(context.Background(), fs)
	if called || fs.Status.CephPlacement != nil {
		t.Fatal("a local-path entry read the Ceph dashboard")
	}
}
