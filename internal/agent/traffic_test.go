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
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// writeCounters writes a port's counters into a sysfs tree.
func writeCounters(t *testing.T, base, name string, rx, errs, drops int64) {
	t.Helper()
	dir := filepath.Join(base, name, "statistics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for counter, v := range map[string]int64{
		"rx_bytes": rx, "tx_bytes": 0, "rx_errors": errs, "tx_errors": 0, "rx_dropped": drops, "tx_dropped": 0,
	} {
		if err := os.WriteFile(filepath.Join(dir, counter), []byte(strconv.FormatInt(v, 10)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func gigabit() *int32 {
	c := int32(1000)
	return &c
}

// sweep reads a port every 30 seconds, moving rx by rate bytes a second.
func sweep(t *testing.T, w *trafficWindows, start time.Time, ticks int, rate int64, rx *int64) (*framev1beta1.LinkTraffic, time.Time) {
	t.Helper()
	var got *framev1beta1.LinkTraffic
	now := start
	for range ticks {
		now = now.Add(30 * time.Second)
		*rx += rate * 30
		writeCounters(t, w.base, "eno1", *rx, 0, 0)
		got = w.observe("eno1", gigabit(), now)
	}
	return got, now
}

func TestFirstReadingHasNoTraffic(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	writeCounters(t, w.base, "eno1", 100, 0, 0)
	if got := w.observe("eno1", gigabit(), time.Now()); got != nil {
		t.Fatalf("one reading is no rate: %+v", got)
	}
}

func TestRatesAndUtilization(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	var rx int64
	writeCounters(t, w.base, "eno1", 0, 0, 0)
	w.observe("eno1", gigabit(), time.Unix(0, 0))
	// 100 MB/s on a gigabit link: 800 Mb/s.
	got, _ := sweep(t, w, time.Unix(0, 0), 4, 100_000_000, &rx)
	if got.RxBytesPerSecond != 100_000_000 || got.TxBytesPerSecond != 0 {
		t.Fatalf("%+v", got)
	}
	if *got.UtilizationPercent != 80 {
		t.Fatalf("utilisation %d", *got.UtilizationPercent)
	}
	if got.HistorySeconds != 120 {
		t.Fatalf("history %d", got.HistorySeconds)
	}
	if got.SustainedUtilizationPercent != nil {
		t.Fatal("two minutes of readings cannot say five")
	}
}

func TestSustainedAppearsAtFiveMinutesAndTheBusierDirectionCounts(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	var rx int64
	writeCounters(t, w.base, "eno1", 0, 0, 0)
	w.observe("eno1", gigabit(), time.Unix(0, 0))
	got, _ := sweep(t, w, time.Unix(0, 0), 10, 50_000_000, &rx)
	if got.SustainedUtilizationPercent == nil || *got.SustainedUtilizationPercent != 40 {
		t.Fatalf("%+v", got.SustainedUtilizationPercent)
	}
}

// A burst shows in the peak, and an average that hides it is why the peak exists.
func TestPeakOutlivesTheMinuteAndTheWindowEnds(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	var rx int64
	writeCounters(t, w.base, "eno1", 0, 0, 0)
	w.observe("eno1", gigabit(), time.Unix(0, 0))
	_, now := sweep(t, w, time.Unix(0, 0), 1, 120_000_000, &rx)
	got, now := sweep(t, w, now, 10, 1_000, &rx)
	if got.PeakRxBytesPerSecond != 120_000_000 || *got.PeakUtilizationPercent != 96 {
		t.Fatalf("%+v", got)
	}
	if got.RxBytesPerSecond != 1_000 {
		t.Fatalf("the burst is over: %d", got.RxBytesPerSecond)
	}
	got, _ = sweep(t, w, now, 30, 1_000, &rx)
	if got.PeakRxBytesPerSecond != 1_000 {
		t.Fatalf("the burst left the 15 minutes: %d", got.PeakRxBytesPerSecond)
	}
	if got.HistorySeconds > 900 {
		t.Fatalf("history %d", got.HistorySeconds)
	}
}

func TestAResetCounterStartsTheWindowAgain(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	start := time.Unix(0, 0)
	writeCounters(t, w.base, "eno1", 1_000_000, 0, 0)
	w.observe("eno1", gigabit(), start)
	writeCounters(t, w.base, "eno1", 2_000_000, 0, 0)
	w.observe("eno1", gigabit(), start.Add(30*time.Second))
	writeCounters(t, w.base, "eno1", 10, 0, 0)
	if got := w.observe("eno1", gigabit(), start.Add(time.Minute)); got != nil {
		t.Fatalf("a difference across a reset: %+v", got)
	}
}

func TestErrorsAndDropsPerMinute(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	start := time.Unix(0, 0)
	writeCounters(t, w.base, "eno1", 0, 0, 0)
	w.observe("eno1", gigabit(), start)
	writeCounters(t, w.base, "eno1", 0, 1, 20)
	got := w.observe("eno1", gigabit(), start.Add(5*time.Minute))
	if got.ErrorsPerMinute != 1 {
		t.Fatalf("one error rounds up: %d", got.ErrorsPerMinute)
	}
	if got.DropsPerMinute != 4 {
		t.Fatalf("drops %d", got.DropsPerMinute)
	}
	writeCounters(t, w.base, "eno1", 0, 1, 20)
	if got := w.observe("eno1", gigabit(), start.Add(10*time.Minute)); got.ErrorsPerMinute != 0 {
		t.Fatalf("nothing new in the window: %d", got.ErrorsPerMinute)
	}
}

func TestNoSpeedLeavesTheByteRatesAndNoPercent(t *testing.T) {
	w := newTrafficWindows(t.TempDir())
	writeCounters(t, w.base, "ens18", 0, 0, 0)
	w.observe("ens18", nil, time.Unix(0, 0))
	writeCounters(t, w.base, "ens18", 3_000, 0, 0)
	got := w.observe("ens18", nil, time.Unix(30, 0))
	if got.RxBytesPerSecond != 100 || got.UtilizationPercent != nil || got.CapacityMbps != nil {
		t.Fatalf("%+v", got)
	}
}

func TestAnnotateCoversBondsAndForgetsWhatLeft(t *testing.T) {
	base := t.TempDir()
	w := newTrafficWindows(base)
	writeCounters(t, base, "bond0", 0, 0, 0)
	writeCounters(t, base, "eno1", 0, 0, 0)
	if err := os.WriteFile(filepath.Join(base, "bond0", "speed"), []byte("2000"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := &framev1beta1.MachineNetwork{
		Interfaces: []framev1beta1.NetworkInterface{{Name: "eno1"}},
		Bonds:      []framev1beta1.NetworkBond{{Name: "bond0"}},
	}
	w.annotate(n, time.Unix(0, 0))
	writeCounters(t, base, "bond0", 125_000_000*30, 0, 0)
	w.annotate(n, time.Unix(30, 0))
	if got := n.Bonds[0].Traffic; got == nil || *got.CapacityMbps != 2000 || *got.UtilizationPercent != 50 {
		t.Fatalf("%+v", got)
	}
	w.annotate(&framev1beta1.MachineNetwork{}, time.Unix(60, 0))
	if len(w.history) != 0 {
		t.Fatalf("a link that left stays in memory: %v", w.history)
	}
}

func at(seconds int) time.Time { return time.Unix(int64(seconds), 0) }

// A failed read is no zero: a reset would follow, and the true counter would
// then read as terabytes of traffic and thousands of errors.
func TestAFailedReadIsSkippedAndNotTakenForAReset(t *testing.T) {
	for _, gone := range []string{"rx_bytes", "tx_bytes", "rx_errors", "tx_errors", "rx_dropped", "tx_dropped"} {
		t.Run(gone, func(t *testing.T) {
			w := newTrafficWindows(t.TempDir())
			const total = int64(5_000_000_000_000)
			writeCounters(t, w.base, "eno1", total, 4000, 0)
			w.observe("eno1", gigabit(), at(0))
			writeCounters(t, w.base, "eno1", total+30_000, 4000, 0)
			w.observe("eno1", gigabit(), at(30))

			path := filepath.Join(w.base, "eno1", "statistics", gone)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got := w.observe("eno1", gigabit(), at(60)); got != nil {
				t.Fatalf("a failed read gave figures: %+v", got)
			}

			writeCounters(t, w.base, "eno1", total+90_000, 4000, 0)
			got := w.observe("eno1", gigabit(), at(90))
			if got.RxBytesPerSecond != 1000 || got.PeakRxBytesPerSecond != 1000 || got.ErrorsPerMinute != 0 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// A control plane away for a while stops the ticks: the next reading spans the
// gap and gives its true average, never a zero.
func TestAGapInTheReadingsGivesTheAverageOverIt(t *testing.T) {
	for _, gap := range []int{90, 330} {
		w := newTrafficWindows(t.TempDir())
		writeCounters(t, w.base, "eno1", 0, 0, 0)
		w.observe("eno1", gigabit(), at(0))
		writeCounters(t, w.base, "eno1", 100_000_000*int64(gap), int64(gap)/60*10, 0)
		got := w.observe("eno1", gigabit(), at(gap))
		if got.RxBytesPerSecond != 100_000_000 || *got.UtilizationPercent != 80 {
			t.Fatalf("gap %d: %+v", gap, got)
		}
		if got.ErrorsPerMinute == 0 {
			t.Fatalf("gap %d lost the errors: %+v", gap, got)
		}
	}
}

func TestOneMissedTickLeavesTheFiveMinutesStanding(t *testing.T) {
	h := []trafficSample{}
	for s := 0; s <= 330; s += 30 {
		if s != 30 {
			h = append(h, trafficSample{at: at(s), rx: int64(s) * 1000})
		}
	}
	if got := trafficOf(h, gigabit()); got.SustainedUtilizationPercent == nil {
		t.Fatalf("%+v", got)
	}
}

// Full duplex: either direction can be the full one.
func TestTheBusierDirectionIsTheOneMeasured(t *testing.T) {
	h := []trafficSample{}
	for s := 0; s <= 300; s += 30 {
		h = append(h, trafficSample{at: at(s), tx: int64(s) * 100_000_000, rx: int64(s)})
	}
	got := trafficOf(h, gigabit())
	if *got.UtilizationPercent != 80 || *got.SustainedUtilizationPercent != 80 || *got.PeakUtilizationPercent != 80 {
		t.Fatalf("%+v", got)
	}
	if got.PeakTxBytesPerSecond != 100_000_000 {
		t.Fatalf("%+v", got)
	}
}

func TestAResetOnAnyCounterStartsAgain(t *testing.T) {
	for name, s := range map[string]trafficSample{
		"tx":     {tx: 1},
		"errors": {errors: 1},
		"drops":  {drops: 1},
	} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			w := newTrafficWindows(base)
			w.history["eno1"] = []trafficSample{{at: at(0), rx: 1, tx: s.tx + 5, errors: s.errors + 5, drops: s.drops + 5}}
			writeCounters(t, base, "eno1", 2, 0, 0)
			if got := w.observe("eno1", gigabit(), at(30)); got != nil {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestSustainedNeedsNearlyFiveMinutes(t *testing.T) {
	short := []trafficSample{{at: at(0)}, {at: at(270), rx: 1}}
	if got := trafficOf(short, gigabit()); got.SustainedUtilizationPercent != nil {
		t.Fatalf("%+v", got)
	}
	enough := []trafficSample{{at: at(0)}, {at: at(285), rx: 1}}
	if got := trafficOf(enough, gigabit()); got.SustainedUtilizationPercent == nil {
		t.Fatalf("%+v", got)
	}
}

func TestABondThatGivesNoSpeedHasNoPercent(t *testing.T) {
	for _, speed := range []string{"-1", "4294967295"} {
		base := t.TempDir()
		w := newTrafficWindows(base)
		writeCounters(t, base, "bond0", 0, 0, 0)
		if err := os.WriteFile(filepath.Join(base, "bond0", "speed"), []byte(speed), 0o644); err != nil {
			t.Fatal(err)
		}
		n := &framev1beta1.MachineNetwork{Bonds: []framev1beta1.NetworkBond{{Name: "bond0"}}}
		w.annotate(n, at(0))
		w.annotate(n, at(30))
		if got := n.Bonds[0].Traffic; got == nil || got.CapacityMbps != nil {
			t.Fatalf("%s: %+v", speed, got)
		}
	}
}
