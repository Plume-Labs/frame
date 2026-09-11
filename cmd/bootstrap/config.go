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
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rmocq/frame/internal/provision"
)

// Config is the flat file that stands in for a FrameInstall when there is no
// apiserver to hold one. frame bootstrap reads it once, from a laptop, to
// install node zero -- the one machine no operator running inside a cluster
// could ever have installed, because the cluster it would run in does not
// exist yet.
type Config struct {
	BMC struct {
		Address            string `yaml:"address"`
		Username           string `yaml:"username"`
		Password           string `yaml:"password"`
		InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
	} `yaml:"bmc"`
	ConfirmSerial string                  `yaml:"confirmSerial"`
	Hostname      string                  `yaml:"hostname"`
	Network       provision.Network       `yaml:"network"`
	Layout        provision.Layout        `yaml:"layout"`
	Cluster       provision.ClusterTarget `yaml:"cluster"`
	BootMode      string                  `yaml:"bootMode"`
	SSHKeyPath    string                  `yaml:"sshKeyPath"` // the private key; its .pub is what enters the image
	MediaBaseURL  string                  `yaml:"mediaBaseURL"`
	Out           string                  `yaml:"out"`
}

// worldReadable is the bit LoadConfig refuses: "other" read permission.
// Checked alone, not as part of a broader "is this file too open" test --
// group-readable and world-writable are both outside what this guard is
// for. A config file carrying a BMC password in plaintext (there is nowhere
// else to put it when no cluster, and so no Secret, exists yet) is allowed
// to exist; it is not allowed to be readable by every other account on the
// laptop it sits on.
const worldReadable = 0o004

// LoadConfig reads and validates path. Every check below runs before the
// config is handed back, and in this order: the file's own permissions
// first (nothing about its content matters if any account on this machine
// can read the password it carries), then whether it parses at all, then
// the two per-field guards a wrong value would otherwise fail on only after
// wasting real time -- an unreachable MediaBaseURL as a twenty-minute
// silence (see validateMediaBaseURL), an unconfirmed serial as a wiped
// disk on the wrong machine (provision.Install's own Pending phase, which
// this only pre-empts) -- and finally that the public half of the named SSH
// key is actually there to read, since only that half is ever meant to
// leave this laptop.
//
// No error path here ever formats cfg or any of its fields wholesale (no
// %v/%+v of the Config or BMC struct, and the raw file bytes are never
// echoed back): the BMC password lives in this struct, and a mistake in an
// error message is exactly the kind of leak a reviewer reading only the
// success path would never see.
func LoadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	if info.Mode().Perm()&worldReadable != 0 {
		return Config{}, fmt.Errorf(
			"config %s is world-readable (mode %s): refusing, because this file carries a BMC password and there is nowhere else on a laptop with no cluster yet to put one",
			path, info.Mode().Perm())
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := validateMediaBaseURL(cfg.MediaBaseURL); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(cfg.ConfirmSerial) == "" {
		return Config{}, fmt.Errorf(
			"confirmSerial is empty: refusing, because an empty confirmation proves nothing about which machine this is and the cost of being wrong is a wiped disk on the wrong one")
	}

	// cluster.mode: join is not merely unvalidated for this command, it is
	// meaningless. frame bootstrap exists for node zero, when no cluster
	// exists yet -- there is nothing to join. Checked here, before the
	// machine is ever touched, for the same reason Task 5's validation
	// runs before Join does: a malformed value refused only deep into
	// Joining is refused after the disk is already wiped. This is a
	// stronger case than that one -- "asked for something this binary
	// cannot do" isn't a bad value, it is the wrong command, and letting
	// Install run image build, media attach and a machine reset before
	// saying so wipes a disk on the way to telling the operator to have
	// used `FrameInstall` instead.
	if cfg.Cluster.Mode != provision.ClusterInit {
		return Config{}, fmt.Errorf(
			"cluster.mode %q: frame bootstrap creates a cluster, from no cluster at all -- it does not join an existing one; joining a node to a cluster that already exists is the controller's job, via a FrameInstall, not this command's",
			cfg.Cluster.Mode)
	}

	// `out` is checked here, with the other four, on the same principle:
	// a bad value refused late is refused after the disk is wiped. It is
	// also the only output of this command that cannot be regenerated --
	// the cluster's admin credential exists in exactly one place and this
	// path is where it lands. An unwritable `out` discovered after Install
	// returns means the cluster is up and unreachable, forever.
	if err := checkOutWritable(cfg.Out); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(cfg.SSHKeyPath) == "" {
		return Config{}, fmt.Errorf("sshKeyPath is empty")
	}
	if _, err := os.Stat(cfg.SSHKeyPath + ".pub"); err != nil {
		return Config{}, fmt.Errorf("ssh key %s: its public half %s.pub could not be read: %w", cfg.SSHKeyPath, cfg.SSHKeyPath, err)
	}

	return cfg, nil
}

// validateMediaBaseURL refuses anything that is not a usable http(s) base
// URL a BMC on the management network can actually reach -- the same shape
// cmd/provisiond/main.go's validateMediaURL already enforces for MEDIA_URL,
// plus one rule that command has no reason to carry: frame bootstrap runs
// on the same laptop it serves media from, which makes localhost and
// 127.0.0.1 a value that parses cleanly, resolves on this machine, and
// means nothing at all to a BMC on a different network. Accepting either
// produces no error -- the process starts, the install proceeds, and the
// machine sits at MediaAttached until PhaseMediaAttached's own timeout
// expires with nothing in the log to say why.
func validateMediaBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf(
			"mediaBaseURL is not set: every built image bakes this address into its own boot arguments, so frame bootstrap refuses to start rather than build an image that fetches its preseed from nowhere")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("mediaBaseURL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("mediaBaseURL %q: scheme must be http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("mediaBaseURL %q: has no host", raw)
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1":
		return fmt.Errorf(
			"mediaBaseURL %q: names this laptop itself (%s); a BMC on the management network cannot reach it, and the failure that produces is a long silence ending in a timeout, not an error",
			raw, u.Hostname())
	}
	return nil
}

// checkOutWritable answers the only question that matters about `out`
// before an install starts: can this process write there.
//
// It is a real write, not a permission calculation: os.Stat's mode bits say
// nothing about a read-only mount, an immutable attribute, or a directory
// this user cannot create in. A file that already exists is opened for
// writing and closed; one that does not is created and removed again, so a
// failed install does not leave an empty kubeconfig behind looking like a
// half-written one.
func checkOutWritable(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf(
			"out is empty: it is where the new cluster's kubeconfig is written, and that credential exists nowhere else -- refusing before anything is wiped rather than after")
	}

	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("out %s is a directory, not a file", path)
		}
		f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("out %s already exists and is not writable: %w", path, err)
		}
		return f.Close()
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("out %s cannot be created: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}
