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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	framev1beta1 "github.com/gothshoot/frame/api/frame/v1beta1"
)

// Reading the node's local storage stacks — ZFS, mdadm, LVM — read-only.
//
// What this code may run is fixed at compile time: storageCommands is the
// complete list of argv, each a listing or status verb, and runStorage
// refuses anything else. Nothing here takes an argument from the cluster or
// the CR; a pool or VG name never reaches an argv. localstorage_test.go
// fails if an entry is added to the list without being added to its copy
// there, and if any entry carries a verb that writes.
//
// Privileges: none beyond what the agent already has (privileged, hostPID,
// the host's / at /host — deploy/kubernetes/base/node-tuning-agent/
// daemonset.yaml). mdadm is read from /proc/mdstat, which is not
// namespaced: no binary, no device node, no nsenter. ZFS is detected from
// /sys/module/zfs before anything runs. zpool, zfs and the LVM tools run
// through nsenter into PID 1's mount namespace, so they are the HOST's
// binaries, matched to the host's kernel module — a userland shipped in
// this image could disagree with the module and print wrong state, which
// is why the image carries none. They reach /dev/zfs and the device-mapper
// through the host's /dev, as the host's own tools would.
//
// Each command runs under the host's coreutils `timeout` (the command is
// killed on the host after storageCommandTimeout), and the whole nsenter
// under a Go context a little longer, so a hung pool (a suspended pool's
// zpool status can block) costs one bounded wait, not the agent's cycle.

// storageCommandTimeout bounds one command on the host.
const storageCommandTimeout = 10 * time.Second

// storageCommands is every argv the local-storage reader may run. Read-only
// verbs only: `zpool list`, `zpool status`, `zfs list`, `pvs`, `vgs`, `lvs`.
var storageCommands = [][]string{
	{"zpool", "list", "-Hp", "-o", "name,size,alloc,free,frag,cap,health"},
	{"zpool", "status", "-P"},
	{"zfs", "list", "-Hp", "-o", "name,used,avail,refer,mountpoint,type"},
	{"pvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "pv_name,vg_name,pv_size,pv_free,pv_attr"},
	{"vgs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "vg_name,vg_size,vg_free,pv_count,lv_count,vg_attr"},
	{"lvs", "--reportformat", "json", "--units", "b", "--nosuffix", "-o", "lv_name,vg_name,lv_size,lv_attr,segtype,data_percent"},
}

// errToolAbsent is returned when the host has no such binary: the stack is
// not installed, which is a state ("absent"), not a failure.
var errToolAbsent = errors.New("tool not installed on the node")

// runStorage runs argv — which must be, element for element, one of
// storageCommands — under the host's timeout.
func runStorage(runner CommandRunner, argv []string) (string, error) {
	if !slices.ContainsFunc(storageCommands, func(c []string) bool { return slices.Equal(c, argv) }) {
		return "", fmt.Errorf("refusing %q: not on the local-storage command list", strings.Join(argv, " "))
	}
	args := append([]string{fmt.Sprintf("%d", int(storageCommandTimeout/time.Second))}, argv...)
	out, err := runner.Run("timeout", args...)
	if err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 127: // timeout: the command was not found
				return "", errToolAbsent
			case 124:
				return "", fmt.Errorf("%s timed out after %s", argv[0], storageCommandTimeout)
			}
		}
		return "", fmt.Errorf("%s: %w: %s", strings.Join(argv[:2], " "), err, firstLine(out))
	}
	return out, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clipRunes(s, 200)
}

// ExecTimeoutRunner execs a command and kills it after Timeout — the Go-side
// backstop behind the host `timeout` (which would not fire if nsenter
// itself hung).
type ExecTimeoutRunner struct {
	Timeout time.Duration
}

// Run implements CommandRunner.
func (r ExecTimeoutRunner) Run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.Timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s: killed after %s", name, r.Timeout)
	}
	return string(out), err
}

// LocalSources is where ObserveLocalStorage reads from; tests substitute
// fixtures, production uses the real /proc and /sys.
type LocalSources struct {
	Runner    CommandRunner
	ReadFile  func(string) ([]byte, error)
	PathExist func(string) bool
}

// DefaultLocalSources reads the node: host binaries through nsenter with
// both timeouts, /proc/mdstat and /sys/module/zfs from the agent's own view
// (neither is namespaced).
func DefaultLocalSources() LocalSources {
	return LocalSources{
		Runner:   HostCommandRunner{Runner: ExecTimeoutRunner{Timeout: storageCommandTimeout + 5*time.Second}},
		ReadFile: os.ReadFile,
		PathExist: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
	}
}

// ObserveLocalStorage reads the three stacks. It never fails as a whole: a
// stack that cannot be read is reported with state "error" and its reason,
// so one broken tool cannot hide the other two, nor the disks.
// serialOf maps kernel names (sdc, sdc2) to disk serials — see
// ObserveDisksIndexed.
func ObserveLocalStorage(src LocalSources, serialOf map[string]string) *framev1beta1.LocalStorage {
	return &framev1beta1.LocalStorage{
		ZFS: observeZFS(src),
		MD:  observeMD(src, serialOf),
		LVM: observeLVM(src, serialOf),
	}
}

func errorText(err error) string {
	return clipRunes(err.Error(), 256)
}

func observeZFS(src LocalSources) framev1beta1.ZFSStatus {
	// No module, no pool: nothing is run on a node without ZFS.
	if !src.PathExist("/sys/module/zfs") {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackAbsent}
	}
	list, err := runStorage(src.Runner, storageCommands[0])
	if errors.Is(err, errToolAbsent) {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackError, Error: "zfs module loaded but no zpool binary on the node"}
	}
	if err != nil {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	status, err := runStorage(src.Runner, storageCommands[1])
	if err != nil {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	datasets, err := runStorage(src.Runner, storageCommands[2])
	if err != nil {
		return framev1beta1.ZFSStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	out := framev1beta1.ZFSStatus{State: framev1beta1.StackPresent}
	pools := parseZpoolList(list)
	mergeZpoolStatus(pools, parseZpoolStatus(status))
	out.Pools, out.PoolsOmitted = capList(pools, maxZPools)
	out.Datasets, out.DatasetsOmitted = capList(parseZfsList(datasets), maxZFSDatasets)
	return out
}

func observeMD(src LocalSources, serialOf map[string]string) framev1beta1.MDStatus {
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
	out := framev1beta1.MDStatus{State: framev1beta1.StackPresent}
	out.Arrays, out.ArraysOmitted = capList(arrays, maxMDArrays)
	return out
}

func observeLVM(src LocalSources, serialOf map[string]string) framev1beta1.LVMStatus {
	pvOut, err := runStorage(src.Runner, storageCommands[3])
	if errors.Is(err, errToolAbsent) {
		return framev1beta1.LVMStatus{State: framev1beta1.StackAbsent}
	}
	if err != nil {
		return framev1beta1.LVMStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	vgOut, err := runStorage(src.Runner, storageCommands[4])
	if err != nil {
		return framev1beta1.LVMStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	lvOut, err := runStorage(src.Runner, storageCommands[5])
	if err != nil {
		return framev1beta1.LVMStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	pvs, vgs, lvs, err := parseLVM(pvOut, vgOut, lvOut)
	if err != nil {
		return framev1beta1.LVMStatus{State: framev1beta1.StackError, Error: errorText(err)}
	}
	// Installed tools, no PV: LVM is not in use here.
	if len(pvs) == 0 && len(vgs) == 0 {
		return framev1beta1.LVMStatus{State: framev1beta1.StackAbsent}
	}
	for i := range pvs {
		pvs[i].Serial = clipRunes(serialOf[strings.TrimPrefix(pvs[i].Name, "/dev/")], 128)
	}
	out := framev1beta1.LVMStatus{State: framev1beta1.StackPresent}
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
