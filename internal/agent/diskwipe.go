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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
//     there is answered "already acted" without touching the disk;
//   - a request older than wipeRequestMaxAge, or dated in the future, is
//     refused;
//   - the commands are fixed: `wipefs -a`, then 100 MiB of zeros with dd,
//     then `sgdisk --zap-all` when the host has it, each on the by-id path
//     (the identity checked above, not a kernel name that can move), under
//     the host's `timeout`, in PID 1's namespaces.
//
// Whatever happens, the answer is written to the Node
// (DiskWipeResultAnnotation) with Ran saying whether any command started.

// wipeRequestMaxAge: the controller waits ten minutes for an answer; a
// request older than this is a leftover and is refused.
const wipeRequestMaxAge = 20 * time.Minute

// wipeRecordPath is the host file holding every request id acted on.
const wipeRecordPath = "var/lib/frame-agent/disk-wipe-acted"

// wipeByIDPath is the shape of a path the agent will wipe.
var wipeByIDPath = regexp.MustCompile(`^/dev/disk/by-id/(wwn-0x[0-9a-fA-F]{8,64}|nvme-(eui|nvme)\.[0-9a-zA-Z._-]{4,96})$`)

// wipeRequestID is the shape of a request id (one line of the record).
var wipeRequestID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

// wipeArgv are the commands run, in order, %s being the by-id path.
func wipeArgv(byID string) [][]string {
	return [][]string{
		{"wipefs", "-a", byID},
		{"dd", "if=/dev/zero", "of=" + byID, "bs=1M", "count=100", "oflag=direct", "status=none"},
		{"sgdisk", "--zap-all", byID},
	}
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
	base := node.DeepCopy()
	node.Annotations[framev1beta1.DiskWipeResultAnnotation] = string(out)
	// Optimistic lock: a request replaced between the read and this write
	// must not get this answer.
	if err := c.Patch(ctx, &node, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
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
		return framev1beta1.DiskWipeResult{Message: "refused: request " + req.ID + " was already acted on by this node; its outcome is unknown, inspect the disk"}
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

	// Write-ahead: no command runs unless the id is recorded first.
	if err := recordActed(env.Root, req.ID); err != nil {
		return refused("cannot record the request before acting: %v", err)
	}
	var notes []string
	for i, argv := range wipeArgv(req.ByIDPath) {
		_, stderr, err := x.Exec(append(slices.Clone(timeoutPrefix), argv...))
		if err == nil {
			continue
		}
		var ee interface{ ExitCode() int }
		if i == 2 && errors.As(err, &ee) && ee.ExitCode() == 127 {
			notes = append(notes, "sgdisk not installed on the node, skipped (wipefs -a already cleared both GPT headers)")
			continue
		}
		return framev1beta1.DiskWipeResult{Ran: true, Message: clipRunes(fmt.Sprintf("%s failed: %v %s", argv[0], err, strings.TrimSpace(stderr)), 500)}
	}
	msg := fmt.Sprintf("%s (%s, serial %s) wiped: wipefs -a, 100 MiB of zeros, sgdisk --zap-all", req.ByIDPath, d.Path, d.SerialNumber)
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
