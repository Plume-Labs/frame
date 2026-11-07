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

// Every empty field below is a read that said nothing, on a device still listed.
func TestAnEmptyFieldNeitherClearsNorRaises(t *testing.T) {
	member, bond := Key{CheckNetDegraded, "bond0/eno2"}, Key{CheckNetDegraded, "bond0"}
	port := Key{CheckNetDegraded, "eno1"}
	expected := expecting(framev1beta1.ExpectedLink{Interface: "eno1"}, framev1beta1.ExpectedLink{Interface: "bond0", Members: 2})
	tests := []struct {
		name   string
		mutate func(*framev1beta1.MachineNetwork)
		key    Key
	}{
		{"member status", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members[1].MIIStatus = "" }, member},
		{"bond mode", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Mode = "" }, member},
		{"bond aggregator", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].AggregatorID = 0 }, member},
		{"member aggregator", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Members[1].AggregatorID = 0 }, member},
		{"bond status", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].MIIStatus = "" }, bond},
		{"mode of a bond with no member", func(n *framev1beta1.MachineNetwork) { n.Bonds[0].Mode = "" }, bond},
		{"bond members", func(n *framev1beta1.MachineNetwork) {
			n.Bonds[0].Members, n.Bonds[0].MembersUnread = nil, true
		}, bond},
		{"operstate", func(n *framev1beta1.MachineNetwork) { n.Interfaces[0].OperState = "" }, port},
		{"operstate unknown", func(n *framev1beta1.MachineNetwork) { n.Interfaces[0].OperState = "unknown" }, port},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := healthyNet()
			tt.mutate(n)
			a := assessNet(n, expected, nil)
			if len(a.Findings) != 0 {
				t.Fatalf("raised: %+v", a.Findings)
			}
			if v := a.Verdict(CheckNetDegraded, nil); v.Status != "Unknown" || v.Reason != "ReadUnavailable" {
				t.Fatalf("%+v", v)
			}
			open := Open{tt.key: ScopeNet}
			a = assessNet(n, expected, open)
			if !a.Keeps(tt.key, ScopeNet) {
				t.Fatal("cleared")
			}
			if v := a.Verdict(CheckNetDegraded, open); v.Status != "True" || v.Reason != "LastReadHeld" {
				t.Fatalf("%+v", v)
			}
		})
	}
}

// Not named, a port owes no state, but nothing can be said of one that gives none.
func TestAnUnnamedPortWithNoStateIsNotHealthy(t *testing.T) {
	n := healthyNet()
	n.Interfaces[2] = framev1beta1.NetworkInterface{Name: "eno3"}
	slow, down := Key{CheckLinkSlow, "eno3"}, Key{CheckNetDegraded, "eno3"}
	open := Open{slow: ScopeNet, down: ScopeNet}
	a := assessNet(n, noNet, open)
	if !a.Keeps(slow, ScopeNet) || !a.Keeps(down, ScopeNet) {
		t.Fatalf("%+v", a.Held)
	}
	a = assessNet(n, noNet, nil)
	for _, c := range []string{CheckNetDegraded, CheckLinkSlow} {
		if v := a.Verdict(c, nil); v.Status != "Unknown" {
			t.Errorf("%s: %+v", c, v)
		}
	}
}

// The speed of a link whose driver tracks no state is still a speed.
func TestAnUnknownStateStillHasItsSpeedChecked(t *testing.T) {
	n := healthyNet()
	n.Interfaces[2].OperState, n.Interfaces[2].SpeedMbps = "unknown", new(int32(100))
	if a := assessNet(n, noNet, nil); !keys(a)[Key{CheckLinkSlow, "eno3"}] {
		t.Fatalf("%+v", a.Findings)
	}
	// Such a driver may never give one: that keeps no node Unknown for good.
	n.Interfaces[2].SpeedMbps = nil
	if v := assessNet(n, noNet, nil).Verdict(CheckLinkSlow, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
	n.Interfaces[2].SpeedMbps = new(int32(10000))
	if v := assessNet(n, noNet, nil).Verdict(CheckNetDegraded, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

// What a silent field costs is NetworkDegraded: the speeds were read.
func TestASilentFieldLeavesLinkSpeedAlone(t *testing.T) {
	named := healthyNet()
	named.Interfaces[0].OperState = "unknown"
	member := healthyNet()
	member.Bonds[0].Members[1].AggregatorID = 0
	for name, tt := range map[string]struct {
		n *framev1beta1.MachineNetwork
		e NetExpectations
	}{
		"named link at unknown":     {named, expecting(framev1beta1.ExpectedLink{Interface: "eno1"})},
		"member with no aggregator": {member, noNet},
	} {
		a := assessNet(tt.n, tt.e, nil)
		if v := a.Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
			t.Errorf("%s: %+v", name, v)
		}
		if v := a.Verdict(CheckLinkSlow, nil); v.Status != "False" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// Outside LACP an aggregator of 0 is the normal case, not a read that failed.
func TestAggregatorsMeanNothingOutsideLACP(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].Mode = "active-backup"
	n.Bonds[0].Members[0].AggregatorID, n.Bonds[0].Members[1].AggregatorID = 0, 7
	a := assessNet(n, noNet, nil)
	if v := a.Verdict(CheckNetDegraded, nil); v.Status != "False" || len(a.Findings) != 0 {
		t.Fatalf("%+v %+v", v, a.Findings)
	}
}

func TestAKnownDownMemberIsRaisedWhateverElseIsMissing(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].Members[1].MIIStatus, n.Bonds[0].Members[1].AggregatorID = "down", 0
	if !keys(assessNet(n, noNet, nil))[Key{CheckNetDegraded, "bond0/eno2"}] {
		t.Fatal("not raised")
	}
}

func TestABondDownAndShortIsOneFinding(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].MIIStatus = "down"
	n.Bonds[0].Members = n.Bonds[0].Members[:1]
	var got []Finding
	for _, f := range assessNet(n, expecting(framev1beta1.ExpectedLink{Interface: "bond0", Members: 2}), nil).Findings {
		if f.Device == "bond0" {
			got = append(got, f)
		}
	}
	if len(got) != 1 || got[0].Severity != severityCritical {
		t.Fatalf("%+v", got)
	}
}

// The bond the module makes when it loads must not raise a critical nothing can
// silence; the same bond named, holding a member, or brought up stays a fault.
func TestAnUnusedBondCountsOnlyIfNamed(t *testing.T) {
	bond9 := Key{CheckNetDegraded, "bond9"}
	empty := func() *framev1beta1.MachineNetwork {
		n := healthyNet()
		n.Bonds = append(n.Bonds, framev1beta1.NetworkBond{Name: "bond9", Mode: "balance-rr", MIIStatus: "down", AdminDown: true})
		return n
	}
	if a := assessNet(empty(), noNet, nil); keys(a)[bond9] || a.Verdict(CheckNetDegraded, nil).Status != "False" {
		t.Fatalf("unused bond judged: %+v", a.Findings)
	}
	if !keys(assessNet(empty(), expecting(framev1beta1.ExpectedLink{Interface: "bond9"}), nil))[bond9] {
		t.Fatal("named bond down: not raised")
	}
	n := empty()
	n.Bonds[1].AdminDown = false
	if !keys(assessNet(n, noNet, nil))[bond9] {
		t.Fatal("bond brought up that lost all its members: not raised")
	}
	n = empty()
	n.Bonds[1].MembersUnread = true
	if keys(assessNet(n, noNet, nil))[bond9] != true {
		t.Fatal("bond whose members could not be read: not raised")
	}
	n = empty()
	n.Bonds[1].Members = []framev1beta1.BondMember{{Name: "eno3", MIIStatus: "up"}}
	if !keys(assessNet(n, noNet, nil))[bond9] {
		t.Fatal("bond with a member down: not raised")
	}
}

func TestSeverities(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].Members[1].MIIStatus = "down"
	n.Interfaces[0].SpeedMbps = new(int32(100))
	e := expecting(framev1beta1.ExpectedLink{Interface: "eno9"}, framev1beta1.ExpectedLink{Interface: "eno3"})
	want := map[Key]string{
		{CheckNetDegraded, "bond0/eno2"}: severityWarning,
		{CheckLinkSlow, "eno1"}:          severityWarning,
		{CheckNetDegraded, "eno9"}:       severityCritical,
		{CheckNetDegraded, "eno3"}:       severityCritical,
	}
	for _, f := range assessNet(n, e, nil).Findings {
		if want[f.Key] != f.Severity {
			t.Errorf("%+v", f)
		}
		delete(want, f.Key)
	}
	if len(want) != 0 {
		t.Fatalf("missing %+v", want)
	}
}

func TestALinkUpWithNoSpeedIsNotHealthy(t *testing.T) {
	n := healthyNet()
	n.Interfaces[0].SpeedMbps = nil
	a := assessNet(n, noNet, nil)
	if v := a.Verdict(CheckLinkSlow, nil); v.Status != "Unknown" || v.Reason != "SpeedNotReported" {
		t.Fatalf("%+v", v)
	}
	if v := a.Verdict(CheckNetDegraded, nil); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
}

func TestAPortDownOwesNoSpeedAndHasNoneChecked(t *testing.T) {
	n := healthyNet()
	n.Interfaces[2].SpeedMbps = new(int32(10)) // eno3, down
	a := assessNet(n, noNet, nil)
	if v := a.Verdict(CheckLinkSlow, nil); v.Status != "False" || len(a.Findings) != 0 {
		t.Fatalf("%+v %+v", v, a.Findings)
	}
}

func TestSpeedLimitIsMetAtTheExpectedValue(t *testing.T) {
	n := healthyNet()
	n.Interfaces[0].SpeedMbps = new(framev1beta1.DefaultMinLinkSpeedMbps)
	if a := assessNet(n, noNet, nil); len(a.Findings) != 0 {
		t.Fatalf("%+v", a.Findings)
	}
}

func TestPolicyMinimumReplacesTheDefault(t *testing.T) {
	e := NetExpectationsFrom(&framev1beta1.NetworkHealthPolicy{Spec: framev1beta1.NetworkHealthPolicySpec{MinLinkSpeedMbps: 25000}}, "node-a")
	if !keys(assessNet(healthyNet(), e, nil))[Key{CheckLinkSlow, "eno1"}] {
		t.Fatal("10 Gb/s passed a minimum of 25")
	}
}

func TestAReportWithNothingListedIsNotHealthy(t *testing.T) {
	seen := metav1.NewTime(now)
	k := Key{CheckNetDegraded, "eno1"}
	e := expecting(framev1beta1.ExpectedLink{Interface: "eno1"})
	open := Open{k: ScopeNetExpected}
	a := assessNet(&framev1beta1.MachineNetwork{ObservedAt: &seen}, e, open)
	if keys(a)[k] {
		t.Fatal("raised as absent")
	}
	if !a.Keeps(k, ScopeNetExpected) {
		t.Fatal("cleared")
	}
	if v := assessNet(&framev1beta1.MachineNetwork{ObservedAt: &seen}, noNet, nil).Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

func TestAnUndatedReportIsUnread(t *testing.T) {
	n := healthyNet()
	n.ObservedAt = nil
	if v := assessNet(n, noNet, nil).Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

func TestBondsPastTheCutAreNotHealthy(t *testing.T) {
	n := healthyNet()
	n.BondsOmitted = 1
	if v := assessNet(n, noNet, nil).Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

func TestMembersPastTheCutAreNotHealthy(t *testing.T) {
	n := healthyNet()
	n.Bonds[0].MembersOmitted = 1
	if v := assessNet(n, noNet, nil).Verdict(CheckNetDegraded, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}

// A typo in the policy must not leave a critical alert nobody can close.
func TestAnAbsentLinkAlertEndsWithItsExpectation(t *testing.T) {
	k := Key{CheckNetDegraded, "eno9"}
	e := expecting(framev1beta1.ExpectedLink{Interface: "eno9"})
	a := assessNet(healthyNet(), e, nil)
	var scope Scope
	for _, f := range a.Findings {
		if f.Key == k {
			scope = f.Scope
		}
	}
	if scope != ScopeNetExpected {
		t.Fatalf("%q", scope)
	}
	open := Open{k: scope}
	a = assessNet(healthyNet(), noNet, open)
	if a.Keeps(k, scope) {
		t.Fatal("held after the policy dropped it")
	}
	if v := a.Verdict(CheckNetDegraded, open); v.Status != "False" {
		t.Fatalf("%+v", v)
	}
	// Still expected, but a silent agent says nothing about it.
	n := healthyNet()
	n.Error = "EIO"
	if !assessNet(n, noNet, open).Keeps(k, scope) {
		t.Fatal("cleared on a failed read")
	}
}

func TestAnUnreadPolicyLeavesLinksUnknownAndStorageAlone(t *testing.T) {
	k := Key{CheckNetDegraded, "bond0/eno2"}
	e := noNet
	e.Unread = "the NetworkHealthPolicy could not be read"
	n := healthyNet()
	n.Bonds[0].Members[1].MIIStatus = "down"
	a := assessNet(n, e, Open{k: ScopeNet})
	if len(a.Findings) != 0 || !a.Keeps(k, ScopeNet) {
		t.Fatalf("%+v", a)
	}
	if v := a.Verdict(CheckLinkSlow, nil); v.Status != "Unknown" {
		t.Fatalf("%+v", v)
	}
}
