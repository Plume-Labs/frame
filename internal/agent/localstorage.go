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
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Reading the node's local storage stacks — ZFS, mdadm, LVM — read-only.
//
// What this code may run is fixed at compile time: storageCommands is the
// complete list of argv, each a listing or status verb, plus one probe
// (timeoutProbe); runStorage refuses anything else. Nothing here takes an
// argument from the cluster or the CR; a pool or VG name never reaches an
// argv. localstorage_test.go fails if an entry is added to the list without
// being added to its copy there, and if any entry carries a verb that writes.
//
// Privileges: none beyond what the agent already has (privileged, hostPID,
// the host's / at /host — deploy/kubernetes/base/node-tuning-agent/
// daemonset.yaml). mdadm is read from /proc/mdstat, which is not
// namespaced: no binary, no device node, no nsenter. ZFS is detected from
// /sys/module/zfs before anything runs. zpool, zfs and the LVM tools run
// through nsenter into PID 1's mount namespace, so they are the HOST's
// binaries, matched to the host's kernel module — a userland shipped in
// this image could disagree with the module and print wrong state, which
// is why the image carries none.
//
// A command can hang — `zpool status` on a suspended pool blocks in the
// kernel, where no signal reaches it. Four guards keep that from costing
// more than the stack it is about (storagerunner.go):
//
//  1. the host's `timeout --foreground -k 2 10` sends TERM at 10 s and KILL
//     2 s later, to the command itself;
//  2. the agent kills the whole process group at its hard deadline if the
//     host side has not ended, and again unconditionally once the command
//     returns (a grandchild that ignored TERM does not outlive it), and
//     cmd.WaitDelay stops it waiting on a pipe a surviving grandchild holds;
//  3. a command whose previous run is still alive on the host (a process
//     in D state survives SIGKILL) is not started again: the stack is
//     published "busy" with its last complete read;
//  4. the collection runs in the background (LocalCollector); the agent's
//     tick waits for it a bounded time and otherwise publishes the last
//     result.

// storageTimeoutSeconds and storageKillAfterSeconds are the host-side
// `timeout -k` values.
const (
	storageTimeoutSeconds   = 10
	storageKillAfterSeconds = 2
)

// timeoutPrefix is prepended to every storage command: the host's GNU
// timeout, in the foreground (so the command stays in the agent's process
// group, where the busy check can see it), killing after the grace period.
var timeoutPrefix = []string{"timeout", "--foreground", "-k", fmt.Sprint(storageKillAfterSeconds), fmt.Sprint(storageTimeoutSeconds)}

// storageCommands is every argv the local-storage reader may run. Read-only
// verbs only: `zpool list`, `zpool status`, `zfs list`, `pvs`, `vgs`, `lvs`.
var storageCommands = [][]string{
	{"zpool", "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,health"},
	{"zpool", "status", "-P"},
	{"zfs", "list", "-Hp", "-t", "filesystem,volume", "-o", "name,used,avail,refer,mountpoint,type"},
	{"pvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "pv_name,vg_name,pv_size,pv_free,pv_attr"},
	{"vgs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "vg_name,vg_size,vg_free,pv_count,lv_count,vg_attr"},
	{"lvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "lv_name,vg_name,lv_size,lv_attr,segtype,data_percent"},
	timeoutProbe,
}

// timeoutProbe tells "the host has no `timeout`" from "the host has no
// zpool": both surface as exit 127 through `timeout`. It is on the list like
// any other command and runs the same way — `timeout … timeout --version`
// still answers 127 when timeout itself is missing — only after a 127.
var timeoutProbe = []string{"timeout", "--version"}

var (
	// errToolAbsent: the host has no such binary — the stack is not
	// installed, a state ("absent"), not a failure.
	errToolAbsent = errors.New("tool not installed on the node")
	// errTimeoutAbsent: the host has no coreutils `timeout`; nothing can be
	// run safely, and it is said as such rather than as "no ZFS".
	errTimeoutAbsent = errors.New("coreutils `timeout` not found on the node: storage commands are not run without it")
	// errBusy: the previous run of this command is still alive on the host.
	errBusy = errors.New("previous run still alive on the node")
)

// StorageExecutor runs one command and returns stdout and stderr apart —
// the LVM reports are JSON on stdout, and a warning on stderr must not
// break the parse.
type StorageExecutor interface {
	Exec(argv []string) (stdout, stderr string, err error)
}

// execListed runs argv — which must be, element for element, one of
// storageCommands — under the host's timeout. The only way anything here
// reaches the executor.
func execListed(x StorageExecutor, argv []string) (string, string, error) {
	if !slices.ContainsFunc(storageCommands, func(c []string) bool { return slices.Equal(c, argv) }) {
		return "", "", fmt.Errorf("refusing %q: not on the local-storage command list", strings.Join(argv, " "))
	}
	return x.Exec(append(slices.Clone(timeoutPrefix), argv...))
}

// runStorage runs a listed command and turns its failure into what the
// stack reports: absent tool, missing timeout, timed out, busy, or error.
func runStorage(x StorageExecutor, argv []string) (string, error) {
	stdout, stderr, err := execListed(x, argv)
	if err == nil {
		return stdout, nil
	}
	if errors.Is(err, errBusy) {
		return "", errBusy
	}
	switch exitCode(err) {
	case 127: // timeout could not find the command — or timeout itself is missing
		if _, _, perr := execListed(x, timeoutProbe); exitCode(perr) == 127 {
			return "", errTimeoutAbsent
		}
		return "", errToolAbsent
	case 124, 137: // TERM at the deadline, or KILL after the grace period
		return "", fmt.Errorf("%s timed out after %ds", strings.Join(argv[:2], " "), storageTimeoutSeconds)
	}
	return "", fmt.Errorf("%s: %w: %s", strings.Join(argv[:2], " "), err, firstLine(stderr))
}

func exitCode(err error) int {
	var e interface{ ExitCode() int }
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return -1
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clipRunes(s, 200)
}

// LocalSources is where a collection reads from; tests substitute fixtures,
// production uses the real /proc and /sys and HostStorageExecutor.
type LocalSources struct {
	Exec      StorageExecutor
	ReadFile  func(string) ([]byte, error)
	PathExist func(string) bool
	Now       func() time.Time
}

// DefaultLocalSources reads the node.
func DefaultLocalSources() LocalSources {
	return LocalSources{
		Exec:     NewHostStorageExecutor(),
		ReadFile: os.ReadFile,
		PathExist: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		Now: time.Now,
	}
}

// lvmInterval spaces the LVM reads: pvs/vgs/lvs scan every block device,
// which is not free every 30 s on a node with many disks. ZFS and md are
// read every cycle.
const lvmInterval = 5 * time.Minute

// ObserveLocalStorage reads the three stacks once. It never fails as a
// whole: a stack that cannot be read is reported with state "error" and its
// reason, so one broken tool cannot hide the other two, nor the disks.
// prev is the last result (nil at first): a busy stack keeps its lists, and
// LVM is reused until lvmInterval has passed. serialOf maps kernel names
// (sdc, sdc2) to disk serials — see ObserveDisksIndexed.
func ObserveLocalStorage(src LocalSources, prev *framev1beta1.LocalStorage, serialOf map[string]string) *framev1beta1.LocalStorage {
	now := metav1.NewTime(src.Now())
	out := &framev1beta1.LocalStorage{
		ZFS: observeZFS(src, now),
		MD:  observeMD(src, serialOf, now),
	}
	if prev != nil && prev.LVM.ReadAt != nil && src.Now().Sub(prev.LVM.ReadAt.Time) < lvmInterval && prev.LVM.State != framev1beta1.StackBusy {
		out.LVM = *prev.LVM.DeepCopy()
	} else {
		out.LVM = observeLVM(src, serialOf, now)
	}
	if prev != nil {
		if out.ZFS.State == framev1beta1.StackBusy {
			keepZFS(&out.ZFS, prev.ZFS)
		}
		if out.LVM.State == framev1beta1.StackBusy {
			keepLVM(&out.LVM, prev.LVM)
		}
	}
	return out
}

func keepZFS(cur *framev1beta1.ZFSStatus, prev framev1beta1.ZFSStatus) {
	p := prev.DeepCopy()
	cur.Pools, cur.PoolsOmitted, cur.Datasets, cur.DatasetsOmitted, cur.ReadAt = p.Pools, p.PoolsOmitted, p.Datasets, p.DatasetsOmitted, p.ReadAt
}

func keepLVM(cur *framev1beta1.LVMStatus, prev framev1beta1.LVMStatus) {
	p := prev.DeepCopy()
	cur.PVs, cur.VGs, cur.LVs, cur.Omitted, cur.ReadAt = p.PVs, p.VGs, p.LVs, p.Omitted, p.ReadAt
}

func errorText(err error) string {
	return clipRunes(err.Error(), 256)
}

func stackFailure(err error) (framev1beta1.StackState, string) {
	if errors.Is(err, errBusy) {
		return framev1beta1.StackBusy, errorText(err)
	}
	return framev1beta1.StackError, errorText(err)
}

func observeZFS(src LocalSources, now metav1.Time) framev1beta1.ZFSStatus {
	// No module, no pool: nothing is run on a node without ZFS.
	if !src.PathExist("/sys/module/zfs") {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent}
	}
	var outs [3]string
	for i := range outs {
		out, err := runStorage(src.Exec, storageCommands[i])
		if errors.Is(err, errToolAbsent) {
			return framev1beta1.ZFSStatus{State: framev1beta1.StackError, Error: "zfs module loaded but no " + storageCommands[i][0] + " binary on the node"}
		}
		if err != nil {
			st, msg := stackFailure(err)
			return framev1beta1.ZFSStatus{State: st, Error: msg}
		}
		outs[i] = out
	}
	out := framev1beta1.ZFSStatus{State: framev1beta1.StackPresent, ReadAt: &now}
	pools := parseZpoolList(outs[0])
	mergeZpoolStatus(pools, parseZpoolStatus(outs[1]))
	out.Pools, out.PoolsOmitted = capList(pools, maxZPools)
	out.Datasets, out.DatasetsOmitted = capList(parseZfsList(outs[2]), maxZFSDatasets)
	return out
}

func observeMD(src LocalSources, serialOf map[string]string, now metav1.Time) framev1beta1.MDStatus {
	data, err := src.ReadFile("/proc/mdstat")
	if errors.Is(err, os.ErrNotExist) {
		// md_mod not loaded.
		return framev1beta1.MDStatus{State: framev1beta1.StackAbsent}
	}
	if err != nil {
		return framev1beta1.MDStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	arrays := parseMdstat(string(data))
	if len(arrays) == 0 {
		return framev1beta1.MDStatus{State: framev1beta1.StackAbsent}
	}
	for i := range arrays {
		for j := range arrays[i].Members {
			arrays[i].Members[j].Serial = clipRunes(serialOf[arrays[i].Members[j].Device], 128)
		}
	}
	out := framev1beta1.MDStatus{State: framev1beta1.StackPresent, ReadAt: &now}
	out.Arrays, out.ArraysOmitted = capList(arrays, maxMDArrays)
	return out
}

func observeLVM(src LocalSources, serialOf map[string]string, now metav1.Time) framev1beta1.LVMStatus {
	var outs [3]string
	for i := range outs {
		out, err := runStorage(src.Exec, storageCommands[3+i])
		if errors.Is(err, errToolAbsent) {
			return framev1beta1.LVMStatus{State: framev1beta1.StackAbsent, ReadAt: &now}
		}
		if err != nil {
			st, msg := stackFailure(err)
			return framev1beta1.LVMStatus{State: st, Error: msg}
		}
		outs[i] = out
	}
	pvs, vgs, lvs, err := parseLVM(outs[0], outs[1], outs[2])
	if err != nil {
		return framev1beta1.LVMStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	// Installed tools, no PV: LVM is not in use here.
	if len(pvs) == 0 && len(vgs) == 0 {
		return framev1beta1.LVMStatus{State: framev1beta1.StackAbsent, ReadAt: &now}
	}
	for i := range pvs {
		pvs[i].Serial = clipRunes(serialOf[strings.TrimPrefix(pvs[i].Name, "/dev/")], 128)
	}
	out := framev1beta1.LVMStatus{State: framev1beta1.StackPresent, ReadAt: &now}
	var o1, o2, o3 int32
	out.PVs, o1 = capList(pvs, maxLVMPVs)
	out.VGs, o2 = capList(vgs, maxLVMVGs)
	out.LVs, o3 = capList(lvs, maxLVMLVs)
	out.Omitted = o1 + o2 + o3
	return out
}

// Bounds mirrored from api/frame/v1beta1/localstorage_types.go.
const (
	maxZPools      = 16
	maxZFSDatasets = 64
	maxZVdevs      = 64
	maxMDArrays    = 16
	maxMDMembers   = 32
	maxLVMPVs      = 64
	maxLVMVGs      = 32
	maxLVMLVs      = 128
)

// capList keeps the first n items and says how many it left out.
func capList[T any](xs []T, n int) ([]T, int32) {
	if len(xs) <= n {
		return xs, 0
	}
	return xs[:n], int32(len(xs) - n)
}

// LocalCollector runs the collection in the background so that a hung
// command never holds the agent's tick. Latest starts a collection if none
// is running, waits for it at most `wait`, and returns the newest complete
// result — possibly the previous cycle's, which its ReadAt dates.
type LocalCollector struct {
	src LocalSources

	mu      sync.Mutex
	running chan struct{} // closed when the current collection ends; nil when idle
	last    *framev1beta1.LocalStorage
}

// NewLocalCollector returns a collector reading from src.
func NewLocalCollector(src LocalSources) *LocalCollector {
	return &LocalCollector{src: src}
}

// Latest returns the newest result, waiting at most wait for a collection
// started now (or already running) to finish. nil only before the first
// collection has ever completed.
func (c *LocalCollector) Latest(serialOf map[string]string, wait time.Duration) *framev1beta1.LocalStorage {
	c.mu.Lock()
	done := c.running
	if done == nil {
		done = make(chan struct{})
		c.running = done
		prev := c.last
		go func() {
			res := ObserveLocalStorage(c.src, prev, serialOf)
			c.mu.Lock()
			c.last = res
			c.running = nil
			c.mu.Unlock()
			close(done)
		}()
	}
	c.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil {
		return nil
	}
	return c.last.DeepCopy()
}
