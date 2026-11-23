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
	var hw Hardware
	hw.CPUModel, hw.AVX, hw.AESNI = readCPU(filepath.Join(src.Root, "proc/cpuinfo"))
	hw.MemBytes = readMem(filepath.Join(src.Root, "proc/meminfo"))
	_, err := os.Stat(filepath.Join(src.Root, "sys/firmware/efi"))
	hw.UEFI = err == nil
	hw.NICs = readNICs(filepath.Join(src.Root, "sys/class/net"))

	out, err := src.Run(ctx, "lsblk", "-J", "-b", "-d", "-o", "NAME,PATH,SIZE,ROTA,MODEL,SERIAL,TYPE,TRAN,RM")
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
		return hw, fmt.Errorf("no disk with a /dev/disk/by-id name was found")
	}
	return hw, nil
}

func parseLsblk(b []byte) ([]Disk, error) {
	var doc struct {
		Blockdevices []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			Rota   bool   `json:"rota"`
			Model  string `json:"model"`
			Serial string `json:"serial"`
			Type   string `json:"type"`
			Tran   string `json:"tran"`
			RM     bool   `json:"rm"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("lsblk output: %w", err)
	}
	var disks []Disk
	for _, d := range doc.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		disks = append(disks, Disk{
			Path: d.Path, Model: strings.TrimSpace(d.Model), Serial: strings.TrimSpace(d.Serial),
			SizeBytes: d.Size, Rotational: d.Rota, Transport: d.Tran,
			Removable: d.RM || d.Tran == "usb",
		})
	}
	return disks, nil
}

// byIDNames maps a device path to its best by-id name: a model-and-serial name
// over a wwn- or eui. one, because the first stays valid when a disk is moved
// to another controller and says what the disk is when it is read aloud.
func byIDNames(dir, root string) map[string]string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
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
		if strings.Contains(n, "-part") {
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
	defer f.Close()
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
			for _, fl := range strings.Fields(v) {
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
	for _, l := range strings.Split(string(b), "\n") {
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
		return nil
	}
	var nics []NIC
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
