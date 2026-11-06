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
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Reading a solid-state disk's own health data, read-only.
//
// What may run is decided here and nowhere else. smartArgvPrefix is the whole
// of the options, a compile-time constant; the only variable is the device,
// which comes from the node's own lsblk, never from the cluster or a CR, and
// must match smartDevice. smartArgv is the only way to build a command, and
// execSMART re-checks the result, so nothing else reaches the executor:
// smartctl can start self-tests and change disk settings, and none of the
// options that do is reachable. smart_test.go fails if the prefix changes
// without its reviewed copy changing too, or if it carries such an option.
//
// --nocheck=standby,0 leaves a sleeping disk asleep, though the manual does not
// promise it during device type detection. Spinning disks are not read at all.
var smartArgvPrefix = []string{"smartctl", "--json=c", "--nocheck=standby,0", "--all", "--log=devstat"}

// smartDevice is a whole disk by its kernel name: nothing else, and no room for
// an option or a path through it.
var smartDevice = regexp.MustCompile(`^/dev/(sd[a-z]{1,3}|nvme[0-9]{1,3}n[0-9]{1,3})$`)

// smartInterval spaces the reads: wear moves over months, and each read is
// several ATA commands on a disk in service.
const smartInterval = 6 * time.Hour

// smartRetry is how soon a failed read is tried again: one that timed out once
// must not leave the disk unread for six hours.
const smartRetry = 30 * time.Minute

// smartArgv builds the one command this file runs.
func smartArgv(device string) ([]string, error) {
	if !smartDevice.MatchString(device) {
		return nil, fmt.Errorf("refusing device %q: not a whole disk by kernel name", device)
	}
	return append(slices.Clone(smartArgvPrefix), device), nil
}

// execSMART runs argv, which must be exactly what smartArgv builds, under
// timeout. The only way anything here reaches the executor.
func execSMART(x StorageExecutor, argv []string) (string, string, error) {
	if len(argv) != len(smartArgvPrefix)+1 ||
		!slices.Equal(argv[:len(smartArgvPrefix)], smartArgvPrefix) ||
		!smartDevice.MatchString(argv[len(argv)-1]) {
		return "", "", fmt.Errorf("refusing %q: not the SMART read", strings.Join(argv, " "))
	}
	return x.Exec(append(slices.Clone(timeoutPrefix), argv...))
}

// SMARTReader keeps the last read of each disk and refreshes them in the
// background: a disk that hangs must not hold up the agent's tick.
type SMARTReader struct {
	exec StorageExecutor
	now  func() time.Time

	mu      sync.Mutex
	last    map[string]*framev1beta1.DiskSMART // by serial
	nextAt  time.Time
	running bool
}

// NewNodeSMARTReader is the agent's reader: smartctl from its own image.
func NewNodeSMARTReader() *SMARTReader { return NewSMARTReader(NewImageStorageExecutor()) }

// NewSMARTReader reads through x.
func NewSMARTReader(x StorageExecutor) *SMARTReader {
	return &SMARTReader{exec: x, now: time.Now, last: map[string]*framev1beta1.DiskSMART{}}
}

// Annotate fills in the last read of each disk and starts a refresh when one
// is due. It never waits for it.
func (r *SMARTReader) Annotate(disks []framev1beta1.ObservedDisk) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range disks {
		if s, ok := r.last[disks[i].SerialNumber]; ok {
			disks[i].SMART = s.DeepCopy()
		}
	}
	if r.running || r.now().Before(r.nextAt) {
		return
	}
	r.running = true
	r.nextAt = r.now().Add(smartInterval)
	go r.refresh(slices.Clone(disks))
}

func (r *SMARTReader) refresh(disks []framev1beta1.ObservedDisk) {
	failed := false
	for i := range disks {
		d := &disks[i]
		// A device off the pattern, an eMMC say, is not one this reader reads:
		// no failure, no report.
		if d.Rotational == nil || *d.Rotational || !smartDevice.MatchString(d.Path) {
			continue
		}
		s := r.read(d)
		failed = failed || s.Error != ""
		r.mu.Lock()
		r.last[d.SerialNumber] = s
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.running = false
	if failed {
		r.nextAt = r.now().Add(smartRetry)
	}
	r.mu.Unlock()
}

func (r *SMARTReader) read(d *framev1beta1.ObservedDisk) *framev1beta1.DiskSMART {
	now := metav1.NewTime(r.now())
	failed := func(err error) *framev1beta1.DiskSMART {
		return &framev1beta1.DiskSMART{ReadAt: &now, Error: clipRunes(err.Error(), 256)}
	}
	argv, err := smartArgv(d.Path)
	if err != nil {
		return failed(err)
	}
	stdout, stderr, err := execSMART(r.exec, argv)
	if errors.Is(err, errBusy) {
		return failed(errBusy)
	}
	// smartctl's exit status is a bit field that also reports a failing disk,
	// and timeout passes it on: 124 from smartctl is a dying disk, not a timeout.
	// So its report comes first, and says whether it could speak to the disk.
	report, perr := parseSMART(stdout)
	if perr == nil {
		report.ReadAt = &now
		return report
	}
	switch {
	case exitCode(err) == 127:
		return failed(errors.New("smartctl not found in the agent image"))
	case exitCode(err) == 124, exitCode(err) == 137:
		return failed(fmt.Errorf("smartctl timed out after %ds", storageTimeoutSeconds))
	case err != nil && stderr != "":
		return failed(fmt.Errorf("%w: %s", err, firstLine(stderr)))
	case err != nil && stdout == "":
		return failed(err)
	}
	return failed(perr)
}

type smartJSON struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	SmartStatus *struct {
		Passed *bool `json:"passed"`
	} `json:"smart_status"`
	NVMe *struct {
		PercentageUsed  *int32 `json:"percentage_used"`
		CriticalWarning *int   `json:"critical_warning"`
	} `json:"nvme_smart_health_information_log"`
	DeviceStatistics struct {
		Pages []struct {
			Table []struct {
				Name string `json:"name"`
				// int64: the same table counts sectors written, past 2^31 after
				// 1 TiB, and one number too wide would lose the whole report.
				Value *int64 `json:"value"`
			} `json:"table"`
		} `json:"pages"`
	} `json:"ata_device_statistics"`
	Attributes struct {
		Table []struct {
			Name  string `json:"name"`
			Value *int32 `json:"value"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

const (
	// Bits 0 and 1 of the exit status: bad command line, device not opened.
	// Above them, smartctl did read the disk.
	smartExitNoRead = 0b11

	// Bit 1 of an NVMe critical warning: temperature past its threshold. smartctl
	// fails the self-assessment on any bit, but a hot room is no failing disk.
	nvmeTemperatureWarning = 0b10

	endurancePercentUsed = "Percentage Used Endurance Indicator"
)

// wearAttributes are the ATA attributes whose normalized value counts down from
// 100, in order of trust. By name, which smartctl takes from its drive database:
// the same ID means something else on other models, 233 being flash writes on
// many and 231 a temperature.
//
// ponytail: a model missing from that database gets the default names, 233 being
// Media_Wearout_Indicator. Refresh the database with the image if one shows up.
var wearAttributes = []string{
	"Media_Wearout_Indicator", "SSD_Life_Left", "Percent_Lifetime_Remain",
	"Remaining_Lifetime_Perc", "Perc_Rated_Life_Remain", "Percent_Life_Remaining",
	"Wear_Leveling_Count",
}

func parseSMART(stdout string) (*framev1beta1.DiskSMART, error) {
	var j smartJSON
	if err := json.Unmarshal([]byte(stdout), &j); err != nil {
		return nil, fmt.Errorf("parsing smartctl output: %w", err)
	}
	if j.Smartctl.ExitStatus&smartExitNoRead != 0 {
		why := fmt.Sprintf("smartctl could not read the disk (exit status %d)", j.Smartctl.ExitStatus)
		if len(j.Smartctl.Messages) > 0 {
			why += ": " + j.Smartctl.Messages[0].String
		}
		return nil, errors.New(why)
	}
	out := &framev1beta1.DiskSMART{WearUsedPercent: wearOf(&j)}
	if j.SmartStatus != nil {
		out.Passed = j.SmartStatus.Passed
		if out.Passed != nil && j.NVMe != nil && j.NVMe.CriticalWarning != nil && *j.NVMe.CriticalWarning == nvmeTemperatureWarning {
			passed := true
			out.Passed = &passed
		}
	}
	return out, nil
}

func wearOf(j *smartJSON) *int32 {
	if j.NVMe != nil && j.NVMe.PercentageUsed != nil {
		return percent(*j.NVMe.PercentageUsed)
	}
	for _, page := range j.DeviceStatistics.Pages {
		for _, row := range page.Table {
			if row.Name == endurancePercentUsed && row.Value != nil {
				return percent(int32(min(max(*row.Value, 0), 100)))
			}
		}
	}
	for _, name := range wearAttributes {
		for _, a := range j.Attributes.Table {
			if a.Name == name && a.Value != nil && *a.Value <= 100 {
				return percent(100 - *a.Value)
			}
		}
	}
	return nil
}

// percent keeps a value inside the CRD's bounds: an NVMe past its rating
// reports above 100, and one value out of range would get the status refused.
func percent(v int32) *int32 {
	v = min(max(v, 0), 100)
	return &v
}
