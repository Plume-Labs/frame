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
	"os"
	"path/filepath"
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
