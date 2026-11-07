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

// NetExpectations is the NetworkHealthPolicy as it applies to one node.
type NetExpectations struct {
	MinSpeedMbps int32
	Links        map[string]framev1beta1.ExpectedLink
	// Unread says why the policy could not be read: links are then unassessed,
	// and storage, which does not depend on it, goes on.
	Unread string
}

// NetExpectationsFrom resolves the policy for a node; nil means the default.
func NetExpectationsFrom(p *framev1beta1.NetworkHealthPolicy, node string) NetExpectations {
	e := NetExpectations{MinSpeedMbps: framev1beta1.DefaultMinLinkSpeedMbps, Links: map[string]framev1beta1.ExpectedLink{}}
	if p == nil {
		return e
	}
	if p.Spec.MinLinkSpeedMbps > 0 {
		e.MinSpeedMbps = p.Spec.MinLinkSpeedMbps
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

func (a *Assessment) assessBond(b *framev1beta1.NetworkBond, e NetExpectations, open Open) {
	key := Key{CheckNetDegraded, b.Name}
	// Loading the bonding module makes a bond0 with no member, which is down and
	// carries nothing: like an unused port, it counts only if the policy names it.
	_, named := e.Links[b.Name]
	unused := !named && !b.MembersUnread && len(b.Members) == 0 && b.MembersOmitted == 0
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
