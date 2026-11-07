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

// What a node's links say, under the same rule as its storage: only a read
// that mentions a device changes anything about it.
//
// ponytail: here because it shares Assessment, Verdict and the alert plumbing
// with the storage checks. Rename the package if a third domain joins them.

const modeLACP = "802.3ad"

// ScopeNetExpected carries what only the policy asked for: a link named there and
// missing from the node. Unlike a device that left a read, it ends when the policy
// stops naming it, or a typo in a name would stay open for good.
const ScopeNetExpected Scope = "network-expected"

// scopeNetState holds what a silent field costs NetworkDegraded alone: a member
// with no status says nothing against the speed of every port.
const scopeNetState Scope = "network-state"

// scopeNetTraffic is a link the agent has no rates for yet, and scopeNetSustained
// one it has fewer than five minutes of: the agent's history starts over with it.
const (
	scopeNetTraffic   Scope = "network-traffic"
	scopeNetSustained Scope = "network-sustained"
)

// saturationHysteresis is how far under the threshold an open alert must fall to
// clear, so that a link hovering at it does not page on every crossing.
const saturationHysteresis = 5

// FullHistorySeconds is the history under which a low figure proves nothing: the
// agent's own five-minute window less its slack, which a test there holds it to.
const FullHistorySeconds = 285

// NetExpectations is the NetworkHealthPolicy as it applies to one node.
type NetExpectations struct {
	MinSpeedMbps     int32
	SaturatedPercent int32
	ErrorsPerMinute  int32
	Links            map[string]framev1beta1.ExpectedLink
	// Unread says why the policy could not be read: links are then unassessed,
	// and storage, which does not depend on it, goes on.
	Unread string
}

// NetExpectationsFrom resolves the policy for a node; nil means the default.
func NetExpectationsFrom(p *framev1beta1.NetworkHealthPolicy, node string) NetExpectations {
	e := NetExpectations{
		MinSpeedMbps:     framev1beta1.DefaultMinLinkSpeedMbps,
		SaturatedPercent: framev1beta1.DefaultLinkSaturatedPercent,
		ErrorsPerMinute:  framev1beta1.DefaultLinkErrorsPerMinute,
		Links:            map[string]framev1beta1.ExpectedLink{},
	}
	if p == nil {
		return e
	}
	if p.Spec.MinLinkSpeedMbps > 0 {
		e.MinSpeedMbps = p.Spec.MinLinkSpeedMbps
	}
	if p.Spec.LinkSaturatedPercent > 0 {
		e.SaturatedPercent = p.Spec.LinkSaturatedPercent
	}
	if p.Spec.LinkErrorsPerMinute > 0 {
		e.ErrorsPerMinute = p.Spec.LinkErrorsPerMinute
	}
	for _, l := range p.Spec.Links {
		if l.Node == node {
			e.Links[l.Interface] = l
		}
	}
	return e
}

func memberDevice(bond, member string) string { return bond + "/" + member }

func (a *Assessment) assessNetwork(n *framev1beta1.MachineNetwork, e NetExpectations, open Open, now time.Time) {
	unread := func(why string) { a.Unread[ScopeNet], a.Unread[ScopeNetExpected] = why, why }
	switch {
	case e.Unread != "":
		unread(e.Unread)
		return
	case n == nil:
		unread("the agent has not reported the node's links")
		return
	case n.Error != "":
		unread("read failed: " + n.Error)
		return
	case n.ObservedAt == nil || now.Sub(n.ObservedAt.Time) > AgentSilentAfter:
		unread("the agent has stopped reporting")
		return
	}

	var listed []string
	for i := range n.Interfaces {
		listed = append(listed, n.Interfaces[i].Name)
		a.assessInterface(&n.Interfaces[i], e, open)
	}
	for i := range n.Bonds {
		listed = append(listed, n.Bonds[i].Name)
		for _, m := range n.Bonds[i].Members {
			listed = append(listed, memberDevice(n.Bonds[i].Name, m.Name))
		}
		a.assessBond(&n.Bonds[i], e, open)
	}

	cut := true
	switch {
	case n.InterfacesOmitted > 0 || n.BondsOmitted > 0:
		a.Cut[ScopeNet] = fmt.Sprintf("%d port(s) and %d bond(s) past the agent's list limit", n.InterfacesOmitted, n.BondsOmitted)
	case len(listed) == 0:
		a.Cut[ScopeNet] = "the agent lists no port and no bond"
	default:
		cut = false
	}
	for name := range e.Links {
		switch key := (Key{CheckNetDegraded, name}); {
		case slices.Contains(listed, name):
		case cut:
			if open.has(key) {
				a.Held[key] = true
			}
		default:
			a.add(CheckNetDegraded, name, ScopeNetExpected, severityCritical, fmt.Sprintf("expected link %s is not on the node", name))
		}
	}
	a.holdUnlisted(open, ScopeNet, listed)
}

// unsure is a device listed with a field that says nothing: its alert stays, and
// nobody can say False about it either.
func (a *Assessment) unsure(key Key, open Open, why string) {
	if open.has(key) {
		a.Held[key] = true
	}
	a.Cut[scopeNetState] = why
}

func (a *Assessment) assessInterface(i *framev1beta1.NetworkInterface, e NetExpectations, open Open) {
	expected, named := e.Links[i.Name]
	// "unknown" is the kernel's own word for a driver that does not track it: no
	// verdict on a link that must be up, but the speed it shows still counts.
	carrying := i.OperState == "up" || i.OperState == "unknown"
	key := Key{CheckLinkSlow, i.Name}
	a.assessTraffic(i.Name, i.Traffic, carrying, i.OperState == "up", e, open)
	switch {
	case i.OperState == "":
		why := fmt.Sprintf("link %s gives no state", i.Name)
		a.unsure(Key{CheckNetDegraded, i.Name}, open, why)
		a.unsure(key, open, why)
		a.Cut[ScopeNet] = why
		return
	case named && i.OperState == "unknown":
		a.unsure(Key{CheckNetDegraded, i.Name}, open, fmt.Sprintf("link %s gives no state", i.Name))
	case named && i.OperState != "up":
		a.add(CheckNetDegraded, i.Name, ScopeNet, severityCritical, fmt.Sprintf("expected link %s is %s", i.Name, i.OperState))
	}

	if !carrying || i.SpeedMbps == nil {
		// A link that is down has no speed: it says nothing of the last one.
		if open.has(key) {
			a.Held[key] = true
		}
		if i.OperState == "up" {
			a.SpeedUnreported++
		}
		return
	}
	want := e.MinSpeedMbps
	if named && expected.SpeedMbps > 0 {
		want = expected.SpeedMbps
	}
	if *i.SpeedMbps < want {
		a.add(CheckLinkSlow, i.Name, ScopeNet, severityWarning,
			fmt.Sprintf("link %s runs at %d Mb/s, %d expected", i.Name, *i.SpeedMbps, want))
	}
}

// assessTraffic judges a link on what it carried. No figure closes anything: a
// link that is down, or an agent that just restarted, says nothing of the load.
//
// up separates a link that is up from one at the kernel's unknown, which carries
// but owes no speed, as for LinkSpeedLow.
func (a *Assessment) assessTraffic(name string, t *framev1beta1.LinkTraffic, carrying, up bool, e NetExpectations, open Open) {
	saturated, errs := Key{CheckSaturated, name}, Key{CheckLinkErrors, name}
	hold := func(k Key) {
		if open.has(k) {
			a.Held[k] = true
		}
	}
	if !carrying {
		hold(saturated)
		hold(errs)
		return
	}
	if t == nil {
		hold(saturated)
		hold(errs)
		a.Cut[scopeNetTraffic] = fmt.Sprintf("link %s has no rates", name)
		a.Cut[scopeNetSustained] = a.Cut[scopeNetTraffic]
		return
	}

	switch {
	case t.ErrorsPerMinute >= int64(e.ErrorsPerMinute):
		// Figures stay out of the message: it is the alert's text, written on change.
		a.add(CheckLinkErrors, name, ScopeNet, severityWarning, fmt.Sprintf("link %s is counting errors", name))
	case t.HistorySeconds < FullHistorySeconds:
		// An agent that just restarted saw no error: that clears nothing.
		hold(errs)
		a.Cut[scopeNetTraffic] = fmt.Sprintf("link %s has under five minutes of readings", name)
	}

	switch {
	case t.CapacityMbps == nil:
		hold(saturated)
		if up {
			a.CapacityUnreported++
		}
	case t.SustainedUtilizationPercent == nil:
		hold(saturated)
		a.Cut[scopeNetSustained] = fmt.Sprintf("link %s has under five minutes of readings", name)
	default:
		limit := e.SaturatedPercent
		if open.has(saturated) {
			limit = max(1, limit-saturationHysteresis)
		}
		if *t.SustainedUtilizationPercent >= limit {
			a.add(CheckSaturated, name, ScopeNet, severityWarning,
				fmt.Sprintf("link %s is near its speed over five minutes, threshold %d %%", name, e.SaturatedPercent))
		}
	}
}

func (a *Assessment) assessBond(b *framev1beta1.NetworkBond, e NetExpectations, open Open) {
	key := Key{CheckNetDegraded, b.Name}
	// An empty status is a read that said nothing, not a bond that is down.
	a.assessTraffic(b.Name, b.Traffic, b.MIIStatus != "down", b.MIIStatus == "up", e, open)
	// Loading the bonding module makes a bond0 that nobody brought up and that has
	// no member: like an unused port, it counts only if the policy names it. A bond
	// that was brought up and lost its members is a real loss, however empty.
	_, named := e.Links[b.Name]
	unused := !named && b.AdminDown && !b.MembersUnread && len(b.Members) == 0
	var bad []string
	switch {
	case b.MIIStatus == "up", unused:
	case b.MIIStatus == "":
		a.unsure(key, open, fmt.Sprintf("bond %s gives no status", b.Name))
	default:
		bad = append(bad, "is "+b.MIIStatus)
	}
	if b.Mode == "" {
		a.unsure(key, open, fmt.Sprintf("bond %s gives no mode", b.Name))
	}
	if want := e.Links[b.Name].Members; b.MembersUnread {
		a.unsure(key, open, fmt.Sprintf("bond %s: its members could not be read", b.Name))
	} else if want > 0 && int32(len(b.Members))+b.MembersOmitted < want {
		bad = append(bad, fmt.Sprintf("holds %d member(s), %d expected", len(b.Members), want))
	}
	if len(bad) > 0 {
		a.add(CheckNetDegraded, b.Name, ScopeNet, severityCritical, fmt.Sprintf("bond %s %s", b.Name, strings.Join(bad, ", ")))
	}
	if b.MembersOmitted > 0 {
		a.Cut[ScopeNet] = fmt.Sprintf("bond %s: %d member(s) past the agent's list limit", b.Name, b.MembersOmitted)
	}
	for i := range b.Members {
		m := &b.Members[i]
		var problems []string
		if m.MIIStatus != "" && m.MIIStatus != "up" {
			problems = append(problems, "link "+m.MIIStatus)
		}
		// An aggregator of 0 is a read that failed, "N/A", or none yet.
		silent := m.MIIStatus == "" || b.Mode == "" || (b.Mode == modeLACP && (b.AggregatorID == 0 || m.AggregatorID == 0))
		// A port in another aggregator has link and carries nothing: LACP did
		// not bundle it, usually the switch side.
		if b.Mode == modeLACP && b.AggregatorID > 0 && m.AggregatorID > 0 && m.AggregatorID != b.AggregatorID {
			problems = append(problems, fmt.Sprintf("in aggregator %d, the bond uses %d", m.AggregatorID, b.AggregatorID))
		}
		switch device := memberDevice(b.Name, m.Name); {
		case len(problems) > 0:
			a.add(CheckNetDegraded, device, ScopeNet, severityWarning,
				fmt.Sprintf("bond %s member %s: %s", b.Name, m.Name, strings.Join(problems, ", ")))
		case silent:
			a.unsure(Key{CheckNetDegraded, device}, open, fmt.Sprintf("bond %s member %s gives no status", b.Name, m.Name))
		}
	}
}
