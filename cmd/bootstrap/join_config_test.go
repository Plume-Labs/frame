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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gothshoot/frame/internal/provision"
)

// testJoinToken is shaped like a real k3s token and is what every secret
// assertion below searches error messages for.
const testJoinToken = "K10deadbeef0123456789::server:t0ps3cr3tjoin"

// existingKubeconfigYAML is a kubeconfig clientcmd accepts. It names a
// server nothing listens on: LoadConfig only parses it, and must never
// contact it.
const existingKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: existing
  cluster:
    server: https://192.0.2.201:6443
    insecure-skip-tls-verify: true
contexts:
- name: existing
  context: {cluster: existing, user: admin}
current-context: existing
users:
- name: admin
  user: {token: kubeconfig-bearer-token}
`

// writePrivate writes content at dir/name with mode, and returns the path.
func writePrivate(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode goes through the umask; chmod makes it exact.
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// validJoinConfig is validConfig turned into a complete join: a server, a
// token read from a 0600 file (with the trailing newline echo leaves), the
// existing cluster's kubeconfig, and no out. Every join test below starts
// here and changes one thing.
func validJoinConfig(t *testing.T, mutate func(*Config)) Config {
	t.Helper()
	dir := t.TempDir()
	tokenFile := writePrivate(t, dir, "node-token", testJoinToken+"\n", 0o600)
	kubeconfig := writePrivate(t, dir, "existing.kubeconfig", existingKubeconfigYAML, 0o600)
	return validConfig(func(c *Config) {
		c.Cluster.Mode = provision.ClusterJoin
		c.Cluster.ServerURL = "https://192.0.2.201:6443"
		c.Cluster.JoinTokenFile = tokenFile
		c.Kubeconfig = kubeconfig
		c.Out = ""
		if mutate != nil {
			mutate(c)
		}
	})
}

// The positive control for every refusal below, and the proof that the
// token read from its file is the token, minus exactly the newline echo
// added, and that the existing cluster's kubeconfig was read at load time.
func TestLoadConfigAcceptsACompleteJoin(t *testing.T) {
	got, err := LoadConfig(writeConfig(t, validJoinConfig(t, nil)))
	if err != nil {
		t.Fatalf("a complete join config was refused: %v", err)
	}
	if got.Cluster.JoinToken != testJoinToken {
		t.Errorf("join token = %q, want the file's content without its trailing newline", got.Cluster.JoinToken)
	}
	if string(got.existingKubeconfig) != existingKubeconfigYAML {
		t.Errorf("the existing cluster's kubeconfig was not read at load time: %d bytes", len(got.existingKubeconfig))
	}
}

// Each case changes one thing from a complete join and names the words
// only its own refusal uses, so a case refused by some other guard by
// coincidence shows up as a failure rather than as a pass.
func TestLoadConfigRefusesAnIncompleteJoin(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, c *Config)
		want   string
	}{
		{"no server URL", func(_ *testing.T, c *Config) { c.Cluster.ServerURL = "" },
			"cluster.serverURL is empty"},
		{"an http server URL", func(_ *testing.T, c *Config) { c.Cluster.ServerURL = "http://192.0.2.201:6443" },
			"scheme must be https"},
		{"a server URL with a path", func(_ *testing.T, c *Config) { c.Cluster.ServerURL = "https://192.0.2.201:6443/x" },
			"scheme and host only"},
		{"no join token file", func(_ *testing.T, c *Config) { c.Cluster.JoinTokenFile = "" },
			"cluster.joinTokenFile is not set"},
		{"no k3s version", func(_ *testing.T, c *Config) { c.Cluster.K3sVersion = "" }, "does not look like a k3s version"},
		{"no kubeconfig", func(_ *testing.T, c *Config) { c.Kubeconfig = "" }, "kubeconfig is not set"},
		{"out set", func(t *testing.T, c *Config) { c.Out = filepath.Join(t.TempDir(), "kubeconfig") },
			"a join produces no kubeconfig"},
		{"a kubeconfig that does not parse", func(t *testing.T, c *Config) {
			c.Kubeconfig = writePrivate(t, t.TempDir(), "bad", "clusters: [", 0o600)
		}, "does not parse as a kubeconfig"},
		{"a group-readable kubeconfig", func(t *testing.T, c *Config) {
			c.Kubeconfig = writePrivate(t, t.TempDir(), "kc", existingKubeconfigYAML, 0o640)
		}, "looser than 0600"},
		{"a kubeconfig reached through a symbolic link", func(t *testing.T, c *Config) {
			link := filepath.Join(t.TempDir(), "kc-link")
			if err := os.Symlink(c.Kubeconfig, link); err != nil {
				t.Fatal(err)
			}
			c.Kubeconfig = link
		}, "is a symbolic link"},
	}
	for _, tc := range cases {
		cfg := validJoinConfig(t, func(c *Config) { tc.mutate(t, c) })
		_, err := LoadConfig(writeConfig(t, cfg))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused, but not by its own guard (want %q): %v", tc.name, tc.want, err)
		}
	}
}

// kubeconfig on an init is refused rather than ignored: ignoring it would
// let an operator who forgot to change the mode believe they ran a join.
func TestLoadConfigRefusesAKubeconfigOnInit(t *testing.T) {
	kc := writePrivate(t, t.TempDir(), "kc", existingKubeconfigYAML, 0o600)
	_, err := LoadConfig(writeConfig(t, validConfig(func(c *Config) { c.Kubeconfig = kc })))
	if err == nil || !strings.Contains(err.Error(), "init creates a new one") {
		t.Fatalf("kubeconfig on an init config was not refused by its own guard: %v", err)
	}
}

// --- secrets by path ---

func TestLoadConfigReadsTheBMCPasswordFromItsFile(t *testing.T) {
	pw := writePrivate(t, t.TempDir(), "bmc-password", " pass word \n", 0o600)
	got, err := LoadConfig(writeConfig(t, validConfig(func(c *Config) {
		c.BMC.Password = ""
		c.BMC.PasswordFile = pw
	})))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one trailing newline is dropped, nothing else: a password may
	// begin or end with a space.
	if got.BMC.Password != " pass word " {
		t.Errorf("BMC password = %q, want the file's content minus its trailing newline", got.BMC.Password)
	}
}

// The clear-text password stays accepted, for every init config written
// before passwordFile existed. validConfig itself carries one, so every
// accept-path test in config_test.go is also this test's positive control;
// this one names the decision.
func TestLoadConfigStillAcceptsAClearTextBMCPassword(t *testing.T) {
	got, err := LoadConfig(writeConfig(t, validConfig(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if got.BMC.Password != "hunter2" {
		t.Errorf("BMC password = %q", got.BMC.Password)
	}
}

func TestLoadConfigRefusesASecretFileLooserThan0600(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o700, 0o610} {
		dir := t.TempDir()
		pw := writePrivate(t, dir, "bmc-password", "pw\n", mode)
		tok := writePrivate(t, dir, "node-token", testJoinToken+"\n", mode)

		_, err := LoadConfig(writeConfig(t, validConfig(func(c *Config) {
			c.BMC.Password = ""
			c.BMC.PasswordFile = pw
		})))
		if err == nil || !strings.Contains(err.Error(), "looser than 0600") {
			t.Errorf("bmc.passwordFile mode %s: not refused by the permission guard: %v", mode, err)
		}

		_, err = LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) { c.Cluster.JoinTokenFile = tok })))
		if err == nil || !strings.Contains(err.Error(), "looser than 0600") {
			t.Errorf("cluster.joinTokenFile mode %s: not refused by the permission guard: %v", mode, err)
		}
	}
}

func TestLoadConfigRefusesAmbiguousOrMisplacedSecrets(t *testing.T) {
	dir := t.TempDir()
	pw := writePrivate(t, dir, "bmc-password", "pw\n", 0o600)
	empty := writePrivate(t, dir, "empty", "\n", 0o600)
	tok := writePrivate(t, dir, "node-token", testJoinToken+"\n", 0o600)

	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"password and passwordFile", validConfig(func(c *Config) { c.BMC.PasswordFile = pw }), "both set"},
		{"an inline join token", validJoinConfig(t, func(c *Config) {
			c.Cluster.JoinTokenFile = ""
			c.Cluster.JoinToken = testJoinToken
		}), "cluster.joinToken is set in the config file"},
		{"an empty token file", validJoinConfig(t, func(c *Config) { c.Cluster.JoinTokenFile = empty }), "is empty"},
		{"a token file on an init", validConfig(func(c *Config) { c.Cluster.JoinTokenFile = tok }), "only read for join"},
		{"a missing token file", validJoinConfig(t, func(c *Config) {
			c.Cluster.JoinTokenFile = filepath.Join(dir, "absent")
		}), "cluster.joinTokenFile"},
	}
	for _, tc := range cases {
		_, err := LoadConfig(writeConfig(t, tc.cfg))
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: refused, but not by its own guard (want %q): %v", tc.name, tc.want, err)
		}
		if strings.Contains(err.Error(), testJoinToken) {
			t.Errorf("%s: the error carries the join token: %v", tc.name, err)
		}
	}
}

// A token the file holds but k3s would not accept is refused -- and the
// refusal names neither the token nor any part of it.
func TestLoadConfigNeverPrintsARefusedJoinToken(t *testing.T) {
	const bad = "K10abc::server:has a space"
	tok := writePrivate(t, t.TempDir(), "node-token", bad+"\n", 0o600)
	_, err := LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) { c.Cluster.JoinTokenFile = tok })))
	if err == nil {
		t.Fatal("a malformed join token was accepted")
	}
	if strings.Contains(err.Error(), "has a space") || strings.Contains(err.Error(), "K10abc") {
		t.Errorf("the error carries the join token: %v", err)
	}
}

// --- installTimeout ---

func TestPhaseTimeoutsKeepsTheDefaultsUnlessInstallingIsSet(t *testing.T) {
	got := phaseTimeouts(0)
	for p, d := range defaultPhaseTimeouts {
		if got[p] != d {
			t.Errorf("phaseTimeouts(0)[%s] = %s, want the default %s", p, got[p], d)
		}
	}

	got = phaseTimeouts(90 * time.Minute)
	if got[provision.PhaseInstalling] != 90*time.Minute {
		t.Errorf("Installing = %s, want the configured 1h30m", got[provision.PhaseInstalling])
	}
	for p, d := range defaultPhaseTimeouts {
		if p != provision.PhaseInstalling && got[p] != d {
			t.Errorf("an Installing override moved %s to %s", p, got[p])
		}
	}
	if defaultPhaseTimeouts[provision.PhaseInstalling] != 45*time.Minute {
		t.Errorf("the override leaked into defaultPhaseTimeouts: Installing = %s",
			defaultPhaseTimeouts[provision.PhaseInstalling])
	}
}

func TestLoadConfigRefusesANegativeInstallTimeout(t *testing.T) {
	_, err := LoadConfig(writeConfig(t, validConfig(func(c *Config) { c.InstallTimeout = -time.Minute })))
	if err == nil || !strings.Contains(err.Error(), "installTimeout") {
		t.Fatalf("a negative installTimeout was not refused by its own guard: %v", err)
	}
}

// The documented join keys, hand-written the way the deployment guide
// shows them -- the same reason TestLoadConfigAcceptsTheDocumentedKeyNames
// exists for init: a round trip through yaml.Marshal cannot catch a wrong
// tag, and "installTimeout: 90m" in particular only works if the duration
// is decoded from its string form.
func TestLoadConfigAcceptsTheDocumentedJoinKeyNames(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestSSHKey(t, keyPath)
	pw := writePrivate(t, dir, "bmc-password", "hunter2\n", 0o600)
	tok := writePrivate(t, dir, "node-token", testJoinToken+"\n", 0o600)
	kc := writePrivate(t, dir, "existing.kubeconfig", existingKubeconfigYAML, 0o600)

	fixture := fmt.Sprintf(`
bmc:
  address: 192.0.2.200
  username: admin
  passwordFile: %s
  insecureSkipVerify: true
confirmSerial: SERIAL123
hostname: w3
network:
  address: 192.0.2.213/24
  gateway: 192.0.2.1
  dns: [192.0.2.1]
layout:
  kind: single-disk
  disks:
    - byID: /dev/disk/by-id/test-disk
      sizeBytes: 500000000000
cluster:
  mode: join
  serverURL: https://192.0.2.201:6443
  joinTokenFile: %s
  k3sVersion: v1.33.4+k3s1
kubeconfig: %s
installTimeout: 90m
bootMode: UEFI
sshKeyPath: %s
mediaBaseURL: http://192.0.2.50:8081
`, pw, tok, kc, keyPath)
	path := writePrivate(t, dir, "config.yaml", fixture, 0o600)

	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("a join config written with the documented key names was refused: %v", err)
	}
	if got.BMC.Password != "hunter2" {
		t.Errorf("bmc.passwordFile: password = %q", got.BMC.Password)
	}
	if got.Cluster.Mode != provision.ClusterJoin || got.Cluster.ServerURL != "https://192.0.2.201:6443" {
		t.Errorf("cluster: got mode %q, serverURL %q", got.Cluster.Mode, got.Cluster.ServerURL)
	}
	if got.Cluster.JoinToken != testJoinToken {
		t.Errorf("cluster.joinTokenFile: token not read from the file")
	}
	if got.Kubeconfig != kc {
		t.Errorf("kubeconfig: got %q", got.Kubeconfig)
	}
	if got.InstallTimeout != 90*time.Minute {
		t.Errorf("installTimeout: got %s", got.InstallTimeout)
	}
}

// A private symlink to a private file is still refused: what the path names
// can change between the check and the read, and O_NOFOLLOW only protects
// the read if the check refused links too.
func TestLoadConfigRefusesASecretFileBehindASymbolicLink(t *testing.T) {
	dir := t.TempDir()
	tok := writePrivate(t, dir, "node-token", testJoinToken+"\n", 0o600)
	link := filepath.Join(dir, "token-link")
	if err := os.Symlink(tok, link); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) { c.Cluster.JoinTokenFile = link })))
	if err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("a join token behind a symbolic link was not refused by its own guard: %v", err)
	}
}

// A 0600 file owned by another account is private to them, not to the
// operator. An unprivileged test cannot chown, so the account running the
// test is what changes instead.
func TestLoadConfigRefusesASecretFileOwnedBySomeoneElse(t *testing.T) {
	cfg := validJoinConfig(t, nil)
	p := writeConfig(t, cfg)
	realUID := currentUID
	currentUID = func() int { return realUID() + 1 }
	defer func() { currentUID = realUID }()

	_, err := LoadConfig(p)
	if err == nil || !strings.Contains(err.Error(), "not by you") {
		t.Fatalf("a secret file owned by another account was not refused by its own guard: %v", err)
	}
}

// A config that holds bmc.password in the clear is a credential file and
// needs 0600; the same config naming the password by path may be 0640.
func TestLoadConfigHoldsAClearTextPasswordConfigTo0600(t *testing.T) {
	p := writeConfig(t, validConfig(nil))
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(p)
	if err == nil || !strings.Contains(err.Error(), "carries bmc.password in the clear") {
		t.Fatalf("a 0640 config carrying a clear password was not refused by its own guard: %v", err)
	}

	pw := writePrivate(t, t.TempDir(), "bmc-password", "pw\n", 0o600)
	p = writeConfig(t, validConfig(func(c *Config) { c.BMC.Password = ""; c.BMC.PasswordFile = pw }))
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err != nil {
		t.Fatalf("a 0640 config naming its password by path was refused: %v", err)
	}
}

// yaml.v3 quotes the value it could not decode. In this file that value
// can be the password, so the message is withheld and only the line kept.
func TestLoadConfigNeverEchoesTheYAMLParserMessage(t *testing.T) {
	// Short on purpose: yaml.v3 abbreviates a long value to its first few
	// characters plus "...", which a Contains on the whole value would miss
	// while the prefix still leaks.
	const secret = "s3cr3tpw"
	path := writePrivate(t, t.TempDir(), "config.yaml", "bmc:\n  insecureSkipVerify: "+secret+"\n", 0o600)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("a config that does not parse was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the parser's message, value included, reached the error: %v", err)
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("the error lost the line the parser named: %v", err)
	}
}

// kubeconfigQuotingSecret is a kubeconfig the parser refuses with a message
// that quotes the value: "cannot decode !!str `...` as a !!int".
const kubeconfigQuotingSecret = "users:\n- name: a\n  user: {token: !!int kubeconfig-secret-value}\n"

func TestLoadConfigNeverEchoesTheKubeconfigParserMessage(t *testing.T) {
	kc := writePrivate(t, t.TempDir(), "kc", kubeconfigQuotingSecret, 0o600)
	_, err := LoadConfig(writeConfig(t, validJoinConfig(t, func(c *Config) { c.Kubeconfig = kc })))
	if err == nil {
		t.Fatal("a kubeconfig that does not parse was accepted")
	}
	if strings.Contains(err.Error(), "kubeconfig-secret-value") {
		t.Errorf("the kubeconfig parser's message, value included, reached the error: %v", err)
	}
}
