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
	"path/filepath"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// What each link carried, from the counters beside those network.go already
// reads. Files only, like it. The history lives here and is lost with the agent:
// the window then starts again, which the controller reads as not known yet.

const (
	rateWindow      = time.Minute
	sustainedWindow = 5 * time.Minute
	peakWindow      = 15 * time.Minute
	// A window is met when the readings reach this close to its start: the
	// agent's tick is 30 seconds, and a reading never lands on the second.
	windowSlack = 15 * time.Second
)

type trafficSample struct {
	at                    time.Time
	rx, tx, errors, drops int64
}

// trafficWindows holds each link's readings. annotate runs on one goroutine at a
// time: NetworkCollector starts a read only when none is running.
type trafficWindows struct {
	base    string
	history map[string][]trafficSample
}

func newTrafficWindows(base string) *trafficWindows {
	return &trafficWindows{base: base, history: map[string][]trafficSample{}}
}

// annotate records a reading of every link in n and fills in its traffic.
func (w *trafficWindows) annotate(n *framev1beta1.MachineNetwork, now time.Time) {
	seen := make(map[string]bool, len(n.Interfaces)+len(n.Bonds))
	for i := range n.Interfaces {
		iface := &n.Interfaces[i]
		seen[iface.Name] = true
		iface.Traffic = w.observe(iface.Name, iface.SpeedMbps, now)
	}
	for i := range n.Bonds {
		b := &n.Bonds[i]
		seen[b.Name] = true
		var capacity *int32
		if speed := number(filepath.Join(w.base, b.Name, "speed")); speed > 0 && speed < 1<<31-1 {
			c := int32(speed)
			capacity = &c
		}
		b.Traffic = w.observe(b.Name, capacity, now)
	}
	for name := range w.history {
		if !seen[name] {
			delete(w.history, name)
		}
	}
}

func (w *trafficWindows) observe(name string, capacity *int32, now time.Time) *framev1beta1.LinkTraffic {
	stat := func(counter string) int64 { return number(filepath.Join(w.base, name, "statistics", counter)) }
	s := trafficSample{
		at: now, rx: stat("rx_bytes"), tx: stat("tx_bytes"),
		errors: stat("rx_errors") + stat("tx_errors"),
		drops:  stat("rx_dropped") + stat("tx_dropped"),
	}
	h := w.history[name]
	// A counter going back is a driver reloaded: what came before says nothing
	// about what follows, and a difference across it would be nonsense.
	if n := len(h); n > 0 && (s.rx < h[n-1].rx || s.tx < h[n-1].tx || s.errors < h[n-1].errors || s.drops < h[n-1].drops) {
		h = nil
	}
	h = append(h, s)
	for len(h) > 1 && now.Sub(h[1].at) >= peakWindow {
		h = h[1:]
	}
	w.history[name] = h
	return trafficOf(h, capacity)
}

// since is the oldest reading no further back than window.
func since(h []trafficSample, window time.Duration) trafficSample {
	last := h[len(h)-1]
	for _, s := range h {
		if last.at.Sub(s.at) <= window+windowSlack {
			return s
		}
	}
	return last
}

func perSecond(from, to int64, d time.Duration) int64 {
	return int64(float64(to-from) / d.Seconds())
}

// percentOfLink is how much of a link of capacity Mb/s that bytes per second fill. Above 100 is
// kept: a port whose counters outrun its stated speed is worth seeing.
func percentOfLink(bytesPerSecond int64, capacity *int32) *int32 {
	if capacity == nil {
		return nil
	}
	p := int32(float64(bytesPerSecond) * 8 / (float64(*capacity) * 1e6) * 100)
	return &p
}

func trafficOf(h []trafficSample, capacity *int32) *framev1beta1.LinkTraffic {
	if len(h) < 2 {
		return nil
	}
	last := h[len(h)-1]
	t := &framev1beta1.LinkTraffic{
		HistorySeconds: int32(last.at.Sub(h[0].at).Seconds()),
		CapacityMbps:   capacity,
	}

	recent := since(h, rateWindow)
	if d := last.at.Sub(recent.at); d > 0 {
		t.RxBytesPerSecond = perSecond(recent.rx, last.rx, d)
		t.TxBytesPerSecond = perSecond(recent.tx, last.tx, d)
	}
	t.UtilizationPercent = percentOfLink(max(t.RxBytesPerSecond, t.TxBytesPerSecond), capacity)

	for i := 1; i < len(h); i++ {
		if d := h[i].at.Sub(h[i-1].at); d > 0 {
			t.PeakRxBytesPerSecond = max(t.PeakRxBytesPerSecond, perSecond(h[i-1].rx, h[i].rx, d))
			t.PeakTxBytesPerSecond = max(t.PeakTxBytesPerSecond, perSecond(h[i-1].tx, h[i].tx, d))
		}
	}
	t.PeakUtilizationPercent = percentOfLink(max(t.PeakRxBytesPerSecond, t.PeakTxBytesPerSecond), capacity)

	sustained := since(h, sustainedWindow)
	d := last.at.Sub(sustained.at)
	if d >= sustainedWindow-windowSlack {
		t.SustainedUtilizationPercent = percentOfLink(max(perSecond(sustained.rx, last.rx, d), perSecond(sustained.tx, last.tx, d)), capacity)
	}
	if d > 0 {
		perMinute := func(from, to int64) int64 {
			if to == from {
				return 0
			}
			return max(1, int64(float64(to-from)/d.Minutes()+0.999))
		}
		t.ErrorsPerMinute = perMinute(sustained.errors, last.errors)
		t.DropsPerMinute = perMinute(sustained.drops, last.drops)
	}
	return t
}
