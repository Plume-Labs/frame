package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// write lays out a file under root, the way sysfs presents it.
func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func port(t *testing.T, root, name string, files map[string]string) {
	t.Helper()
	base := filepath.Join(sysClassNet, name)
	full := map[string]string{filepath.Join(base, "device", "vendor"): "0x8086"}
	for k, v := range files {
		full[filepath.Join(base, k)] = v
	}
	write(t, root, full)
}

func TestObserveNetworkReadsPortsAndBonds(t *testing.T) {
	root := t.TempDir()
	port(t, root, "eno1", map[string]string{
		"operstate": "up", "speed": "10000", "duplex": "full", "mtu": "9000",
		"statistics/rx_errors": "3", "statistics/tx_dropped": "7",
		"bonding_slave/mii_status": "up", "bonding_slave/state": "active",
		"bonding_slave/link_failure_count": "2", "bonding_slave/ad_aggregator_id": "1",
	})
	port(t, root, "eno2", map[string]string{
		"operstate": "down", "speed": "-1", "duplex": "unknown", "mtu": "1500",
		"bonding_slave/mii_status": "down", "bonding_slave/ad_aggregator_id": "2",
	})
	write(t, root, map[string]string{
		"sys/class/net/bond0/mtu":                    "9000",
		"sys/class/net/bond0/bonding/mode":           "802.3ad 4",
		"sys/class/net/bond0/bonding/mii_status":     "up",
		"sys/class/net/bond0/bonding/slaves":         "eno1 eno2",
		"sys/class/net/bond0/bonding/ad_aggregator":  "1",
		"sys/class/net/bond0/bonding/ad_num_ports":   "1",
		"sys/class/net/bond0/bonding/ad_partner_mac": "02:00:00:00:00:01",
		"sys/class/net/veth0/operstate":              "up",
		"sys/class/net/bonding_masters":              "bond0",
	})
	if err := os.Symlink("../bond0", filepath.Join(root, sysClassNet, "eno1", "master")); err != nil {
		t.Fatal(err)
	}

	got := ObserveNetwork(root, time.Now())
	if got.Error != "" || len(got.Interfaces) != 2 || len(got.Bonds) != 1 {
		t.Fatalf("%+v", got)
	}
	up, down := got.Interfaces[0], got.Interfaces[1]
	if up.Name != "eno1" || *up.SpeedMbps != 10000 || up.Duplex != "full" || up.MTU != 9000 || up.Master != "bond0" ||
		up.RxErrors != 3 || up.TxDropped != 7 {
		t.Errorf("%+v", up)
	}
	if down.SpeedMbps != nil {
		t.Errorf("a down link has no speed: %d", *down.SpeedMbps)
	}
	b := got.Bonds[0]
	if b.Mode != "802.3ad" || b.AggregatorID != 1 || b.PortsInAggregator != 1 || b.PartnerMAC != "02:00:00:00:00:01" || b.MTU != 9000 {
		t.Errorf("%+v", b)
	}
	if len(b.Members) != 2 || b.Members[0].LinkFailures != 2 || b.Members[1].MIIStatus != "down" || b.Members[1].AggregatorID != 2 {
		t.Errorf("%+v", b.Members)
	}
}

func TestSpeedTheKernelCannotGiveIsAbsent(t *testing.T) {
	for _, raw := range []string{"-1", "4294967295", "", "garbage"} {
		root := t.TempDir()
		port(t, root, "eno1", map[string]string{"speed": raw})
		if s := ObserveNetwork(root, time.Now()).Interfaces[0].SpeedMbps; s != nil {
			t.Errorf("%q gave %d", raw, *s)
		}
	}
}

func TestListsPastTheirLimitAreCounted(t *testing.T) {
	root := t.TempDir()
	for i := range maxInterfaces + 3 {
		port(t, root, "p"+strconv.Itoa(i), nil)
	}
	got := ObserveNetwork(root, time.Now())
	if len(got.Interfaces) != maxInterfaces || got.InterfacesOmitted != 3 {
		t.Fatalf("%d %d", len(got.Interfaces), got.InterfacesOmitted)
	}

	root = t.TempDir()
	members := make([]string, 0, maxBondMembers+2)
	for i := range maxBondMembers + 2 {
		members = append(members, "m"+strconv.Itoa(i))
	}
	write(t, root, map[string]string{"sys/class/net/bond0/bonding/slaves": strings.Join(members, " ")})
	b := ObserveNetwork(root, time.Now()).Bonds[0]
	if len(b.Members) != maxBondMembers || b.MembersOmitted != 2 {
		t.Fatalf("%d %d", len(b.Members), b.MembersOmitted)
	}
}

func TestUnreadableTreeIsAStateNotAnEmptyNode(t *testing.T) {
	got := ObserveNetwork(filepath.Join(t.TempDir(), "absent"), time.Now())
	if got.Error == "" || got.ObservedAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestAMemberListThatCannotBeReadIsSaid(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"sys/class/net/bond0/bonding/mode": "802.3ad 4"})
	if b := ObserveNetwork(root, time.Now()).Bonds[0]; !b.MembersUnread {
		t.Fatalf("%+v", b)
	}
	write(t, root, map[string]string{"sys/class/net/bond0/bonding/slaves": ""})
	if b := ObserveNetwork(root, time.Now()).Bonds[0]; b.MembersUnread || len(b.Members) != 0 {
		t.Fatalf("an empty bond is a read: %+v", b)
	}
}

func TestAMemberNameCannotLeaveTheTree(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"sys/class/net/bond0/bonding/slaves": "../../../etc .. . etc/bonding_slave eno1",
		"etc/bonding_slave/state":            "secret",
	})
	b := ObserveNetwork(root, time.Now()).Bonds[0]
	if len(b.Members) != 1 || b.Members[0].Name != "eno1" || !b.MembersUnread {
		t.Fatalf("%+v", b)
	}
}

func TestVirtualFunctionsAreNotPorts(t *testing.T) {
	root := t.TempDir()
	port(t, root, "ens2f0", nil)
	for i := range maxInterfaces + 8 {
		port(t, root, "a"+strconv.Itoa(i), map[string]string{"device/physfn/vendor": "0x8086"})
	}
	got := ObserveNetwork(root, time.Now())
	if len(got.Interfaces) != 1 || got.Interfaces[0].Name != "ens2f0" || got.InterfacesOmitted != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAnEntryThatCannotBeLookedAtIsNotAbsent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through the mode")
	}
	root := t.TempDir()
	port(t, root, "eno1", nil)
	dir := filepath.Join(root, sysClassNet, "eno1")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got := ObserveNetwork(root, time.Now())
	if len(got.Bonds) != 0 || len(got.Interfaces) != 1 || got.Interfaces[0].Name != "eno1" || got.Interfaces[0].OperState != "" {
		t.Fatalf("%+v", got)
	}
}

// Neither taken for a virtual function, and so dropped.
func TestADeviceThatCannotBeLookedAtIsStillAPort(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads through the mode")
	}
	root := t.TempDir()
	port(t, root, "eno1", map[string]string{"operstate": "up"})
	dir := filepath.Join(root, sysClassNet, "eno1", "device")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	got := ObserveNetwork(root, time.Now())
	if len(got.Interfaces) != 1 || got.Interfaces[0].Name != "eno1" {
		t.Fatalf("%+v", got)
	}
}

// bonding_masters is a plain file among the links, on every node with bonds.
func TestBondingMastersIsNotABond(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"sys/class/net/bonding_masters": "bond0"})
	got := ObserveNetwork(root, time.Now())
	if len(got.Bonds)+len(got.Interfaces) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestBondsPastTheirLimitAreCounted(t *testing.T) {
	root := t.TempDir()
	for i := range maxBonds + 2 {
		write(t, root, map[string]string{"sys/class/net/bond" + strconv.Itoa(i) + "/bonding/slaves": ""})
	}
	got := ObserveNetwork(root, time.Now())
	if len(got.Bonds) != maxBonds || got.BondsOmitted != 2 {
		t.Fatalf("%d %d", len(got.Bonds), got.BondsOmitted)
	}
}

// One value past its CRD bound gets the whole status refused.
func TestValuesFitTheirFields(t *testing.T) {
	long := strings.Repeat("x", 300)
	root := t.TempDir()
	port(t, root, "eno1", map[string]string{"operstate": long, "duplex": long})
	write(t, root, map[string]string{
		"sys/class/net/bond0/bonding/mode":            long,
		"sys/class/net/bond0/bonding/mii_status":      long,
		"sys/class/net/bond0/bonding/ad_partner_mac":  long,
		"sys/class/net/bond0/bonding/slaves":          "eno1",
		"sys/class/net/eno1/bonding_slave/mii_status": long,
		"sys/class/net/eno1/bonding_slave/state":      long,
	})
	got := ObserveNetwork(root, time.Now())
	i, b := got.Interfaces[0], got.Bonds[0]
	m := b.Members[0]
	for name, tt := range map[string]struct {
		s   string
		max int
	}{
		"operstate": {i.OperState, 32}, "duplex": {i.Duplex, 16}, "mode": {b.Mode, 32}, "bond mii": {b.MIIStatus, 16},
		"partner": {b.PartnerMAC, 32}, "member mii": {m.MIIStatus, 16}, "state": {m.State, 16},
	} {
		if len(tt.s) != tt.max {
			t.Errorf("%s: %d", name, len(tt.s))
		}
	}
}

func TestAnIntegerPastItsWidthIsNothing(t *testing.T) {
	root := t.TempDir()
	port(t, root, "eno1", map[string]string{"mtu": "4294967295"})
	port(t, root, "eno2", map[string]string{"mtu": "-1"})
	got := ObserveNetwork(root, time.Now())
	if got.Interfaces[0].MTU != 0 || got.Interfaces[1].MTU != 0 {
		t.Fatalf("%+v", got.Interfaces)
	}
}

func TestEveryCounterAndStateIsRead(t *testing.T) {
	root := t.TempDir()
	port(t, root, "eno1", map[string]string{
		"statistics/rx_errors": "1", "statistics/tx_errors": "2", "statistics/rx_dropped": "3", "statistics/tx_dropped": "4",
	})
	write(t, root, map[string]string{
		"sys/class/net/bond0/bonding/slaves":     "eno1",
		"sys/class/net/eno1/bonding_slave/state": "backup",
	})
	got := ObserveNetwork(root, time.Now())
	i := got.Interfaces[0]
	if i.RxErrors != 1 || i.TxErrors != 2 || i.RxDropped != 3 || i.TxDropped != 4 {
		t.Fatalf("%+v", i)
	}
	if got.Bonds[0].Members[0].State != "backup" {
		t.Fatalf("%+v", got.Bonds[0])
	}
}

func TestAHungReadDoesNotHoldTheCaller(t *testing.T) {
	release := make(chan struct{})
	reads := 0
	c := &NetworkCollector{read: func() *framev1beta1.MachineNetwork {
		reads++ // one at a time: nothing else runs while the first hangs
		<-release
		return &framev1beta1.MachineNetwork{Error: "done"}
	}}
	if got := c.Latest(time.Millisecond); got != nil {
		t.Fatalf("%+v", got)
	}
	if got := c.Latest(time.Millisecond); got != nil {
		t.Fatalf("%+v", got)
	}
	close(release)
	if got := c.Latest(time.Second); got == nil || got.Error != "done" {
		t.Fatalf("%+v", got)
	}
	if reads != 1 {
		t.Fatalf("%d reads at once", reads)
	}
}

func TestAReadThatHangsLeavesTheLastReport(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	first := true
	c := &NetworkCollector{read: func() *framev1beta1.MachineNetwork {
		if !first {
			<-block
		}
		first = false
		return &framev1beta1.MachineNetwork{Error: "first"}
	}}
	if got := c.Latest(time.Second); got == nil || got.Error != "first" {
		t.Fatalf("%+v", got)
	}
	got := c.Latest(time.Millisecond)
	if got == nil || got.Error != "first" {
		t.Fatalf("%+v", got)
	}
	got.Error = "scribbled on" // a copy: the caller patches and keeps it
	if again := c.Latest(time.Millisecond); again == nil || again.Error != "first" {
		t.Fatalf("%+v", again)
	}
}

func TestEachCallAfterAFinishedReadStartsAnother(t *testing.T) {
	var reads atomic.Int32
	c := &NetworkCollector{read: func() *framev1beta1.MachineNetwork {
		reads.Add(1)
		return &framev1beta1.MachineNetwork{}
	}}
	c.Latest(time.Second)
	c.Latest(time.Second)
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d reads", n)
	}
}
