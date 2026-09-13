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

// Package storage joins Frame's two sources of truth about a machine's
// disks. It imports nothing but the API types: the join is the argument of
// docs/superpowers/specs/2026-09-13-storage-design.md §3, and an argument
// that needs a cluster to run is an argument nobody checks.
package storage

import (
	"fmt"
	"sort"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// Join returns every disagreement between what the BMC lists and what the
// node's kernel reports, matched on serial number and nothing else.
//
// An unidentified disk — one whose serial is empty on either side — is
// never matched, not even against another unidentified disk. Two empty
// strings are equal in Go, so a plain map join reports agreement between
// two disks that were never identified at all; each comes back as its own
// divergence instead.
//
// The result is sorted by serial then reason, so a status field does not
// churn on map iteration order.
func Join(bmc []framev1beta1.DriveInfo, observed []framev1beta1.ObservedDisk) []framev1beta1.DiskDivergence {
	byBMC := make(map[string]framev1beta1.DriveInfo, len(bmc))
	var out []framev1beta1.DiskDivergence

	for _, d := range bmc {
		if d.SerialNumber == "" {
			out = append(out, framev1beta1.DiskDivergence{
				Reason: "bmc-only",
				Detail: fmt.Sprintf("bay %s reports no serial number, so it cannot be matched", d.Location),
			})
			continue
		}
		byBMC[d.SerialNumber] = d
	}

	seen := make(map[string]bool, len(observed))
	for _, o := range observed {
		if o.SerialNumber == "" {
			out = append(out, framev1beta1.DiskDivergence{
				Reason: "os-only",
				Detail: fmt.Sprintf("%s reports no serial number, so it cannot be matched", o.Path),
			})
			continue
		}
		seen[o.SerialNumber] = true

		b, ok := byBMC[o.SerialNumber]
		if !ok {
			out = append(out, framev1beta1.DiskDivergence{
				SerialNumber: o.SerialNumber,
				Reason:       "os-only",
				Detail:       fmt.Sprintf("%s is not in the BMC's drive list", o.Path),
			})
			continue
		}
		if b.SizeGB != o.SizeGB {
			out = append(out, framev1beta1.DiskDivergence{
				SerialNumber: o.SerialNumber,
				Reason:       "mismatch",
				Detail:       fmt.Sprintf("BMC says %d GB, node says %d GB", b.SizeGB, o.SizeGB),
			})
		}
	}

	for _, d := range bmc {
		if d.SerialNumber == "" || seen[d.SerialNumber] {
			continue
		}
		out = append(out, framev1beta1.DiskDivergence{
			SerialNumber: d.SerialNumber,
			Reason:       "bmc-only",
			Detail:       fmt.Sprintf("bay %s: the BMC lists it, the node's kernel does not", d.Location),
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SerialNumber != out[j].SerialNumber {
			return out[i].SerialNumber < out[j].SerialNumber
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}
