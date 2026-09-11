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

// These specs go through the real apiserver (k8sClient / envtest, wired in
// suite_test.go), because the thing under test is the CEL rules the
// apiserver compiles — not a Go zero-value fact.
//
// This is a table, not a plain *testing.T table test: a bare Test function in
// this package sharing k8sClient would run as its own top-level Test, and go
// test orders top-level tests by source file name — this file
// (frameinstall_v1beta1_schema_test.go) sorts before suite_test.go, so it
// would run before BeforeSuite populates k8sClient and panic on a nil
// client. See frametask_v1beta1_schema_test.go's header for the same trap,
// found first there. DescribeTable registers into the one suite
// BeforeSuite guards, which is how every sibling schema test in this package
// avoids it.
//
// validFrameInstall returns a complete, accepted mirror/init object; every
// reject case below mutates exactly the field(s) its named rule reads, and
// nothing else, so that a case refused by some other rule is a bug in the
// case, not a pass. Fixture names are prefixed fi-schema- and unique per
// case — lot 1 lost time to a fixture name colliding with another test's
// object and failing with AlreadyExists depending on seed order.
var _ = Describe("FrameInstall v1beta1 schema", func() {
	validFrameInstall := func(name string) *framev1beta1.FrameInstall {
		return &framev1beta1.FrameInstall{
			ObjectMeta: metav1.ObjectMeta{Name: "fi-schema-" + name, Namespace: "default"},
			Spec: framev1beta1.FrameInstallSpec{
				MachineRef:    "bmc-1",
				ConfirmSerial: "SN-001",
				Hostname:      "node-1",
				Network: framev1beta1.InstallNetwork{
					Address: "192.168.2.210/24",
					Gateway: "192.168.2.1",
				},
				Layout: framev1beta1.InstallLayout{
					Kind: "mirror",
					Disks: []framev1beta1.InstallDisk{
						{ByID: "/dev/disk/by-id/ata-disk-a", SizeBytes: 500_000_000_000},
						{ByID: "/dev/disk/by-id/ata-disk-b", SizeBytes: 500_000_000_000},
					},
				},
				Cluster: framev1beta1.InstallCluster{
					Mode:       "init",
					K3sVersion: "v1.30.0+k3s1",
				},
				SSHKeyRef: "install-ssh-key",
			},
		}
	}

	DescribeTable("validates the fields a typo would wipe the wrong disks or join the wrong cluster through",
		func(slug string, mutate func(fi *framev1beta1.FrameInstall), wantErr string) {
			fi := validFrameInstall(slug)
			mutate(fi)
			err := k8sClient.Create(ctx, fi)
			if wantErr == "" {
				Expect(err).NotTo(HaveOccurred(), "the apiserver rejected a valid spec: %v", err)
				DeferCleanup(func() { _ = k8sClient.Delete(ctx, fi) })
				return
			}
			Expect(err).To(HaveOccurred(), "the apiserver accepted it")
			Expect(err.Error()).To(ContainSubstring(wantErr))
		},

		Entry("accepts a complete mirror, init object", "accept-mirror-init",
			func(fi *framev1beta1.FrameInstall) {
				// no mutation: validFrameInstall's base is already a
				// complete mirror/init object.
			}, ""),

		Entry("accepts a complete single-disk, join object", "accept-single-disk-join",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Layout = framev1beta1.InstallLayout{
					Kind:  "single-disk",
					Disks: []framev1beta1.InstallDisk{{ByID: "/dev/disk/by-id/ata-disk-a", SizeBytes: 500_000_000_000}},
				}
				fi.Spec.Cluster = framev1beta1.InstallCluster{
					Mode:         "join",
					ServerURL:    "https://192.168.2.10:6443",
					JoinTokenRef: "k3s-join-token",
					K3sVersion:   "v1.30.0+k3s1",
				}
			}, ""),

		Entry("rejects a mirror with one disk", "mirror-one-disk",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Layout.Disks = fi.Spec.Layout.Disks[:1]
			}, "a mirror is exactly two disks"),

		Entry("rejects a mirror with three disks", "mirror-three-disks",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Layout.Disks = append(fi.Spec.Layout.Disks,
					framev1beta1.InstallDisk{ByID: "/dev/disk/by-id/ata-disk-c", SizeBytes: 500_000_000_000})
			}, "a mirror is exactly two disks"),

		Entry("rejects a single-disk layout with two disks", "single-disk-two-disks",
			func(fi *framev1beta1.FrameInstall) {
				// Only Kind changes. The base's two disks are left as-is:
				// changing anything about them would risk also tripping the
				// by-id rule below rather than isolating this one.
				fi.Spec.Layout.Kind = "single-disk"
			}, "single-disk is exactly one disk"),

		Entry("rejects a disk named by kernel name", "disk-kernel-name",
			func(fi *framev1beta1.FrameInstall) {
				// Disk count stays 2 and kind stays mirror, so this cannot
				// also trip either disk-count rule.
				fi.Spec.Layout.Disks[0].ByID = "/dev/sda"
			}, "disks must be named by /dev/disk/by-id, never by kernel name"),

		Entry("rejects a raw layout with no recipe", "raw-no-recipe",
			func(fi *framev1beta1.FrameInstall) {
				// Kind moves to raw and Disks is dropped entirely: the
				// mirror/single-disk disk-count rules both short-circuit to
				// true on kind != 'mirror' / kind != 'single-disk' regardless
				// of what Disks holds, so only the raw rule's has(self.raw)
				// clause is exercised.
				fi.Spec.Layout = framev1beta1.InstallLayout{Kind: "raw"}
			}, "layout raw needs a recipe"),

		Entry("rejects mode join with no joinTokenRef", "join-no-token",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Cluster.Mode = "join"
				fi.Spec.Cluster.ServerURL = "https://192.168.2.10:6443"
			}, "joining an existing cluster needs both serverURL and joinTokenRef"),

		Entry("rejects mode join with no serverURL", "join-no-server-url",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Cluster.Mode = "join"
				fi.Spec.Cluster.JoinTokenRef = "k3s-join-token"
			}, "joining an existing cluster needs both serverURL and joinTokenRef"),

		Entry("rejects an address that is not CIDR", "address-not-cidr",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Network.Address = "192.168.2.210"
			}, "address must be a CIDR"),

		Entry("rejects a gateway that is not an IP", "gateway-not-ip",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.Network.Gateway = "not-an-ip"
			}, "gateway must be an IP address"),

		Entry("rejects an empty confirmSerial", "empty-confirm-serial",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.ConfirmSerial = ""
			}, "spec.confirmSerial"),

		Entry("rejects a bootMode of Bios", "boot-mode-bios",
			func(fi *framev1beta1.FrameInstall) {
				fi.Spec.BootMode = "Bios"
			}, "spec.bootMode"),
	)
})
