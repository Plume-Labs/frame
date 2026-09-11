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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/rmocq/frame/internal/redfish"
)

// This file is split out of framemachine_controller_test.go, which it would
// otherwise push over this repo's line ceiling, the same reason
// framemachine_fake_test.go was split out. It has its own minimal
// fake/reconciler setup rather than sharing the other file's — those are
// local closures inside that file's own Describe literal and are not
// reachable from here.
//
// Round-2 review, finding 2: internal/redfish.Snapshot.LogPossiblyStale is
// the only place a BMC whose out-of-range pagination probe doesn't come
// back the way this iLO4's does shows up — the probe still succeeds,
// EventLog/EventLogCounts/EventLogTotal all still populate, but from what
// may be the machine's oldest page rather than its newest, with nothing
// else in status to tell the two apart. This proves applySnapshot actually
// copies that flag into status.EventLogPossiblyStale — the field
// MachineEventLog.tsx's banner reads — rather than it dead-ending inside
// the Snapshot the moment it leaves internal/redfish.
var _ = Describe("FrameMachine controller — LogPossiblyStale", func() {
	It("copies LogPossiblyStale from the snapshot into status.EventLogPossiblyStale", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "fm-stale-log-creds", Namespace: "default"},
			StringData: map[string]string{"username": "admin", "password": "secret"},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		fake := &fakeRedfish{snapshot: &redfish.Snapshot{
			PowerState:         "On",
			PostState:          "InPostDiscoveryComplete",
			SensorsTrustworthy: true,
			Log:                []redfish.LogEntry{{ID: "1", Severity: "Warning"}},
			LogTotal:           175,
			LogCounts:          map[string]int{"Warning": 1},
			LogPossiblyStale:   true,
		}}
		r := &FrameMachineReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(32),
			NewClient: func(_ context.Context, _ client.Client, _ string, _ framev1beta1.BMCSpec) (redfish.Client, error) {
				return fake, nil
			},
		}

		fm := &framev1beta1.FrameMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "fm-stale-log", Namespace: "default"},
			Spec: framev1beta1.FrameMachineSpec{
				BMC: framev1beta1.BMCSpec{
					Address:        "192.168.2.60",
					CredentialsRef: "fm-stale-log-creds",
					TLS:            framev1beta1.BMCTLSSpec{InsecureSkipVerify: true},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fm)).To(Succeed())

		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "fm-stale-log", Namespace: "default"}})
		Expect(err).NotTo(HaveOccurred())

		var got framev1beta1.FrameMachine
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fm-stale-log", Namespace: "default"}, &got)).To(Succeed())
		Expect(got.Status.EventLogPossiblyStale).To(BeTrue())
		// The rest of the log still populates from whatever page was
		// actually read — this flag is additive, not a replacement for the
		// existing best-effort fields.
		Expect(got.Status.EventLogTotal).To(BeNumerically("==", 175))
	})
})
