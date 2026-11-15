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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

type wipeExec struct {
	argvs [][]string
	fail  map[string]error // by command name
	// size is what blockdev answers (a 600 GB disk by default).
	size int64
	// label is a "bluestore block device" label the disk still reads at
	// this offset in bytes (a pointer: nil means none).
	label *int64
}

func (w *wipeExec) Exec(argv []string) (string, string, error) {
	w.argvs = append(w.argvs, argv)
	cmd := argv[len(timeoutPrefix)]
	if err := w.fail[cmd]; err != nil {
		return "", "boom", err
	}
	switch cmd {
	case "blockdev":
		if w.size == 0 {
			w.size = 600_127_266_816
		}
		return fmt.Sprintf("%d\n", w.size), "", nil
	case "dd":
		for _, a := range argv {
			if v, ok := strings.CutPrefix(a, "skip="); ok && w.label != nil {
				n, err := strconv.ParseInt(v, 10, 64)
				if err == nil && n*4096 == *w.label {
					return "\x00\x00bluestore block device\n", "", nil
				}
			}
		}
	}
	return "", "", nil
}

// changing are the argvs that write to the disk (not the size or the read-backs).
func changing(argvs [][]string) [][]string {
	var out [][]string
	for _, a := range argvs {
		c := a[len(timeoutPrefix):]
		if c[0] == "blockdev" || (c[0] == "dd" && strings.HasPrefix(c[1], "if=/dev/disk")) {
			continue
		}
		out = append(out, c)
	}
	return out
}

type exit127 struct{}

func (exit127) Error() string { return "exit status 127" }
func (exit127) ExitCode() int { return 127 }

const wipeByID = "/dev/disk/by-id/wwn-0x5000cca012345678"

var wipeNow = time.Date(2026, 11, 13, 18, 30, 0, 0, time.UTC)

func freeDisk() framev1beta1.ObservedDisk {
	return framev1beta1.ObservedDisk{Path: "/dev/sdc", SerialNumber: "SAS600NEW", ByIDPath: wipeByID, Occupancy: "free", LogicalSectorBytes: 512}
}

func wipeSetup(t *testing.T, req framev1beta1.DiskWipeRequest) (client.Client, WipeEnv) {
	t.Helper()
	raw, _ := json.Marshal(req)
	c := fake.NewClientBuilder().WithScheme(newDiskStatusScheme(t)).WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "r320", Annotations: map[string]string{framev1beta1.DiskWipeRequestAnnotation: string(raw)}},
	}).Build()
	env := WipeEnv{
		Root:    t.TempDir(),
		Resolve: func(p string) (string, error) { return "/dev/sdc", nil },
		Holders: func(string) ([]string, error) { return nil, nil },
		Now:     func() time.Time { return wipeNow },
	}
	return c, env
}

func goodRequest() framev1beta1.DiskWipeRequest {
	return framev1beta1.DiskWipeRequest{ID: "0a1b2c3d-0", ByIDPath: wipeByID, Serial: "SAS600NEW", RequestedAt: metav1.NewTime(wipeNow.Add(-time.Minute))}
}

func wipeResult(t *testing.T, c client.Client) framev1beta1.DiskWipeResult {
	t.Helper()
	var n corev1.Node
	if err := c.Get(context.Background(), client.ObjectKey{Name: "r320"}, &n); err != nil {
		t.Fatal(err)
	}
	var res framev1beta1.DiskWipeResult
	if err := json.Unmarshal([]byte(n.Annotations[framev1beta1.DiskWipeResultAnnotation]), &res); err != nil {
		t.Fatalf("no result: %v", err)
	}
	return res
}

func TestWipeRunsTheFixedCommandsOnTheByIDPath(t *testing.T) {
	c, env := wipeSetup(t, goodRequest())
	x := &wipeExec{}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	res := wipeResult(t, c)
	if !res.OK || !res.Ran || res.ID != "0a1b2c3d-0" {
		t.Fatalf("result = %+v", res)
	}
	want := wipeArgv(wipeByID, 600_127_266_816)
	got := changing(x.argvs)
	if len(got) != len(want) {
		t.Fatalf("argvs = %v", x.argvs)
	}
	for i, a := range got {
		if strings.Join(a, " ") != strings.Join(want[i], " ") {
			t.Fatalf("argv %d = %v", i, a)
		}
	}
	for _, a := range x.argvs {
		if strings.Contains(strings.Join(a, " "), "/dev/sd") {
			t.Fatalf("a kernel name reached the command line: %v", a)
		}
	}
	// A 600 GB disk (558 GiB): head, copies at 1, 10 and 100 GiB, tail.
	var seeks []string
	for _, a := range got {
		for _, f := range a {
			if strings.HasPrefix(f, "seek=") {
				seeks = append(seeks, f)
			}
		}
	}
	if strings.Join(seeks, " ") != "seek=1024 seek=10240 seek=102400 seek=572225" {
		t.Fatalf("seeks = %v", seeks)
	}
	// Answered: a second tick does nothing.
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	n := len(x.argvs)
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if len(x.argvs) != n {
		t.Fatalf("ran again: %v", x.argvs)
	}
}

func TestWipeRefusals(t *testing.T) {
	cases := []struct {
		name   string
		req    func(*framev1beta1.DiskWipeRequest)
		disks  func() []framev1beta1.ObservedDisk
		env    func(*WipeEnv)
		reason string
	}{
		{"serial differs", func(r *framev1beta1.DiskWipeRequest) { r.Serial = "OTHER" }, nil, nil, "has serial"},
		{"not free", nil, func() []framev1beta1.ObservedDisk {
			d := freeDisk()
			d.Occupancy = "lvm-pv"
			return []framev1beta1.ObservedDisk{d}
		}, nil, "not free"},
		{"partitioned", nil, func() []framev1beta1.ObservedDisk {
			d := freeDisk()
			d.Occupancy = "partitioned"
			return []framev1beta1.ObservedDisk{d}
		}, nil, "not free"},
		{"absent", nil, func() []framev1beta1.ObservedDisk { return nil }, nil, "0 disks"},
		{"two at the path", nil, func() []framev1beta1.ObservedDisk { return []framev1beta1.ObservedDisk{freeDisk(), freeDisk()} }, nil, "2 disks"},
		{"old request", func(r *framev1beta1.DiskWipeRequest) { r.RequestedAt = metav1.NewTime(wipeNow.Add(-time.Hour)) }, nil, nil, "window"},
		{"request the controller gave up on", func(r *framev1beta1.DiskWipeRequest) { r.RequestedAt = metav1.NewTime(wipeNow.Add(-6 * time.Minute)) }, nil, nil, "window"},
		{"future request", func(r *framev1beta1.DiskWipeRequest) { r.RequestedAt = metav1.NewTime(wipeNow.Add(time.Hour)) }, nil, nil, "window"},
		{"sdX path", func(r *framev1beta1.DiskWipeRequest) { r.ByIDPath = "/dev/sdc" }, nil, nil, "not a /dev/disk/by-id"},
		{"by-id of another kind", func(r *framev1beta1.DiskWipeRequest) { r.ByIDPath = "/dev/disk/by-id/ata-ST500_Z2AW5NWA" }, nil, nil, "not a /dev/disk/by-id"},
		{"resolves elsewhere", nil, nil, func(e *WipeEnv) { e.Resolve = func(string) (string, error) { return "/dev/sda", nil } }, "resolves to /dev/sda"},
		{"held", nil, nil, func(e *WipeEnv) { e.Holders = func(string) ([]string, error) { return []string{"dm-0"}, nil } }, "held by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := goodRequest()
			if tc.req != nil {
				tc.req(&req)
			}
			c, env := wipeSetup(t, req)
			if tc.env != nil {
				tc.env(&env)
			}
			disks := []framev1beta1.ObservedDisk{freeDisk()}
			if tc.disks != nil {
				disks = tc.disks()
			}
			x := &wipeExec{}
			if err := ServiceWipeRequest(context.Background(), c, "r320", disks, x, env); err != nil {
				t.Fatal(err)
			}
			res := wipeResult(t, c)
			if res.OK || res.Ran || !strings.Contains(res.Message, tc.reason) {
				t.Fatalf("result = %+v, want a refusal containing %q", res, tc.reason)
			}
			if len(x.argvs) != 0 {
				t.Fatalf("ran %v", x.argvs)
			}
		})
	}
}

func TestWipeOncePerRequestID(t *testing.T) {
	c, env := wipeSetup(t, goodRequest())
	if err := recordActed(env.Root, "0a1b2c3d-0"); err != nil {
		t.Fatal(err)
	}
	x := &wipeExec{}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if res := wipeResult(t, c); res.OK || !res.Ran || !strings.Contains(res.Message, "already acted") {
		t.Fatalf("result = %+v", res)
	}
	if len(x.argvs) != 0 {
		t.Fatalf("ran %v", x.argvs)
	}
}

func TestWipeRecordsBeforeActing(t *testing.T) {
	c, env := wipeSetup(t, goodRequest())
	// A root where the record cannot be written: nothing runs.
	blocker := filepath.Join(env.Root, "var")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	x := &wipeExec{}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if res := wipeResult(t, c); res.Ran || !strings.Contains(res.Message, "cannot") {
		t.Fatalf("result = %+v", res)
	}
	if len(x.argvs) != 0 {
		t.Fatalf("ran %v", x.argvs)
	}
}

func TestWipeStopsAtTheFirstFailureButSkipsAMissingSgdisk(t *testing.T) {
	c, env := wipeSetup(t, goodRequest())
	x := &wipeExec{fail: map[string]error{"sgdisk": exit127{}}}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if res := wipeResult(t, c); !res.OK || !strings.Contains(res.Message, "sgdisk not installed") {
		t.Fatalf("result = %+v", res)
	}

	c, env = wipeSetup(t, goodRequest())
	x = &wipeExec{fail: map[string]error{"wipefs": errors.New("exit status 1")}}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if res := wipeResult(t, c); res.OK || !res.Ran || len(changing(x.argvs)) != 1 {
		t.Fatalf("result = %+v argvs %v", res, x.argvs)
	}
}

func TestLsblkLogSecIsTolerant(t *testing.T) {
	for _, raw := range []string{`512`, `"4096"`, `null`, `"x"`} {
		var n lsblkInt
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	var n lsblkInt
	_ = json.Unmarshal([]byte(`"4096"`), &n)
	if logicalSector(n) != 4096 {
		t.Fatalf("got %d", logicalSector(n))
	}
}

func TestWipeZeroesEveryBlueStoreLabelOffsetTheDiskReaches(t *testing.T) {
	// A 2 TB disk reaches 1000 GiB; a 500 MiB one reaches none of them.
	if got := labelOffsets(2_000_398_934_016); len(got) != 5 || got[4] != 1000*gib {
		t.Fatalf("offsets of a 2 TB disk = %v", got)
	}
	if got := labelOffsets(500 * mib); len(got) != 1 {
		t.Fatalf("offsets of a 500 MiB disk = %v", got)
	}
	for _, a := range wipeArgv(wipeByID, 500*mib) {
		if a[0] == "dd" && slices.Contains(a, "seek=1024") {
			t.Fatalf("a 500 MiB disk is not written at 1 GiB: %v", a)
		}
	}
	// Disks too small for a tail are not written past their end.
	for _, a := range wipeArgv(wipeByID, 150*mib) {
		if strings.Contains(strings.Join(a, " "), "seek=") {
			t.Fatalf("150 MiB disk: %v", a)
		}
	}
}

func TestWipeFailsWhenABlueStoreLabelIsStillReadBack(t *testing.T) {
	for _, off := range []int64{0, gib, 100 * gib} {
		c, env := wipeSetup(t, goodRequest())
		x := &wipeExec{label: &off}
		if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
			t.Fatal(err)
		}
		res := wipeResult(t, c)
		if res.OK || !res.Ran || !strings.Contains(res.Message, "bluestore block device") || !strings.Contains(res.Message, fmt.Sprintf("%d GiB", off/gib)) {
			t.Fatalf("label at %d GiB: %+v", off/gib, res)
		}
	}
}

func TestWipeRefusesBeforeActingWhenTheSizeIsUnreadable(t *testing.T) {
	c, env := wipeSetup(t, goodRequest())
	x := &wipeExec{fail: map[string]error{"blockdev": errors.New("exit status 1")}}
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if res := wipeResult(t, c); res.OK || res.Ran || len(x.argvs) != 1 {
		t.Fatalf("result = %+v argvs %v", res, x.argvs)
	}
	// Nothing was recorded: the same id may run once the size reads.
	if ids, _ := actedIDs(env.Root); len(ids) != 0 {
		t.Fatalf("recorded %v", ids)
	}
}
