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

package redfish

import (
	"context"
	"errors"
	"fmt"
)

// This file holds the memory-module decoding split out of decode.go to keep
// that file under this lot's line ceiling. It is split here rather than
// elsewhere because it is the one resource with two shapes worth explaining
// at length: Finding 3 (docs/superpowers/specs/2026-09-10-lot1-hardware-
// redfish-design.md) found the captured iLO4 answering
// GET /redfish/v1/Systems/1/Memory/ with the older HPE Memory schema, not the
// current DMTF one this lot was written against.

// memoryJSON carries both the DMTF (current) Memory schema and the older
// schema an iLO4 actually sends (Finding 3): the captured hardware answers
// with members named proc1dimm1 etc. and fields DIMMType, SizeMB,
// DIMMStatus, Rank — not CapacityMiB, MemoryDeviceType or DeviceLocator.
// memoryModuleFrom prefers the DMTF field whenever a document sets it, so a
// newer iLO (iLO5+) that does use the current schema still decodes
// correctly.
type memoryJSON struct {
	// DMTF schema.
	DeviceLocator    string      `json:"DeviceLocator"`
	CapacityMiB      int32       `json:"CapacityMiB"`
	MemoryDeviceType string      `json:"MemoryDeviceType"`
	Manufacturer     string      `json:"Manufacturer"`
	Status           statusBlock `json:"Status"`

	// Older HPE schema, as served by iLO4. SizeMB is misnamed: despite the
	// field name, its unit is MiB, the same one CapacityMiB uses — verified
	// against the captures (a 16 GiB DIMM reports SizeMB: 16384).
	//
	// SocketLocator is the human-readable slot label ("PROC 1 DIMM 1"), the
	// same label printed on the chassis silkscreen beside the physical
	// socket — this is what Slot should carry (round 1 fix, see
	// task-6b-report.md). Id is the machine-shaped member identifier
	// ("proc1dimm1") and is Slot's last-resort fallback only, for an iLO
	// revision that omits SocketLocator: someone pulling a DIMM under time
	// pressure needs the label the hardware itself prints, not one they
	// have to decode first.
	ID            string `json:"Id"`
	SizeMB        int32  `json:"SizeMB"`
	DIMMType      string `json:"DIMMType"`
	SocketLocator string `json:"SocketLocator"`
}

func (c *client) readMemory(ctx context.Context, collectionPath string, snap *Snapshot) error {
	refs, err := c.collectionMembers(ctx, collectionPath)
	switch {
	case err == nil:
		for _, ref := range refs {
			var m memoryJSON
			if gerr := c.get(ctx, ref, &m); gerr != nil {
				if errors.Is(gerr, errNotFound) {
					continue
				}
				return fmt.Errorf("redfish: read memory module %s: %w", ref, gerr)
			}
			snap.Inventory.MemoryModules = append(snap.Inventory.MemoryModules, memoryModuleFrom(m))
		}
		return nil
	case errors.Is(err, errNotFound):
		return nil
	default:
		return fmt.Errorf("redfish: read memory: %w", err)
	}
}

// memoryModuleFrom maps a decoded memoryJSON onto the exported MemoryModule,
// preferring the DMTF field and falling back to the older HPE one that an
// iLO4 actually populates (Finding 3). Slot has a three-deep fallback:
// DeviceLocator (DMTF) first, then SocketLocator (HPE's own human-readable
// label, e.g. "PROC 1 DIMM 1" — this is what a person pulling a DIMM reads
// off the chassis), and only then the member Id (e.g. "proc1dimm1") for an
// iLO revision that sends neither locator. Round 1 review corrected this
// from Id-first: the slot label is the point of this field, and it should
// be the label the hardware itself prints, not one that needs decoding.
func memoryModuleFrom(m memoryJSON) MemoryModule {
	slot := m.DeviceLocator
	if slot == "" {
		slot = m.SocketLocator
	}
	if slot == "" {
		slot = m.ID
	}
	sizeMiB := m.CapacityMiB
	if sizeMiB == 0 {
		sizeMiB = m.SizeMB
	}
	typ := m.MemoryDeviceType
	if typ == "" {
		typ = m.DIMMType
	}
	return MemoryModule{
		Slot:         slot,
		SizeMiB:      sizeMiB,
		Type:         typ,
		Manufacturer: m.Manufacturer,
	}
}
