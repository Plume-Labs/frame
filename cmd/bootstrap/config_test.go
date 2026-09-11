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

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/rmocq/frame/internal/provision"
)

// The laptop's address is not discoverable from inside this process, and a
// wrong one produces a twenty-minute silence ending in a timeout. Refuse
// early instead.
func TestLoadConfigRequiresAMediaBaseURLTheBMCCanReach(t *testing.T) {
	for _, url := range []string{"", "http://localhost:8081", "http://127.0.0.1:8081"} {
		p := writeConfig(t, validConfig(func(c *Config) { c.MediaBaseURL = url }))
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("MediaBaseURL %q was accepted; a BMC cannot reach it", url)
		}
	}
}

// A valid MediaBaseURL is the positive control for the test above: without
// it, a validateMediaBaseURL that rejected everything would pass every case
// there too.
func TestLoadConfigAcceptsAReachableMediaBaseURL(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("a valid config was refused: %v", err)
	}
}

func TestLoadConfigRequiresTheConfirmedSerial(t *testing.T) {
	p := writeConfig(t, validConfig(func(c *Config) { c.ConfirmSerial = "" }))
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("a config with no confirmed serial was accepted")
	}
}

// join is not merely unvalidated for this command, it is meaningless: frame
// bootstrap exists for node zero, when no cluster exists yet, so there is
// nothing to join. Letting it through would run image build, media attach
// and a machine reset before failing at the Ready phase with no cluster to
// check the joined node against -- a wiped disk on the way to a config
// error.
func TestLoadConfigRequiresClusterInit(t *testing.T) {
	p := writeConfig(t, validConfig(func(c *Config) { c.Cluster.Mode = provision.ClusterJoin }))
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("cluster.mode: join was accepted; frame bootstrap has no cluster to join")
	}
}

// init is the positive control for the test above: without it, a check that
// refused every mode -- including the only one this command can ever act
// on -- would pass that test for the wrong reason.
func TestLoadConfigAcceptsClusterInit(t *testing.T) {
	p := writeConfig(t, validConfig(func(c *Config) { c.Cluster.Mode = provision.ClusterInit }))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("cluster.mode: init was refused: %v", err)
	}
}

// A password in a config file is a password on a laptop's disk. It is allowed
// -- there is nowhere else to put it when no cluster exists -- but the file
// must not be world-readable.
func TestLoadConfigRefusesAWorldReadableFile(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("a world-readable config carrying a BMC password was accepted")
	}
}

// The 0600 mode writeConfig itself uses is the positive control for the
// test above: without it, a permission check that always refused would pass
// that test for the wrong reason, and every other test in this file would
// have been failing on the permission guard all along rather than on
// whatever each one actually means to exercise.
func TestLoadConfigAcceptsAPrivateFile(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("a 0600 config was refused: %v", err)
	}
}

func TestLoadConfigReadsThePublicHalfOfTheKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	writeTestSSHKey(t, key) // writes a valid ed25519 private key and its .pub beside it
	cfg := validConfig(func(c *Config) { c.SSHKeyPath = key })
	got, err := LoadConfig(writeConfig(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if got.SSHKeyPath != key {
		t.Errorf("key path = %s", got.SSHKeyPath)
	}
}

func TestLoadConfigFailsWhenThePublicHalfIsMissing(t *testing.T) {
	cfg := validConfig(func(c *Config) { c.SSHKeyPath = filepath.Join(t.TempDir(), "absent") })
	if _, err := LoadConfig(writeConfig(t, cfg)); err == nil {
		t.Fatal("a config naming a key with no .pub was accepted")
	}
}

// An empty sshKeyPath must be refused by its own check, not by accident
// through the .pub stat that follows it: "" + ".pub" is the relative path
// ".pub", which happens to fail to stat from this package's test working
// directory too -- so asserting only that *an* error came back does not
// discriminate the dedicated guard from that coincidence. Measured: with
// the guard disabled, this test still saw an error, just the .pub stat's
// "could not be read" message instead of this one's "sshKeyPath is empty".
// So this asserts the guard's own message, which only it produces.
func TestLoadConfigRequiresSSHKeyPath(t *testing.T) {
	cfg := validConfig(func(c *Config) { c.SSHKeyPath = "" })
	_, err := LoadConfig(writeConfig(t, cfg))
	if err == nil {
		t.Fatal("a config with no sshKeyPath was accepted")
	}
	if !strings.Contains(err.Error(), "sshKeyPath is empty") {
		t.Fatalf("sshKeyPath is empty was refused, but not by its own guard: %v", err)
	}
}

// The non-empty SSHKeyPath validConfig sets by default is the positive
// control for the test above: without it, a check that refused every
// config regardless of SSHKeyPath would pass that test for the wrong
// reason, and every accept-path test in this file would already be failing
// on this guard rather than on whatever each one actually exercises.
func TestLoadConfigAcceptsANonEmptySSHKeyPath(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("a config with a valid sshKeyPath was refused: %v", err)
	}
}

// A round-trip test -- marshal a Config, unmarshal it back -- cannot catch
// a missing or wrong yaml tag: both directions apply the same implicit
// rule, so a lowercased, unseparated key like "serverurl" marshals to
// itself and back with every test in this file staying green. This test
// instead hand-writes a fixture using the *documented* key names -- the
// ones an operator, rebuilding node zero from a laptop with no cluster
// left to consult, would actually type -- and asserts each one landed on
// the field it names, not on the zero value a silently-ignored key would
// leave behind.
func TestLoadConfigAcceptsTheDocumentedKeyNames(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestSSHKey(t, keyPath)
	out := filepath.Join(dir, "kubeconfig")

	fixture := fmt.Sprintf(`
bmc:
  address: 192.168.2.200
  username: admin
  password: hunter2
  insecureSkipVerify: true
confirmSerial: SERIAL123
hostname: node-zero
network:
  address: 192.168.2.210/24
  gateway: 192.168.2.1
  dns:
    - 192.168.2.1
layout:
  kind: single-disk
  disks:
    - byID: /dev/disk/by-id/test-disk
      sizeBytes: 500000000000
cluster:
  mode: init
  k3sVersion: v1.33.4+k3s1
bootMode: UEFI
sshKeyPath: %s
mediaBaseURL: http://192.168.2.50:8081
out: %s
`, keyPath, out)

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("a config written with the documented key names was refused: %v", err)
	}

	if got.BMC.Address != "192.168.2.200" {
		t.Errorf("bmc.address: got %q", got.BMC.Address)
	}
	if got.Network.Gateway != "192.168.2.1" {
		t.Errorf("network.gateway: got %q", got.Network.Gateway)
	}
	if len(got.Layout.Disks) != 1 || got.Layout.Disks[0].ByID != "/dev/disk/by-id/test-disk" {
		t.Errorf("layout.disks[0].byID: got %+v", got.Layout.Disks)
	}
	if got.Layout.Disks[0].SizeBytes != 500_000_000_000 {
		t.Errorf("layout.disks[0].sizeBytes: got %d", got.Layout.Disks[0].SizeBytes)
	}
	if got.Cluster.Mode != provision.ClusterInit {
		t.Errorf("cluster.mode: got %q", got.Cluster.Mode)
	}
	if got.Cluster.K3sVersion != "v1.33.4+k3s1" {
		t.Errorf("cluster.k3sVersion: got %q", got.Cluster.K3sVersion)
	}
	if got.SSHKeyPath != keyPath {
		t.Errorf("sshKeyPath: got %q", got.SSHKeyPath)
	}
	if got.Out != out {
		t.Errorf("out: got %q", got.Out)
	}
}

// validConfig returns a Config that LoadConfig accepts unmodified -- the
// positive control every negative test above starts from and mutates
// exactly one field away from. It needs no *testing.T: it manages its own
// scratch directory, rather than a t.TempDir() tied to whichever test calls
// it, so it can be reused as a plain value builder.
func validConfig(mutate func(*Config)) Config {
	dir, err := os.MkdirTemp("", "frame-bootstrap-cfg-")
	if err != nil {
		panic(err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestSSHKeyOrPanic(keyPath)

	var cfg Config
	cfg.BMC.Address = "192.168.2.200"
	cfg.BMC.Username = "admin"
	cfg.BMC.Password = "hunter2"
	cfg.BMC.InsecureSkipVerify = true
	cfg.ConfirmSerial = "SERIAL123"
	cfg.Hostname = "node-zero"
	cfg.Network = provision.Network{
		Address: "192.168.2.210/24",
		Gateway: "192.168.2.1",
		DNS:     []string{"192.168.2.1"},
	}
	cfg.Layout = provision.Layout{
		Kind:  provision.LayoutSingleDisk,
		Disks: []provision.Disk{{ByID: "/dev/disk/by-id/test-disk", SizeBytes: 500_000_000_000}},
	}
	cfg.Cluster = provision.ClusterTarget{
		Mode:       provision.ClusterInit,
		K3sVersion: "v1.33.4+k3s1",
	}
	cfg.BootMode = "UEFI"
	cfg.SSHKeyPath = keyPath
	cfg.MediaBaseURL = "http://192.168.2.50:8081"
	cfg.Out = filepath.Join(dir, "kubeconfig")

	if mutate != nil {
		mutate(&cfg)
	}
	return cfg
}

// writeConfig marshals cfg to YAML and writes it 0600 -- private, the shape
// TestLoadConfigRefusesAWorldReadableFile's own chmod deliberately breaks.
func writeConfig(t *testing.T, cfg Config) string {
	t.Helper()
	b, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeTestSSHKey writes a real ed25519 private key to path, PEM-encoded in
// OpenSSH format, and its public half beside it at path+".pub" as an
// authorized_keys line -- the same shape LoadConfig checks for and main.go
// later reads.
func writeTestSSHKey(t *testing.T, path string) {
	t.Helper()
	if err := writeTestSSHKeyErr(path); err != nil {
		t.Fatal(err)
	}
}

func writeTestSSHKeyOrPanic(path string) {
	if err := writeTestSSHKeyErr(path); err != nil {
		panic(err)
	}
}

func writeTestSSHKeyErr(path string) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(sshPub), 0o644)
}

// I6. Four inputs were already validated up front on the stated principle
// that a bad value refused late is refused after the disk is wiped. `out`
// was not one of them, and it is the one output this command produces that
// cannot be regenerated: the new cluster's admin credential exists in
// exactly one place and this path is where it lands.
func TestLoadConfigRefusesAnOutPathItCannotWriteTo(t *testing.T) {
	dir := t.TempDir()
	readOnly := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}

	existingDir := filepath.Join(dir, "a-directory")
	if err := os.Mkdir(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}

	unwritableFile := filepath.Join(dir, "unwritable")
	if err := os.WriteFile(unwritableFile, []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}

	for name, out := range map[string]string{
		"empty":                              "",
		"in a directory that does not exist": filepath.Join(dir, "nope", "kubeconfig"),
		"in a read-only directory":           filepath.Join(readOnly, "kubeconfig"),
		"is itself a directory":              existingDir,
		"exists and is not writable":         unwritableFile,
	} {
		p := writeConfig(t, validConfig(func(c *Config) { c.Out = out }))
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("%s: out %q was accepted; the kubeconfig would be lost after the disk was wiped", name, out)
		}
	}
}

// The positive control: a writable `out` is accepted, and checking it
// leaves nothing behind -- an empty file at that path would read like a
// half-written kubeconfig to whoever looks next.
func TestLoadConfigAcceptsAWritableOutAndCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "kubeconfig")
	p := writeConfig(t, validConfig(func(c *Config) { c.Out = out }))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("a writable out was refused: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("checking out left a file behind at %s", out)
	}
}

// An `out` that already exists and is writable is fine: a reinstall
// overwrites the previous cluster's kubeconfig, which is what it means.
func TestLoadConfigAcceptsAnExistingWritableOut(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(out, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := writeConfig(t, validConfig(func(c *Config) { c.Out = out }))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("an existing writable out was refused: %v", err)
	}
	b, err := os.ReadFile(out)
	if err != nil || string(b) != "previous" {
		t.Errorf("checking out modified it: %q, %v", b, err)
	}
}

// mediaAddr and mediaBaseURL used to be documented as agreeing "by
// construction" -- one Go const and one hand-written YAML field, which is
// not construction. A mismatch produces the same twenty-minute silence a
// wrong host does, with nothing in the log to say the BMC fetched from a
// port nothing listens on.
func TestLoadConfigRefusesAMediaBaseURLOnADifferentPortFromTheListener(t *testing.T) {
	for _, url := range []string{
		"http://192.168.2.50:8080",
		"http://192.168.2.50",
		"http://192.168.2.50:30581",
	} {
		p := writeConfig(t, validConfig(func(c *Config) { c.MediaBaseURL = url }))
		if _, err := LoadConfig(p); err == nil {
			t.Errorf("mediaBaseURL %q was accepted; frame bootstrap listens on %s", url, mediaAddr)
		}
	}
}

// The positive control: the port the listener actually binds is accepted,
// and it is read from mediaAddr rather than restated, so changing the
// const moves both sides together.
func TestLoadConfigAcceptsAMediaBaseURLOnTheListenersOwnPort(t *testing.T) {
	_, port, err := net.SplitHostPort(mediaAddr)
	if err != nil {
		t.Fatal(err)
	}
	p := writeConfig(t, validConfig(func(c *Config) {
		c.MediaBaseURL = "http://192.168.2.50:" + port
	}))
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("the listener's own port was refused: %v", err)
	}
}
