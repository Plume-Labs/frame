package storage

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// healthyNet is a node on a two-port LACP bond and a management port.
func healthyNet() *framev1beta1.MachineNetwork {
	seen := metav1.NewTime(now.Add(-time.Minute))
	return &framev1beta1.MachineNetwork{
		ObservedAt: &seen,
		Interfaces: []framev1beta1.NetworkInterface{
			{Name: "eno1", OperState: "up", SpeedMbps: new(int32(10000)), Master: "bond0"},
			{Name: "eno2", OperState: "up", SpeedMbps: new(int32(10000)), Master: "bond0"},
			{Name: "eno3", OperState: "down"},
		},
		Bonds: []framev1beta1.NetworkBond{{
			Name: "bond0", Mode: modeLACP, MIIStatus: "up", AggregatorID: 1, PortsInAggregator: 2,
			Members: []framev1beta1.BondMember{
				{Name: "eno1", MIIStatus: "up", AggregatorID: 1},
				{Name: "eno2", MIIStatus: "up", AggregatorID: 1},
			},
		}},
	}
}

func assessNet(n *framev1beta1.MachineNetwork, e NetExpectations, open Open) Assessment {
	return Assess(&framev1beta1.FrameMachineStatus{Network: n}, defaults, e, false, open, now)
}

func expecting(links ...framev1beta1.ExpectedLink) NetExpectations {
	for i := range links {
		links[i].Node = "node-a"
	}
	return NetExpectationsFrom(&framev1beta1.NetworkHealthPolicy{Spec: framev1beta1.NetworkHealthPolicySpec{Links: links}}, "node-a")
}

func TestHealthyLinksHaveNoFindingAndAnUnusedPortIsFine(t *testing.T) {
	a := assessNet(healthyNet(), noNet, nil)
	if len(a.Findings) != 0 {
		t.Fatalf("%+v", a.Findings)
	}
	for _, c := range []string{CheckNetDegraded, CheckLinkSlow} {
		if v := a.Verdict(c, nil); v.Status != "False" {
			t.Errorf("%s: %+v", c, v)
		}
	}
}

func TestLinkProblemsAreFound(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*framev1beta1.MachineNetwork)
		e      NetExpectations
		want   Key
	}{
		{"link under the default", func(n *framev1beta1.MachineNetwork) { n.Interfaces[0].SpeedMbps = new(int32(100)) }, noNet, Key{CheckLinkSlow, "eno1"}},
		{"link under its own expectation", func(n *framev1beta1.MachineNetwork) { n.Interfaces[0].SpeedMbps = new(int32(1000)) },
			expecting(framev1beta1.ExpectedLink{Interface: "eno1", SpeedMbps: 10000}), Key{CheckLinkSlow, "eno1"}},
		{"member down", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members[1].MIIStatus = "down" }, noNet, Key{CheckNetDegraded, "bond0/eno2"}},
		{"member up in the wrong aggregator", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members[1].AggregatorID = 2 }, noNet, Key{CheckNetDegraded, "bond0/eno2"}},
		{"bond down", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].MIIStatus = "down" }, noNet, Key{CheckNetDegraded, "bond0"}},
		{"member gone from the bond", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members = n.Bonds[0].Members[:1] },
			expecting(framev1beta1.ExpectedLink{Interface: "bond0", Members: 2}), Key{CheckNetDegraded, "bond0"}},
		{"required port down", func(*framev1beta1.MachineNetwork) {}, expecting(framev1beta1.ExpectedLink{Interface: "eno3"}), Key{CheckNetDegraded, "eno3"}},
		{"required port absent", func(*framev1beta1.MachineNetwork) {}, expecting(framev1beta1.ExpectedLink{Interface: "eno9"}), Key{CheckNetDegraded, "eno9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := healthyNet()
			tt.mutate(n)
			a := assessNet(n, tt.e, nil)
			if got := keys(a); len(got) != 1 || !got[tt.want] {
				t.Fatalf("findings %v, want only %v", got, tt.want)
			}
			if v := a.Verdict(tt.want.Check, nil); v.Status != "True" {
				t.Fatalf("%+v", v)
			}
		})
	}
}

func TestExpectationsOfAnotherNodeDoNotApply(t *testing.T) {
	p := &framev1beta1.NetworkHealthPolicy{Spec: framev1beta1.NetworkHealthPolicySpec{
		Links: []framev1beta1.ExpectedLink{{Node: "node-b", Interface: "eno3"}},
	}}
	if a := assessNet(healthyNet(), NetExpectationsFrom(p, "node-a"), nil); len(a.Findings) != 0 {
		t.Fatalf("%+v", a.Findings)
	}
}

func TestAnUnreadableReportNeitherRaisesNorClears(t *testing.T) {
	old := metav1.NewTime(now.Add(-AgentSilentAfter - time.Minute))
	tests := map[string]func(*framev1beta1.MachineNetwork){
		"read failed":  func(n *framev1beta1.MachineNetwork) { n.Error = "permission denied" },
		"agent silent": func(n *framev1beta1.MachineNetwork) { n.ObservedAt = &old },
	}
	k := Key{CheckNetDegraded, "bond0/eno2"}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			n := healthyNet()
			n.Bonds[0].Members[1].MIIStatus = "down"
			mutate(n)
			a := assessNet(n, noNet, nil)
			if len(a.Findings) != 0 {
				t.Fatalf("raised: %+v", a.Findings)
			}
			if v := a.Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
				t.Fatalf("%+v", v)
			}
			healthy := healthyNet()
			mutate(healthy)
			open := Open{k: ScopeNet}
			a = assessNet(healthy, noNet, open)
			if !a.Keeps(k, ScopeNet) {
				t.Fatal("cleared")
			}
		})
	}
	if a := assessNet(nil, noNet, nil); a.Verdict(CheckLinkSlow, nil).Status != "Unknown" {
		t.Fatal("no report is not healthy")
	}
}

func TestReadsThatSayNothingKeepTheAlert(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*framev1beta1.MachineNetwork)
		key    Key
	}{
		{"member gone from the list", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members = n.Bonds[0].Members[:1] }, Key{CheckNetDegraded, "bond0/eno2"}},
		{"bond gone", func(n *framev1beta1.MachineNetwork) { n.Bonds = nil }, Key{CheckNetDegraded, "bond0/eno2"}},
		{"slow link went down", func(n *framev1beta1.MachineNetwork) {
			n.Interfaces[0].OperState, n.Interfaces[0].SpeedMbps = "down", nil
		}, Key{CheckLinkSlow, "eno1"}},
		{"slow link lost its speed", func(n *framev1beta1.MachineNetwork) { n.Interfaces[0].SpeedMbps = nil }, Key{CheckLinkSlow, "eno1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := healthyNet()
			tt.mutate(n)
			if !assessNet(n, noNet, Open{tt.key: ScopeNet}).Keeps(tt.key, ScopeNet) {
				t.Fatal("cleared")
			}
		})
	}
}

func TestRestoredLinkClears(t *testing.T) {
	k := Key{CheckNetDegraded, "bond0/eno2"}
	a := assessNet(healthyNet(), noNet, Open{k: ScopeNet})
	if a.Keeps(k, ScopeNet) || keys(a)[k] {
		t.Fatal("still open")
	}
}

func TestAPartialListIsNotHealthyAndRaisesNoFalseAbsence(t *testing.T) {
	n := healthyNet()
	n.InterfacesOmitted = 1
	a := assessNet(n, expecting(framev1beta1.ExpectedLink{Interface: "eno9"}), nil)
	if keys(a)[Key{CheckNetDegraded, "eno9"}] {
		t.Fatal("a link past the cut reported absent")
	}
	if v := a.Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

func TestMembersPastTheCutStillCount(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].Members = n.Bonds[0].Members[:1]
	n.Bonds[0].MembersOmitted = 1
	a := assessNet(n, expecting(framev1beta1.ExpectedLink{Interface: "bond0", Members: 2}), nil)
	if len(a.Findings) != 0 {
		t.Fatalf("%+v", a.Findings)
	}
}
