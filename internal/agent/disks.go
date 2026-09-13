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

package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	framev1beta1 "github.com/rmocq/frame/api/frame/v1beta1"
)

// lsblkColumns are the columns ObserveDisks parses. They are listed here,
// once, so the argv and the parser cannot drift apart — a column dropped
// from the request but still read from the JSON yields a zero value that
// looks exactly like a disk with nothing on it.
const lsblkColumns = "NAME,PATH,SERIAL,SIZE,TYPE,FSTYPE,MOUNTPOINT"

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Serial     *string       `json:"serial"`
	Size       int64         `json:"size"`
	Type       string        `json:"type"`
	FSType     *string       `json:"fstype"`
	MountPoint *string       `json:"mountpoint"`
	Children   []lsblkDevice `json:"children"`
}

// ObserveDisks reads the node's block devices through runner and returns one
// entry per whole disk.
//
// Three rules, each with a test:
//
//  1. Only type "disk" is returned. A partition is not a thing a
//     FrameDiskClaim can claim.
//  2. A disk with no serial is dropped, not returned with an empty one.
//     FrameDiskClaim's first guard compares a hand-typed serial against
//     these entries, and two empty strings are equal — an entry wearing ""
//     would match a claim that also left the field blank.
//  3. A runner failure is an error, never an empty list. "I could not
//     look" and "there is nothing there" are the two answers that must
//     never be confused: the second authorises a wipe.
func ObserveDisks(runner CommandRunner) ([]framev1beta1.ObservedDisk, error) {
	out, err := runner.Run("lsblk", "-J", "-b", "-o", lsblkColumns)
	if err != nil {
		return nil, fmt.Errorf("running lsblk: %w", err)
	}

	var payload struct {
		BlockDevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	disks := make([]framev1beta1.ObservedDisk, 0, len(payload.BlockDevices))
	for _, dev := range payload.BlockDevices {
		if dev.Type != "disk" {
			continue
		}
		serial := ""
		if dev.Serial != nil {
			serial = strings.TrimSpace(*dev.Serial)
		}
		if serial == "" {
			continue
		}
		disks = append(disks, framev1beta1.ObservedDisk{
			Path:         dev.Path,
			SerialNumber: serial,
			SizeGB:       int32(dev.Size / 1_000_000_000),
			Occupancy:    occupancyOf(dev),
		})
	}
	return disks, nil
}

// occupancyOf says what is using the disk, and errs towards "occupied".
// FrameDiskClaim's third guard fails closed on anything that is not "free",
// so a signature this function does not recognise must never fall through
// to "free": the default branch returns "in-use" precisely because an
// unknown filesystem is still a filesystem.
func occupancyOf(dev lsblkDevice) string {
	if dev.MountPoint != nil && *dev.MountPoint != "" {
		return "mounted"
	}
	for _, child := range dev.Children {
		if child.MountPoint != nil && *child.MountPoint != "" {
			return "mounted"
		}
	}

	fstype := ""
	if dev.FSType != nil {
		fstype = *dev.FSType
	}
	switch {
	case fstype == "":
		if len(dev.Children) > 0 {
			return "partitioned"
		}
		return "free"
	case strings.HasPrefix(fstype, "ceph"):
		return "ceph-osd"
	case fstype == "LVM2_member":
		return "lvm-pv"
	default:
		return "in-use"
	}
}
