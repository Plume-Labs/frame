package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
