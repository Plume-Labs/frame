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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Wiping a new disk for an OSD replacement: the agent's one destructive
// storage act. Its boundary, each point with a test:
//
//   - the request (framev1beta1.DiskWipeRequest, on the Node) names a by-id
//     path and a serial; the agent acts only when its own lsblk of the same
//     tick reports exactly one whole disk at that path, with that serial,
//     and that disk is "free" (occupancyOf: no signature, no partition, no
//     mount, no md/zfs membership) and has no holder in /sys;
//   - the path must be a /dev/disk/by-id/wwn-… or nvme-… link (what the
//     agent itself publishes), and must resolve, in the agent's /dev (the
//     host's), to the kernel device lsblk reported;
//   - each request id is acted on at most once: the id is written to the
//     host before the first command runs, and a request whose id is already
//     there is answered "already acted" without touching the disk (Ran is
//     then true: the earlier attempt may have run, its answer lost);
//   - a request older than wipeRequestMaxAge, or dated in the future, is
//     refused;
//   - the commands are fixed: `blockdev --getsize64` (read-only), `wipefs
//     -a`, 100 MiB of zeros at the head with dd, 1 MiB of zeros at each of
//     1, 10, 100 and 1000 GiB (where the disk reaches them: BlueStore keeps
//     copies of its label there, and Rook reads any of them to recognise a
//     disk as an old OSD), 100 MiB at the tail, then `sgdisk --zap-all` when
//     the host has it; then one read of 4 KiB at 0, 1, 10, 100 and 1000 GiB
//     to check that no "bluestore block device" label is left. Each command
//     is on the by-id path (the identity checked above, not a kernel name
//     that can move), under the host's `timeout`, in PID 1's namespaces.
//
// Whatever happens, the answer is written to the Node
// (DiskWipeResultAnnotation) with Ran saying whether any command started.

// wipeRequestMaxAge: a request older than this is a leftover and is
// refused. With the two minutes of clock skew tolerated below and a 30 s
// tick on each side, it stays well short of the ten minutes the controller
// waits for an answer, so that a request the controller gave up on
// (paused, then maybe cancelled) can no longer be acted on.
const wipeRequestMaxAge = 5 * time.Minute

// wipeRecordPath is the host file holding every request id acted on.
const wipeRecordPath = "var/lib/frame-agent/disk-wipe-acted"

// wipeByIDPath is the shape of a path the agent will wipe.
var wipeByIDPath = regexp.MustCompile(`^/dev/disk/by-id/(wwn-0x[0-9a-fA-F]{8,64}|nvme-(eui|nvme)\.[0-9a-zA-Z._-]{4,96})$`)

// wipeRequestID is the shape of a request id (one line of the record).
var wipeRequestID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

const (
	// wipeEdgeMiB is what is zeroed at the head and at the tail of the disk.
	wipeEdgeMiB = 100
	// bluestoreMagic opens a BlueStore label; its copies sit at the offsets
	// below, so a disk that once was an OSD still reads as one (and Rook
	// re-adopts it) unless each of them is zeroed too.
	bluestoreMagic = "bluestore block device"
	mib            = int64(1) << 20
	gib            = int64(1) << 30
)

// wipeLabelOffsetsGiB are the offsets, beyond the head, where a BlueStore
// label copy can be: zeroed (1 MiB each) and then read back.
var wipeLabelOffsetsGiB = []int64{1, 10, 100, 1000}

// wipeSizeArgv reads the disk's size in bytes (read-only).
func wipeSizeArgv(byID string) []string { return []string{"blockdev", "--getsize64", byID} }

// labelOffsets are the offsets, in bytes, checked after the wipe: 0 and
// each copy the disk is big enough to hold.
func labelOffsets(size int64) []int64 {
	out := []int64{0}
	for _, g := range wipeLabelOffsetsGiB {
		if g*gib+mib <= size {
			out = append(out, g*gib)
		}
	}
	return out
}

// wipeArgv are the commands that change the disk, in order, for a disk of
// size bytes; byID is the by-id path.
func wipeArgv(byID string, size int64) [][]string {
	zero := func(count, seekMiB int64) []string {
		a := []string{"dd", "if=/dev/zero", "of=" + byID, "bs=1M", fmt.Sprintf("count=%d", count), "oflag=direct", "status=none"}
		if seekMiB > 0 {
			a = append(a, fmt.Sprintf("seek=%d", seekMiB))
		}
		return a
	}
	out := [][]string{
		{"wipefs", "-a", byID},
		zero(wipeEdgeMiB, 0),
	}
	for _, off := range labelOffsets(size)[1:] {
		out = append(out, zero(1, off/mib))
	}
	if size >= 2*wipeEdgeMiB*mib {
		out = append(out, zero(wipeEdgeMiB, size/mib-wipeEdgeMiB))
	}
	return append(out, []string{"sgdisk", "--zap-all", byID})
}

// wipeVerifyArgvs read 4 KiB at each label offset: stdout is checked for
// the BlueStore magic.
func wipeVerifyArgvs(byID string, size int64) [][]string {
	var out [][]string
	for _, off := range labelOffsets(size) {
		out = append(out, []string{"dd", "if=" + byID, "bs=4096", "count=1", fmt.Sprintf("skip=%d", off/4096), "iflag=direct", "status=none"})
	}
	return out
}

// WipeEnv is what ServiceWipeRequest reads from the node.
type WipeEnv struct {
	// Root is the host's root filesystem as mounted in the agent (/host).
	Root string
	// Resolve resolves a /dev path to its target (filepath.EvalSymlinks in
	// production: the agent mounts the host's /dev).
	Resolve func(string) (string, error)
	// Holders lists /sys/block/<name>/holders.
	Holders func(name string) ([]string, error)
	Now     func() time.Time
}

// DefaultWipeEnv is the production environment.
func DefaultWipeEnv(root string) WipeEnv {
	return WipeEnv{
		Root:    root,
		Resolve: filepath.EvalSymlinks,
		Holders: func(name string) ([]string, error) {
			ents, err := os.ReadDir(filepath.Join("/sys/block", name, "holders"))
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(ents))
			for _, e := range ents {
				out = append(out, e.Name())
			}
			return out, nil
		},
		Now: time.Now,
	}
}

// ServiceWipeRequest answers the Node's wipe request, if one is pending.
// disks is this tick's lsblk read.
func ServiceWipeRequest(ctx context.Context, c client.Client, nodeName string,
	disks []framev1beta1.ObservedDisk, x StorageExecutor, env WipeEnv) error {
	var node corev1.Node
	if err := c.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("reading node %s: %w", nodeName, err)
	}
	raw := node.Annotations[framev1beta1.DiskWipeRequestAnnotation]
	if raw == "" {
		return nil
	}
	var req framev1beta1.DiskWipeRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil || !wipeRequestID.MatchString(req.ID) {
		return fmt.Errorf("disk wipe request on %s is unreadable; ignored", nodeName)
	}
	if prev, ok := node.Annotations[framev1beta1.DiskWipeResultAnnotation]; ok {
		var res framev1beta1.DiskWipeResult
		if json.Unmarshal([]byte(prev), &res) == nil && res.ID == req.ID {
			return nil // answered
		}
	}
	res := wipe(req, disks, x, env)
	res.ID = req.ID
	res.At = metav1.NewTime(env.Now())
	out, _ := json.Marshal(res)
	// The answer carries the request's id, so it is written even when the
	// request was withdrawn or replaced meanwhile: it is the only record of
	// what ran, and the controller reads it by id. A wipe takes seconds and
	// the Node moves meanwhile (kubelet): a conflict is retried on a fresh
	// read, so that no other annotation is overwritten.
	var err error
	for range 5 {
		base := node.DeepCopy()
		if node.Annotations == nil {
			node.Annotations = map[string]string{}
		}
		node.Annotations[framev1beta1.DiskWipeResultAnnotation] = string(out)
		if err = c.Patch(ctx, &node, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); !apierrors.IsConflict(err) {
			break
		}
		if gerr := c.Get(ctx, client.ObjectKey{Name: nodeName}, &node); gerr != nil {
			err = gerr
			break
		}
	}
	if err != nil {
		return fmt.Errorf("answering the disk wipe request %s on %s (ran: %t, ok: %t): %w", req.ID, nodeName, res.Ran, res.OK, err)
	}
	return nil
}

func refused(format string, a ...any) framev1beta1.DiskWipeResult {
	return framev1beta1.DiskWipeResult{Message: "refused: " + fmt.Sprintf(format, a...)}
}

func wipe(req framev1beta1.DiskWipeRequest, disks []framev1beta1.ObservedDisk, x StorageExecutor, env WipeEnv) framev1beta1.DiskWipeResult {
	now := env.Now()
	if req.RequestedAt.IsZero() {
		return refused("request has no date")
	}
	if age := now.Sub(req.RequestedAt.Time); age > wipeRequestMaxAge || age < -2*time.Minute {
		return refused("request dated %s is outside the %s window", req.RequestedAt.UTC().Format(time.RFC3339), wipeRequestMaxAge)
	}
	if !wipeByIDPath.MatchString(req.ByIDPath) {
		return refused("%q is not a /dev/disk/by-id/wwn- or nvme- path", req.ByIDPath)
	}
	if strings.TrimSpace(req.Serial) == "" {
		return refused("request names no serial")
	}
	acted, err := actedIDs(env.Root)
	if err != nil {
		return refused("cannot read the record of past wipes: %v", err)
	}
	if slices.Contains(acted, req.ID) {
		return framev1beta1.DiskWipeResult{Ran: true, Message: "refused: request " + req.ID + " was already acted on by this node; its outcome is unknown, inspect the disk"}
	}

	var match []framev1beta1.ObservedDisk
	for _, d := range disks {
		if d.ByIDPath == req.ByIDPath {
			match = append(match, d)
		}
	}
	if len(match) != 1 {
		return refused("%d disks at %s in this read", len(match), req.ByIDPath)
	}
	d := match[0]
	if d.SerialNumber != req.Serial {
		return refused("the disk at %s has serial %q, not %q", req.ByIDPath, d.SerialNumber, req.Serial)
	}
	if d.Occupancy != OccupancyFree {
		return refused("the disk at %s is %s, not free", req.ByIDPath, d.Occupancy)
	}
	if d.MountPoint != "" || len(d.Members) > 0 || d.FSType != "" {
		return refused("the disk at %s is in use", req.ByIDPath)
	}
	target, err := env.Resolve(req.ByIDPath)
	if err != nil {
		return refused("%s does not resolve: %v", req.ByIDPath, err)
	}
	if target != d.Path {
		return refused("%s resolves to %s, lsblk reported %s", req.ByIDPath, target, d.Path)
	}
	name := strings.TrimPrefix(d.Path, "/dev/")
	if name == d.Path || name == "" || strings.Contains(name, "/") {
		return refused("unexpected kernel path %q", d.Path)
	}
	holders, err := env.Holders(name)
	if err != nil {
		return refused("cannot read the holders of %s: %v", name, err)
	}
	if len(holders) > 0 {
		return refused("%s is held by %v", name, holders)
	}

	// The size is read before anything changes: a failure here costs no id.
	stdout, serr, err := x.Exec(append(slices.Clone(timeoutPrefix), wipeSizeArgv(req.ByIDPath)...))
	if err != nil {
		return refused("cannot read the size of %s: %v %s", req.ByIDPath, err, strings.TrimSpace(serr))
	}
	size, err := strconv.ParseInt(strings.TrimSpace(stdout), 10, 64)
	if err != nil || size < mib {
		return refused("the size of %s reads as %q", req.ByIDPath, strings.TrimSpace(stdout))
	}

	// Write-ahead: no command runs unless the id is recorded first.
	if err := recordActed(env.Root, req.ID); err != nil {
		return refused("cannot record the request before acting: %v", err)
	}
	var notes []string
	for _, argv := range wipeArgv(req.ByIDPath, size) {
		_, stderr, err := x.Exec(append(slices.Clone(timeoutPrefix), argv...))
		if err == nil {
			continue
		}
		var ee interface{ ExitCode() int }
		if argv[0] == "sgdisk" && errors.As(err, &ee) && ee.ExitCode() == 127 {
			notes = append(notes, "sgdisk not installed on the node, skipped (wipefs -a already cleared both GPT headers)")
			continue
		}
		return framev1beta1.DiskWipeResult{Ran: true, Message: clipRunes(fmt.Sprintf("%s failed: %v %s", argv[0], err, strings.TrimSpace(stderr)), 500)}
	}
	// Read back: a BlueStore label left anywhere makes Rook take the disk
	// for the OSD it was (osd.6 came back from the dead that way).
	for i, argv := range wipeVerifyArgvs(req.ByIDPath, size) {
		out, stderr, err := x.Exec(append(slices.Clone(timeoutPrefix), argv...))
		if err != nil {
			return framev1beta1.DiskWipeResult{Ran: true, Message: clipRunes(fmt.Sprintf("verification read %d failed: %v %s", i, err, strings.TrimSpace(stderr)), 500)}
		}
		if strings.Contains(out, bluestoreMagic) {
			off := labelOffsets(size)[i]
			return framev1beta1.DiskWipeResult{Ran: true, Message: clipRunes(fmt.Sprintf("a %q label is still there at offset %d GiB of %s after the wipe: not usable, inspect the disk", bluestoreMagic, off/gib, req.ByIDPath), 500)}
		}
	}
	msg := fmt.Sprintf("%s (%s, serial %s, %d GiB) wiped: wipefs -a, zeros at the head, the tail and the BlueStore label offsets (0, 1, 10, 100, 1000 GiB where present), sgdisk --zap-all; no BlueStore label read back",
		req.ByIDPath, d.Path, d.SerialNumber, size/gib)
	if len(notes) > 0 {
		msg += "; " + strings.Join(notes, "; ")
	}
	return framev1beta1.DiskWipeResult{OK: true, Ran: true, Message: clipRunes(msg, 500)}
}

func actedIDs(root string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, wipeRecordPath))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(raw)), nil
}

func recordActed(root, id string) error {
	p := filepath.Join(root, wipeRecordPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
