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
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/gothshoot/frame/internal/provision"
)

// Config is the flat file that stands in for a FrameInstall when there is no
// apiserver to hold one. frame bootstrap reads it once, from a laptop, to
// install node zero -- the one machine no operator running inside a cluster
// could ever have installed, because the cluster it would run in does not
// exist yet -- or to reinstall a machine and have it rejoin a cluster that
// still exists (cluster.mode: join), when the controller that would
// normally do that is not an option: the machine being reinstalled is the
// one it runs on, or the cluster is too degraded to host it.
type Config struct {
	BMC struct {
		Address  string `yaml:"address"`
		Username string `yaml:"username"`
		// Password is still accepted in the clear, for compatibility: every
		// init config written before PasswordFile existed carries it here,
		// and refusing it would break the one command meant for the day a
		// cluster is gone -- the worst day to discover a config format
		// changed. The file holding it is permission-checked either way.
		// Setting both is refused rather than resolved by precedence: the
		// operator meant one of them, and guessing which is how a stale
		// password gets used.
		Password string `yaml:"password"`
		// PasswordFile names a file holding only the password (a trailing
		// newline is dropped). Preferred: the config itself then carries no
		// secret and can be kept, copied and diffed like any other file.
		PasswordFile       string `yaml:"passwordFile"`
		InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
	} `yaml:"bmc"`
	ConfirmSerial string            `yaml:"confirmSerial"`
	Hostname      string            `yaml:"hostname"`
	Network       provision.Network `yaml:"network"`
	Layout        provision.Layout  `yaml:"layout"`
	Cluster       ClusterConfig     `yaml:"cluster"`
	BootMode      string            `yaml:"bootMode"`
	SSHKeyPath    string            `yaml:"sshKeyPath"` // the private key; its .pub is what enters the image
	MediaBaseURL  string            `yaml:"mediaBaseURL"`
	// Out is where an init writes the new cluster's kubeconfig. init only:
	// a join produces none, and a join config naming one is refused rather
	// than left to suggest a file will appear there.
	Out string `yaml:"out"`
	// Kubeconfig names the EXISTING cluster's kubeconfig. join only, and
	// required there: it is what the Ready phase checks the rejoined node
	// against. frame bootstrap has no cluster of its own to fall back to,
	// so without it a join could wipe the disk, install k3s, and only then
	// discover it has no way to tell whether the node ever came back.
	Kubeconfig string `yaml:"kubeconfig"`
	// InstallTimeout overrides the Installing phase's budget (45 minutes by
	// default, see defaultPhaseTimeouts). A Go duration: "90m", "1h30m".
	// Zero or absent keeps the default. It is the one budget worth tuning
	// per machine: it covers the virtual-media read and `updates: security`,
	// both of which depend on the BMC and the archive mirror, not on Frame.
	InstallTimeout time.Duration `yaml:"installTimeout"`

	// existingKubeconfig is the content of Kubeconfig, read once at load
	// time so an unreadable or unparsable file is refused before the BMC is
	// touched, not at Ready, after the disk is wiped. Unexported, so it
	// never round-trips through YAML.
	existingKubeconfig []byte
}

// ClusterConfig is provision.ClusterTarget plus the one field a file on a
// laptop needs that a FrameInstall does not: where to read the join token
// from. The controller reads it from a Secret (joinTokenRef); this command
// has no Secret to read, so it reads a file.
type ClusterConfig struct {
	provision.ClusterTarget `yaml:",inline"`
	// JoinTokenFile names a file holding only the k3s join token (a
	// trailing newline is dropped) -- the content of
	// /var/lib/rancher/k3s/server/node-token on a server node. join only.
	//
	// The inline joinToken key is refused outright, unlike bmc.password:
	// there is no compatibility to keep, because no config carrying it was
	// ever accepted -- this command refused cluster.mode: join until the
	// same change that introduced this field. A cluster-wide join token is
	// also a heavier secret than one machine's BMC password: anyone who
	// holds it can add a node to the cluster.
	JoinTokenFile string `yaml:"joinTokenFile"`
}

// worldReadable is the bit LoadConfig refuses: "other" read permission.
// Checked alone, not as part of a broader "is this file too open" test --
// group-readable and world-writable are both outside what this guard is
// for. A config file carrying a BMC password in plaintext (there is nowhere
// else to put it when no cluster, and so no Secret, exists yet) is allowed
// to exist; it is not allowed to be readable by every other account on the
// laptop it sits on.
const worldReadable = 0o004

// groupOrOtherReadable is the stricter bit set refused on a file that holds
// nothing BUT a secret (bmc.passwordFile, cluster.joinTokenFile) or an
// admin credential (kubeconfig). The config file itself only refuses
// "other", because it is a document an operator may reasonably share with
// their own group; a file whose entire content is a password or a join
// token has no such reader.
const groupOrOtherReadable = 0o044

// LoadConfig reads and validates path. Every check below runs before the
// config is handed back, and in this order: the file's own permissions
// first (nothing about its content matters if any account on this machine
// can read the password it carries), then whether it parses at all, then
// the two per-field guards a wrong value would otherwise fail on only after
// wasting real time -- an unreachable MediaBaseURL as a twenty-minute
// silence (see validateMediaBaseURL), an unconfirmed serial as a wiped
// disk on the wrong machine (provision.Install's own Pending phase, which
// this only pre-empts) -- then the secrets named by path (resolveSecrets),
// the cluster target and what its mode requires (out for init; the
// existing cluster's kubeconfig for join), and finally that the public
// half of the named SSH key is actually there to read, since only that
// half is ever meant to leave this laptop. All of it before any BMC is
// dialled: run() calls nothing that reaches off this laptop until
// LoadConfig has returned.
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

	if err := resolveSecrets(&cfg); err != nil {
		return Config{}, err
	}

	// The cluster target is checked here, with the rest, for the reason
	// every check in this function exists: provision.Install would refuse
	// the same values at its own Pending phase, but a hand-written file is
	// best corrected while the operator is still at the keyboard. Join's
	// two required fields get their own message first, naming the config
	// keys to set, since ValidateClusterTarget can only name the values.
	if cfg.Cluster.Mode == provision.ClusterJoin {
		if strings.TrimSpace(cfg.Cluster.ServerURL) == "" {
			return Config{}, errors.New(
				"cluster.mode is join but cluster.serverURL is empty: it is the existing cluster's API " +
					"address, e.g. https://192.0.2.201:6443")
		}
		if cfg.Cluster.JoinToken == "" {
			return Config{}, errors.New(
				"cluster.mode is join but cluster.joinTokenFile is not set: it names a file holding the " +
					"token from /var/lib/rancher/k3s/server/node-token on a server node")
		}
	}
	if err := provision.ValidateClusterTarget(cfg.Cluster.ClusterTarget); err != nil {
		return Config{}, err
	}

	switch cfg.Cluster.Mode {
	case provision.ClusterInit:
		// kubeconfig is meaningless here -- there is no existing cluster
		// for an init to check against -- and silently ignoring it would
		// let an operator believe their join config was a join.
		if cfg.Kubeconfig != "" {
			return Config{}, errors.New(
				"kubeconfig is set but cluster.mode is init: kubeconfig names an existing cluster to rejoin, " +
					"and init creates a new one; remove one or the other")
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
	case provision.ClusterJoin:
		if cfg.Out != "" {
			return Config{}, fmt.Errorf(
				"out %s is set but cluster.mode is join: a join produces no kubeconfig, the existing "+
					"cluster's own stays the one to use; remove out", cfg.Out)
		}
		kc, err := loadExistingKubeconfig(cfg.Kubeconfig)
		if err != nil {
			return Config{}, err
		}
		cfg.existingKubeconfig = kc
	}

	if cfg.InstallTimeout < 0 {
		return Config{}, fmt.Errorf("installTimeout %s is negative", cfg.InstallTimeout)
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
			"mediaBaseURL is not set: every built image bakes this address into its own boot arguments, so frame bootstrap refuses to start rather than build an image that fetches its autoinstall seed from nowhere")
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

	// The port this command actually listens on and the port it tells the
	// BMC to fetch from were documented as agreeing "by construction" and
	// nothing checked it -- construction here being one const and one
	// hand-written config field, which is not construction. A mismatch
	// produces the same twenty-minute silence a wrong host does.
	_, listenPort, err := net.SplitHostPort(mediaAddr)
	if err != nil {
		return fmt.Errorf("media listener address %q is malformed: %w", mediaAddr, err)
	}
	if u.Port() != listenPort {
		return fmt.Errorf(
			"mediaBaseURL %q names port %q, but frame bootstrap serves the installer image and seed on "+
				"port %s; the BMC would fetch from somewhere nothing is listening",
			raw, u.Port(), listenPort)
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
			"out is empty: it is where the new cluster's kubeconfig is written, and that credential " +
				"exists nowhere else -- refusing before anything is wiped rather than after")
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

// resolveSecrets moves every secret the config names by path into the
// field the rest of this command reads, so main.go stays unaware of where
// a secret came from.
//
// No error here ever carries a secret's content -- only the key and the
// path. The content is not even logged when it is refused for being empty.
func resolveSecrets(cfg *Config) error {
	if cfg.BMC.PasswordFile != "" {
		if cfg.BMC.Password != "" {
			return errors.New(
				"bmc.password and bmc.passwordFile are both set: keep one -- a password in two places is how " +
					"the stale one ends up used")
		}
		pw, err := readSecretFile("bmc.passwordFile", cfg.BMC.PasswordFile)
		if err != nil {
			return err
		}
		cfg.BMC.Password = pw
	}

	if cfg.Cluster.JoinToken != "" {
		return errors.New(
			"cluster.joinToken is set in the config file: refusing -- put the token in a file of its own, " +
				"mode 0600, and name it with cluster.joinTokenFile")
	}
	if cfg.Cluster.JoinTokenFile != "" {
		if cfg.Cluster.Mode != provision.ClusterJoin {
			return fmt.Errorf("cluster.joinTokenFile is set but cluster.mode is %q: a join token is only "+
				"read for join", cfg.Cluster.Mode)
		}
		tok, err := readSecretFile("cluster.joinTokenFile", cfg.Cluster.JoinTokenFile)
		if err != nil {
			return err
		}
		cfg.Cluster.JoinToken = tok
	}
	return nil
}

// readSecretFile reads a file holding exactly one secret. It refuses a file
// any group member or other account can read, and drops exactly one
// trailing newline -- the one `echo` and every editor add -- and nothing
// else: a password may legitimately begin or end with a space.
func readSecretFile(key, path string) (string, error) {
	b, err := readPrivateFile(key, path)
	if err != nil {
		return "", err
	}
	v := strings.TrimSuffix(string(b), "\n")
	if v == "" {
		return "", fmt.Errorf("%s %s is empty", key, path)
	}
	return v, nil
}

// readPrivateFile stats path before reading it and refuses it if it is
// readable by group or other. The mode is checked on what the path resolves
// to, so a private symlink to a world-readable file is refused too.
func readPrivateFile(key, path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	if info.Mode().Perm()&groupOrOtherReadable != 0 {
		return nil, fmt.Errorf(
			"%s %s is readable by group or other (mode %s): refusing, it holds a credential -- chmod 600 it",
			key, path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

// loadExistingKubeconfig reads and parses the existing cluster's
// kubeconfig. Parsed here, not just read: a file that is not a kubeconfig
// would otherwise be discovered at the Ready phase, with the machine
// already wiped and reinstalled and nothing able to say whether it rejoined.
// Only parsed -- the cluster is not contacted, since nothing at load time
// is allowed to reach off this laptop.
func loadExistingKubeconfig(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New(
			"cluster.mode is join but kubeconfig is not set: it names the existing cluster's kubeconfig, " +
				"which is what the Ready phase checks the rejoined node against -- frame bootstrap has no " +
				"cluster of its own to ask")
	}
	b, err := readPrivateFile("kubeconfig", path)
	if err != nil {
		return nil, err
	}
	// The parser's own error is not wrapped: a YAML error quotes the line
	// it choked on, and in a kubeconfig that line may be a client key.
	if _, err := clientcmd.RESTConfigFromKubeConfig(b); err != nil {
		return nil, fmt.Errorf(
			"kubeconfig %s does not parse as a kubeconfig (the parser's message is withheld: it can quote "+
				"the file, and the file holds credentials)", path)
	}
	return b, nil
}
