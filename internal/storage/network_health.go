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

// NetExpectations is the NetworkHealthPolicy as it applies to one node.
type NetExpectations struct {
	MinSpeedMbps int32
	Links        map[string]framev1beta1.ExpectedLink
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
	switch {
	case n == nil:
		a.Unread[ScopeNet] = "the agent has not reported the node's links"
		return
	case n.Error != "":
		a.Unread[ScopeNet] = "read failed: " + n.Error
		return
	case n.ObservedAt == nil || now.Sub(n.ObservedAt.Time) > AgentSilentAfter:
		a.Unread[ScopeNet] = "the agent has stopped reporting"
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
		a.assessBond(&n.Bonds[i], e)
	}

	cut := n.InterfacesOmitted > 0 || n.BondsOmitted > 0
	if cut {
		a.Cut[ScopeNet] = fmt.Sprintf("%d port(s) and %d bond(s) past the agent's list limit", n.InterfacesOmitted, n.BondsOmitted)
	}
	for name := range e.Links {
		if !cut && !slices.Contains(listed, name) {
			a.add(CheckNetDegraded, name, ScopeNet, severityCritical, fmt.Sprintf("expected link %s is not on the node", name))
		}
	}
	a.holdUnlisted(open, ScopeNet, listed)
}

func (a *Assessment) assessInterface(i *framev1beta1.NetworkInterface, e NetExpectations, open Open) {
	expected, named := e.Links[i.Name]
	if named && i.OperState != "up" {
		a.add(CheckNetDegraded, i.Name, ScopeNet, severityCritical, fmt.Sprintf("expected link %s is %s", i.Name, i.OperState))
	}

	key := Key{CheckLinkSlow, i.Name}
	if i.OperState != "up" || i.SpeedMbps == nil {
		// A link that is down has no speed: it says nothing of the last one.
		if open.has(key) {
			a.Held[key] = true
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

func (a *Assessment) assessBond(b *framev1beta1.NetworkBond, e NetExpectations) {
	if b.MIIStatus != "" && b.MIIStatus != "up" {
		a.add(CheckNetDegraded, b.Name, ScopeNet, severityCritical, fmt.Sprintf("bond %s is %s", b.Name, b.MIIStatus))
	}
	if want := e.Links[b.Name].Members; want > 0 && int32(len(b.Members))+b.MembersOmitted < want {
		a.add(CheckNetDegraded, b.Name, ScopeNet, severityCritical,
			fmt.Sprintf("bond %s holds %d member(s), %d expected", b.Name, len(b.Members), want))
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
		// A port in another aggregator has link and carries nothing: LACP did
		// not bundle it, usually the switch side.
		if b.Mode == modeLACP && b.AggregatorID > 0 && m.AggregatorID > 0 && m.AggregatorID != b.AggregatorID {
			problems = append(problems, fmt.Sprintf("in aggregator %d, the bond uses %d", m.AggregatorID, b.AggregatorID))
		}
		if len(problems) > 0 {
			a.add(CheckNetDegraded, memberDevice(b.Name, m.Name), ScopeNet, severityWarning,
				fmt.Sprintf("bond %s member %s: %s", b.Name, m.Name, strings.Join(problems, ", ")))
		}
	}
}
