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

package storage

import (
	"fmt"
	"slices"
	"strings"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// What a machine's storage reading says, as findings the machine controller
// turns into conditions and FrameAlerts. Pure: what is open comes in as an
// argument, and nothing here reaches a client.
//
// The rule the whole file serves: only a complete read may change anything.
// A stack in error or busy is Unread, its lists are not looked at, and so it
// neither raises a finding nor clears one; the same goes for an agent that has
// stopped reporting, and for a BMC inventory the last probe did not refresh.
//
// And a read that says nothing about a device clears nothing. A pool a reboot
// failed to import reads the same as a pool destroyed on purpose, a disk the
// BMC failed to return the same as one pulled: the alert stays until the
// device reads healthy, or until someone deletes the FrameAlert.

// The four conditions, which are also the alert names.
const (
	CheckDegraded     = "StorageDegraded"
	CheckScrubErrors  = "ScrubErrors"
	CheckCapacityHigh = "StorageCapacityHigh"
	CheckWearHigh     = "DiskWearHigh"
)

// Checks lists them in the order the conditions are written.
var Checks = []string{CheckDegraded, CheckScrubErrors, CheckCapacityHigh, CheckWearHigh}

// Scope is the reading a check rests on.
type Scope string

const (
	ScopeMD     Scope = "md"
	ScopeZFS    Scope = "zfs"
	ScopeDrives Scope = "drives"
	ScopeSMART  Scope = "smart"
)

// checkScopes says which readings each check needs.
var checkScopes = map[string][]Scope{
	CheckDegraded:     {ScopeMD, ScopeZFS, ScopeSMART},
	CheckScrubErrors:  {ScopeZFS},
	CheckCapacityHigh: {ScopeZFS},
	CheckWearHigh:     {ScopeDrives, ScopeSMART},
}

const (
	severityCritical = "critical"
	severityWarning  = "warning"

	// capacityHysteresis is how far under the threshold an open capacity
	// alert must fall to clear.
	capacityHysteresis = 2

	// MaxInventoryDrives mirrors the MaxItems marker on
	// MachineInventory.Drives: a list that long may have been cut.
	MaxInventoryDrives = 32

	// AgentSilentAfter is how long a report may age before the node's stacks
	// count as unread. The agent reports every 30 s; this leaves a node room
	// to restart its pod.
	AgentSilentAfter = 10 * time.Minute
)

// Key names one alert on one machine.
type Key struct {
	Check  string
	Device string
}

// Open is what already fires for a machine, and the scope each alert rests on.
type Open map[Key]Scope

func (o Open) has(k Key) bool {
	_, ok := o[k]
	return ok
}

// Finding is one thing wrong now. Device is what the alert is about: an md
// array, a pool, a drive's serial.
type Finding struct {
	Key
	Scope    Scope
	Severity string
	Message  string
}

// Assessment is the outcome of one pass.
type Assessment struct {
	Findings []Finding
	// Unread maps each scope this pass could not read to why.
	Unread map[Scope]string
	// Cut maps each scope read only in part to why: what was read counts,
	// but no one can say False about the rest.
	Cut map[Scope]string
	// Held lists open alerts this reading can say nothing about, although
	// their scope was read: a pool mid-scrub has no error count, a device
	// missing from the list may be cut, unimported or simply not returned.
	Held map[Key]bool
	// WearUnreported counts the SSDs with no endurance figure, from their BMC
	// or from themselves.
	WearUnreported int
	// SMARTFailed counts the disks whose last SMART read failed: nothing on
	// their health, though their wear may come from the BMC.
	SMARTFailed int
}

// Thresholds are StorageHealthPolicySpec with its zero values resolved.
type Thresholds struct {
	ZFSCapacityPercent int32
	SSDWearPercent     int32
}

// ThresholdsFrom resolves a policy; nil means the defaults, and so does a
// zero, which the CRD's minimum forbids but a hand-built object may carry.
func ThresholdsFrom(p *framev1beta1.StorageHealthPolicy) Thresholds {
	t := Thresholds{framev1beta1.DefaultZFSCapacityPercent, framev1beta1.DefaultSSDWearPercent}
	if p != nil && p.Spec.ZFSCapacityPercent > 0 {
		t.ZFSCapacityPercent = p.Spec.ZFSCapacityPercent
	}
	if p != nil && p.Spec.SSDWearPercent > 0 {
		t.SSDWearPercent = p.Spec.SSDWearPercent
	}
	return t
}

// Assess reads a machine's status. inventoryFresh says the BMC answered this
// pass; open is what already fires for this machine.
func Assess(st *framev1beta1.FrameMachineStatus, t Thresholds, inventoryFresh bool, open Open, now time.Time) Assessment {
	a := Assessment{Unread: map[Scope]string{}, Cut: map[Scope]string{}, Held: map[Key]bool{}}
	a.assessLocal(st.Storage, t, open, now)
	a.assessDrives(st.Inventory, st.Storage, t, inventoryFresh, open)
	a.assessSMART(st.Storage, st.Inventory, t, open)
	return a
}

func (a *Assessment) add(check, device string, scope Scope, severity, message string) {
	a.Findings = append(a.Findings, Finding{Key{check, device}, scope, severity, message})
}

func (a *Assessment) assessLocal(s *framev1beta1.MachineStorage, t Thresholds, open Open, now time.Time) {
	switch {
	case s == nil || s.Local == nil:
		why := "the agent has not reported the node's stacks"
		a.Unread[ScopeMD], a.Unread[ScopeZFS], a.Unread[ScopeSMART] = why, why, why
		return
	case s.ObservedAt == nil || now.Sub(s.ObservedAt.Time) > AgentSilentAfter:
		why := "the agent has stopped reporting"
		a.Unread[ScopeMD], a.Unread[ScopeZFS], a.Unread[ScopeSMART] = why, why, why
		return
	}
	a.assessMD(&s.Local.MD, open)
	a.assessZFS(&s.Local.ZFS, t, open)
}

// stackUnread says why a stack's lists are not to be looked at, or "".
func stackUnread(state framev1beta1.StackState, errText string) string {
	switch state {
	case framev1beta1.StackPresent, framev1beta1.StackAbsent:
		return ""
	case framev1beta1.StackError:
		return "read failed: " + errText
	case framev1beta1.StackBusy:
		return "previous read still running on the node"
	}
	return fmt.Sprintf("unknown stack state %q", state)
}

func (a *Assessment) assessMD(md *framev1beta1.MDStatus, open Open) {
	if why := stackUnread(md.State, md.Error); why != "" {
		a.Unread[ScopeMD] = why
		return
	}
	listed := make([]string, 0, len(md.Arrays))
	for i := range md.Arrays {
		arr := &md.Arrays[i]
		listed = append(listed, arr.Name)
		if arr.Degraded {
			a.add(CheckDegraded, arr.Name, ScopeMD, severityCritical, mdMessage(arr))
		}
	}
	a.holdUnlisted(open, ScopeMD, listed)
	if md.ArraysOmitted > 0 {
		a.Cut[ScopeMD] = fmt.Sprintf("%d array(s) past the agent's list limit", md.ArraysOmitted)
	}
}

// mdMessage leaves the resync progress out: it moves every minute, and a
// message that moves is a write per pass on the alert. The console reads it
// from status.
func mdMessage(arr *framev1beta1.MDArray) string {
	var failed []string
	for _, m := range arr.Members {
		if m.Failed {
			failed = append(failed, m.Device+serialNote(m.Serial))
		}
	}
	msg := fmt.Sprintf("md array %s (%s) is degraded: state %s, %d of %d disks active, slots [%s]",
		arr.Name, arr.Level, arr.State, arr.ActiveDisks, arr.RaidDisks, arr.Slots)
	if len(failed) > 0 {
		msg += "; failed: " + strings.Join(failed, ", ")
	}
	return msg
}

func serialNote(serial string) string {
	if serial == "" {
		return ""
	}
	return " (serial " + serial + ")"
}

func (a *Assessment) assessZFS(z *framev1beta1.ZFSStatus, t Thresholds, open Open) {
	if why := stackUnread(z.State, z.Error); why != "" {
		a.Unread[ScopeZFS] = why
		return
	}
	listed := make([]string, 0, len(z.Pools))
	for i := range z.Pools {
		p := &z.Pools[i]
		listed = append(listed, p.Name)
		a.assessPool(p, t, open)
	}
	a.holdUnlisted(open, ScopeZFS, listed)
	if z.PoolsOmitted > 0 {
		a.Cut[ScopeZFS] = fmt.Sprintf("%d pool(s) past the agent's list limit", z.PoolsOmitted)
	}
}

func (a *Assessment) assessPool(p *framev1beta1.ZPool, t Thresholds, open Open) {
	if msg := poolProblems(p); msg != "" {
		a.add(CheckDegraded, p.Name, ScopeZFS, severityCritical, msg)
	} else if p.VdevsOmitted > 0 || p.Health == "" {
		a.Cut[ScopeZFS] = fmt.Sprintf("pool %s was not read in full", p.Name)
		if key := (Key{CheckDegraded, p.Name}); open.has(key) {
			a.Held[key] = true
		}
	}

	switch key := (Key{CheckScrubErrors, p.Name}); {
	case p.ScrubErrors != nil && *p.ScrubErrors > 0:
		a.add(CheckScrubErrors, p.Name, ScopeZFS, severityCritical,
			fmt.Sprintf("pool %s: last scrub found %d errors (%s)", p.Name, *p.ScrubErrors, p.LastScrub))
	case p.ScrubErrors == nil && open.has(key) && p.Scan != "none requested":
		// The count exists only for a completed scrub: a new one running,
		// or cancelled, says nothing about the errors the last one found.
		a.Held[key] = true
	}

	key := Key{CheckCapacityHigh, p.Name}
	if p.CapacityPercent == nil {
		// zpool prints "-" for a pool it cannot size, which is when it matters.
		if open.has(key) {
			a.Held[key] = true
		}
		return
	}
	limit := t.ZFSCapacityPercent
	if open.has(key) {
		limit -= capacityHysteresis
	}
	if *p.CapacityPercent >= limit {
		a.add(CheckCapacityHigh, p.Name, ScopeZFS, severityWarning,
			fmt.Sprintf("pool %s is over the capacity threshold of %d %%", p.Name, t.ZFSCapacityPercent))
	}
}

// poolProblems is "" for a healthy pool. A pool with no health column says
// nothing either way, and is not reported as unhealthy.
func poolProblems(p *framev1beta1.ZPool) string {
	var bad []string
	for i := range p.Vdevs {
		v := &p.Vdevs[i]
		switch {
		case slices.Contains([]string{"DEGRADED", "FAULTED", "OFFLINE", "UNAVAIL", "REMOVED"}, v.State):
			bad = append(bad, fmt.Sprintf("%s (%s)", v.Name, v.State))
		case v.ReadErrors+v.WriteErrors+v.ChecksumErrors > 0:
			bad = append(bad, v.Name+" (errors)")
		}
	}
	if (p.Health == "" || p.Health == "ONLINE") && len(bad) == 0 {
		return ""
	}
	msg := fmt.Sprintf("pool %s is %s", p.Name, p.Health)
	if len(bad) > 0 {
		msg += ": " + strings.Join(bad, ", ")
	}
	return msg
}

// holdUnlisted keeps the open alerts of a scope whose device the read did not
// list: it says nothing about them.
func (a *Assessment) holdUnlisted(open Open, scope Scope, listed []string) {
	for k, s := range open {
		if s == scope && !slices.Contains(listed, k.Device) {
			a.Held[k] = true
		}
	}
}

func (a *Assessment) assessDrives(inv *framev1beta1.MachineInventory, s *framev1beta1.MachineStorage, t Thresholds, fresh bool, open Open) {
	switch {
	case !fresh:
		a.Unread[ScopeDrives] = "the BMC did not answer the last probe"
		return
	case inv == nil || len(inv.Drives) == 0:
		a.Unread[ScopeDrives] = "the BMC lists no drives"
		return
	}
	listed := make([]string, 0, len(inv.Drives))
	for i := range inv.Drives {
		d := &inv.Drives[i]
		device := d.SerialNumber
		if device == "" {
			device = d.Location
		}
		listed = append(listed, device)
		switch used := d.SSDEnduranceUsedPercent; {
		case used != nil && *used >= t.SSDWearPercent:
			a.add(CheckWearHigh, device, ScopeDrives, severityWarning,
				fmt.Sprintf("drive %s (bay %s, %s) has used %d %% of its endurance", device, d.Location, d.Model, *used))
		case used == nil:
			// A figure from the disk settles it, in assessSMART, either way.
			reported := a.smartReportsWear(s, d.SerialNumber)
			if key := (Key{CheckWearHigh, device}); open.has(key) && !reported {
				a.Held[key] = true
			}
			if d.MediaType == "SSD" && !reported {
				a.WearUnreported++
			}
		}
	}
	// One disk the BMC failed to return does not fail the probe, so a drive
	// missing here may be replaced or may be back next pass.
	a.holdUnlisted(open, ScopeDrives, listed)
	if len(inv.Drives) >= MaxInventoryDrives {
		a.Cut[ScopeDrives] = fmt.Sprintf("the inventory holds %d drives at most", MaxInventoryDrives)
	}
}

// smartReportsWear says the disk itself gives the figure its BMC does not, in a
// report still current.
func (a *Assessment) smartReportsWear(s *framev1beta1.MachineStorage, serial string) bool {
	if _, unread := a.Unread[ScopeSMART]; unread || s == nil {
		return false
	}
	for i := range s.Observed {
		d := &s.Observed[i]
		if d.SerialNumber == serial && d.SMART != nil && d.SMART.WearUsedPercent != nil {
			return true
		}
	}
	return false
}

func bmcDrive(inv *framev1beta1.MachineInventory, serial string) *framev1beta1.DriveInfo {
	if inv == nil {
		return nil
	}
	for i := range inv.Drives {
		if inv.Drives[i].SerialNumber == serial {
			return &inv.Drives[i]
		}
	}
	return nil
}

// assessSMART reads what the disks say of themselves. Wear only where the BMC
// gives none: one figure per disk, and the BMC's comes first.
func (a *Assessment) assessSMART(s *framev1beta1.MachineStorage, inv *framev1beta1.MachineInventory, t Thresholds, open Open) {
	if _, unread := a.Unread[ScopeSMART]; unread || s == nil {
		return
	}
	listed := make([]string, 0, len(s.Observed))
	for i := range s.Observed {
		d := &s.Observed[i]
		listed = append(listed, d.SerialNumber)
		wear, health := Key{CheckWearHigh, d.SerialNumber}, Key{CheckDegraded, d.SerialNumber}
		if d.SMART != nil && d.SMART.Error != "" {
			a.SMARTFailed++
		}
		// No read, a failed one, or a disk silent on this: nothing to clear on.
		if d.SMART == nil || d.SMART.Passed == nil {
			if open.has(health) {
				a.Held[health] = true
			}
		} else if !*d.SMART.Passed {
			a.add(CheckDegraded, d.SerialNumber, ScopeSMART, severityCritical,
				fmt.Sprintf("disk %s (%s, %s) reports a failing SMART self-assessment", d.SerialNumber, d.Path, d.Model))
		}
		bmc := bmcDrive(inv, d.SerialNumber)
		switch {
		case bmc != nil && bmc.SSDEnduranceUsedPercent != nil:
		case d.SMART == nil || d.SMART.WearUsedPercent == nil:
			if open.has(wear) {
				a.Held[wear] = true
			}
			// assessDrives has counted the SSDs it lists.
			if d.SMART != nil && (bmc == nil || bmc.MediaType != "SSD") {
				a.WearUnreported++
			}
		case *d.SMART.WearUsedPercent >= t.SSDWearPercent:
			a.add(CheckWearHigh, d.SerialNumber, ScopeSMART, severityWarning,
				fmt.Sprintf("disk %s (%s, %s) has used %d %% of its endurance", d.SerialNumber, d.Path, d.Model, *d.SMART.WearUsedPercent))
		}
	}
	a.holdUnlisted(open, ScopeSMART, listed)
}

// Verdict is one condition's value.
type Verdict struct {
	// Status is "True", "False" or "Unknown".
	Status  string
	Reason  string
	Message string
}

// maxConditionMessage keeps a machine with many bad vdevs well inside a
// condition message's limit.
const maxConditionMessage = 1024

// Keeps says an open alert must stay open although no finding backs it: this
// pass could not look at what it is about.
func (a Assessment) Keeps(k Key, scope Scope) bool {
	_, unread := a.Unread[scope]
	return unread || a.Held[k]
}

// Verdict gives check's condition. A check with an open alert it cannot
// re-read stays True; one that cannot be read and has nothing open is
// Unknown, because False would claim a read nobody made.
func (a Assessment) Verdict(check string, open Open) Verdict {
	var msgs []string
	for _, f := range a.Findings {
		if f.Check == check {
			msgs = append(msgs, f.Message)
		}
	}
	if len(msgs) > 0 {
		return Verdict{"True", "Detected", clipRunes(strings.Join(msgs, "; "), maxConditionMessage)}
	}
	for k, scope := range open {
		if k.Check == check && a.Keeps(k, scope) {
			return Verdict{"True", "LastReadHeld", "an open alert could not be re-read, it stays open"}
		}
	}
	var unread []string
	for _, s := range checkScopes[check] {
		if why, ok := a.Unread[s]; ok {
			unread = append(unread, fmt.Sprintf("%s: %s", s, why))
		} else if why, ok := a.Cut[s]; ok {
			unread = append(unread, fmt.Sprintf("%s: %s", s, why))
		}
	}
	if check == CheckDegraded && a.SMARTFailed > 0 {
		unread = append(unread, fmt.Sprintf("%s: %d disk(s) could not be read", ScopeSMART, a.SMARTFailed))
	}
	if len(unread) > 0 {
		return Verdict{"Unknown", "ReadUnavailable", clipRunes(strings.Join(unread, "; "), maxConditionMessage)}
	}
	if check == CheckWearHigh && a.WearUnreported > 0 {
		return Verdict{"Unknown", "WearNotReported", fmt.Sprintf("neither the BMC nor the disk gives an endurance figure for %d SSD(s)", a.WearUnreported)}
	}
	return Verdict{"False", "Healthy", ""}
}
