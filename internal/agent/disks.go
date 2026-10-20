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
	"unicode/utf8"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// lsblkColumns are the columns ObserveDisks parses. They are listed here,
// once, so the argv and the parser cannot drift apart — a column dropped
// from the request but still read from the JSON yields a zero value that
// looks exactly like a disk with nothing on it.
//
// WWN, MODEL, ROTA and LABEL exist in every util-linux lsblk this agent
// can meet. ID-LINK is deliberately absent: it is recent, and an lsblk
// that does not know a column refuses the whole invocation.
const lsblkColumns = "NAME,PATH,SERIAL,SIZE,TYPE,FSTYPE,MOUNTPOINT,WWN,MODEL,ROTA,LABEL"

// maxMembers mirrors ObservedDisk.Members' MaxItems. A report over the
// schema bound would be rejected whole by the apiserver, so the agent
// trims rather than lose every disk for one oversized list.
const maxMembers = 16

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Serial     *string       `json:"serial"`
	Size       int64         `json:"size"`
	Type       string        `json:"type"`
	FSType     *string       `json:"fstype"`
	MountPoint *string       `json:"mountpoint"`
	WWN        *string       `json:"wwn"`
	Model      *string       `json:"model"`
	Rota       lsblkBool     `json:"rota"`
	Label      *string       `json:"label"`
	Children   []lsblkDevice `json:"children"`
}

// lsblkBool is lsblk's ROTA column. Recent util-linux emits a JSON
// boolean, older releases the string "0" or "1", and a device the kernel
// says nothing about emits null. Null stays nil: "not reported" is not
// "solid state".
type lsblkBool struct {
	Value *bool
}

func (b *lsblkBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var out bool
	switch t := v.(type) {
	case nil:
		b.Value = nil
		return nil
	case bool:
		out = t
	case float64:
		out = t != 0
	case string:
		switch strings.TrimSpace(t) {
		case "1", "true":
			out = true
		case "0", "false":
			out = false
		default:
			b.Value = nil
			return nil
		}
	default:
		b.Value = nil
		return nil
	}
	b.Value = &out
	return nil
}

// Bounds mirrored from the kubebuilder markers on ObservedDisk and
// MachineStorage (api/frame/v1beta1/framemachine_types.go). The apiserver
// rejects the whole status write when one value is one character too long or
// the list one item too long, so the agent keeps within them: descriptive
// strings are clipped, identifiers that clipping would falsify (serial,
// WWN, kernel path) make the value — or, for serial and path, the disk — be
// left out, and disks past the 64th are left out. A left-out disk the BMC
// lists then shows up as a bmc-only divergence rather than freezing the
// machine's disk list — unless the divergence list itself reaches its own
// cap of 64, where internal/storage.Join keeps bmc-only entries first and
// truncates the rest.
const (
	maxObservedDisks = 64
	maxPathLen       = 512
	maxSerialLen     = 128
	maxWWNLen        = 64
	maxModelLen      = 256
	maxMountPointLen = 512
	maxFSTypeLen     = 64
)

// clipRunes cuts s to at most n characters (the CRD's MaxLength unit),
// never inside a character.
func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
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
	disks, _, err := ObserveDisksIndexed(runner)
	return disks, err
}

// ObserveDisksIndexed is ObserveDisks plus an index from every kernel name
// under a serial-bearing disk (the disk itself and its partitions, e.g.
// "sdc" and "sdc2") to that disk's serial. The local-storage reader uses it
// to name the disk behind an md member or an LVM PV; built from the same
// lsblk call, so the kernel names and the serials belong to one instant.
func ObserveDisksIndexed(runner CommandRunner) ([]framev1beta1.ObservedDisk, map[string]string, error) {
	out, err := runner.Run("lsblk", "-J", "-b", "-o", lsblkColumns)
	if err != nil {
		return nil, nil, fmt.Errorf("running lsblk: %w", err)
	}

	var payload struct {
		BlockDevices []lsblkDevice `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return nil, nil, fmt.Errorf("parsing lsblk output: %w", err)
	}

	index := make(map[string]string)
	for _, dev := range payload.BlockDevices {
		serial := str(dev.Serial)
		if dev.Type != lsblkTypeDisk || serial == "" {
			continue
		}
		walk(dev, func(d lsblkDevice) {
			if d.Type == lsblkTypeDisk || d.Type == "part" {
				index[d.Name] = serial
			}
		})
	}

	disks := make([]framev1beta1.ObservedDisk, 0, len(payload.BlockDevices))
	for _, dev := range payload.BlockDevices {
		if dev.Type != lsblkTypeDisk {
			continue
		}
		serial := ""
		if dev.Serial != nil {
			serial = strings.TrimSpace(*dev.Serial)
		}
		// An over-long serial is dropped like a missing one: clipped, it
		// would be a different identifier, and published whole it would get
		// the entire status rejected.
		if serial == "" || utf8.RuneCountInString(serial) > maxSerialLen {
			continue
		}
		// The kernel path is shown and joined on as-is; clipped, it would
		// name another device, so an over-long one drops the disk.
		if utf8.RuneCountInString(dev.Path) > maxPathLen {
			continue
		}
		if len(disks) == maxObservedDisks {
			break
		}
		// Same for the WWN: a clipped WWN names another disk, so an
		// over-long one is not published (and no by-id path is derived).
		wwn := str(dev.WWN)
		if utf8.RuneCountInString(wwn) > maxWWNLen {
			wwn = ""
		}
		disks = append(disks, framev1beta1.ObservedDisk{
			Path:         dev.Path,
			SerialNumber: serial,
			SizeGB:       int32(dev.Size / 1_000_000_000),
			Occupancy:    occupancyOf(dev),
			WWN:          wwn,
			ByIDPath:     byIDPathFromWWN(wwn),
			Model:        clipRunes(str(dev.Model), maxModelLen),
			Rotational:   dev.Rota.Value,
			MountPoint:   clipRunes(firstMountPoint(dev), maxMountPointLen),
			FSType:       clipRunes(str(dev.FSType), maxFSTypeLen),
			Members:      membersOf(dev),
		})
	}
	return disks, index, nil
}

// byIDPathFromWWN derives the /dev/disk/by-id link udev creates for a
// disk's World Wide Name. It is computed, not read from the filesystem: the
// agent never lists /dev/disk/by-id, so a udev rule set that names the link
// differently makes this string point nowhere.
//
// The link's prefix depends on the transport, and the WWN's own shape says
// which: SCSI/SATA disks report a NAA identifier ("0x5000c500…") and get
// "wwn-0x…"; NVMe namespaces report an EUI-64/NGUID ("eui.…") or an NVMe
// identifier ("nvme.…") and get "nvme-eui.…"/"nvme-nvme.…" — never "wwn-".
// Any other shape yields an empty path rather than a guess, as does an empty
// WWN (a bare "wwn-" would look like a link).
func byIDPathFromWWN(wwn string) string {
	switch {
	case strings.HasPrefix(wwn, "0x"):
		return "/dev/disk/by-id/wwn-" + wwn
	case strings.HasPrefix(wwn, "eui."), strings.HasPrefix(wwn, "nvme."):
		return "/dev/disk/by-id/nvme-" + wwn
	default:
		return ""
	}
}

// walk calls fn on dev and every descendant, depth first, in lsblk order.
// A raid member's md device hangs below the partition, not below the
// disk, so one level of children is not enough to see it.
func walk(dev lsblkDevice, fn func(lsblkDevice)) {
	fn(dev)
	for _, c := range dev.Children {
		walk(c, fn)
	}
}

// firstMountPoint is the first non-empty mountpoint on the disk or any of
// its descendants, in lsblk order.
func firstMountPoint(dev lsblkDevice) string {
	found := ""
	walk(dev, func(d lsblkDevice) {
		if found == "" {
			found = str(d.MountPoint)
		}
	})
	return found
}

// anyFSType reports whether the disk or any descendant carries fstype.
func anyFSType(dev lsblkDevice, fstype string) bool {
	hit := false
	walk(dev, func(d lsblkDevice) {
		if str(d.FSType) == fstype {
			hit = true
		}
	})
	return hit
}

// membersOf names what the disk is a member of: the md devices built on it
// (descendants of type raid*, e.g. "md0") and, for a zfs_member, the pool
// label lsblk reports. Duplicates are dropped — two partitions of one disk
// in the same array are one membership — and the list is capped at
// maxMembers.
func membersOf(dev lsblkDevice) []string {
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		// A name over the schema's item bound is skipped, not truncated: a
		// cut pool name would name a different pool.
		if name == "" || len(name) > 128 || seen[name] || len(out) >= maxMembers {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	walk(dev, func(d lsblkDevice) {
		switch {
		case strings.HasPrefix(d.Type, "raid"):
			add(d.Name)
		case str(d.FSType) == "zfs_member":
			add(str(d.Label))
		}
	})
	return out
}

// occupancyMounted is the occupancy of a disk that it or one of its
// partitions is mounted.
const occupancyMounted = "mounted"

// lsblkTypeDisk is the lsblk TYPE of a whole disk, as opposed to a
// partition.
const lsblkTypeDisk = "disk"

// occupancyOf says what is using the disk, and errs towards "occupied".
// FrameDiskClaim's third guard fails closed on anything that is not "free",
// so a signature this function does not recognise must never fall through
// to "free": the default branch returns "in-use" precisely because an
// unknown filesystem is still a filesystem.
//
// Precedence: a mount anywhere on the disk wins ("mounted"), then a raid
// or zfs membership anywhere on it, then the disk's own signature. Only a
// disk with no signature, no children and no mount is "free".
func occupancyOf(dev lsblkDevice) string {
	if firstMountPoint(dev) != "" {
		return occupancyMounted
	}
	if anyFSType(dev, "linux_raid_member") {
		return "md-member"
	}
	if anyFSType(dev, "zfs_member") {
		return "zfs-member"
	}

	fstype := str(dev.FSType)
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
