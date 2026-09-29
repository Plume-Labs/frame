package provision

import (
	"encoding/base64"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

// markerPath is where the installed system records which installation produced
// it. Frame reads it over SSH before believing the machine is its own.
const markerPath = "/etc/frame-install-uid"

// lockedPasswordHash is the `frame` user's password as subiquity's identity
// section requires one: a crypt(3) hash "conforming to what passwd requires".
// It is the SHA-512 hash of 48 random bytes that were discarded the moment it
// was computed, so no password opens it. Frame logs in with its key only;
// `ssh.allow-pw` is off.
//
// The hash is what keeps the account shut, not the lock -- or not the
// install-time lock alone. The late-command `passwd -l` does lock it in
// /target, but subiquity also writes the user into
// /etc/cloud/cloud.cfg.d/99-installer.cfg with `lock_passwd: false`, and
// cloud-init's users_groups module runs `passwd -u frame` on first boot (seen
// in the QEMU rehearsal: `passwd -S frame` read P after the first boot). So a
// second late-command (relockCommand) leaves a runcmd that locks it again;
// runcmd runs after users_groups, in the same first boot.
//
// identity rather than cloud-init user-data because identity creates the user
// DURING the install, where late-commands can see it; user-data users only
// appear on first boot.
const lockedPasswordHash = "$6$RqkZHWLekUnwUvem$NpQkV0V.Whw0jxqSQxY/D2/HstLVk8T.QSY0UxlJbolWFfqb84.szv0vflKVeFgAhrkHxwtNtlSFZXXxYR2jf1"

// diskPlaceholders are what storage.config names the disks by until
// early-commands resolve them. The spec names disks by /dev/disk/by-id/wwn-…,
// which carries no serial for subiquity's `match: {serial: …}` to use, and
// udev's ID_SERIAL behind an HBA is not known ahead of time. So the rendered
// document says a placeholder path, and an early-command rewrites
// /autoinstall.yaml -- which subiquity re-reads after early-commands, as its
// reference documents -- with `readlink -f` of each by-id path.
//
// Distinct letters, not indexes: /dev/frame-disk-1 is a prefix of
// /dev/frame-disk-10, and a sed rewriting the first would corrupt the second.
var diskPlaceholders = []string{"/dev/frame-disk-a", "/dev/frame-disk-b"}

// The two boot modes Options.BootMode accepts (install.go's validateOptions)
// and the renderer lays disks out for.
const (
	bootUEFI   = "UEFI"
	bootLegacy = "Legacy"
)

// hostnameShape is an RFC 1123 label, lowercase: the name becomes the
// Kubernetes node name, which refuses uppercase, and it lands in YAML and in
// /etc/hosts, where anything else is either refused later or silently wrong.
var hostnameShape = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// uidShape is what an install UID is allowed to look like: the controller and
// `frame bootstrap` generate UUID-shaped values. Anything else is refused,
// because it is written into a shell command and a file on the target.
var uidShape = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// ValidateSpec runs every refusal RenderAutoinstall makes that depends only on
// the Spec. The build API calls it before anything is fetched, Install calls it
// before the machine is touched, and RenderAutoinstall calls it too -- one list
// of refusals, not three that drift.
func ValidateSpec(s Spec) error {
	_, err := validate(s)
	return err
}

// validated is what validate derives while checking, so the renderer never
// parses the address a second time and, eventually, differently.
type validated struct {
	cidr string
}

func validate(s Spec) (validated, error) {
	if !uidShape.MatchString(s.UID) {
		return validated{}, fmt.Errorf("spec: UID %q must be 1-64 letters, digits or dashes; without it the installed system could not be told apart from any other machine at that address", s.UID)
	}
	if !hostnameShape.MatchString(s.Hostname) {
		return validated{}, fmt.Errorf("spec: hostname %q must be a lowercase RFC 1123 label (letters, digits, dashes, 1-63 characters); it becomes the node name", s.Hostname)
	}
	if s.BootMode != bootUEFI && s.BootMode != bootLegacy {
		return validated{}, fmt.Errorf("spec: boot mode %q must be exactly \"UEFI\" or \"Legacy\"; the disk layout differs between the two", s.BootMode)
	}
	if err := checkPublicKeyOnly(s.SSHPublicKey); err != nil {
		return validated{}, err
	}

	addr, ipnet, err := net.ParseCIDR(s.Network.Address)
	if err != nil {
		return validated{}, fmt.Errorf("network address %q: %w", s.Network.Address, err)
	}
	if addr.To4() == nil {
		return validated{}, fmt.Errorf("network address %q: must be IPv4", s.Network.Address)
	}
	if net.ParseIP(s.Network.Gateway) == nil {
		return validated{}, fmt.Errorf("network gateway %q is not an IP address", s.Network.Gateway)
	}
	// Refused, not defaulted: with no resolver the installer cannot reach the
	// archive `updates: security` fetches from, and a guessed resolver (the
	// gateway) turns "you did not say" into a failure one layer later.
	if len(s.Network.DNS) == 0 {
		return validated{}, fmt.Errorf("network DNS: at least one resolver is required")
	}
	for _, dns := range s.Network.DNS {
		if net.ParseIP(dns) == nil {
			return validated{}, fmt.Errorf("network DNS %q is not an IP address", dns)
		}
	}

	want := map[LayoutKind]int{LayoutSingleDisk: 1, LayoutMirror: 2}[s.Layout.Kind]
	if want == 0 {
		return validated{}, fmt.Errorf("layout %q: unknown kind", s.Layout.Kind)
	}
	if len(s.Layout.Disks) != want {
		return validated{}, fmt.Errorf("layout %s: needs exactly %d disks, got %d", s.Layout.Kind, want, len(s.Layout.Disks))
	}
	if err := validateDisks(s.Layout.Disks); err != nil {
		return validated{}, err
	}

	ones, _ := ipnet.Mask.Size()
	return validated{cidr: fmt.Sprintf("%s/%d", addr, ones)}, nil
}

// The document, as Go types. Marshalled, never templated: no value a spec
// carries can change the document's structure, whatever characters it holds.
type userData struct {
	Autoinstall autoinstall `yaml:"autoinstall"`
}

type autoinstall struct {
	Version          int               `yaml:"version"`
	InteractiveSects []string          `yaml:"interactive-sections"`
	RefreshInstaller map[string]bool   `yaml:"refresh-installer"`
	Updates          string            `yaml:"updates"`
	Apt              map[string]string `yaml:"apt"`
	Locale           string            `yaml:"locale"`
	Keyboard         map[string]string `yaml:"keyboard"`
	Network          netplan           `yaml:"network"`
	Storage          storage           `yaml:"storage"`
	Identity         identity          `yaml:"identity"`
	SSH              sshSection        `yaml:"ssh"`
	EarlyCommands    []string          `yaml:"early-commands"`
	LateCommands     []string          `yaml:"late-commands"`
	ErrorCommands    []string          `yaml:"error-commands"`
	Shutdown         string            `yaml:"shutdown"`
}

type netplan struct {
	Version   int                 `yaml:"version"`
	Ethernets map[string]ethernet `yaml:"ethernets"`
}

type ethernet struct {
	Match       map[string]string   `yaml:"match"`
	Optional    bool                `yaml:"optional"`
	DHCP4       bool                `yaml:"dhcp4"`
	Addresses   []string            `yaml:"addresses"`
	Routes      []map[string]string `yaml:"routes"`
	Nameservers map[string][]string `yaml:"nameservers"`
}

type storage struct {
	Swap   map[string]int   `yaml:"swap"`
	Config []map[string]any `yaml:"config"`
}

type identity struct {
	Hostname string `yaml:"hostname"`
	Username string `yaml:"username"`
	Realname string `yaml:"realname"`
	Password string `yaml:"password"`
}

type sshSection struct {
	InstallServer  bool     `yaml:"install-server"`
	AllowPW        bool     `yaml:"allow-pw"`
	AuthorizedKeys []string `yaml:"authorized-keys"`
}

// RenderAutoinstall turns a Spec into the NoCloud user-data subiquity installs
// from. It refuses rather than emits when something is wrong, because the two
// ways this goes badly are silent: an image carrying secret material, and an
// installation that wipes a disk nobody named.
//
// beaconBase and token are where the machine reports progress. An empty
// beaconBase renders no beacon commands at all -- the cold-start path
// (`frame bootstrap`) has nothing reading a beacon.
func RenderAutoinstall(s Spec, beaconBase, token string) (string, error) {
	v, err := validate(s)
	if err != nil {
		return "", err
	}
	if err := ValidateBeaconBase(beaconBase); err != nil {
		return "", err
	}

	early := []string{
		// First, before anything is probed or wiped: a failing early-command
		// aborts the install, which is what makes this a guard. The early
		// beacon comes after it, so a refused machine never reports `early`.
		fmt.Sprintf("if ! ( %s ); then echo 'FRAME: named disk is not the expected size, refusing' > /dev/console; exit 1; fi",
			diskSizeAssertion(s.Layout)),
		diskResolution(s.Layout),
	}
	late := []string{
		"echo 'frame ALL=(ALL) NOPASSWD:ALL' > /target/etc/sudoers.d/frame && chmod 440 /target/etc/sudoers.d/frame",
		fmt.Sprintf("echo '%s' > /target%s && chmod 444 /target%s", s.UID, markerPath, markerPath),
		// Written by Frame, not left to subiquity: join.go reads the new
		// cluster's kubeconfig over sudo, and without a 127.0.1.1 line for the
		// machine's own name every sudo prints "unable to resolve host".
		fmt.Sprintf("echo '%[1]s' > /target/etc/hostname && (grep -qx '127.0.1.1 %[1]s' /target/etc/hosts || echo '127.0.1.1 %[1]s' >> /target/etc/hosts)", s.Hostname),
		"curtin in-target --target=/target -- passwd -l frame",
		relockCommand(targetRoot),
	}
	errs := []string{}
	if strings.TrimSpace(beaconBase) != "" {
		early = append(early,
			BeaconSend(beaconBase, token, CheckpointEarly),
			BeaconHeartbeat(beaconBase, token, CheckpointEarly))
		late = append(late, BeaconSend(beaconBase, token, CheckpointLate))
		errs = append(errs, BeaconSend(beaconBase, token, CheckpointFailed))
	}

	doc := userData{Autoinstall: autoinstall{
		Version:          1,
		InteractiveSects: []string{},
		RefreshInstaller: map[string]bool{"update": false},
		// autoinstall has no "none". security is explicit rather than
		// defaulted, so the choice shows: the install reaches the archive,
		// and the kernel installed can be newer than the ISO's.
		Updates:  "security",
		Apt:      map[string]string{"fallback": "offline-install"},
		Locale:   "en_US.UTF-8",
		Keyboard: map[string]string{"layout": "us"},
		Network: netplan{Version: 2, Ethernets: map[string]ethernet{
			// One stanza over every en* port: networkd only configures a link
			// with carrier, so on a machine with four ports and one cable the
			// address lands on the cabled one without Frame knowing its name
			// or MAC. UNPROVEN ON HARDWARE -- recette step 1.
			"frame0": {
				Match:       map[string]string{"name": "en*"},
				Optional:    true,
				DHCP4:       false,
				Addresses:   []string{v.cidr},
				Routes:      []map[string]string{{"to": "default", "via": s.Network.Gateway}},
				Nameservers: map[string][]string{"addresses": s.Network.DNS},
			},
		}},
		Storage: storage{
			// No swap: the node runs kubelet.
			Swap:   map[string]int{"size": 0},
			Config: storageConfig(s.Layout, s.BootMode),
		},
		Identity: identity{Hostname: s.Hostname, Username: "frame", Realname: "Frame", Password: lockedPasswordHash},
		SSH: sshSection{
			InstallServer:  true,
			AllowPW:        false,
			AuthorizedKeys: []string{strings.TrimSpace(s.SSHPublicKey)},
		},
		EarlyCommands: early,
		LateCommands:  late,
		ErrorCommands: errs,
		Shutdown:      "reboot",
	}}

	b, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return "#cloud-config\n" + string(b), nil
}

// targetRoot is where subiquity mounts the installed system while
// late-commands run. A parameter of the command builders below rather than a
// literal inside them, so the tests can run those commands against a
// temporary directory.
const targetRoot = "/target"

// writeFileCommand writes content to path and sets its mode. The content
// travels base64-encoded: the alphabet has no quote, no backslash, no $ and
// no newline, so whatever the content holds it cannot leave the single-quoted
// shell word it sits in. base64 is coreutils, present in the live system.
func writeFileCommand(path, content, mode string) string {
	return fmt.Sprintf("echo '%s' | base64 -d > '%s' && chmod %s '%s'",
		base64.StdEncoding.EncodeToString([]byte(content)), path, mode, path)
}

// relockCommand leaves a cloud-init config on the target whose runcmd locks
// `frame` again on first boot, after users_groups has unlocked it (see
// lockedPasswordHash). 99-zz sorts after subiquity's 99-installer.cfg.
//
// Not proven: that 99-installer.cfg carries no runcmd of its own. cloud.cfg.d
// files merge top-level keys by replacement, so if it ever did, this file
// would silently replace it. The rehearsal's 99-installer.cfg had none.
func relockCommand(target string) string {
	b, err := yaml.Marshal(map[string][][]string{"runcmd": {{"passwd", "-l", "frame"}}})
	if err != nil {
		panic(err) // a constant map; cannot fail
	}
	return writeFileCommand(target+"/etc/cloud/cloud.cfg.d/99-zz-frame-lock.cfg", string(b), "644")
}

// diskResolution rewrites each placeholder in /autoinstall.yaml with the
// kernel path its by-id link resolves to. It refuses (exit 1, install aborted,
// nothing wiped) if a link resolves to no block device, or if two links
// resolve to the same one: validateDisks compares names, and two different
// names can still be one disk -- a mirror of a disk with itself installs,
// boots, and has no redundancy at all.
//
// Every disk is resolved and compared before the first rewrite, so a refusal
// leaves /autoinstall.yaml as it was rather than half-rewritten.
func diskResolution(l Layout) string {
	parts := make([]string, 0, 2*len(l.Disks)+1)
	for i, d := range l.Disks {
		parts = append(parts, fmt.Sprintf(
			"p%[1]d=$(readlink -f '%[2]s') && [ -b \"$p%[1]d\" ] || { echo 'FRAME: %[2]s is not a block device, refusing' > /dev/console; exit 1; }",
			i, d.ByID))
	}
	for i := 1; i < len(l.Disks); i++ {
		parts = append(parts, fmt.Sprintf(
			"[ \"$p0\" != \"$p%[1]d\" ] || { echo 'FRAME: %[2]s and %[3]s are the same disk, refusing' > /dev/console; exit 1; }",
			i, l.Disks[0].ByID, l.Disks[i].ByID))
	}
	for i := range l.Disks {
		parts = append(parts, fmt.Sprintf("sed -i \"s#%s#$p%d#g\" /autoinstall.yaml", diskPlaceholders[i], i))
	}
	return strings.Join(parts, "; ")
}

// storageConfig is the curtin storage for a layout. Disks not listed here are
// not touched: curtin only wipes what storage.config references.
//
// UEFI: an ESP on each disk, grub_device on the ESP partitions, so either
// disk boots alone. Legacy: a bios_grub partition on each disk, grub_device on
// the disks. Mirror: both disks wiped recursively -- the live system
// auto-assembles leftover md arrays -- and / on md0 over the two root
// partitions.
func storageConfig(l Layout, bootMode string) []map[string]any {
	uefi := bootMode == bootUEFI
	var cfg []map[string]any
	roots := make([]string, 0, len(l.Disks))
	for i := range l.Disks {
		id := fmt.Sprintf("disk%d", i)
		disk := map[string]any{"type": "disk", "id": id, "path": diskPlaceholders[i],
			"ptable": "gpt", "wipe": "superblock-recursive", "preserve": false}
		var boot map[string]any
		if uefi {
			boot = map[string]any{"type": "partition", "id": id + "-esp", "device": id, "number": 1,
				"size": "1G", "flag": "boot", "grub_device": true, "wipe": "superblock", "preserve": false}
		} else {
			disk["grub_device"] = true
			boot = map[string]any{"type": "partition", "id": id + "-biosgrub", "device": id, "number": 1,
				"size": "1M", "flag": "bios_grub", "preserve": false}
		}
		root := map[string]any{"type": "partition", "id": id + "-root", "device": id, "number": 2,
			"size": -1, "wipe": "superblock", "preserve": false}
		cfg = append(cfg, disk, boot, root)
		roots = append(roots, id+"-root")
		if uefi {
			cfg = append(cfg, map[string]any{"type": "format", "id": id + "-esp-fs", "volume": id + "-esp", "fstype": "fat32"})
		}
	}
	rootVolume := roots[0]
	if l.Kind == LayoutMirror {
		cfg = append(cfg, map[string]any{"type": "raid", "id": "md0", "name": "md0", "raidlevel": "raid1", "devices": roots, "preserve": false})
		rootVolume = "md0"
	}
	cfg = append(cfg,
		map[string]any{"type": "format", "id": "root-fs", "volume": rootVolume, "fstype": "ext4"},
		map[string]any{"type": "mount", "id": "root-mnt", "device": "root-fs", "path": "/"})
	if uefi {
		// Only the first ESP is mounted; the second is formatted and carries
		// grub via grub_device, as a boot path for when the first disk is gone.
		cfg = append(cfg, map[string]any{"type": "mount", "id": "esp-mnt", "device": "disk0-esp-fs", "path": "/boot/efi"})
	}
	return cfg
}

// checkShellValue refuses what breaks the context every interpolated value
// lands in: a single-quoted shell word inside an early- or late-command,
// where a quote or a backslash ends the word and the rest becomes commands run
// as root. The YAML around it is marshalled, so YAML is not this function's
// concern.
func checkShellValue(field, v string) error {
	if strings.ContainsAny(v, "\n\r") {
		return fmt.Errorf("%s: must not contain a newline", field)
	}
	if strings.ContainsAny(v, "'\\") {
		return fmt.Errorf("%s: must not contain a quote or a backslash; this value is interpolated into a shell command in the installer", field)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s: must not contain control characters", field)
		}
	}
	return nil
}

// checkPublicKeyOnly keeps private key material out of the served document,
// which travels over an unauthenticated HTTP path. ssh.ParseAuthorizedKey is
// not that guard on its own: handed a whole key file it finds the public line,
// ignores the private one, and returns no error. The single-line rule is what
// closes it.
func checkPublicKeyOnly(key string) error {
	k := strings.TrimSpace(key)
	if n := utf8.RuneCountInString(k); n == 0 || n > 1024 {
		return fmt.Errorf("ssh key: must be between 1 and 1024 characters, got %d", n)
	}
	if strings.Contains(k, "PRIVATE KEY") {
		return fmt.Errorf("ssh key: this is private key material, which must never enter an installer image")
	}
	if strings.ContainsAny(k, "\n\r") {
		return fmt.Errorf("ssh key: must be one key on one line; a second line is a second key")
	}
	_, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(k))
	if err != nil {
		return fmt.Errorf("ssh key: not a usable authorized_keys line: %w", err)
	}
	// An options prefix (command=, no-pty…) would run as `frame`, who has
	// NOPASSWD:ALL.
	if len(options) != 0 {
		return fmt.Errorf("ssh key: carries authorized_keys options (%q); this value must be a bare key", strings.Join(options, ","))
	}
	// Two keys on one line: the parser folds the second into the first one's
	// comment field.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(comment))); err == nil {
		return fmt.Errorf("ssh key: more than one key in this value; the second is hidden in the first one's comment field")
	}
	return nil
}
