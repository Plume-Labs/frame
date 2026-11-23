// Package installer is the guided installation: it looks at the machine it
// runs on (a live Ubuntu booted from the Hearthrack ISO), asks a few
// questions, and turns the answers into the autoinstall document that
// internal/provision already knows how to render and the installer already
// knows how to apply. It never installs anything itself and, like
// internal/provision, imports nothing from Kubernetes and no module outside
// the standard library.
package installer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Disk is one whole disk the machine can see.
type Disk struct {
	ByID       string `json:"byID"` // "/dev/disk/by-id/...": the only name an installation may use
	Path       string `json:"path"` // "/dev/sda": for display, never for installing
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	SizeBytes  int64  `json:"sizeBytes"`
	Rotational bool   `json:"rotational"`
	Transport  string `json:"transport"`
	// Removable disks (the USB key this was booted from) are shown but cannot
	// be chosen: wiping the installer's own medium mid-install ends badly.
	Removable bool `json:"removable"`
	// InUse: a partition of it is mounted (the live system's own medium, for
	// one). Contents lists the filesystems and partitions found on it, so the
	// summary says what the erase destroys.
	InUse    bool     `json:"inUse"`
	Contents []string `json:"contents"`
}

// Confirm is what the person retypes to authorise erasing the disk: its
// serial, or the last part of its by-id name when it reports none.
func (d Disk) Confirm() string {
	if s := strings.TrimSpace(d.Serial); s != "" {
		return s
	}
	return filepath.Base(d.ByID)
}

// NIC is a physical network port.
type NIC struct {
	Name string `json:"name"`
	MAC  string `json:"mac"`
	Up   bool   `json:"up"`
}

// Hardware is what Detect read.
type Hardware struct {
	CPUModel string `json:"cpuModel"`
	AVX      bool   `json:"avx"`
	AESNI    bool   `json:"aesni"`
	MemBytes int64  `json:"memBytes"`
	UEFI     bool   `json:"uefi"`
	Disks    []Disk `json:"disks"`
	NICs     []NIC  `json:"nics"`
}

// Source is where Detect reads from. Root is "/" on a real machine and a
// temporary tree in tests; Run executes a command.
type Source struct {
	Root string
	Run  func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Detect reads the machine. A missing optional fact (no cpuinfo, no NIC) is
// left empty; a missing disk list is an error, since nothing can be installed
// without one.
func Detect(ctx context.Context, src Source) (Hardware, error) {
	hw := Hardware{Disks: []Disk{}, NICs: []NIC{}}
	hw.CPUModel, hw.AVX, hw.AESNI = readCPU(filepath.Join(src.Root, "proc/cpuinfo"))
	hw.MemBytes = readMem(filepath.Join(src.Root, "proc/meminfo"))
	_, err := os.Stat(filepath.Join(src.Root, "sys/firmware/efi"))
	hw.UEFI = err == nil
	hw.NICs = readNICs(filepath.Join(src.Root, "sys/class/net"))

	out, err := src.Run(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,ROTA,MODEL,SERIAL,TYPE,TRAN,RM,FSTYPE,MOUNTPOINTS")
	if err != nil {
		return hw, fmt.Errorf("lsblk: %w", err)
	}
	disks, err := parseLsblk(out)
	if err != nil {
		return hw, err
	}
	byID := byIDNames(filepath.Join(src.Root, "dev/disk/by-id"), src.Root)
	for _, d := range disks {
		d.ByID = byID[d.Path]
		if d.ByID == "" {
			continue // no stable name: cannot be installed to, so not offered
		}
		hw.Disks = append(hw.Disks, d)
	}
	if len(hw.Disks) == 0 {
		return hw, fmt.Errorf("no disk with a /dev/disk/by-id name was found (%d disk(s) seen without one; "+
			"a virtual disk needs a serial)", len(disks))
	}
	return hw, nil
}

type lsblkNode struct {
	Path        string      `json:"path"`
	Size        int64       `json:"size"`
	Rota        bool        `json:"rota"`
	Model       string      `json:"model"`
	Serial      string      `json:"serial"`
	Type        string      `json:"type"`
	Tran        string      `json:"tran"`
	RM          bool        `json:"rm"`
	Fstype      string      `json:"fstype"`
	Mountpoints []*string   `json:"mountpoints"`
	Children    []lsblkNode `json:"children"`
}

func parseLsblk(b []byte) ([]Disk, error) {
	var doc struct {
		Blockdevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("lsblk output: %w", err)
	}
	disks := []Disk{}
	for _, d := range doc.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		disk := Disk{
			Path: d.Path, Model: strings.TrimSpace(d.Model), Serial: strings.TrimSpace(d.Serial),
			SizeBytes: d.Size, Rotational: d.Rota, Transport: d.Tran,
			// mmc is an SD card: the live medium on some machines.
			Removable: d.RM || d.Tran == "usb" || strings.HasPrefix(filepath.Base(d.Path), "mmcblk"),
			Contents:  []string{},
		}
		d.walk(&disk)
		disks = append(disks, disk)
	}
	return disks, nil
}

// walk records the mounted state and the filesystems of n and everything under it.
func (n lsblkNode) walk(d *Disk) {
	for _, m := range n.Mountpoints {
		if m != nil && *m != "" {
			d.InUse = true
		}
	}
	if n.Type != "disk" {
		what := filepath.Base(n.Path)
		if n.Fstype != "" {
			what += " " + n.Fstype
		}
		d.Contents = append(d.Contents, what)
	} else if n.Fstype != "" {
		d.Contents = append(d.Contents, "whole disk "+n.Fstype)
	}
	for _, c := range n.Children {
		c.walk(d)
	}
}

// byIDNames maps a device path to its best by-id name: a model-and-serial name
// over a wwn- or eui. one, because the first stays valid when a disk is moved
// to another controller and says what the disk is when it is read aloud.
func byIDNames(dir, root string) map[string]string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	// Only names that identify the hardware. lvm-pv-uuid-*, md-* and dm-* name
	// what is ON the disk, vanish when it is erased, and would then be
	// "confirmed" by retyping a name that is not the disk's.
	allowed := func(n string) bool {
		for _, p := range []string{"ata-", "scsi-", "nvme-", "virtio-", "wwn-", "usb-", "mmc-"} {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
		return false
	}
	rank := func(n string) int {
		switch {
		case strings.HasPrefix(n, "wwn-"), strings.HasPrefix(n, "nvme-eui."), strings.HasPrefix(n, "nvme-nvme."):
			return 1
		}
		return 0
	}
	best := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, "-part") || !allowed(n) {
			continue
		}
		target, err := filepath.EvalSymlinks(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		target = "/" + strings.TrimPrefix(strings.TrimPrefix(target, root), "/")
		cur, ok := best[target]
		if !ok || rank(n) < rank(filepath.Base(cur)) || (rank(n) == rank(filepath.Base(cur)) && n < filepath.Base(cur)) {
			best[target] = "/dev/disk/by-id/" + n
		}
	}
	return best
}

func readCPU(path string) (model string, avx, aes bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "model name":
			if model == "" {
				model = strings.TrimSpace(v)
			}
		case "flags":
			set := map[string]bool{}
			for fl := range strings.FieldsSeq(v) {
				set[fl] = true
			}
			return model, set["avx"], set["aes"]
		}
	}
	return model, false, false
}

func readMem(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for l := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				kb, _ := strconv.ParseInt(f[0], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// readNICs lists ports backed by a device (so not lo, bridges or veths).
func readNICs(dir string) []NIC {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []NIC{}
	}
	nics := []NIC{}
	for _, e := range entries {
		base := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(base, "device")); err != nil {
			continue
		}
		mac, _ := os.ReadFile(filepath.Join(base, "address"))
		state, _ := os.ReadFile(filepath.Join(base, "operstate"))
		nics = append(nics, NIC{
			Name: e.Name(),
			MAC:  strings.ToLower(strings.TrimSpace(string(mac))),
			Up:   strings.TrimSpace(string(state)) == "up",
		})
	}
	sort.Slice(nics, func(i, j int) bool { return nics[i].Name < nics[j].Name })
	return nics
}
