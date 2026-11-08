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
	"testing"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// The bond and the VLAN reach the renderer. The first time these fields
// existed they were validated by the apiserver and dropped right here.
func TestToProvisionSpecCarriesTheBondAndTheVLAN(t *testing.T) {
	fi := &framev1beta1.FrameInstall{Spec: framev1beta1.FrameInstallSpec{
		Network: framev1beta1.InstallNetwork{
			Address: "192.0.2.210/24", Gateway: "192.0.2.254", DNS: []string{"192.0.2.254"},
			Bond: &framev1beta1.InstallBond{Members: []framev1beta1.MACAddress{"52:54:00:aa:00:01", "52:54:00:aa:00:02"}},
			VLAN: &framev1beta1.InstallVLAN{ID: 20},
		},
	}}
	got := toProvisionSpec(fi, "uid", "key", "").Network
	if got.Bond == nil || len(got.Bond.Members) != 2 || got.Bond.Members[1] != "52:54:00:aa:00:02" {
		t.Fatalf("bond = %+v", got.Bond)
	}
	if got.VLAN == nil || got.VLAN.ID != 20 || got.VLAN.ParentMAC != "" {
		t.Fatalf("vlan = %+v", got.VLAN)
	}

	fi.Spec.Network.Bond, fi.Spec.Network.VLAN = nil, &framev1beta1.InstallVLAN{ID: 7, ParentMAC: "52:54:00:aa:00:09"}
	got = toProvisionSpec(fi, "uid", "key", "").Network
	if got.Bond != nil || got.VLAN == nil || got.VLAN.ParentMAC != "52:54:00:aa:00:09" {
		t.Fatalf("network = %+v", got)
	}
}
