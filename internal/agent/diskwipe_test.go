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
	"os"
	"path/filepath"
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
}

func (w *wipeExec) Exec(argv []string) (string, string, error) {
	w.argvs = append(w.argvs, argv)
	cmd := argv[len(timeoutPrefix)]
	if err := w.fail[cmd]; err != nil {
		return "", "boom", err
	}
	return "", "", nil
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
	want := wipeArgv(wipeByID)
	if len(x.argvs) != len(want) {
		t.Fatalf("argvs = %v", x.argvs)
	}
	for i, a := range x.argvs {
		if strings.Join(a, " ") != strings.Join(append(append([]string{}, timeoutPrefix...), want[i]...), " ") {
			t.Fatalf("argv %d = %v", i, a)
		}
		if strings.Contains(strings.Join(a, " "), "/dev/sd") {
			t.Fatalf("a kernel name reached the command line: %v", a)
		}
	}
	// Answered: a second tick does nothing.
	if err := ServiceWipeRequest(context.Background(), c, "r320", []framev1beta1.ObservedDisk{freeDisk()}, x, env); err != nil {
		t.Fatal(err)
	}
	if len(x.argvs) != 3 {
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
		{"not free", nil, func() []framev1beta1.ObservedDisk { d := freeDisk(); d.Occupancy = "lvm-pv"; return []framev1beta1.ObservedDisk{d} }, nil, "not free"},
		{"partitioned", nil, func() []framev1beta1.ObservedDisk { d := freeDisk(); d.Occupancy = "partitioned"; return []framev1beta1.ObservedDisk{d} }, nil, "not free"},
		{"absent", nil, func() []framev1beta1.ObservedDisk { return nil }, nil, "0 disks"},
		{"two at the path", nil, func() []framev1beta1.ObservedDisk { return []framev1beta1.ObservedDisk{freeDisk(), freeDisk()} }, nil, "2 disks"},
		{"old request", func(r *framev1beta1.DiskWipeRequest) { r.RequestedAt = metav1.NewTime(wipeNow.Add(-time.Hour)) }, nil, nil, "window"},
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
	if res := wipeResult(t, c); res.OK || res.Ran || !strings.Contains(res.Message, "already acted") {
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
	if res := wipeResult(t, c); res.OK || !res.Ran || len(x.argvs) != 1 {
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
