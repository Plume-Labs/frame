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
	"regexp"
	"strconv"
	"strings"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// Parsers for the local-storage reader. Text formats, not JSON, for ZFS on
// purpose: `zpool status -j` only exists from OpenZFS 2.3, and the agent
// meets older nodes; the text layout has been stable for a decade.

// ── zpool list -Hp ─────────────────────────────────────────────────────────

func parseInt64(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// optPercent reads a -p percentage column; "-" (not applicable) is nil.
func optPercent(s string) *int32 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return nil
	}
	p := int32(v)
	return &p
}

// parseZpoolList reads name,size,alloc,free,frag,cap,health, tab-separated.
func parseZpoolList(out string) []framev1beta1.ZPool {
	var pools []framev1beta1.ZPool
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 7 || f[0] == "" {
			continue
		}
		pools = append(pools, framev1beta1.ZPool{
			Name:                 clipRunes(f[0], 128),
			SizeBytes:            parseInt64(f[1]),
			AllocatedBytes:       parseInt64(f[2]),
			FreeBytes:            parseInt64(f[3]),
			FragmentationPercent: optPercent(f[4]),
			CapacityPercent:      optPercent(f[5]),
			Health:               clipRunes(f[6], 32),
		})
	}
	return pools
}

// ── zpool status -P ────────────────────────────────────────────────────────

type zpoolStatus struct {
	name, scan, errors string
	vdevs              []framev1beta1.ZVdev
}

// zpoolKey is any "key:" line of zpool status (pool, state, status,
// action, see, scan, expand, remove, checkpoint, config, errors — and any
// key a later OpenZFS adds). Keys are right-aligned with at most a few
// spaces (the files listed under "errors:" are indented further); continuation
// lines and the config table start with a tab.
var zpoolKey = regexp.MustCompile(`^ {0,4}([a-z][a-z ]*):(?:\s(.*))?$`)

// zpoolStates are the words the STATE column can hold. A row is read by
// finding its state word, so a name with spaces (a GUID row, a path with a
// space) stays whole.
var zpoolStates = map[string]bool{
	"ONLINE": true, "DEGRADED": true, "FAULTED": true, "OFFLINE": true, "UNAVAIL": true,
	"REMOVED": true, "AVAIL": true, "INUSE": true, "SPLIT": true, "SUSPENDED": true,
}

// zpoolCounter reads a READ/WRITE/CKSUM column. zpool abbreviates large
// counts (1.2K, 3M); the value is rounded, never dropped — an error count
// must not read as zero.
func zpoolCounter(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1e3, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1e6, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1e9, strings.TrimSuffix(s, "G")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(v * mult), true
}

func parseZpoolStatus(out string) []zpoolStatus {
	var all []zpoolStatus
	var cur *zpoolStatus
	section := ""
	rootSeen := false
	inGroup := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		if m := zpoolKey.FindStringSubmatch(line); m != nil {
			key, val := m[1], strings.TrimSpace(m[2])
			if key == "pool" {
				all = append(all, zpoolStatus{name: val})
				cur = &all[len(all)-1]
				rootSeen = false
				inGroup = false
			}
			section = key
			if cur == nil {
				continue
			}
			switch key {
			case "scan":
				cur.scan = val
			case "errors":
				cur.errors = val
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch section {
		case "scan":
			// Continuation lines of an in-progress scan. An expand: or
			// remove: block has its own key (zpoolKey matches any key),
			// so it ends the scan section instead of being appended.
			if t := strings.TrimSpace(line); t != "" {
				cur.scan += "; " + t
			}
		case "config":
			if !strings.HasPrefix(line, "\t") {
				continue
			}
			body := strings.TrimPrefix(line, "\t")
			indent := len(body) - len(strings.TrimLeft(body, " "))
			f := strings.Fields(body)
			if len(f) == 0 || (f[0] == "NAME" && len(f) > 1 && f[1] == "STATE") {
				continue
			}
			// Split name | state | counters | note on the state word.
			si := -1
			for k := 1; k < len(f); k++ {
				if zpoolStates[f[k]] {
					si = k
					break
				}
			}
			name := strings.Join(f, " ")
			var rest []string
			if si > 0 {
				name = strings.Join(f[:si], " ")
				rest = f[si:]
			}
			// The first row is the pool itself: its state is the pool's.
			if !rootSeen && indent == 0 && name == cur.name {
				rootSeen = true
				continue
			}
			// A row at indent 0 that is not the pool opens a group (logs,
			// cache, spares, special, dedup): the group header sits at
			// depth 0 like a top-level vdev, its members one below it.
			// Under the pool root every row is indented one step more than
			// its depth.
			if indent == 0 {
				inGroup = true
			}
			depth := indent / 2
			if !inGroup {
				depth--
			}
			v := framev1beta1.ZVdev{Name: clipRunes(name, 512), Depth: int32(max(depth, 0))}
			if len(rest) >= 1 {
				v.State = clipRunes(rest[0], 32)
			}
			n := 1
			if len(rest) >= 4 {
				r1, ok1 := zpoolCounter(rest[1])
				r2, ok2 := zpoolCounter(rest[2])
				r3, ok3 := zpoolCounter(rest[3])
				if ok1 && ok2 && ok3 {
					v.ReadErrors, v.WriteErrors, v.ChecksumErrors = r1, r2, r3
					n = 4
				}
			}
			if len(rest) > n {
				v.Note = clipRunes(strings.Join(rest[n:], " "), 256)
			}
			cur.vdevs = append(cur.vdevs, v)
		}
	}
	return all
}

var scrubDone = regexp.MustCompile(`scrub repaired .* with (\d+) errors on (.+?)(;|$)`)

// mergeZpoolStatus folds status into the pools listed by zpool list,
// matched on name. A pool status knows but list does not (a race between
// the two commands) is skipped: the next cycle sees it in both.
func mergeZpoolStatus(pools []framev1beta1.ZPool, st []zpoolStatus) {
	byName := map[string]int{}
	for i := range pools {
		byName[pools[i].Name] = i
	}
	for _, s := range st {
		i, ok := byName[clipRunes(s.name, 128)]
		if !ok {
			continue
		}
		p := &pools[i]
		p.Scan = clipRunes(s.scan, 512)
		p.Errors = clipRunes(s.errors, 256)
		if m := scrubDone.FindStringSubmatch(s.scan); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 32); err == nil {
				e := int32(n)
				p.ScrubErrors = &e
			}
			p.LastScrub = clipRunes(strings.TrimSpace(m[2]), 64)
		}
		p.Vdevs, p.VdevsOmitted = capList(s.vdevs, maxZVdevs)
	}
}

// ── zfs list -Hp ───────────────────────────────────────────────────────────

func parseZfsList(out string) []framev1beta1.ZFSDataset {
	var ds []framev1beta1.ZFSDataset
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		mp := f[4]
		if mp == "-" || mp == "none" {
			mp = ""
		}
		ds = append(ds, framev1beta1.ZFSDataset{
			Name:       clipRunes(f[0], 256),
			UsedBytes:  parseInt64(f[1]),
			AvailBytes: parseInt64(f[2]),
			ReferBytes: parseInt64(f[3]),
			MountPoint: clipRunes(mp, 512),
			Type:       clipRunes(f[5], 16),
		})
	}
	return ds
}

// ── /proc/mdstat ───────────────────────────────────────────────────────────

var (
	mdHeader  = regexp.MustCompile(`^(md[\w/]+)\s*:\s*(.*)$`)
	mdMember  = regexp.MustCompile(`^([\w./-]+)\[\d+\]((?:\([A-Z]\))*)$`)
	mdCounts  = regexp.MustCompile(`\[(\d+)/(\d+)\]\s*\[([U_]+)\]`)
	mdSync    = regexp.MustCompile(`(resync|recovery|check|reshape|repair)\s*=\s*([\d.]+%)`)
	mdPending = regexp.MustCompile(`(resync|recovery|check|reshape)\s*=\s*(DELAYED|PENDING)`)
)

// parseMdstat reads /proc/mdstat. "Personalities" and "unused devices" are
// not arrays; each "mdN : state [level] members…" line starts one, and the
// indented lines after it carry its counts and sync progress.
func parseMdstat(data string) []framev1beta1.MDArray {
	var arrays []framev1beta1.MDArray
	var cur *framev1beta1.MDArray
	for _, line := range strings.Split(data, "\n") {
		if m := mdHeader.FindStringSubmatch(line); m != nil {
			arrays = append(arrays, parseMdHeader(m[1], m[2]))
			cur = &arrays[len(arrays)-1]
			continue
		}
		if cur == nil || !strings.HasPrefix(line, " ") {
			if strings.TrimSpace(line) == "" {
				cur = nil
			}
			continue
		}
		if m := mdCounts.FindStringSubmatch(line); m != nil {
			raid, _ := strconv.Atoi(m[1])
			active, _ := strconv.Atoi(m[2])
			cur.RaidDisks, cur.ActiveDisks = int32(raid), int32(active)
			cur.Slots = clipRunes(m[3], 64)
		}
		if m := mdSync.FindStringSubmatch(line); m != nil {
			cur.Sync = clipRunes(m[1]+" "+m[2], 64)
		} else if m := mdPending.FindStringSubmatch(line); m != nil {
			cur.Sync = clipRunes(m[1]+" "+m[2], 64)
		}
	}
	for i := range arrays {
		a := &arrays[i]
		failed := false
		for _, mem := range a.Members {
			failed = failed || mem.Failed
		}
		a.Degraded = failed ||
			strings.HasPrefix(a.State, "inactive") ||
			(a.RaidDisks > 0 && a.ActiveDisks < a.RaidDisks) ||
			strings.Contains(a.Slots, "_")
	}
	return arrays
}

// parseMdHeader reads "active raid1 sdd2[0] sdc2[1](F)" or
// "active (auto-read-only) raid1 …" or "inactive sdb[0](S)".
func parseMdHeader(name, rest string) framev1beta1.MDArray {
	a := framev1beta1.MDArray{Name: clipRunes(name, 32)}
	f := strings.Fields(rest)
	i := 0
	if i < len(f) {
		a.State = f[i]
		i++
	}
	for i < len(f) && strings.HasPrefix(f[i], "(") {
		a.State += " " + f[i]
		i++
	}
	a.State = clipRunes(a.State, 32)
	if i < len(f) && !strings.Contains(f[i], "[") {
		a.Level = clipRunes(f[i], 16)
		i++
	}
	for ; i < len(f); i++ {
		m := mdMember.FindStringSubmatch(f[i])
		if m == nil {
			continue
		}
		if len(a.Members) == maxMDMembers {
			a.MembersOmitted++
			continue
		}
		a.Members = append(a.Members, framev1beta1.MDMember{
			Device: clipRunes(m[1], 64),
			Failed: strings.Contains(m[2], "(F)"),
			Spare:  strings.Contains(m[2], "(S)"),
		})
	}
	return a
}

// ── LVM JSON reports ───────────────────────────────────────────────────────

type lvmReport struct {
	Report []map[string][]map[string]string `json:"report"`
}

func lvmRows(out, key string) ([]map[string]string, error) {
	var r lvmReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, fmt.Errorf("parsing %s report: %w", key, err)
	}
	var rows []map[string]string
	for _, rep := range r.Report {
		rows = append(rows, rep[key]...)
	}
	return rows, nil
}

func parseLVM(pvOut, vgOut, lvOut string) ([]framev1beta1.LVMPhysicalVolume, []framev1beta1.LVMVolumeGroup, []framev1beta1.LVMLogicalVolume, error) {
	pvRows, err := lvmRows(pvOut, "pv")
	if err != nil {
		return nil, nil, nil, err
	}
	vgRows, err := lvmRows(vgOut, "vg")
	if err != nil {
		return nil, nil, nil, err
	}
	lvRows, err := lvmRows(lvOut, "lv")
	if err != nil {
		return nil, nil, nil, err
	}
	var pvs []framev1beta1.LVMPhysicalVolume
	for _, r := range pvRows {
		pvs = append(pvs, framev1beta1.LVMPhysicalVolume{
			Name:      clipRunes(r["pv_name"], 512),
			VG:        clipRunes(r["vg_name"], 128),
			SizeBytes: parseInt64(r["pv_size"]),
			FreeBytes: parseInt64(r["pv_free"]),
			Attr:      clipRunes(r["pv_attr"], 16),
		})
	}
	var vgs []framev1beta1.LVMVolumeGroup
	for _, r := range vgRows {
		vgs = append(vgs, framev1beta1.LVMVolumeGroup{
			Name:      clipRunes(r["vg_name"], 128),
			SizeBytes: parseInt64(r["vg_size"]),
			FreeBytes: parseInt64(r["vg_free"]),
			PVCount:   int32(parseInt64(r["pv_count"])),
			LVCount:   int32(parseInt64(r["lv_count"])),
			Attr:      clipRunes(r["vg_attr"], 16),
		})
	}
	var lvs []framev1beta1.LVMLogicalVolume
	for _, r := range lvRows {
		lvs = append(lvs, framev1beta1.LVMLogicalVolume{
			Name:        clipRunes(r["lv_name"], 128),
			VG:          clipRunes(r["vg_name"], 128),
			SizeBytes:   parseInt64(r["lv_size"]),
			Attr:        clipRunes(r["lv_attr"], 16),
			SegType:     clipRunes(r["segtype"], 32),
			DataPercent: clipRunes(r["data_percent"], 16),
		})
	}
	return pvs, vgs, lvs, nil
}
