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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	framev1beta1 "github.com/Plume-Labs/frame/api/frame/v1beta1"
)

// Reading the node's links. Files only, under root: no command, so nothing
// joins the command list in localstorage.go. /proc/net/bonding is not used:
// /proc/net follows the reader's network namespace, which is the pod's, while
// /sys here is the host's mount, and sysfs carries the same bonding state.
//
// Physical ports are the entries with a device link; veth, flannel and bridges
// come and go with pods and say nothing about the machine, and an SR-IOV virtual
// function, which has a device link too, would push real ports past the limit.

const (
	sysClassNet = "sys/class/net"

	maxInterfaces  = 32
	maxBonds       = 8
	maxBondMembers = 16
	maxNameLen     = 15
)

// NetworkCollector reads in the background: speed, duplex and the bond's lists go
// through the driver, and one that hangs must not hold up the agent's tick. A read
// that never returns leaves the last report to age, which the controller sees.
type NetworkCollector struct {
	read func() *framev1beta1.MachineNetwork

	mu      sync.Mutex
	last    *framev1beta1.MachineNetwork
	running chan struct{}
}

// NewNetworkCollector reads root's links.
func NewNetworkCollector(root string) *NetworkCollector {
	windows := newTrafficWindows(filepath.Join(root, sysClassNet))
	return &NetworkCollector{read: func() *framev1beta1.MachineNetwork {
		n := ObserveNetwork(root, time.Now())
		// Its own time: a slow driver above must not read as a long interval.
		windows.annotate(n, time.Now())
		return n
	}}
}

// Latest returns the newest report, waiting at most wait for a read started now
// or already running. nil only before the first read has ever finished.
func (c *NetworkCollector) Latest(wait time.Duration) *framev1beta1.MachineNetwork {
	c.mu.Lock()
	done := c.running
	if done == nil {
		done = make(chan struct{})
		c.running = done
		go func() {
			res := c.read()
			c.mu.Lock()
			c.last, c.running = res, nil
			c.mu.Unlock()
			close(done)
		}()
	}
	c.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.DeepCopy()
}

// ObserveNetwork reads root's links. It never fails: a tree it cannot read
// comes back with Error set, which is a state and not an empty node.
func ObserveNetwork(root string, now time.Time) *framev1beta1.MachineNetwork {
	seen := metav1.NewTime(now)
	out := &framev1beta1.MachineNetwork{ObservedAt: &seen}
	base := filepath.Join(root, sysClassNet)
	entries, err := os.ReadDir(base)
	if err != nil {
		out.Error = clipRunes(err.Error(), 256)
		return out
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	port := func(i framev1beta1.NetworkInterface) {
		if len(out.Interfaces) == maxInterfaces {
			out.InterfacesOmitted++
			return
		}
		out.Interfaces = append(out.Interfaces, i)
	}
	for _, name := range names {
		dir := filepath.Join(base, name)
		bond, berr := exists(filepath.Join(dir, "bonding"))
		vf, verr := exists(filepath.Join(dir, "device", "physfn"))
		device, derr := exists(filepath.Join(dir, "device"))
		switch {
		// Unreadable is not absent: a port with no state, which the controller
		// will not take for healthy, and neither a bond nor nothing.
		case errors.Join(berr, verr, derr) != nil:
			port(framev1beta1.NetworkInterface{Name: clipRunes(name, maxNameLen)})
		case bond:
			if len(out.Bonds) == maxBonds {
				out.BondsOmitted++
				continue
			}
			out.Bonds = append(out.Bonds, readBond(base, name))
		case device && !vf:
			port(readInterface(dir, name))
		}
	}
	return out
}

// exists says whether path is there, and gives the error when it cannot tell.
// ENOTDIR is an answer: bonding_masters sits among the links as a plain file.
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return err == nil, err
}

// text is a sysfs attribute, trimmed. A read error is "": a link that is down
// answers EINVAL on speed and duplex, which is how the kernel says it has none.
// So "" is never a state: the controller reads it as nothing known.
func text(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func number(path string) int64 {
	n, err := strconv.ParseInt(text(path), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func int32At(path string) int32 {
	n := number(path)
	if n < 0 || n > 1<<31-1 {
		return 0
	}
	return int32(n)
}

// firstField is the name in attributes printed as "802.3ad 4".
func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func readInterface(dir, name string) framev1beta1.NetworkInterface {
	stat := func(counter string) int64 { return number(filepath.Join(dir, "statistics", counter)) }
	iface := framev1beta1.NetworkInterface{
		Name:      clipRunes(name, maxNameLen),
		OperState: clipRunes(text(filepath.Join(dir, "operstate")), 32),
		Duplex:    clipRunes(text(filepath.Join(dir, "duplex")), 16),
		MTU:       int32At(filepath.Join(dir, "mtu")),
		RxErrors:  stat("rx_errors"),
		TxErrors:  stat("tx_errors"),
		RxDropped: stat("rx_dropped"),
		TxDropped: stat("tx_dropped"),
	}
	// -1 and its unsigned reading both mean unknown.
	if speed := number(filepath.Join(dir, "speed")); speed > 0 && speed < 1<<31-1 {
		s := int32(speed)
		iface.SpeedMbps = &s
	}
	if target, err := os.Readlink(filepath.Join(dir, "master")); err == nil {
		iface.Master = clipRunes(filepath.Base(target), maxNameLen)
	}
	return iface
}

func readBond(base, name string) framev1beta1.NetworkBond {
	dir := filepath.Join(base, name)
	attr := func(a string) string { return filepath.Join(dir, "bonding", a) }
	bond := framev1beta1.NetworkBond{
		Name:              clipRunes(name, maxNameLen),
		Mode:              clipRunes(firstField(text(attr("mode"))), 32),
		MIIStatus:         clipRunes(text(attr("mii_status")), 16),
		MTU:               int32At(filepath.Join(dir, "mtu")),
		AggregatorID:      int32At(attr("ad_aggregator")),
		PortsInAggregator: int32At(attr("ad_num_ports")),
		PartnerMAC:        clipRunes(text(attr("ad_partner_mac")), 32),
	}
	if flags, err := strconv.ParseUint(text(filepath.Join(dir, "flags")), 0, 64); err == nil {
		bond.AdminDown = flags&syscall.IFF_UP == 0
	}
	slaves, err := os.ReadFile(attr("slaves"))
	bond.MembersUnread = err != nil
	for member := range strings.FieldsSeq(string(slaves)) {
		// The kernel forbids a slash in a name; a tree that has one is no sysfs.
		if strings.ContainsRune(member, '/') || member == "." || member == ".." {
			bond.MembersUnread = true
			continue
		}
		if len(bond.Members) == maxBondMembers {
			bond.MembersOmitted++
			continue
		}
		slave := filepath.Join(base, member, "bonding_slave")
		bond.Members = append(bond.Members, framev1beta1.BondMember{
			Name:         clipRunes(member, maxNameLen),
			MIIStatus:    clipRunes(text(filepath.Join(slave, "mii_status")), 16),
			State:        clipRunes(text(filepath.Join(slave, "state")), 16),
			LinkFailures: number(filepath.Join(slave, "link_failure_count")),
			AggregatorID: int32At(filepath.Join(slave, "ad_aggregator_id")),
		})
	}
	return bond
}
